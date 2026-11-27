package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/openai"
	"bohrium-club-auto/internal/store"
)

// gatewayAPIKeyView combines persisted policy with live derived status and
// SQLite-backed usage counters. Anonymous embedding keeps the existing key JSON
// fields flat and backward compatible.
type gatewayAPIKeyView struct {
	config.APIKey
	Status              string     `json:"status"`
	RequestCount        int64      `json:"requestCount"`
	TotalTokens         int64      `json:"totalTokens"`
	TodayRequests       int64      `json:"todayRequests"`
	TodayTokens         int64      `json:"todayTokens"`
	LastUsedAt          *time.Time `json:"lastUsedAt,omitempty"`
	RemainingQuota      *int64     `json:"remainingQuota,omitempty"`
	RemainingDailyQuota *int64     `json:"remainingDailyQuota,omitempty"`
}

func deriveGatewayKeyStatus(key config.APIKey, usage store.GatewayKeyUsage, now time.Time) string {
	if !key.Enabled {
		return "disabled"
	}
	if key.Expired(now) {
		return "expired"
	}
	if key.TokenQuota > 0 && usage.TotalTokens >= key.TokenQuota {
		return "quota_exhausted"
	}
	if key.DailyTokenQuota > 0 && usage.TodayTokens >= key.DailyTokenQuota {
		return "quota_exhausted"
	}
	return "active"
}

func (s *Server) gatewayAPIKeyViews(keys []config.APIKey) []gatewayAPIKeyView {
	now := time.Now().UTC()
	usageByKey, _ := s.store.GatewayKeyUsageSummary(now, s.cfg.Get().Schedule.Timezone)
	views := make([]gatewayAPIKeyView, 0, len(keys))
	for _, key := range keys {
		key.AllowedModels = append([]string(nil), key.AllowedModels...)
		usage := usageByKey[key.ID]
		view := gatewayAPIKeyView{
			APIKey:        key,
			Status:        deriveGatewayKeyStatus(key, usage, now),
			RequestCount:  usage.RequestCount,
			TotalTokens:   usage.TotalTokens,
			TodayRequests: usage.TodayRequests,
			TodayTokens:   usage.TodayTokens,
			LastUsedAt:    usage.LastUsedAt,
		}
		if key.TokenQuota > 0 {
			remaining := max(int64(0), key.TokenQuota-usage.TotalTokens)
			view.RemainingQuota = &remaining
		}
		if key.DailyTokenQuota > 0 {
			remaining := max(int64(0), key.DailyTokenQuota-usage.TodayTokens)
			view.RemainingDailyQuota = &remaining
		}
		views = append(views, view)
	}
	return views
}

func (s *Server) handleGatewayListKeys(w http.ResponseWriter, r *http.Request) {
	keys := s.gatewayAPIKeyViews(s.cfg.Get().Gateway.APIKeys)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apiKeys": keys, "total": len(keys)})
}

type gatewayAPIKeyRequest struct {
	Name             *string   `json:"name"`
	Enabled          *bool     `json:"enabled"`
	DefaultModel     *string   `json:"defaultModel"`
	RestrictModels   *bool     `json:"restrictModels"`
	AllowedModels    *[]string `json:"allowedModels"`
	RateLimit        *int      `json:"rateLimit"`
	ConcurrencyLimit *int      `json:"concurrencyLimit"`
	ExpiresAt        *string   `json:"expiresAt"`
	TokenQuota       *int64    `json:"tokenQuota"`
	DailyTokenQuota  *int64    `json:"dailyTokenQuota"`
}

type gatewayKeyNotFoundError struct{}

func (e *gatewayKeyNotFoundError) Error() string { return "API Key 不存在" }

func normalizeAllowedModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		modelID := config.NormalizeModelID(raw)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		out = append(out, modelID)
	}
	return out
}

func applyGatewayAPIKeyRequest(key *config.APIKey, body gatewayAPIKeyRequest, now time.Time) error {
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			return &badInputError{"Key 备注名不能为空"}
		}
		if len([]rune(name)) > 80 {
			return &badInputError{"Key 备注名不能超过 80 个字符"}
		}
		key.Name = name
	}
	if body.Enabled != nil {
		key.Enabled = *body.Enabled
	}
	if body.DefaultModel != nil {
		key.DefaultModel = config.NormalizeModelID(*body.DefaultModel)
	}
	if body.RestrictModels != nil {
		key.RestrictModels = *body.RestrictModels
	}
	if body.AllowedModels != nil {
		if len(*body.AllowedModels) > 200 {
			return &badInputError{"单个 Key 最多配置 200 个模型"}
		}
		key.AllowedModels = normalizeAllowedModels(*body.AllowedModels)
	}
	if key.RestrictModels && len(key.AllowedModels) == 0 {
		return &badInputError{"启用模型白名单后至少需要一个模型；如需暂停调用请直接停用 Key"}
	}
	if key.RestrictModels && key.DefaultModel != "" && !key.AllowsModel(key.DefaultModel) {
		return &badInputError{"默认模型必须包含在该 Key 的模型白名单中"}
	}
	if body.RateLimit != nil {
		if *body.RateLimit < 0 || *body.RateLimit > 1_000_000 {
			return &badInputError{"每分钟请求数需在 0 ~ 1000000 之间"}
		}
		key.RateLimit = *body.RateLimit
	}
	if body.ConcurrencyLimit != nil {
		if *body.ConcurrencyLimit < 0 || *body.ConcurrencyLimit > 100_000 {
			return &badInputError{"并发数需在 0 ~ 100000 之间"}
		}
		key.ConcurrencyLimit = *body.ConcurrencyLimit
	}
	if body.ExpiresAt != nil {
		raw := strings.TrimSpace(*body.ExpiresAt)
		if raw == "" {
			key.ExpiresAt = ""
		} else {
			expiresAt, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return &badInputError{"过期时间必须是有效的 RFC3339 时间"}
			}
			key.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
		}
	}
	if body.TokenQuota != nil {
		if *body.TokenQuota < 0 {
			return &badInputError{"总 Token 额度不能为负数；0 表示不限"}
		}
		key.TokenQuota = *body.TokenQuota
	}
	if body.DailyTokenQuota != nil {
		if *body.DailyTokenQuota < 0 {
			return &badInputError{"每日 Token 额度不能为负数；0 表示不限"}
		}
		key.DailyTokenQuota = *body.DailyTokenQuota
	}
	key.UpdatedAt = now.UTC().Format(time.RFC3339)
	return nil
}

func decodeGatewayAPIKeyRequest(r *http.Request) (gatewayAPIKeyRequest, error) {
	var body gatewayAPIKeyRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&body); err != nil {
		return body, &badInputError{"无效请求体"}
	}
	return body, nil
}

func writeGatewayKeyMutationError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var badInput *badInputError
	var notFound *gatewayKeyNotFoundError
	if errors.As(err, &badInput) {
		status = http.StatusBadRequest
	} else if errors.As(err, &notFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
}

func (s *Server) handleGatewayCreateKey(w http.ResponseWriter, r *http.Request) {
	body, err := decodeGatewayAPIKeyRequest(r)
	if err != nil {
		writeGatewayKeyMutationError(w, err)
		return
	}
	now := time.Now().UTC()
	name := "API Key " + now.Format("01-02 15:04")
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		name = strings.TrimSpace(*body.Name)
	}
	newKey := config.APIKey{
		ID:        openai.RandomID("key_"),
		Name:      name,
		Key:       config.GenerateAPIKey(),
		CreatedAt: now.Format(time.RFC3339),
		Enabled:   true,
	}
	body.Name = &name
	if err := applyGatewayAPIKeyRequest(&newKey, body, now); err != nil {
		writeGatewayKeyMutationError(w, err)
		return
	}
	updated, err := s.cfg.Update(func(c *config.Config) error {
		c.Gateway.APIKeys = append(c.Gateway.APIKeys, newKey)
		return nil
	})
	if err != nil {
		writeGatewayKeyMutationError(w, err)
		return
	}
	views := s.gatewayAPIKeyViews(updated.Gateway.APIKeys)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": views[len(views)-1], "apiKeys": views})
}

func (s *Server) handleGatewayUpdateKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	body, err := decodeGatewayAPIKeyRequest(r)
	if err != nil {
		writeGatewayKeyMutationError(w, err)
		return
	}
	updated, err := s.cfg.Update(func(c *config.Config) error {
		for i := range c.Gateway.APIKeys {
			if c.Gateway.APIKeys[i].ID == id {
				return applyGatewayAPIKeyRequest(&c.Gateway.APIKeys[i], body, time.Now().UTC())
			}
		}
		return &gatewayKeyNotFoundError{}
	})
	if err != nil {
		writeGatewayKeyMutationError(w, err)
		return
	}
	views := s.gatewayAPIKeyViews(updated.Gateway.APIKeys)
	var current *gatewayAPIKeyView
	for i := range views {
		if views[i].ID == id {
			current = &views[i]
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": current, "apiKeys": views})
}

func (s *Server) handleGatewayRegenerateKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	updated, err := s.cfg.Update(func(c *config.Config) error {
		for i := range c.Gateway.APIKeys {
			if c.Gateway.APIKeys[i].ID == id {
				c.Gateway.APIKeys[i].Key = config.GenerateAPIKey()
				c.Gateway.APIKeys[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				return nil
			}
		}
		return &gatewayKeyNotFoundError{}
	})
	if err != nil {
		writeGatewayKeyMutationError(w, err)
		return
	}
	views := s.gatewayAPIKeyViews(updated.Gateway.APIKeys)
	var current *gatewayAPIKeyView
	for i := range views {
		if views[i].ID == id {
			current = &views[i]
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": current, "apiKeys": views})
}

func (s *Server) handleGatewayDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	updated, err := s.cfg.Update(func(c *config.Config) error {
		next := make([]config.APIKey, 0, len(c.Gateway.APIKeys))
		removed := false
		for _, key := range c.Gateway.APIKeys {
			if key.ID == id {
				removed = true
				continue
			}
			next = append(next, key)
		}
		if !removed {
			return &gatewayKeyNotFoundError{}
		}
		c.Gateway.APIKeys = next
		return nil
	})
	if err != nil {
		writeGatewayKeyMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "deleted": true, "apiKeys": s.gatewayAPIKeyViews(updated.Gateway.APIKeys),
		"message": fmt.Sprintf("已删除 API Key %s", id),
	})
}
