package openai

import (
	"fmt"
	"net/http"
	"time"

	"bohrium-club-auto/internal/config"
)

// UserQuotaView describes the authenticated key's own lifetime and daily token
// limits. It intentionally has no pool-account balances: those belong to the
// admin routing view and are not the caller's spendable key quota.
type UserQuotaView struct {
	Unit               string `json:"unit"`
	Limit              int64  `json:"limit"`
	Used               int64  `json:"used"`
	Remaining          int64  `json:"remaining"`
	DailyLimit         int64  `json:"daily_limit"`
	DailyUsed          int64  `json:"daily_used"`
	DailyRemaining     int64  `json:"daily_remaining"`
	EffectiveRemaining int64  `json:"effective_remaining"`
	Unlimited          bool   `json:"unlimited"`
}

// UserBalanceResponse is compatible with CC Switch's generic
// {{baseUrl}}/user/balance query, which reads the numeric balance field.
// A balance of -1 together with unlimited=true represents no key-level cap.
type UserBalanceResponse struct {
	Object       string                `json:"object"`
	IsActive     bool                  `json:"is_active"`
	IsAvailable  bool                  `json:"is_available"`
	Balance      int64                 `json:"balance"`
	Currency     string                `json:"currency"`
	Unit         string                `json:"unit"`
	Unlimited    bool                  `json:"unlimited"`
	APIKeyID     string                `json:"api_key_id"`
	APIKeyName   string                `json:"api_key_name"`
	ExpiresAt    string                `json:"expires_at,omitempty"`
	Quota        UserQuotaView         `json:"quota"`
	BalanceInfos []BalanceCurrencyInfo `json:"balance_infos"`
	FetchedAt    string                `json:"fetched_at"`
}

func (g *Gateway) handleGetUserBalance(w http.ResponseWriter, r *http.Request) {
	key, ok := callerKeyPolicy(r.Context())
	if !ok || key.ID == "" {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "api_key_required", "The user balance endpoint requires a concrete API key; anonymous access has no key-level quota")
		return
	}
	if g.mgr == nil || g.mgr.Store() == nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "store_error", publicMsgStoreError)
		return
	}
	now := time.Now().UTC()
	totalUsed, dailyUsed, err := g.mgr.Store().GatewayKeyUsageForAdmission(
		key.ID,
		now,
		g.cfg.Get().Schedule.Timezone,
	)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "store_error", publicMsgStoreError)
		return
	}
	resp := buildUserBalanceResponse(key, totalUsed, dailyUsed, now)
	WriteJSON(w, http.StatusOK, resp)
}

func buildUserBalanceResponse(key config.APIKey, totalUsed, dailyUsed int64, now time.Time) UserBalanceResponse {
	totalRemaining := int64(-1)
	if key.TokenQuota > 0 {
		totalRemaining = max(int64(0), key.TokenQuota-totalUsed)
	}
	dailyRemaining := int64(-1)
	if key.DailyTokenQuota > 0 {
		dailyRemaining = max(int64(0), key.DailyTokenQuota-dailyUsed)
	}

	unlimited := key.TokenQuota <= 0 && key.DailyTokenQuota <= 0
	effective := int64(-1)
	switch {
	case key.TokenQuota > 0 && key.DailyTokenQuota > 0:
		effective = min(totalRemaining, dailyRemaining)
	case key.TokenQuota > 0:
		effective = totalRemaining
	case key.DailyTokenQuota > 0:
		effective = dailyRemaining
	}
	balanceText := "-1"
	if effective >= 0 {
		balanceText = fmt.Sprintf("%d", effective)
	}
	grantedText := "0"
	if key.TokenQuota > 0 {
		grantedText = fmt.Sprintf("%d", key.TokenQuota)
	}
	return UserBalanceResponse{
		Object:      "balance",
		IsActive:    key.Enabled && !key.Expired(now),
		IsAvailable: key.Enabled && !key.Expired(now) && (unlimited || effective > 0),
		Balance:     effective,
		Currency:    "TOKEN",
		Unit:        "TOKEN",
		Unlimited:   unlimited,
		APIKeyID:    key.ID,
		APIKeyName:  key.Name,
		ExpiresAt:   key.ExpiresAt,
		Quota: UserQuotaView{
			Unit:               "TOKEN",
			Limit:              key.TokenQuota,
			Used:               totalUsed,
			Remaining:          totalRemaining,
			DailyLimit:         key.DailyTokenQuota,
			DailyUsed:          dailyUsed,
			DailyRemaining:     dailyRemaining,
			EffectiveRemaining: effective,
			Unlimited:          unlimited,
		},
		BalanceInfos: []BalanceCurrencyInfo{
			{
				Currency:        "TOKEN",
				TotalBalance:    balanceText,
				GrantedBalance:  grantedText,
				ToppedUpBalance: "0",
			},
		},
		FetchedAt: now.UTC().Format(time.RFC3339),
	}
}
