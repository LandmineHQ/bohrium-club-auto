package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/openai"
	"bohrium-club-auto/internal/prompts"
	"bohrium-club-auto/internal/store"
)

func (s *Server) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	gwCfg := s.cfg.Get().Gateway
	allViews, summary := s.buildAccountViews()
	poolViews, poolTotalEff, _ := s.gateway.Router().PoolSummary()
	bindings, _ := s.store.ListSessionBindings(time.Now().UTC(), 100)

	// Partition non-pooled candidate accounts for the left drag-and-drop column.
	candidates := make([]AccountSummaryView, 0)
	for _, v := range allViews {
		if !v.PoolEnabled {
			candidates = append(candidates, v)
		}
	}

	var (
		catalog    *bohrium.SciXModelCatalog
		catalogErr string
	)
	if len(allViews) > 0 {
		cat, err := s.gateway.ListCatalog(ctx, false)
		if err != nil {
			catalogErr = err.Error()
		} else {
			catalog = cat
		}
	}

	modelEnabled := gwCfg.ModelEnabled
	if modelEnabled == nil {
		modelEnabled = map[string]bool{}
	}

	pgPrompts, _ := prompts.PlaygroundPrompts()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"bohriumLoggedIn": len(allViews) > 0,
		"config": map[string]any{
			"enabled":                gwCfg.Enabled,
			"defaultModel":           gwCfg.DefaultModel,
			"allowNoAuth":            gwCfg.AllowNoAuth,
			"corsOrigins":            gwCfg.CORSOrigins,
			"preflightMode":          gwCfg.PreflightMode,
			"sciMasterPrompt":        gwCfg.SciMasterPrompt,
			"analyticsRetentionDays": gwCfg.AnalyticsRetentionDays,
			"storeId":                gwCfg.StoreID,
			"apiKeys":                gwCfg.APIKeys,
			"modelEnabled":           modelEnabled,
		},
		"pool": map[string]any{
			"pooled":                poolViews,
			"candidates":            candidates,
			"totalEffectiveBalance": poolTotalEff,
			"activeBindingCount":    len(bindings),
			"ttlDays":               7,
		},
		"accounts":          allViews,
		"accountSummary":    summary,
		"bindings":          bindings,
		"catalog":           catalog,
		"catalogError":      catalogErr,
		"stats":             s.gateway.Stats(),
		"playgroundPrompts": pgPrompts,
	})
}

// handleGatewaySetPool atomically updates which accounts belong to the OpenAI routing pool
// and their order (directly backing drag-and-drop pool management).
func (s *Server) handleGatewaySetPool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountIDs []string `json:"accountIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	if err := s.store.SetPoolMembership(body.AccountIDs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Quickly fetch initial balance for any newly pooled account that has no snapshot yet.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	s.mgr.RefreshStaleBalances(ctx, 24*time.Hour)

	allViews, summary := s.buildAccountViews()
	poolViews, poolTotalEff, _ := s.gateway.Router().PoolSummary()
	candidates := make([]AccountSummaryView, 0)
	for _, v := range allViews {
		if !v.PoolEnabled {
			candidates = append(candidates, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"pool": map[string]any{
			"pooled":                poolViews,
			"candidates":            candidates,
			"totalEffectiveBalance": poolTotalEff,
			"ttlDays":               7,
		},
		"accounts":       allViews,
		"accountSummary": summary,
		"message":        "OpenAI 路由池已更新",
	})
}

func (s *Server) handleGatewayListBindings(w http.ResponseWriter, r *http.Request) {
	bindings, err := s.store.ListSessionBindings(time.Now().UTC(), 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"ttlDays":  7,
		"bindings": bindings,
	})
}

func (s *Server) handleGatewayDeleteBinding(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimSpace(r.PathValue("hash"))
	deleted, err := s.store.DeleteSessionBinding(hash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "绑定记录不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true})
}

func (s *Server) handleGatewayClearBindings(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.ClearSessionBindings()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": n})
}

func (s *Server) handleGatewayRefreshModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cat, err := s.gateway.ListCatalog(ctx, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": cat})
}

func (s *Server) handleGatewayUpdateModel(w http.ResponseWriter, r *http.Request) {
	rawID := strings.TrimSpace(r.PathValue("id"))
	rawID = strings.TrimPrefix(rawID, "scix/")
	key := strings.ToLower(strings.TrimSpace(rawID))
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "模型 ID 不能为空"})
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请提供有效的 enabled 布尔值"})
		return
	}
	updated, err := s.cfg.Update(func(c *config.Config) error {
		if c.Gateway.ModelEnabled == nil {
			c.Gateway.ModelEnabled = make(map[string]bool)
		}
		c.Gateway.ModelEnabled[key] = *body.Enabled
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"modelId":      rawID,
		"enabled":      *body.Enabled,
		"modelEnabled": updated.Gateway.ModelEnabled,
	})
}

func (s *Server) handleGatewayCreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "API Key " + time.Now().Format("01-02 15:04")
	}
	newKey := config.APIKey{
		ID:        openai.RandomID("key_"),
		Name:      name,
		Key:       config.GenerateAPIKey(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Enabled:   true,
	}
	updated, err := s.cfg.Update(func(c *config.Config) error {
		c.Gateway.APIKeys = append(c.Gateway.APIKeys, newKey)
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"key":     newKey,
		"apiKeys": updated.Gateway.APIKeys,
	})
}

func (s *Server) handleGatewayUpdateKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	found := false
	updated, err := s.cfg.Update(func(c *config.Config) error {
		for i := range c.Gateway.APIKeys {
			if c.Gateway.APIKeys[i].ID == id {
				found = true
				if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
					c.Gateway.APIKeys[i].Name = strings.TrimSpace(*body.Name)
				}
				if body.Enabled != nil {
					c.Gateway.APIKeys[i].Enabled = *body.Enabled
				}
				break
			}
		}
		if !found {
			return &badInputError{"API Key 不存在"}
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apiKeys": updated.Gateway.APIKeys})
}

func (s *Server) handleGatewayDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	updated, err := s.cfg.Update(func(c *config.Config) error {
		next := make([]config.APIKey, 0, len(c.Gateway.APIKeys))
		removed := false
		for _, k := range c.Gateway.APIKeys {
			if k.ID == id {
				removed = true
				continue
			}
			next = append(next, k)
		}
		if !removed {
			return &badInputError{"API Key 不存在"}
		}
		c.Gateway.APIKeys = next
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apiKeys": updated.Gateway.APIKeys})
}

func (s *Server) handleGatewayUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled                *bool           `json:"enabled"`
		DefaultModel           *string         `json:"defaultModel"`
		AllowNoAuth            *bool           `json:"allowNoAuth"`
		CORSOrigins            *string         `json:"corsOrigins"`
		PreflightMode          *bool           `json:"preflightMode"`
		SciMasterPrompt        *bool           `json:"sciMasterPrompt"`
		AnalyticsRetentionDays *int            `json:"analyticsRetentionDays"`
		ModelEnabled           map[string]bool `json:"modelEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	updated, err := s.cfg.Update(func(c *config.Config) error {
		if body.Enabled != nil {
			c.Gateway.Enabled = *body.Enabled
		}
		if body.DefaultModel != nil && strings.TrimSpace(*body.DefaultModel) != "" {
			c.Gateway.DefaultModel = strings.TrimSpace(*body.DefaultModel)
		}
		if body.AllowNoAuth != nil {
			c.Gateway.AllowNoAuth = *body.AllowNoAuth
		}
		if body.CORSOrigins != nil {
			c.Gateway.CORSOrigins = strings.TrimSpace(*body.CORSOrigins)
		}
		if body.PreflightMode != nil {
			c.Gateway.PreflightMode = *body.PreflightMode
		}
		if body.SciMasterPrompt != nil {
			c.Gateway.SciMasterPrompt = *body.SciMasterPrompt
		}
		if body.ModelEnabled != nil {
			nextMap := make(map[string]bool, len(body.ModelEnabled))
			for k, v := range body.ModelEnabled {
				nk := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(k), "scix/"))
				if nk != "" {
					nextMap[nk] = v
				}
			}
			c.Gateway.ModelEnabled = nextMap
		}
		if body.AnalyticsRetentionDays != nil {
			d := *body.AnalyticsRetentionDays
			if d < 1 || d > 3650 {
				return &badInputError{"数据保留天数需在 1 ~ 3650 天之间"}
			}
			c.Gateway.AnalyticsRetentionDays = d
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var purged int64
	if body.AnalyticsRetentionDays != nil {
		purged, _ = s.store.PurgeExpiredGatewayUsage(time.Now().UTC(), updated.Gateway.AnalyticsRetentionDays)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"config":  updated.Gateway,
		"purged":  purged,
		"message": "OpenAI 网关配置已保存并立即生效",
	})
}

func (s *Server) handleGatewayAnalytics(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	retDays := cfg.Gateway.AnalyticsRetentionDays
	if retDays <= 0 {
		retDays = config.DefaultAnalyticsRetentionDays
	}

	rangeDays := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 3650 {
			rangeDays = n
		}
	}
	granularity := strings.TrimSpace(r.URL.Query().Get("granularity"))
	apiKeyID := strings.TrimSpace(r.URL.Query().Get("apiKeyId"))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))

	analytics, err := s.store.QueryGatewayAnalytics(store.UsageQueryOptions{
		RangeDays:   rangeDays,
		Granularity: granularity,
		APIKeyID:    apiKeyID,
		Model:       model,
		AccountID:   accountID,
		Timezone:    cfg.Schedule.Timezone,
		Now:         time.Now().UTC(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Merge currently configured API keys so users can filter/see names cleanly even before or after renames.
	keyNameMap := make(map[string]string, len(cfg.Gateway.APIKeys)+2)
	keyNameMap["playground"] = "控制台调试"
	keyNameMap["public"] = "无鉴权访问 (Public)"
	seenKeys := make(map[string]bool, len(analytics.AvailableKeys)+len(cfg.Gateway.APIKeys))
	for _, k := range cfg.Gateway.APIKeys {
		if strings.TrimSpace(k.Name) != "" {
			keyNameMap[k.ID] = strings.TrimSpace(k.Name)
		}
	}
	for i := range analytics.AvailableKeys {
		id := analytics.AvailableKeys[i].ID
		seenKeys[id] = true
		if n, ok := keyNameMap[id]; ok {
			analytics.AvailableKeys[i].Name = n
		}
	}
	for _, k := range cfg.Gateway.APIKeys {
		if !seenKeys[k.ID] {
			name := strings.TrimSpace(k.Name)
			if name == "" {
				name = k.ID
			}
			analytics.AvailableKeys = append(analytics.AvailableKeys, store.FilterOption{ID: k.ID, Name: name})
			seenKeys[k.ID] = true
		}
	}
	for i := range analytics.ByKey {
		if n, ok := keyNameMap[analytics.ByKey[i].ID]; ok {
			analytics.ByKey[i].Name = n
		}
	}
	for i := range analytics.ByKeyModel {
		if n, ok := keyNameMap[analytics.ByKeyModel[i].APIKeyID]; ok {
			analytics.ByKeyModel[i].APIKeyName = n
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                     true,
		"analyticsRetentionDays": retDays,
		"analytics":              analytics,
	})
}

func (s *Server) handleGatewayAnalyticsLogs(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	q := r.URL.Query()
	rangeDays := 30
	if raw := strings.TrimSpace(q.Get("days")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 3650 {
			rangeDays = n
		}
	}
	page, _ := strconv.Atoi(q.Get("page"))
	pageSize, _ := strconv.Atoi(q.Get("pageSize"))

	res, err := s.store.ListGatewayUsageLogs(store.UsageLogsQueryOptions{
		RangeDays: rangeDays,
		APIKeyID:  strings.TrimSpace(q.Get("apiKeyId")),
		Model:     strings.TrimSpace(q.Get("model")),
		AccountID: strings.TrimSpace(q.Get("accountId")),
		Status:    strings.TrimSpace(q.Get("status")),
		Stream:    strings.TrimSpace(q.Get("stream")),
		Query:     strings.TrimSpace(q.Get("q")),
		Timezone:  cfg.Schedule.Timezone,
		Now:       time.Now().UTC(),
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// Map current API key names so renamed keys display consistently.
	keyNameMap := map[string]string{
		"playground": "控制台调试",
		"public":     "无鉴权访问 (Public)",
	}
	for _, k := range cfg.Gateway.APIKeys {
		if strings.TrimSpace(k.Name) != "" {
			keyNameMap[k.ID] = strings.TrimSpace(k.Name)
		}
	}
	for i := range res.Items {
		if n, ok := keyNameMap[res.Items[i].APIKeyID]; ok {
			res.Items[i].APIKeyName = n
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"items":      res.Items,
		"total":      res.Total,
		"page":       res.Page,
		"pageSize":   res.PageSize,
		"totalPages": res.TotalPages,
	})
}

func (s *Server) handleGatewayPurgeAnalytics(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClearAll bool `json:"clearAll"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var (
		purged int64
		err    error
	)
	if body.ClearAll {
		purged, err = s.store.ClearGatewayUsage()
	} else {
		retDays := s.cfg.Get().Gateway.AnalyticsRetentionDays
		if retDays <= 0 {
			retDays = config.DefaultAnalyticsRetentionDays
		}
		purged, err = s.store.PurgeExpiredGatewayUsage(time.Now().UTC(), retDays)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"purged": purged,
	})
}

func (s *Server) handleGatewayPreflight(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID string `json:"accountId"`
		Model     string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	allowed, reason, accName, err := s.gateway.Preflight(ctx, body.AccountID, body.Model)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"allowed":     allowed,
		"reason":      reason,
		"accountName": accName,
	})
}

// handleGatewayPlayground lets an authenticated admin test /v1/chat/completions or /v1/responses
// directly from the Web Console using either automatic pool routing or an explicit account override.
func (s *Server) handleGatewayPlayground(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID       string `json:"accountId"` // empty = automatic weighted pool routing
		Endpoint        string `json:"endpoint"`  // "chat" | "responses"
		Model           string `json:"model"`
		Prompt          string `json:"prompt"`
		System          string `json:"system"`
		ReasoningEffort string `json:"reasoningEffort"`
		Stream          bool   `json:"stream"`
		MaxTokens       int    `json:"maxTokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		if rnd, pErr := prompts.RandomPlaygroundPrompt(); pErr == nil && rnd != "" {
			body.Prompt = rnd
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请输入测试提示词"})
			return
		}
	}
	if body.MaxTokens <= 0 {
		body.MaxTokens = 1024
	}

	var (
		targetPath string
		reqPayload map[string]any
	)
	if strings.EqualFold(body.Endpoint, "responses") {
		targetPath = "/v1/responses"
		reqPayload = map[string]any{
			"model":             body.Model,
			"input":             body.Prompt,
			"stream":            body.Stream,
			"max_output_tokens": body.MaxTokens,
		}
		if strings.TrimSpace(body.System) != "" {
			reqPayload["instructions"] = strings.TrimSpace(body.System)
		}
		if strings.TrimSpace(body.ReasoningEffort) != "" {
			reqPayload["reasoning"] = map[string]any{"effort": strings.TrimSpace(body.ReasoningEffort)}
		}
	} else {
		targetPath = "/v1/chat/completions"
		msgs := make([]map[string]any, 0, 2)
		if strings.TrimSpace(body.System) != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": strings.TrimSpace(body.System)})
		}
		msgs = append(msgs, map[string]any{"role": "user", "content": body.Prompt})
		reqPayload = map[string]any{
			"model":      body.Model,
			"messages":   msgs,
			"stream":     body.Stream,
			"max_tokens": body.MaxTokens,
		}
		if strings.TrimSpace(body.ReasoningEffort) != "" {
			reqPayload["reasoning_effort"] = strings.TrimSpace(body.ReasoningEffort)
		}
	}

	b, _ := json.Marshal(reqPayload)
	subCtx := openai.WithCallerKeyIdentity(r.Context(), "playground", "控制台调试")
	if strings.TrimSpace(body.AccountID) != "" {
		subCtx = openai.WithExplicitAccountID(subCtx, body.AccountID)
	}
	subReq, err := http.NewRequestWithContext(subCtx, http.MethodPost, targetPath, bytes.NewReader(b))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	subReq.RemoteAddr = r.RemoteAddr
	subReq.Header.Set("Content-Type", "application/json")
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		subReq.Header.Set("X-Forwarded-For", xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		subReq.Header.Set("X-Real-IP", xri)
	}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		subReq.Header.Set("User-Agent", ua)
	} else {
		subReq.Header.Set("User-Agent", "Bohrium-Console-Playground")
	}
	gwCfg := s.cfg.Get().Gateway
	for _, k := range gwCfg.APIKeys {
		if k.Enabled && k.Key != "" {
			subReq.Header.Set("Authorization", "Bearer "+k.Key)
			break
		}
	}
	s.mux.ServeHTTP(w, subReq)
}
