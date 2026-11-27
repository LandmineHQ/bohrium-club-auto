package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/store"
)

var (
	// ErrEmptyPool indicates that no accounts have been manually dragged into the OpenAI routing pool.
	ErrEmptyPool = errors.New("OpenAI 路由池为空：默认账号不会自动入池，请先在 Web 控制台「OpenAI 路由池」将至少一个账号拖入池中")
	// ErrNoHealthyPoolAccount indicates that pooled accounts exist but none are currently healthy/available.
	ErrNoHealthyPoolAccount = errors.New("OpenAI 路由池中暂无可用账号（均已过期、余额不足或处于冷却期）")
)

// RouteRequest provides conversation identity hints for sticky weighted routing.
type RouteRequest struct {
	SessionHash         string
	ExplicitAccountID   string // Optional internal override (e.g., Playground testing a specific account)
	PrevRespAccountID   string // Owner account of previous_response_id when chaining Responses API
	PrevRespSessionHash string // Session hash of previous_response_id chain
	ModelHint           string
	IsToolContinuation  bool
	ExcludeAccountIDs   map[string]bool
}

// RouteDecision describes the selected account, its isolated Bohrium Client, and session/run IDs.
type RouteDecision struct {
	Account          store.Account
	Client           *bohrium.Client
	SessionHash      string
	SciXSessionID    string
	SciXRunID        string
	SessionCreatedAt string
	Mode             string // "direct" | "bound" | "new_bind" | "rebound"
}

// PoolAccountView summarizes a pooled account's live routing weight and binding telemetry for the admin UI.
type PoolAccountView struct {
	AccountID        string    `json:"accountId"`
	UserID           int64     `json:"userId"`
	DisplayName      string    `json:"displayName"`
	Nickname         string    `json:"nickname"`
	Label            string    `json:"label"`
	PhotonBalance    float64   `json:"photonBalance"`
	OrgBalance       float64   `json:"orgBalance"`
	EffectiveBalance float64   `json:"effectiveBalance"`
	WalletBalanceCNY float64   `json:"walletBalanceCny"`
	WeightPercent    float64   `json:"weightPercent"`
	Healthy          bool      `json:"healthy"`
	HealthStatus     string    `json:"healthStatus"`
	CooldownUntil    time.Time `json:"cooldownUntil,omitempty"`
	ActiveBindings   int       `json:"activeBindings"`
	BalanceUpdatedAt time.Time `json:"balanceUpdatedAt,omitempty"`
}

// PoolRouter routes OpenAI /v1/* traffic across explicitly pooled Bohrium accounts using:
// 1. 7-day SQLite-backed session_hash sticky bindings;
// 2. Balance-proportional weighted probabilistic sampling (P_i ∝ max(0, photon + org)) for new sessions;
// 3. Automatic pre-first-byte failover and rebind when a bound account becomes unavailable.
type PoolRouter struct {
	mgr   *bohrium.Manager
	store *store.Store
}

// NewPoolRouter creates a weighted-balance SQLite-backed OpenAI pool router.
func NewPoolRouter(mgr *bohrium.Manager) *PoolRouter {
	return &PoolRouter{
		mgr:   mgr,
		store: mgr.Store(),
	}
}

// SelectAccount resolves which pooled account should handle an OpenAI request.
func (r *PoolRouter) SelectAccount(ctx context.Context, req RouteRequest) (*RouteDecision, error) {
	now := time.Now().UTC()

	// 1. Explicit internal account override (used when admin picks a specific account in Playground).
	if id := strings.TrimSpace(req.ExplicitAccountID); id != "" {
		acc, ok, err := r.store.GetAccount(id)
		if err != nil {
			return nil, err
		}
		if !ok || strings.TrimSpace(acc.Token) == "" {
			return nil, fmt.Errorf("指定的账号不存在或未登录: %s", id)
		}
		runID := bohrium.NewCanonicalUUID()
		return &RouteDecision{
			Account:          *acc,
			Client:           r.mgr.ForAccount(acc.ID),
			SessionHash:      req.SessionHash,
			SciXSessionID:    bohrium.DeterministicSessionUUID("direct:" + acc.ID + ":" + req.SessionHash),
			SciXRunID:        runID,
			SessionCreatedAt: now.Format("2006-01-02T15:04:05.000Z"),
			Mode:             "direct",
		}, nil
	}

	sessionHash := strings.TrimSpace(req.PrevRespSessionHash)
	if sessionHash == "" {
		sessionHash = strings.TrimSpace(req.SessionHash)
	}
	if sessionHash == "" {
		sessionHash = ComputeSessionHash("", "", "", "", bohrium.NewCanonicalUUID())
	}

	// 2. Check existing active (<7d) SQLite session binding.
	hadBinding := false
	binding, found, err := r.store.GetSessionBinding(sessionHash, now)
	if err == nil && found && binding != nil {
		hadBinding = true
		if !req.ExcludeAccountIDs[binding.AccountID] {
			acc, ok, aErr := r.store.GetAccount(binding.AccountID)
			if aErr == nil && ok && acc.IsHealthyForPool(now) {
				runID := binding.SciXRunID
				if !req.IsToolContinuation || runID == "" {
					runID = bohrium.NewCanonicalUUID()
				}
				_ = r.store.TouchSessionBinding(sessionHash, runID, req.ModelHint, now)
				return &RouteDecision{
					Account:          *acc,
					Client:           r.mgr.ForAccount(acc.ID),
					SessionHash:      sessionHash,
					SciXSessionID:    binding.SciXSessionID,
					SciXRunID:        runID,
					SessionCreatedAt: binding.SessionCreatedAt,
					Mode:             "bound",
				}, nil
			}
		}
	}

	// 2b. If Responses previous_response_id pointed to a still-healthy pooled account (e.g. binding cleared/expired),
	// prefer sticking to that account if it wasn't excluded.
	if prevAccID := strings.TrimSpace(req.PrevRespAccountID); prevAccID != "" && !req.ExcludeAccountIDs[prevAccID] {
		if acc, ok, aErr := r.store.GetAccount(prevAccID); aErr == nil && ok && acc.IsHealthyForPool(now) {
			sessID := bohrium.NewCanonicalUUID()
			runID := bohrium.NewCanonicalUUID()
			b, rErr := r.store.RebindSession(sessionHash, acc.ID, sessID, runID, req.ModelHint, now)
			if rErr == nil && b != nil {
				return &RouteDecision{
					Account:          *acc,
					Client:           r.mgr.ForAccount(acc.ID),
					SessionHash:      sessionHash,
					SciXSessionID:    b.SciXSessionID,
					SciXRunID:        runID,
					SessionCreatedAt: b.SessionCreatedAt,
					Mode:             "bound",
				}, nil
			}
		}
	}

	// 3. New session or failover: load explicitly pooled accounts and filter healthy candidates.
	poolAccounts, err := r.store.ListPoolAccounts()
	if err != nil {
		return nil, err
	}
	if len(poolAccounts) == 0 {
		return nil, ErrEmptyPool
	}

	candidates := make([]store.Account, 0, len(poolAccounts))
	for _, acc := range poolAccounts {
		if req.ExcludeAccountIDs[acc.ID] {
			continue
		}
		if acc.IsHealthyForPool(now) {
			candidates = append(candidates, acc)
		}
	}
	if len(candidates) == 0 {
		return nil, ErrNoHealthyPoolAccount
	}

	// If any pooled candidate has never fetched its balance snapshot yet, refresh it quickly (bounded 5s)
	// so newly dragged-in accounts participate with accurate real photon weights on their first request.
	for i := range candidates {
		if candidates[i].BalanceUpdatedAt.IsZero() {
			subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if assets, bErr := r.mgr.RefreshAccountBalance(subCtx, candidates[i].ID); bErr == nil && assets != nil {
				candidates[i].PhotonBalance = assets.PhotonBalance
				candidates[i].OrgBalance = assets.OrgBalance
				candidates[i].WalletBalanceCNY = assets.WalletBalance
				candidates[i].BalanceUpdatedAt = now
			}
			cancel()
		}
	}

	chosen := PickWeightedByBalance(candidates)
	newSciXSessionID := bohrium.NewCanonicalUUID()
	newSciXRunID := bohrium.NewCanonicalUUID()

	if hadBinding || len(req.ExcludeAccountIDs) > 0 {
		b, rErr := r.store.RebindSession(sessionHash, chosen.ID, newSciXSessionID, newSciXRunID, req.ModelHint, now)
		if rErr != nil {
			return nil, rErr
		}
		return &RouteDecision{
			Account:          chosen,
			Client:           r.mgr.ForAccount(chosen.ID),
			SessionHash:      sessionHash,
			SciXSessionID:    b.SciXSessionID,
			SciXRunID:        newSciXRunID,
			SessionCreatedAt: b.SessionCreatedAt,
			Mode:             "rebound",
		}, nil
	}

	sessCreatedAt := now.Format("2006-01-02T15:04:05.000Z")
	_ = r.store.UpsertSessionBinding(store.SessionBinding{
		SessionHash:      sessionHash,
		AccountID:        chosen.ID,
		SciXSessionID:    newSciXSessionID,
		SciXRunID:        newSciXRunID,
		SessionCreatedAt: sessCreatedAt,
		ModelHint:        req.ModelHint,
		HitCount:         1,
		CreatedAt:        now,
		LastUsedAt:       now,
		ExpiresAt:        now.Add(store.SessionBindingTTL),
	})
	return &RouteDecision{
		Account:          chosen,
		Client:           r.mgr.ForAccount(chosen.ID),
		SessionHash:      sessionHash,
		SciXSessionID:    newSciXSessionID,
		SciXRunID:        newSciXRunID,
		SessionCreatedAt: sessCreatedAt,
		Mode:             "new_bind",
	}, nil
}

// SelectCatalogClient returns a healthy pooled account's Client for fetching the live SciX model catalog,
// falling back to any logged-in account when requested by admin UI (allowAnyAccount=true).
func (r *PoolRouter) SelectCatalogClient(allowAnyAccount bool) (*bohrium.Client, *store.Account, error) {
	now := time.Now().UTC()
	poolAccounts, err := r.store.ListPoolAccounts()
	if err == nil && len(poolAccounts) > 0 {
		var healthy []store.Account
		for _, a := range poolAccounts {
			if a.IsHealthyForPool(now) {
				healthy = append(healthy, a)
			}
		}
		if len(healthy) > 0 {
			chosen := PickWeightedByBalance(healthy)
			return r.mgr.ForAccount(chosen.ID), &chosen, nil
		}
		// Fallback to first pooled account with a token even if cooling down
		for _, a := range poolAccounts {
			if strings.TrimSpace(a.Token) != "" {
				cp := a
				return r.mgr.ForAccount(cp.ID), &cp, nil
			}
		}
	}
	if !allowAnyAccount {
		if len(poolAccounts) == 0 {
			return nil, nil, ErrEmptyPool
		}
		return nil, nil, ErrNoHealthyPoolAccount
	}
	allAccounts, err := r.store.ListAccounts()
	if err != nil || len(allAccounts) == 0 {
		return nil, nil, bohrium.ErrBohriumNotLoggedIn
	}
	for _, a := range allAccounts {
		if strings.TrimSpace(a.Token) != "" {
			cp := a
			return r.mgr.ForAccount(cp.ID), &cp, nil
		}
	}
	return nil, nil, bohrium.ErrBohriumNotLoggedIn
}

// PoolSummary computes each pooled account's live selection probability percentage and active binding counts.
func (r *PoolRouter) PoolSummary() ([]PoolAccountView, float64, error) {
	now := time.Now().UTC()
	poolAccounts, err := r.store.ListPoolAccounts()
	if err != nil {
		return nil, 0, err
	}
	bindCounts, _ := r.store.AccountBindingCounts(now)

	totalEffective := 0.0
	healthyCount := 0
	for _, a := range poolAccounts {
		if a.IsHealthyForPool(now) {
			healthyCount++
			totalEffective += a.EffectiveBalance()
		}
	}

	views := make([]PoolAccountView, 0, len(poolAccounts))
	for _, a := range poolAccounts {
		eff := a.EffectiveBalance()
		healthy := a.IsHealthyForPool(now)
		pct := 0.0
		if healthy {
			if totalEffective > 0 {
				pct = math.Round((eff/totalEffective)*1000) / 10 // 1 decimal place
			} else if healthyCount > 0 {
				pct = math.Round((100.0/float64(healthyCount))*10) / 10
			}
		}
		views = append(views, PoolAccountView{
			AccountID:        a.ID,
			UserID:           a.UserID,
			DisplayName:      a.DisplayName(),
			Nickname:         a.Nickname,
			Label:            a.Label,
			PhotonBalance:    a.PhotonBalance,
			OrgBalance:       a.OrgBalance,
			EffectiveBalance: eff,
			WalletBalanceCNY: a.WalletBalanceCNY,
			WeightPercent:    pct,
			Healthy:          healthy,
			HealthStatus:     a.HealthStatus,
			CooldownUntil:    a.CooldownUntil,
			ActiveBindings:   bindCounts[a.ID],
			BalanceUpdatedAt: a.BalanceUpdatedAt,
		})
	}
	return views, totalEffective, nil
}

// PickWeightedByBalance selects one account from candidates with probability strictly proportional
// to EffectiveBalance() = max(0, photonBalance + orgBalance) using crypto/rand.
// If all candidates have 0 balance, falls back to uniform random selection.
func PickWeightedByBalance(candidates []store.Account) store.Account {
	if len(candidates) == 1 {
		return candidates[0]
	}
	total := 0.0
	for _, c := range candidates {
		total += c.EffectiveBalance()
	}
	if total <= 0 {
		idx := cryptoRandIndex(len(candidates))
		return candidates[idx]
	}
	target := cryptoRandFloat64() * total
	cum := 0.0
	for _, c := range candidates {
		w := c.EffectiveBalance()
		if w <= 0 {
			continue
		}
		cum += w
		if target < cum {
			return c
		}
	}
	// Floating-point edge fallback: return last positive-weight candidate.
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i].EffectiveBalance() > 0 {
			return candidates[i]
		}
	}
	return candidates[0]
}

// ComputeSessionHash derives a stable SHA-256 routing key for an OpenAI conversation.
func ComputeSessionHash(apiKeyScope, explicitSessionKey, firstSys, firstUser, fallbackSeed string) string {
	if k := strings.TrimSpace(explicitSessionKey); k != "" {
		sum := sha256.Sum256([]byte("explicit:" + apiKeyScope + ":" + k))
		return hex.EncodeToString(sum[:])
	}
	fs := strings.TrimSpace(firstSys)
	if len(fs) > 256 {
		fs = fs[:256]
	}
	fu := strings.TrimSpace(firstUser)
	if len(fu) > 512 {
		fu = fu[:512]
	}
	if fu != "" || fs != "" {
		sum := sha256.Sum256([]byte("conv:" + apiKeyScope + ":" + fs + "\x00" + fu))
		return hex.EncodeToString(sum[:])
	}
	if fb := strings.TrimSpace(fallbackSeed); fb != "" {
		sum := sha256.Sum256([]byte("seed:" + apiKeyScope + ":" + fb))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256([]byte("anon:" + bohrium.NewCanonicalUUID()))
	return hex.EncodeToString(sum[:])
}

func cryptoRandFloat64() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	// Use top 53 bits for uniform [0.0, 1.0) IEEE 754 float64
	u := binary.BigEndian.Uint64(b[:]) >> 11
	return float64(u) / float64(uint64(1)<<53)
}

func cryptoRandIndex(n int) int {
	if n <= 1 {
		return 0
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint64(b[:]) % uint64(n))
}
