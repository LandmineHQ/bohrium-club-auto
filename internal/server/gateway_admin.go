package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/openai"
	"bohrium-club-auto/internal/prompts"
)

func (s *Server) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	gwCfg := s.cfg.Get().Gateway
	auth := s.store.GetAuth()
	var (
		catalog    *bohrium.SciXModelCatalog
		catalogErr string
	)
	if auth.Token != "" {
		cat, err := s.gateway.ListCatalog(ctx, false)
		if err != nil {
			catalogErr = err.Error()
		} else {
			catalog = cat
		}
	}

	pgPrompts, _ := prompts.PlaygroundPrompts()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"bohriumLoggedIn":   auth.Token != "",
		"config": map[string]any{
			"enabled":       gwCfg.Enabled,
			"defaultModel":  gwCfg.DefaultModel,
			"allowNoAuth":   gwCfg.AllowNoAuth,
			"corsOrigins":   gwCfg.CORSOrigins,
			"preflightMode": gwCfg.PreflightMode,
			"storeId":       gwCfg.StoreID,
			"apiKeys":       gwCfg.APIKeys,
		},
		"catalog":           catalog,
		"catalogError":      catalogErr,
		"stats":             s.gateway.Stats(),
		"playgroundPrompts": pgPrompts,
	})
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
		Enabled       *bool   `json:"enabled"`
		DefaultModel  *string `json:"defaultModel"`
		AllowNoAuth   *bool   `json:"allowNoAuth"`
		CORSOrigins   *string `json:"corsOrigins"`
		PreflightMode *bool   `json:"preflightMode"`
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
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"config":  updated.Gateway,
		"message": "OpenAI 网关配置已保存并立即生效",
	})
}

func (s *Server) handleGatewayPreflight(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	allowed, reason, err := s.gateway.Preflight(ctx, body.Model)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"allowed": allowed,
		"reason":  reason,
	})
}

// handleGatewayPlayground lets an authenticated admin test /v1/chat/completions or /v1/responses
// directly from the Web Console without needing to pass an API key manually.
func (s *Server) handleGatewayPlayground(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint        string `json:"endpoint"` // "chat" | "responses"
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
	subReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetPath, bytes.NewReader(b))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	subReq.Header.Set("Content-Type", "application/json")
	// Inject first enabled API key for internal dispatch through the real /v1/* route.
	gwCfg := s.cfg.Get().Gateway
	for _, k := range gwCfg.APIKeys {
		if k.Enabled && k.Key != "" {
			subReq.Header.Set("Authorization", "Bearer "+k.Key)
			break
		}
	}
	s.mux.ServeHTTP(w, subReq)
}
