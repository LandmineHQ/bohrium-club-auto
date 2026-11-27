package bohrium

import (
	"context"
	"strings"
	"sync"
	"time"

	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/store"
)

// Manager coordinates multiple isolated Bohrium accounts backed by the shared SQLite Store.
// Each account gets its own Client instance with isolated credentials, persistent UUIDv4 StoreID,
// and live SciX model catalog cache.
type Manager struct {
	http          *chromehttp.Client
	cfg           *config.Manager
	store         *store.Store
	sharedCatalog *sharedCatalogCache

	mu        sync.RWMutex
	endpoints Endpoints
	clients   map[string]*Client
}

// NewManager creates a multi-account Bohrium client manager.
func NewManager(httpClient *chromehttp.Client, cfg *config.Manager, st *store.Store) *Manager {
	return &Manager{
		http:          httpClient,
		cfg:           cfg,
		store:         st,
		sharedCatalog: &sharedCatalogCache{},
		endpoints:     defaultEndpoints(),
		clients:       make(map[string]*Client),
	}
}

// Store returns the underlying multi-account SQLite store.
func (m *Manager) Store() *store.Store {
	return m.store
}

// SetEndpoints overrides upstream base URLs on all existing and future per-account clients (for testing).
func (m *Manager) SetEndpoints(ep Endpoints) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ep.ClubBase != "" {
		m.endpoints.ClubBase = strings.TrimRight(ep.ClubBase, "/")
	}
	if ep.PlatformBase != "" {
		m.endpoints.PlatformBase = strings.TrimRight(ep.PlatformBase, "/")
	}
	if ep.WWWBase != "" {
		m.endpoints.WWWBase = strings.TrimRight(ep.WWWBase, "/")
	}
	if ep.SBCloudBase != "" {
		m.endpoints.SBCloudBase = strings.TrimRight(ep.SBCloudBase, "/")
	}
	if ep.AgentBase != "" {
		m.endpoints.AgentBase = strings.TrimRight(ep.AgentBase, "/")
	}
	if ep.SigmaLLMBase != "" {
		m.endpoints.SigmaLLMBase = strings.TrimRight(ep.SigmaLLMBase, "/")
	}
	if ep.PlatformGatewayBase != "" {
		m.endpoints.PlatformGatewayBase = strings.TrimRight(ep.PlatformGatewayBase, "/")
	} else if ep.WWWBase != "" && m.endpoints.PlatformGatewayBase == DefaultPlatformGatewayBaseURL {
		m.endpoints.PlatformGatewayBase = strings.TrimRight(ep.WWWBase, "/")
	}
	for _, cli := range m.clients {
		cli.SetEndpoints(m.endpoints)
	}
}

// ForAccount returns the cached per-account Client bound to accountID.
func (m *Manager) ForAccount(accountID string) *Client {
	id := strings.TrimSpace(accountID)
	m.mu.RLock()
	if cli, ok := m.clients[id]; ok {
		m.mu.RUnlock()
		return cli
	}
	ep := m.endpoints
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if cli, ok := m.clients[id]; ok {
		return cli
	}
	cli := NewClient(m.http, m.cfg, m.store.ForAccount(id))
	cli.sharedCatalog = m.sharedCatalog
	cli.SetEndpoints(ep)
	m.clients[id] = cli
	return cli
}

// ForLogin returns a short-lived Client and LoginScope used when adding or re-authenticating a Bohrium account.
// Newly authenticated accounts are persisted with PoolEnabled=false by default.
func (m *Manager) ForLogin(label string) (*Client, *store.LoginScope) {
	m.mu.RLock()
	ep := m.endpoints
	m.mu.RUnlock()

	scope := m.store.ForLogin(label)
	cli := NewClient(m.http, m.cfg, scope)
	cli.SetEndpoints(ep)
	return cli, scope
}

// Ephemeral returns a client suitable for pre-authentication operations (WeChat QR, OAuth URL, OTP send, PDF count).
func (m *Manager) Ephemeral() *Client {
	m.mu.RLock()
	ep := m.endpoints
	m.mu.RUnlock()
	cli := NewClient(m.http, m.cfg, m.store)
	cli.SetEndpoints(ep)
	return cli
}

// DeleteAccount evicts any cached client and permanently removes the account (and its bindings) from SQLite.
func (m *Manager) DeleteAccount(accountID string) error {
	id := strings.TrimSpace(accountID)
	m.mu.Lock()
	delete(m.clients, id)
	m.mu.Unlock()
	return m.store.DeleteAccount(id)
}

// RefreshAccountBalance queries live Bohrium photon/org/wallet balances for accountID and persists the snapshot.
// If the stored account has no nickname yet, it also performs a best-effort profile backfill.
func (m *Manager) RefreshAccountBalance(ctx context.Context, accountID string) (*PointsAndCapacity, error) {
	cli := m.ForAccount(accountID)
	assets, err := cli.GetPointsAndCapacity(ctx)
	if err != nil {
		return nil, err
	}
	_ = m.store.UpdateAccountBalance(accountID, assets.PhotonBalance, assets.OrgBalance, assets.WalletBalance)
	if acc, ok, _ := m.store.GetAccount(accountID); ok && acc != nil && strings.TrimSpace(acc.Nickname) == "" {
		_, _, _ = cli.SyncProfile(ctx)
	}
	return assets, nil
}

// SyncMissingProfiles backfills missing Bohrium nicknames/profiles for existing accounts in SQLite.
func (m *Manager) SyncMissingProfiles(ctx context.Context) int {
	accounts, err := m.store.ListAccounts()
	if err != nil {
		return 0
	}
	now := time.Now().UTC()
	updated := 0
	for _, acc := range accounts {
		if ctx.Err() != nil {
			return updated
		}
		if strings.TrimSpace(acc.Token) == "" || strings.TrimSpace(acc.Nickname) != "" {
			continue
		}
		if !acc.ExpiresAt.IsZero() && !acc.ExpiresAt.After(now) {
			continue
		}
		subCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if _, changed, err := m.ForAccount(acc.ID).SyncProfile(subCtx); err == nil && changed {
			updated++
		}
		cancel()
	}
	return updated
}

// RefreshStaleBalances updates balance snapshots for pooled accounts whose balance is older than maxAge.
func (m *Manager) RefreshStaleBalances(ctx context.Context, maxAge time.Duration) {
	accounts, err := m.store.ListPoolAccounts()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, acc := range accounts {
		if ctx.Err() != nil {
			return
		}
		if acc.Token == "" || (!acc.ExpiresAt.IsZero() && !acc.ExpiresAt.After(now)) {
			continue
		}
		if !acc.BalanceUpdatedAt.IsZero() && now.Sub(acc.BalanceUpdatedAt) < maxAge {
			continue
		}
		subCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _ = m.RefreshAccountBalance(subCtx, acc.ID)
		cancel()
	}
}

// WarmSciXCatalog eagerly populates the per-account and shared SciX model catalog cache
// using the first healthy authenticated account (preferring pooled accounts).
func (m *Manager) WarmSciXCatalog(ctx context.Context) error {
	accounts, err := m.store.ListPoolAccounts()
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		accounts, err = m.store.ListAccounts()
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	var lastErr error
	for _, acc := range accounts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.TrimSpace(acc.Token) == "" {
			continue
		}
		if !acc.ExpiresAt.IsZero() && !acc.ExpiresAt.After(now) {
			continue
		}
		if _, cErr := m.ForAccount(acc.ID).GetSciXModelCatalog(ctx, false); cErr == nil {
			return nil
		} else {
			lastErr = cErr
		}
	}
	return lastErr
}
