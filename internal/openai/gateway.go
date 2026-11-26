package openai

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/config"
)

const (
	maxRequestBodyBytes = 32 * 1024 * 1024 // 32 MiB to accommodate multimodal base64 images
	maxFailoverAttempts = 3
)

type ctxKey string

const (
	ctxKeyExplicitAccount ctxKey = "bohrium_explicit_account_id"
	ctxKeyAPIKeyID        ctxKey = "bohrium_api_key_id"
	ctxKeyAPIKeyName      ctxKey = "bohrium_api_key_name"
)

// WithExplicitAccountID attaches an internal-only account override (used by Admin Playground).
// External /v1/* callers cannot set this context value and always use the weighted OpenAI routing pool.
func WithExplicitAccountID(ctx context.Context, accountID string) context.Context {
	return context.WithValue(ctx, ctxKeyExplicitAccount, strings.TrimSpace(accountID))
}

// WithCallerKeyIdentity overrides the API key identity recorded in analytics (used by Admin Playground).
func WithCallerKeyIdentity(ctx context.Context, keyID, keyName string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyAPIKeyID, strings.TrimSpace(keyID))
	return context.WithValue(ctx, ctxKeyAPIKeyName, strings.TrimSpace(keyName))
}

func explicitAccountFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyExplicitAccount).(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func apiKeyScopeFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyAPIKeyID).(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return "default"
}

func apiKeyIdentityFromCtx(ctx context.Context) (string, string) {
	id := apiKeyScopeFromCtx(ctx)
	name := ""
	if v, ok := ctx.Value(ctxKeyAPIKeyName).(string); ok {
		name = strings.TrimSpace(v)
	}
	if name == "" {
		name = id
	}
	return id, name
}

// Gateway exposes full OpenAI-compatible /v1/models, /v1/balance, /v1/chat/completions, and /v1/responses endpoints
// backed by a multi-account weighted-balance routing pool over Bohrium desktop client's native Sigma LLM protocol.
type Gateway struct {
	cfg     *config.Manager
	mgr     *bohrium.Manager
	router  *PoolRouter
	respDir *ResponseStore
}

// NewGateway constructs a multi-account OpenAI-compatible Bohrium gateway.
func NewGateway(cfg *config.Manager, mgr *bohrium.Manager) *Gateway {
	return &Gateway{
		cfg:     cfg,
		mgr:     mgr,
		router:  NewPoolRouter(mgr),
		respDir: OpenResponseStore(mgr.Store()),
	}
}

// Router returns the underlying multi-account PoolRouter.
func (g *Gateway) Router() *PoolRouter {
	return g.router
}

// RegisterRoutes mounts all OpenAI /v1/* endpoints onto mux.
func (g *Gateway) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("OPTIONS /v1/", g.handleCORS)
	mux.HandleFunc("GET /v1/models", g.withMiddleware(g.handleListModels))
	mux.HandleFunc("GET /v1/models/{id...}", g.withMiddleware(g.handleGetModel))
	mux.HandleFunc("GET /v1/balance", g.withMiddleware(g.handleGetBalance))
	mux.HandleFunc("POST /v1/chat/completions", g.withMiddleware(g.handleChatCompletions))
	mux.HandleFunc("POST /v1/responses", g.withMiddleware(g.handleCreateResponse))
	mux.HandleFunc("GET /v1/responses/{id}/input_items", g.withMiddleware(g.handleGetResponseInputItems))
	mux.HandleFunc("GET /v1/responses/{id}", g.withMiddleware(g.handleGetResponse))
	mux.HandleFunc("DELETE /v1/responses/{id}", g.withMiddleware(g.handleDeleteResponse))
}

// Stats returns live gateway request & token telemetry for the Web Console.
func (g *Gateway) Stats() StatsSnapshot {
	return g.respDir.Stats()
}

// ListCatalog returns the live Bohrium SciX model catalog (for Web Console & admin API).
func (g *Gateway) ListCatalog(ctx context.Context, forceRefresh bool) (*bohrium.SciXModelCatalog, error) {
	cli, _, err := g.router.SelectCatalogClient(true)
	if err != nil {
		return nil, err
	}
	return cli.GetSciXModelCatalog(ctx, forceRefresh)
}

// Preflight runs Bohrium's /billing/preflight admission check for a model using either a specific account or a pooled account.
func (g *Gateway) Preflight(ctx context.Context, accountID, modelID string) (bool, string, string, error) {
	var (
		cli     *bohrium.Client
		accName string
	)
	if strings.TrimSpace(accountID) != "" {
		acc, ok, err := g.mgr.Store().GetAccount(accountID)
		if err != nil || !ok {
			return false, "", "", fmt.Errorf("账号不存在: %s", accountID)
		}
		cli = g.mgr.ForAccount(acc.ID)
		accName = acc.DisplayName()
	} else {
		c, acc, err := g.router.SelectCatalogClient(false)
		if err != nil {
			return false, "", "", err
		}
		cli = c
		if acc != nil {
			accName = acc.DisplayName()
		}
	}
	resolved, _, err := cli.ResolveSciXModel(ctx, modelID)
	if err != nil {
		return false, "", accName, err
	}
	allowed, reason, err := cli.SciXBillingPreflight(ctx, resolved.ID, bohrium.NewTraceparent())
	return allowed, reason, accName, err
}

func (g *Gateway) applyCORS(w http.ResponseWriter) {
	origins := "*"
	if g.cfg != nil {
		if v := strings.TrimSpace(g.cfg.Get().Gateway.CORSOrigins); v != "" {
			origins = v
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", origins)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, x-api-key, api-key, OpenAI-Beta, x-scix-execution, x-session-id, traceparent, X-Refresh-Models")
	w.Header().Set("Access-Control-Expose-Headers", "x-request-id, openai-request-id, x-bohrium-routing-mode")
}

func (g *Gateway) handleCORS(w http.ResponseWriter, r *http.Request) {
	g.applyCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) withMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.applyCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		gwCfg := g.cfg.Get().Gateway
		if !gwCfg.Enabled {
			WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "gateway_disabled", "OpenAI gateway is disabled in configuration")
			return
		}
		keyID, keyName := "", ""
		if existingID, ok := r.Context().Value(ctxKeyAPIKeyID).(string); ok && strings.TrimSpace(existingID) != "" {
			keyID, keyName = apiKeyIdentityFromCtx(r.Context())
		}
		if keyID == "" {
			key := extractAPIKey(r)
			if !gwCfg.AllowNoAuth {
				matchedID, matchedName, ok := validateAPIKey(key, gwCfg.APIKeys)
				if !ok {
					WriteError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Invalid or missing API key; provide Authorization: Bearer <sk-brm-...>")
					return
				}
				keyID, keyName = matchedID, matchedName
			} else {
				if matchedID, matchedName, ok := validateAPIKey(key, gwCfg.APIKeys); ok {
					keyID, keyName = matchedID, matchedName
				} else {
					keyID, keyName = "public", "无鉴权访问 (Public)"
				}
			}
		}
		ctx := WithCallerKeyIdentity(r.Context(), keyID, keyName)
		next(w, r.WithContext(ctx))
	}
}

func extractAPIKey(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
			return strings.TrimSpace(auth[7:])
		}
		return auth
	}
	if k := strings.TrimSpace(r.Header.Get("x-api-key")); k != "" {
		return k
	}
	if k := strings.TrimSpace(r.Header.Get("api-key")); k != "" {
		return k
	}
	return ""
}

func validateAPIKey(candidate string, keys []config.APIKey) (string, string, bool) {
	if candidate == "" {
		return "", "", false
	}
	cBytes := []byte(candidate)
	matchedID := ""
	matchedName := ""
	matched := false
	for _, k := range keys {
		if !k.Enabled || k.Key == "" {
			continue
		}
		if subtle.ConstantTimeCompare(cBytes, []byte(k.Key)) == 1 {
			matched = true
			matchedID = k.ID
			matchedName = strings.TrimSpace(k.Name)
			if matchedName == "" {
				matchedName = k.ID
			}
		}
	}
	return matchedID, matchedName, matched
}

type modelDisabledError struct {
	modelID string
	msg     string
}

func (e *modelDisabledError) Error() string {
	return e.msg
}

// IsModelEnabled reports whether m is active on the OpenAI-compatible gateway.
func (g *Gateway) IsModelEnabled(m bohrium.SciXModel) bool {
	if g.cfg == nil {
		return m.DefaultGatewayEnabled()
	}
	return g.cfg.Get().Gateway.IsModelEnabled(m.ID, m.DefaultGatewayEnabled())
}

func normalizeUniqueStrings(values []string, lower bool) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func resolveModelDisplayMetadata(m bohrium.SciXModel) (name, description string) {
	if m.Presentation != nil {
		if n := strings.TrimSpace(m.Presentation.Name); n != "" {
			name = n
		}
		description = strings.TrimSpace(m.Presentation.Description)
	}
	if name == "" {
		name = strings.TrimSpace(m.Name)
	}
	if name == "" {
		name = m.ID
	}
	return name, description
}

func toOpenAIModel(m bohrium.SciXModel) OpenAIModel {
	obj := strings.TrimSpace(m.Object)
	if obj == "" {
		obj = "model"
	}
	ownedBy := strings.TrimSpace(m.OwnedBy)
	if ownedBy == "" {
		ownedBy = "sigma"
	}

	displayName, description := resolveModelDisplayMetadata(m)
	ctxWin := max(0, m.ContextWindow)
	maxOut := max(0, m.MaxOutputTokens)

	inputMods := normalizeUniqueStrings(m.Input, true)
	if len(inputMods) == 0 {
		inputMods = []string{"text"}
	}

	supportedParams := []string{
		"temperature",
		"top_p",
		"max_tokens",
		"max_completion_tokens",
		"stop",
		"presence_penalty",
		"frequency_penalty",
		"seed",
		"logprobs",
		"top_logprobs",
		"logit_bias",
		"response_format",
		"structured_outputs",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
	}

	out := OpenAIModel{
		ID:                     m.ID,
		Object:                 obj,
		Created:                m.Created,
		OwnedBy:                ownedBy,
		Name:                   displayName,
		DisplayName:            displayName,
		Description:            description,
		ContextWindow:          ctxWin,
		ContextLength:          ctxWin,
		MaxModelLen:            ctxWin,
		MaxInputTokens:         ctxWin,
		MaxOutputTokens:        maxOut,
		MaxTokens:              maxOut,
		Input:                  append([]string(nil), inputMods...),
		Architecture: &OpenAIModelArchitecture{
			Modality:         strings.Join(inputMods, "+") + "->text",
			InputModalities:  append([]string(nil), inputMods...),
			OutputModalities: []string{"text"},
		},
		SupportedEndpointTypes: []string{"openai", "openai-response"},
	}

	if ctxWin > 0 || maxOut > 0 {
		out.Limit = &OpenAIModelLimit{
			Context: ctxWin,
			Output:  maxOut,
		}
		out.TopProvider = &OpenAIModelTopProvider{
			ContextLength:       ctxWin,
			MaxCompletionTokens: maxOut,
			IsModerated:         false,
		}
	}

	if m.SupportsReasoning() {
		supportedParams = append(supportedParams, "reasoning", "reasoning_effort", "include_reasoning")
		defaultEffort := strings.TrimSpace(m.ReasoningEffort)
		efforts := normalizeUniqueStrings(m.ReasoningEfforts, false)
		if len(efforts) == 0 && defaultEffort != "" {
			efforts = []string{defaultEffort}
		}
		out.ReasoningEffort = defaultEffort
		out.ReasoningEfforts = append([]string(nil), efforts...)
		out.Thinking = &OpenAIModelThinking{
			Supported: true,
			Levels:    append([]string(nil), efforts...),
			Default:   defaultEffort,
		}
	}
	out.SupportedParameters = supportedParams

	return out
}

// BuildOpenAIModelList returns the GET /v1/models payload containing only enabled models.
func (g *Gateway) BuildOpenAIModelList(cat *bohrium.SciXModelCatalog) OpenAIModelList {
	list := OpenAIModelList{
		Object: "list",
		Data:   []OpenAIModel{},
	}
	if cat == nil {
		return list
	}
	for _, m := range cat.Data {
		if !g.IsModelEnabled(m) {
			continue
		}
		list.Data = append(list.Data, toOpenAIModel(m))
	}
	return list
}

func (g *Gateway) resolveEnabledModel(ctx context.Context, cli *bohrium.Client, requested string) (bohrium.SciXModel, *bohrium.SciXModelCatalog, error) {
	req := strings.TrimSpace(requested)
	cat, err := cli.GetSciXModelCatalog(ctx, false)
	if err != nil {
		return bohrium.SciXModel{}, nil, err
	}

	if req == "" {
		var candidates []string
		if g.cfg != nil {
			if def := strings.TrimSpace(g.cfg.Get().Gateway.DefaultModel); def != "" {
				candidates = append(candidates, def)
			}
		}
		for _, m := range cat.Data {
			if m.Presentation != nil && m.Presentation.Default {
				candidates = append(candidates, m.ID)
			}
		}
		for _, m := range cat.Data {
			if !m.Hidden {
				candidates = append(candidates, m.ID)
			}
		}
		for _, m := range cat.Data {
			candidates = append(candidates, m.ID)
		}
		for _, cand := range candidates {
			if m, ok := cat.FindModel(cand); ok && g.IsModelEnabled(m) {
				return m, cat, nil
			}
		}
		return bohrium.SciXModel{}, cat, &modelDisabledError{msg: "no enabled models available in gateway settings"}
	}

	m, ok := cat.FindModel(req)
	if !ok && time.Since(cat.FetchedAt) > 5*time.Second {
		if fresh, rErr := cli.GetSciXModelCatalog(ctx, true); rErr == nil {
			cat = fresh
			m, ok = cat.FindModel(req)
		}
	}
	if !ok {
		return bohrium.SciXModel{}, cat, fmt.Errorf("model %q not found in Bohrium SciX catalog", requested)
	}
	if !g.IsModelEnabled(m) {
		return bohrium.SciXModel{}, cat, &modelDisabledError{
			modelID: m.ID,
			msg:     fmt.Sprintf("model %q is disabled in gateway settings", m.ID),
		}
	}
	return m, cat, nil
}

func (g *Gateway) handleListModels(w http.ResponseWriter, r *http.Request) {
	force := strings.EqualFold(r.Header.Get("X-Refresh-Models"), "true") || r.URL.Query().Get("refresh") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	cli, _, err := g.router.SelectCatalogClient(false)
	if err != nil {
		writePoolRouteError(w, err)
		return
	}
	cat, err := cli.GetSciXModelCatalog(ctx, force)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_error", "catalog_unavailable", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, g.BuildOpenAIModelList(cat))
}

func (g *Gateway) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	cli, _, err := g.router.SelectCatalogClient(false)
	if err != nil {
		writePoolRouteError(w, err)
		return
	}
	m, _, err := g.resolveEnabledModel(ctx, cli, id)
	if err != nil {
		var disErr *modelDisabledError
		if errors.As(err, &disErr) {
			WriteError(w, http.StatusNotFound, "invalid_request_error", "model_disabled", disErr.Error())
			return
		}
		WriteError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, toOpenAIModel(m))
}

// BalanceResponse represents the OpenAI-gateway /v1/balance aggregated pool response payload.
type BalanceResponse struct {
	Object            string                `json:"object"`
	IsAvailable       bool                  `json:"is_available"`
	Currency          string                `json:"currency"`
	Balance           float64               `json:"balance"`
	PhotonBalance     float64               `json:"photon_balance"`
	OrgBalance        float64               `json:"org_balance"`
	OrgBalanceLimit   float64               `json:"org_balance_limit"`
	WalletBalanceCNY  float64               `json:"wallet_balance_cny"`
	Expiring7DPhotons float64               `json:"expiring_7d_photons"`
	PoolAccountCount  int                   `json:"pool_account_count"`
	HealthyPoolCount  int                   `json:"healthy_pool_count"`
	PoolAccounts      []PoolAccountView     `json:"pool_accounts"`
	Membership        BalanceMembership     `json:"membership"`
	Quotas            BalanceQuotas         `json:"quotas"`
	Storage           BalanceStorage        `json:"storage"`
	ExpiringLots      []bohrium.ExpiringLot `json:"expiring_lots"`
	BalanceInfos      []BalanceCurrencyInfo `json:"balance_infos"`
	FetchedAt         string                `json:"fetched_at"`
	Warnings          []string              `json:"warnings,omitempty"`
}

// BalanceMembership summarizes Bohrium membership tier & periodic benefits.
type BalanceMembership struct {
	Level           any     `json:"level"`
	ExpireAt        string  `json:"expire_at,omitempty"`
	Photons         float64 `json:"photons"`
	NextBenefitDate string  `json:"next_benefit_date,omitempty"`
}

// BalanceQuotas summarizes remaining feature quotas.
type BalanceQuotas struct {
	PaperSearch   bohrium.QuotaCounter `json:"paper_search"`
	KnowledgeChat bohrium.QuotaCounter `json:"knowledge_chat"`
	DeepResearch  bohrium.QuotaCounter `json:"deep_research"`
}

// BalanceStorage mirrors knowledge-base storage utilization.
type BalanceStorage struct {
	RemainingBytes int64   `json:"remaining_bytes"`
	UsedBytes      int64   `json:"used_bytes"`
	TotalBytes     int64   `json:"total_bytes"`
	UsedPercent    float64 `json:"used_percent"`
	RemainingHuman string  `json:"remaining_human"`
	UsedHuman      string  `json:"used_human"`
	TotalHuman     string  `json:"total_human"`
}

// BalanceCurrencyInfo matches the balance_infos convention used by OpenAI-compatible gateways.
type BalanceCurrencyInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}

func (g *Gateway) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	poolAccounts, err := g.mgr.Store().ListPoolAccounts()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "store_error", err.Error())
		return
	}
	if len(poolAccounts) == 0 {
		WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "empty_pool", ErrEmptyPool.Error())
		return
	}

	var (
		totalPhotons    float64
		totalOrg        float64
		totalOrgLimit   float64
		totalWalletCNY  float64
		totalExpiring7D float64
		allLots         []bohrium.ExpiringLot
		warnings        []string
		firstAssets     *bohrium.PointsAndCapacity
	)

	// Refresh live balances for every account in the OpenAI routing pool.
	for _, acc := range poolAccounts {
		assets, aErr := g.mgr.RefreshAccountBalance(ctx, acc.ID)
		if aErr != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", acc.DisplayName(), aErr))
			totalPhotons += acc.PhotonBalance
			totalOrg += acc.OrgBalance
			totalWalletCNY += acc.WalletBalanceCNY
			continue
		}
		if firstAssets == nil {
			firstAssets = assets
		}
		totalPhotons += assets.PhotonBalance
		totalOrg += assets.OrgBalance
		totalOrgLimit += assets.OrgBalanceLimit
		totalWalletCNY += assets.WalletBalance
		totalExpiring7D += assets.Expiring7DayPhotons
		allLots = append(allLots, assets.ExpiringLots...)
	}

	views, _, _ := g.router.PoolSummary()
	healthyCnt := 0
	for _, v := range views {
		if v.Healthy {
			healthyCnt++
		}
	}

	combinedPhotons := totalPhotons + totalOrg
	resp := BalanceResponse{
		Object:            "balance",
		IsAvailable:       healthyCnt > 0 && (combinedPhotons > 0 || totalWalletCNY > 0),
		Currency:          "photon",
		Balance:           totalPhotons,
		PhotonBalance:     totalPhotons,
		OrgBalance:        totalOrg,
		OrgBalanceLimit:   totalOrgLimit,
		WalletBalanceCNY:  totalWalletCNY,
		Expiring7DPhotons: totalExpiring7D,
		PoolAccountCount:  len(views),
		HealthyPoolCount:  healthyCnt,
		PoolAccounts:      views,
		ExpiringLots:      allLots,
		BalanceInfos: []BalanceCurrencyInfo{
			{
				Currency:        "PHOTON",
				TotalBalance:    fmt.Sprintf("%.2f", combinedPhotons),
				GrantedBalance:  fmt.Sprintf("%.2f", totalPhotons),
				ToppedUpBalance: fmt.Sprintf("%.2f", totalOrg),
			},
			{
				Currency:        "CNY",
				TotalBalance:    fmt.Sprintf("%.2f", totalWalletCNY),
				GrantedBalance:  "0.00",
				ToppedUpBalance: fmt.Sprintf("%.2f", totalWalletCNY),
			},
		},
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Warnings:  warnings,
	}
	if firstAssets != nil {
		resp.Membership = BalanceMembership{
			Level:           firstAssets.MembershipLevel,
			ExpireAt:        firstAssets.MembershipExpire,
			Photons:         firstAssets.MembershipPhotons,
			NextBenefitDate: firstAssets.NextBenefitDate,
		}
		resp.Quotas = BalanceQuotas{
			PaperSearch:   firstAssets.PaperQuota,
			KnowledgeChat: firstAssets.KnowledgeChat,
			DeepResearch:  firstAssets.DeepResearch,
		}
		resp.Storage = BalanceStorage{
			RemainingBytes: firstAssets.Storage.RemainingBytes,
			UsedBytes:      firstAssets.Storage.UsedBytes,
			TotalBytes:     firstAssets.Storage.TotalBytes,
			UsedPercent:    firstAssets.Storage.UsedPercent,
			RemainingHuman: firstAssets.Storage.RemainingHuman,
			UsedHuman:      firstAssets.Storage.UsedHuman,
			TotalHuman:     firstAssets.Storage.TotalHuman,
		}
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "read_error", "Failed to read request body")
		return
	}
	if int64(len(bodyBytes)) > maxRequestBodyBytes {
		WriteError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "payload_too_large", "Request payload exceeds 32 MiB limit")
		return
	}

	var req ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "Invalid JSON body: "+err.Error())
		return
	}

	ctx := r.Context()
	isToolCont := isChatToolContinuation(&req)
	sessionHash := computeChatSessionHash(r, &req)
	explicitAcc := explicitAccountFromCtx(ctx)
	excluded := make(map[string]bool)

	baseTel := reqTelemetry{
		RequestedModel:  strings.TrimSpace(req.Model),
		ReasoningEffort: strings.TrimSpace(req.ReasoningEffort),
		SessionHash:     sessionHash,
		ClientIP:        extractClientIP(r),
		UserAgent:       strings.TrimSpace(r.Header.Get("User-Agent")),
		HasTools:        len(req.Tools) > 0 || len(req.Functions) > 0,
		HasVision:       chatHasVision(&req),
		PreflightMode:   g.effectivePreflightMode(isToolCont),
		SciMasterPrompt: g.sciMasterPromptEnabled(),
	}

	var (
		lastStatus  int
		lastErrType string
		lastErrCode string
		lastErrMsg  string
	)

	attempts := maxFailoverAttempts
	if explicitAcc != "" {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		tel := baseTel
		decision, rErr := g.router.SelectAccount(ctx, RouteRequest{
			SessionHash:        sessionHash,
			ExplicitAccountID:  explicitAcc,
			ModelHint:          req.Model,
			IsToolContinuation: isToolCont,
			ExcludeAccountIDs:  excluded,
		})
		if rErr != nil {
			if attempt > 0 && lastStatus > 0 {
				WriteError(w, lastStatus, lastErrType, lastErrCode, lastErrMsg)
				g.recordFailure(ctx, "/v1/chat/completions", "", req.Model, "", "", "", req.Stream, lastStatus, start, lastErrMsg, tel)
				return
			}
			status := writePoolRouteError(w, rErr)
			g.recordFailure(ctx, "/v1/chat/completions", "", req.Model, "", "", "", req.Stream, status, start, rErr.Error(), tel)
			return
		}

		model, _, mErr := g.resolveEnabledModel(ctx, decision.Client, req.Model)
		if mErr != nil {
			var disErr *modelDisabledError
			if errors.As(mErr, &disErr) {
				WriteError(w, http.StatusForbidden, "invalid_request_error", "model_disabled", disErr.Error())
				g.recordFailure(ctx, "/v1/chat/completions", "", req.Model, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusForbidden, start, disErr.Error(), tel)
				return
			}
			if isAuthErrorText(mErr.Error()) && explicitAcc == "" {
				g.markAccountExpired(decision.Account.ID, mErr.Error())
				excluded[decision.Account.ID] = true
				lastStatus, lastErrType, lastErrCode, lastErrMsg = http.StatusServiceUnavailable, "authentication_error", "account_expired", mErr.Error()
				continue
			}
			WriteError(w, http.StatusBadRequest, "invalid_request_error", "model_not_found", mErr.Error())
			g.recordFailure(ctx, "/v1/chat/completions", "", req.Model, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusBadRequest, start, mErr.Error(), tel)
			return
		}

		upstreamAPI := model.EffectiveAPI()
		tel.Bridged = upstreamAPI != "chat_completions"
		tel.SciMasterPrompt = shouldInjectSciMasterPrompt(model.ID, g.sciMasterPromptEnabled())
		tel.UsageMultiplier = modelUsageMultiplier(model)
		tel.ReasoningEffort = resolveRecordedReasoningEffort(req.ReasoningEffort, model, nil)
		if strings.TrimSpace(req.Model) == model.ID {
			tel.RequestedModel = ""
		}

		traceparent := strings.TrimSpace(r.Header.Get("traceparent"))
		if traceparent == "" {
			traceparent = bohrium.NewTraceparent()
		}

		if !isToolCont {
			if status, errType, code, reason, ok := g.runPreflightIfEnabled(ctx, decision.Client, model.ID, traceparent); !ok {
				if status == http.StatusPaymentRequired && explicitAcc == "" {
					g.markAccountInsufficient(decision.Account.ID, reason)
					excluded[decision.Account.ID] = true
					lastStatus, lastErrType, lastErrCode, lastErrMsg = status, errType, code, reason
					continue
				}
				WriteError(w, status, errType, code, reason)
				g.recordFailure(ctx, "/v1/chat/completions", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, status, start, reason, tel)
				return
			}
		}

		var (
			payloadMap map[string]any
			question   string
			subPath    string
			normErr    error
		)
		opts := SciXRequestOptions{
			SciMasterPrompt: g.sciMasterPromptEnabled(),
			ForceStream:     true,
			SessionID:       decision.SciXSessionID,
		}
		if upstreamAPI == "chat_completions" {
			subPath = "/chat/completions"
			payloadMap, question, normErr = NormalizeChatRequestForSciX(&req, model, opts)
		} else {
			subPath = "/responses"
			payloadMap, question, normErr = ChatToResponsesRequest(&req, model, opts)
		}
		if normErr != nil {
			WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", normErr.Error())
			g.recordFailure(ctx, "/v1/chat/completions", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusBadRequest, start, normErr.Error(), tel)
			return
		}
		tel.ReasoningEffort = resolveRecordedReasoningEffort(req.ReasoningEffort, model, payloadMap)
		payloadBytes, err := json.Marshal(payloadMap)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "server_error", "marshal_error", err.Error())
			return
		}

		execHdr := decision.Client.BuildSciXExecutionHeader(bohrium.SciXExecOptions{
			IncomingHeader:   r.Header.Get(bohrium.SciXExecutionHeader),
			SessionID:        decision.SciXSessionID,
			RootSessionID:    decision.SciXSessionID,
			RunID:            decision.SciXRunID,
			RootRunID:        decision.SciXRunID,
			SessionCreatedAt: decision.SessionCreatedAt,
			Question:         question,
			ToolCallID:       lastChatToolCallID(&req),
			ModelID:          model.ID,
		})

		upResp, err := decision.Client.DoSciXLLM(ctx, subPath, payloadBytes, true, execHdr, traceparent)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "upstream_transport_error", err.Error())
			g.recordFailure(ctx, "/v1/chat/completions", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusBadGateway, start, err.Error(), tel)
			return
		}
		tel.RequestID = extractUpstreamRequestID(upResp)

		if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(io.LimitReader(upResp.Body, 256*1024))
			_ = upResp.Body.Close()

			// Pre-first-byte failover on account auth expiry (401) or photon exhaustion (402 / insufficient_balance).
			if explicitAcc == "" && shouldFailoverUpstreamStatus(upResp.StatusCode, errBody) {
				errTxt := extractUpstreamErrorText(errBody, upResp.StatusCode)
				if upResp.StatusCode == http.StatusUnauthorized {
					g.markAccountExpired(decision.Account.ID, errTxt)
					lastStatus, lastErrType, lastErrCode, lastErrMsg = http.StatusUnauthorized, "authentication_error", "account_expired", errTxt
				} else {
					g.markAccountInsufficient(decision.Account.ID, errTxt)
					lastStatus, lastErrType, lastErrCode, lastErrMsg = http.StatusPaymentRequired, "insufficient_quota", "insufficient_balance", errTxt
				}
				excluded[decision.Account.ID] = true
				continue
			}

			copyUpstreamRequestID(w, upResp)
			errMsg := forwardUpstreamError(w, upResp.StatusCode, errBody)
			g.recordFailure(ctx, "/v1/chat/completions", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, upResp.StatusCode, start, errMsg, tel)
			return
		}

		// Upstream returned 2xx: commit response to client (no failover after first byte).
		defer upResp.Body.Close()
		copyUpstreamRequestID(w, upResp)
		w.Header().Set("X-Bohrium-Routing-Mode", decision.Mode)

		if upstreamAPI == "chat_completions" {
			g.finishChatFromChatUpstream(ctx, w, &req, model, decision, upResp, start, tel)
		} else {
			g.finishChatFromResponsesUpstream(ctx, w, &req, model, decision, upResp, start, tel)
		}
		return
	}

	WriteError(w, lastStatus, lastErrType, lastErrCode, lastErrMsg)
	g.recordFailure(ctx, "/v1/chat/completions", "", req.Model, "", "", "", req.Stream, lastStatus, start, lastErrMsg, baseTel)
}

func (g *Gateway) finishChatFromChatUpstream(ctx context.Context, w http.ResponseWriter, req *ChatCompletionRequest, model bohrium.SciXModel, decision *RouteDecision, upResp *http.Response, start time.Time, tel reqTelemetry) {
	keyID, keyName := apiKeyIdentityFromCtx(ctx)
	ttft := newTTFTTracker(upResp.Body, isSSEContentType(upResp) || req.Stream)
	if req.Stream {
		usage, sErr := PipeChatSSE(w, ttft)
		errStr := ""
		if sErr != nil {
			errStr = sErr.Error()
		}
		g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/chat/completions", "chat_completions", model.ID, decision, true, http.StatusOK, start, ttft.FirstTokenMs(start), usage, errStr, tel))
		return
	}

	var (
		parsed *ChatCompletionResponse
		err    error
	)
	if isSSEContentType(upResp) {
		parsed, err = AggregateChatSSE(ttft, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure(ctx, "/v1/chat/completions", "chat_completions", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, err.Error(), tel)
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(ttft, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure(ctx, "/v1/chat/completions", "chat_completions", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, rErr.Error(), tel)
			return
		}
		var direct ChatCompletionResponse
		if json.Unmarshal(rawResp, &direct) != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rawResp)
			return
		}
		parsed = &direct
	}

	var usage UsageMetrics
	if parsed != nil && parsed.Usage != nil {
		usage = usageFromChat(parsed.Usage)
	}
	WriteJSON(w, http.StatusOK, parsed)

	g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/chat/completions", "chat_completions", model.ID, decision, false, http.StatusOK, start, ttft.FirstTokenMs(start), usage, "", tel))
}

func (g *Gateway) finishChatFromResponsesUpstream(ctx context.Context, w http.ResponseWriter, req *ChatCompletionRequest, model bohrium.SciXModel, decision *RouteDecision, upResp *http.Response, start time.Time, tel reqTelemetry) {
	keyID, keyName := apiKeyIdentityFromCtx(ctx)
	ttft := newTTFTTracker(upResp.Body, isSSEContentType(upResp) || req.Stream)
	if req.Stream {
		includeUsage := req.StreamOptions == nil || req.StreamOptions.IncludeUsage
		usage, sErr := StreamResponsesToChatSSE(w, ttft, model.ID, includeUsage)
		errStr := ""
		if sErr != nil {
			errStr = sErr.Error()
		}
		g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/chat/completions", "responses", model.ID, decision, true, http.StatusOK, start, ttft.FirstTokenMs(start), usage, errStr, tel))
		return
	}

	var (
		respMap map[string]any
		err     error
	)
	if isSSEContentType(upResp) {
		respMap, err = AggregateResponsesSSE(ttft, nil, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure(ctx, "/v1/chat/completions", "responses", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, err.Error(), tel)
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(ttft, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure(ctx, "/v1/chat/completions", "responses", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, rErr.Error(), tel)
			return
		}
		if err := json.Unmarshal(rawResp, &respMap); err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "invalid_upstream_json", err.Error())
			return
		}
	}
	chatResp := OpenAIResponseToChatResponse(respMap, model.ID)
	usage := usageFromChat(chatResp.Usage)
	WriteJSON(w, http.StatusOK, chatResp)

	g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/chat/completions", "responses", model.ID, decision, false, http.StatusOK, start, ttft.FirstTokenMs(start), usage, "", tel))
}

func (g *Gateway) handleCreateResponse(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "read_error", "Failed to read request body")
		return
	}
	if int64(len(bodyBytes)) > maxRequestBodyBytes {
		WriteError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "payload_too_large", "Request payload exceeds 32 MiB limit")
		return
	}

	var req ResponsesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "Invalid JSON body: "+err.Error())
		return
	}
	if req.Background != nil && *req.Background {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter", "background=true is not supported; use synchronous streaming or non-streaming requests")
		return
	}

	turnItems, err := ParseResponsesInputItems(req.Input, g.respDir)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_input", err.Error())
		return
	}

	var (
		allItems         []map[string]any
		prevRootID       string
		prevAccountID    string
		prevSessionHash  string
	)
	effectiveInstr := req.Instructions
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		prevItems, prevInstr, rootID, accID, sessHash, found := g.respDir.BuildChainItems(strings.TrimSpace(req.PreviousResponseID))
		if !found {
			WriteError(w, http.StatusNotFound, "invalid_request_error", "previous_response_not_found", fmt.Sprintf("previous_response_id %q was not found", req.PreviousResponseID))
			return
		}
		allItems = append(allItems, prevItems...)
		prevRootID = rootID
		prevAccountID = accID
		prevSessionHash = sessHash
		if effectiveInstr == nil && prevInstr != nil {
			effectiveInstr = prevInstr
		}
	}
	allItems = append(allItems, turnItems...)

	if len(allItems) == 0 && (effectiveInstr == nil || strings.TrimSpace(*effectiveInstr) == "") {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "missing_required_parameter", "Responses API requires input or instructions")
		return
	}

	ctx := r.Context()
	isToolCont := isResponsesToolContinuation(turnItems)
	toolCallID := lastResponsesToolCallID(turnItems)
	sessionHash := computeResponsesSessionHash(r, &req, allItems, prevRootID, prevSessionHash)
	explicitAcc := explicitAccountFromCtx(ctx)
	shouldStore := req.Store == nil || *req.Store
	excluded := make(map[string]bool)

	reqEffort := ""
	if req.Reasoning != nil && req.Reasoning.Effort != nil {
		reqEffort = strings.TrimSpace(*req.Reasoning.Effort)
	}
	baseTel := reqTelemetry{
		RequestedModel:  strings.TrimSpace(req.Model),
		ReasoningEffort: reqEffort,
		SessionHash:     sessionHash,
		ClientIP:        extractClientIP(r),
		UserAgent:       strings.TrimSpace(r.Header.Get("User-Agent")),
		HasTools:        len(req.Tools) > 0,
		HasVision:       responsesHasVision(allItems),
		PreflightMode:   g.effectivePreflightMode(isToolCont),
		SciMasterPrompt: g.sciMasterPromptEnabled(),
	}

	var (
		lastStatus  int
		lastErrType string
		lastErrCode string
		lastErrMsg  string
	)

	attempts := maxFailoverAttempts
	if explicitAcc != "" {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		tel := baseTel
		decision, rErr := g.router.SelectAccount(ctx, RouteRequest{
			SessionHash:         sessionHash,
			ExplicitAccountID:   explicitAcc,
			PrevRespAccountID:   prevAccountID,
			PrevRespSessionHash: prevSessionHash,
			ModelHint:           req.Model,
			IsToolContinuation:  isToolCont,
			ExcludeAccountIDs:   excluded,
		})
		if rErr != nil {
			if attempt > 0 && lastStatus > 0 {
				WriteError(w, lastStatus, lastErrType, lastErrCode, lastErrMsg)
				g.recordFailure(ctx, "/v1/responses", "", req.Model, "", "", "", req.Stream, lastStatus, start, lastErrMsg, tel)
				return
			}
			status := writePoolRouteError(w, rErr)
			g.recordFailure(ctx, "/v1/responses", "", req.Model, "", "", "", req.Stream, status, start, rErr.Error(), tel)
			return
		}

		model, _, mErr := g.resolveEnabledModel(ctx, decision.Client, req.Model)
		if mErr != nil {
			var disErr *modelDisabledError
			if errors.As(mErr, &disErr) {
				WriteError(w, http.StatusForbidden, "invalid_request_error", "model_disabled", disErr.Error())
				g.recordFailure(ctx, "/v1/responses", "", req.Model, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusForbidden, start, disErr.Error(), tel)
				return
			}
			if isAuthErrorText(mErr.Error()) && explicitAcc == "" {
				g.markAccountExpired(decision.Account.ID, mErr.Error())
				excluded[decision.Account.ID] = true
				lastStatus, lastErrType, lastErrCode, lastErrMsg = http.StatusServiceUnavailable, "authentication_error", "account_expired", mErr.Error()
				continue
			}
			WriteError(w, http.StatusBadRequest, "invalid_request_error", "model_not_found", mErr.Error())
			g.recordFailure(ctx, "/v1/responses", "", req.Model, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusBadRequest, start, mErr.Error(), tel)
			return
		}

		upstreamAPI := model.EffectiveAPI()
		tel.Bridged = upstreamAPI != "responses"
		tel.SciMasterPrompt = shouldInjectSciMasterPrompt(model.ID, g.sciMasterPromptEnabled())
		tel.UsageMultiplier = modelUsageMultiplier(model)
		tel.ReasoningEffort = resolveRecordedReasoningEffort(reqEffort, model, nil)
		if strings.TrimSpace(req.Model) == model.ID {
			tel.RequestedModel = ""
		}

		traceparent := strings.TrimSpace(r.Header.Get("traceparent"))
		if traceparent == "" {
			traceparent = bohrium.NewTraceparent()
		}

		if !isToolCont {
			if status, errType, code, reason, ok := g.runPreflightIfEnabled(ctx, decision.Client, model.ID, traceparent); !ok {
				if status == http.StatusPaymentRequired && explicitAcc == "" {
					g.markAccountInsufficient(decision.Account.ID, reason)
					excluded[decision.Account.ID] = true
					lastStatus, lastErrType, lastErrCode, lastErrMsg = status, errType, code, reason
					continue
				}
				WriteError(w, status, errType, code, reason)
				g.recordFailure(ctx, "/v1/responses", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, status, start, reason, tel)
				return
			}
		}

		var (
			payloadMap map[string]any
			question   string
			subPath    string
			normErr    error
		)
		opts := SciXRequestOptions{
			SciMasterPrompt: g.sciMasterPromptEnabled(),
			ForceStream:     true,
			SessionID:       decision.SciXSessionID,
		}
		if upstreamAPI == "responses" {
			subPath = "/responses"
			payloadMap, question, normErr = NormalizeResponsesRequestForSciX(&req, model, allItems, effectiveInstr, opts)
		} else {
			subPath = "/chat/completions"
			payloadMap, question, normErr = ResponsesToChatRequest(&req, model, allItems, effectiveInstr, opts)
		}
		if normErr != nil {
			WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", normErr.Error())
			g.recordFailure(ctx, "/v1/responses", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusBadRequest, start, normErr.Error(), tel)
			return
		}
		tel.ReasoningEffort = resolveRecordedReasoningEffort(reqEffort, model, payloadMap)
		payloadBytes, err := json.Marshal(payloadMap)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "server_error", "marshal_error", err.Error())
			return
		}

		execHdr := decision.Client.BuildSciXExecutionHeader(bohrium.SciXExecOptions{
			IncomingHeader:   r.Header.Get(bohrium.SciXExecutionHeader),
			SessionID:        decision.SciXSessionID,
			RootSessionID:    decision.SciXSessionID,
			RunID:            decision.SciXRunID,
			RootRunID:        decision.SciXRunID,
			SessionCreatedAt: decision.SessionCreatedAt,
			Question:         question,
			ToolCallID:       toolCallID,
			ModelID:          model.ID,
		})

		upResp, err := decision.Client.DoSciXLLM(ctx, subPath, payloadBytes, true, execHdr, traceparent)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "upstream_transport_error", err.Error())
			g.recordFailure(ctx, "/v1/responses", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, http.StatusBadGateway, start, err.Error(), tel)
			return
		}
		tel.RequestID = extractUpstreamRequestID(upResp)

		if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
			errBody, _ := io.ReadAll(io.LimitReader(upResp.Body, 256*1024))
			_ = upResp.Body.Close()

			if explicitAcc == "" && shouldFailoverUpstreamStatus(upResp.StatusCode, errBody) {
				errTxt := extractUpstreamErrorText(errBody, upResp.StatusCode)
				if upResp.StatusCode == http.StatusUnauthorized {
					g.markAccountExpired(decision.Account.ID, errTxt)
					lastStatus, lastErrType, lastErrCode, lastErrMsg = http.StatusUnauthorized, "authentication_error", "account_expired", errTxt
				} else {
					g.markAccountInsufficient(decision.Account.ID, errTxt)
					lastStatus, lastErrType, lastErrCode, lastErrMsg = http.StatusPaymentRequired, "insufficient_quota", "insufficient_balance", errTxt
				}
				excluded[decision.Account.ID] = true
				continue
			}

			copyUpstreamRequestID(w, upResp)
			errMsg := forwardUpstreamError(w, upResp.StatusCode, errBody)
			g.recordFailure(ctx, "/v1/responses", upstreamAPI, model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, req.Stream, upResp.StatusCode, start, errMsg, tel)
			return
		}

		defer upResp.Body.Close()
		copyUpstreamRequestID(w, upResp)
		w.Header().Set("X-Bohrium-Routing-Mode", decision.Mode)

		if upstreamAPI == "responses" {
			g.finishResponsesFromResponsesUpstream(ctx, w, &req, model, decision, turnItems, effectiveInstr, shouldStore, upResp, start, tel)
		} else {
			g.finishResponsesFromChatUpstream(ctx, w, &req, model, decision, turnItems, effectiveInstr, shouldStore, upResp, start, tel)
		}
		return
	}

	WriteError(w, lastStatus, lastErrType, lastErrCode, lastErrMsg)
	g.recordFailure(ctx, "/v1/responses", "", req.Model, "", "", "", req.Stream, lastStatus, start, lastErrMsg, baseTel)
}

func (g *Gateway) finishResponsesFromResponsesUpstream(ctx context.Context, w http.ResponseWriter, req *ResponsesRequest, model bohrium.SciXModel, decision *RouteDecision, turnItems []map[string]any, effectiveInstr *string, shouldStore bool, upResp *http.Response, start time.Time, tel reqTelemetry) {
	keyID, keyName := apiKeyIdentityFromCtx(ctx)
	ttft := newTTFTTracker(upResp.Body, isSSEContentType(upResp) || req.Stream)
	if req.Stream {
		terminalResp, usage, sErr := PipeResponsesSSE(w, ttft, req)
		if shouldStore && terminalResp != nil {
			if id, _ := terminalResp["id"].(string); id != "" {
				g.respDir.Save(&StoredResponse{
					ID:                 id,
					PreviousResponseID: req.PreviousResponseID,
					AccountID:          decision.Account.ID,
					SessionHash:        decision.SessionHash,
					Model:              model.ID,
					Instructions:       effectiveInstr,
					InputItems:         turnItems,
					OutputItems:        extractOutputItems(terminalResp),
					Response:           terminalResp,
				})
			}
		}
		errStr := ""
		if sErr != nil {
			errStr = sErr.Error()
		}
		g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/responses", "responses", model.ID, decision, true, http.StatusOK, start, ttft.FirstTokenMs(start), usage, errStr, tel))
		return
	}

	var (
		respMap map[string]any
		err     error
	)
	if isSSEContentType(upResp) {
		respMap, err = AggregateResponsesSSE(ttft, req, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure(ctx, "/v1/responses", "responses", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, err.Error(), tel)
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(ttft, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure(ctx, "/v1/responses", "responses", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, rErr.Error(), tel)
			return
		}
		if err := json.Unmarshal(rawResp, &respMap); err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "invalid_upstream_json", err.Error())
			return
		}
		patchClientResponseMeta(respMap, req)
	}
	usage := usageFromResponseMap(respMap)
	if shouldStore {
		if id, _ := respMap["id"].(string); id != "" {
			g.respDir.Save(&StoredResponse{
				ID:                 id,
				PreviousResponseID: req.PreviousResponseID,
				AccountID:          decision.Account.ID,
				SessionHash:        decision.SessionHash,
				Model:              model.ID,
				Instructions:       effectiveInstr,
				InputItems:         turnItems,
				OutputItems:        extractOutputItems(respMap),
				Response:           respMap,
			})
		}
	}
	WriteJSON(w, http.StatusOK, respMap)

	g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/responses", "responses", model.ID, decision, false, http.StatusOK, start, ttft.FirstTokenMs(start), usage, "", tel))
}

func (g *Gateway) finishResponsesFromChatUpstream(ctx context.Context, w http.ResponseWriter, req *ResponsesRequest, model bohrium.SciXModel, decision *RouteDecision, turnItems []map[string]any, effectiveInstr *string, shouldStore bool, upResp *http.Response, start time.Time, tel reqTelemetry) {
	keyID, keyName := apiKeyIdentityFromCtx(ctx)
	ttft := newTTFTTracker(upResp.Body, isSSEContentType(upResp) || req.Stream)
	respID := RandomID("resp_")

	if req.Stream {
		finalResp, usage, sErr := StreamChatToResponsesSSE(w, ttft, req, model.ID, respID)
		if shouldStore && finalResp != nil && finalResp.Status != "failed" {
			respMap := structToMap(finalResp)
			g.respDir.Save(&StoredResponse{
				ID:                 finalResp.ID,
				PreviousResponseID: req.PreviousResponseID,
				AccountID:          decision.Account.ID,
				SessionHash:        decision.SessionHash,
				Model:              model.ID,
				Instructions:       effectiveInstr,
				InputItems:         turnItems,
				OutputItems:        finalResp.Output,
				Response:           respMap,
			})
		}
		errStr := ""
		if sErr != nil {
			errStr = sErr.Error()
		}
		g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/responses", "chat_completions", model.ID, decision, true, http.StatusOK, start, ttft.FirstTokenMs(start), usage, errStr, tel))
		return
	}

	var (
		chatResp *ChatCompletionResponse
		err      error
	)
	if isSSEContentType(upResp) {
		chatResp, err = AggregateChatSSE(ttft, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure(ctx, "/v1/responses", "chat_completions", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, err.Error(), tel)
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(ttft, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure(ctx, "/v1/responses", "chat_completions", model.ID, decision.Account.ID, decision.Account.DisplayName(), decision.Mode, false, http.StatusBadGateway, start, rErr.Error(), tel)
			return
		}
		var direct ChatCompletionResponse
		if err := json.Unmarshal(rawResp, &direct); err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "invalid_upstream_json", err.Error())
			return
		}
		chatResp = &direct
	}
	if chatResp.ID != "" && !strings.HasPrefix(chatResp.ID, "resp_") {
		respID = "resp_" + strings.ReplaceAll(chatResp.ID, "-", "")
	}
	openAIResp := ChatResponseToOpenAIResponse(chatResp, req, model.ID, respID)
	usage := usageFromChat(chatResp.Usage)

	if shouldStore {
		g.respDir.Save(&StoredResponse{
			ID:                 openAIResp.ID,
			PreviousResponseID: req.PreviousResponseID,
			AccountID:          decision.Account.ID,
			SessionHash:        decision.SessionHash,
			Model:              model.ID,
			Instructions:       effectiveInstr,
			InputItems:         turnItems,
			OutputItems:        openAIResp.Output,
			Response:           structToMap(openAIResp),
		})
	}
	WriteJSON(w, http.StatusOK, openAIResp)

	g.respDir.RecordRequest(buildLogEntry(keyID, keyName, "/v1/responses", "chat_completions", model.ID, decision, false, http.StatusOK, start, ttft.FirstTokenMs(start), usage, "", tel))
}

func (g *Gateway) handleGetResponse(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	rec, ok := g.respDir.Get(id)
	if !ok || rec.Response == nil {
		WriteError(w, http.StatusNotFound, "invalid_request_error", "response_not_found", fmt.Sprintf("Response %q not found", id))
		return
	}
	WriteJSON(w, http.StatusOK, rec.Response)
}

func (g *Gateway) handleDeleteResponse(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !g.respDir.Delete(id) {
		WriteError(w, http.StatusNotFound, "invalid_request_error", "response_not_found", fmt.Sprintf("Response %q not found", id))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"object":  "response.deleted",
		"deleted": true,
	})
}

func (g *Gateway) handleGetResponseInputItems(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	rec, ok := g.respDir.Get(id)
	if !ok {
		WriteError(w, http.StatusNotFound, "invalid_request_error", "response_not_found", fmt.Sprintf("Response %q not found", id))
		return
	}
	items := rec.InputItems
	if items == nil {
		items = []map[string]any{}
	}
	firstID := ""
	lastID := ""
	if len(items) > 0 {
		firstID, _ = items[0]["id"].(string)
		lastID, _ = items[len(items)-1]["id"].(string)
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"object":   "list",
		"data":     items,
		"first_id": firstID,
		"last_id":  lastID,
		"has_more": false,
	})
}

func (g *Gateway) runPreflightIfEnabled(ctx context.Context, cli *bohrium.Client, modelID, traceparent string) (status int, errType, code, msg string, ok bool) {
	if g.cfg == nil || !g.cfg.Get().Gateway.PreflightMode {
		return 0, "", "", "", true
	}
	allowed, reason, err := cli.SciXBillingPreflight(ctx, modelID, traceparent)
	if err != nil {
		return http.StatusBadGateway, "upstream_error", "billing_unavailable", "Billing admission unavailable: " + err.Error(), false
	}
	if !allowed {
		if reason == "" {
			reason = "[insufficient_balance] Insufficient balance"
		}
		return http.StatusPaymentRequired, "insufficient_quota", "insufficient_balance", reason, false
	}
	return 0, "", "", "", true
}

func (g *Gateway) markAccountInsufficient(accountID, reason string) {
	coolUntil := time.Now().UTC().Add(30 * time.Minute)
	_ = g.mgr.Store().UpdateAccountHealth(accountID, "insufficient", reason, coolUntil)
	go func(id string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = g.mgr.RefreshAccountBalance(ctx, id)
	}(accountID)
}

func (g *Gateway) markAccountExpired(accountID, reason string) {
	_ = g.mgr.Store().UpdateAccountHealth(accountID, "expired", reason, time.Time{})
}

func writePoolRouteError(w http.ResponseWriter, err error) int {
	if errors.Is(err, ErrEmptyPool) {
		WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "empty_pool", err.Error())
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, ErrNoHealthyPoolAccount) {
		WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "no_healthy_pool_accounts", err.Error())
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, bohrium.ErrBohriumNotLoggedIn) {
		WriteError(w, http.StatusServiceUnavailable, "authentication_error", "bohrium_not_logged_in", err.Error())
		return http.StatusServiceUnavailable
	}
	WriteError(w, http.StatusBadGateway, "upstream_error", "routing_error", err.Error())
	return http.StatusBadGateway
}

func shouldFailoverUpstreamStatus(status int, body []byte) bool {
	if status == http.StatusUnauthorized || status == http.StatusPaymentRequired {
		return true
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "insufficient_balance") || strings.Contains(string(body), "BILLING_PHOTON_NOT_ENOUGH")
}

func isAuthErrorText(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "http 401") || strings.Contains(l, "unauthorized") || strings.Contains(l, "not logged in")
}

func extractUpstreamErrorText(body []byte, status int) string {
	var parsed ErrorEnvelope
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return fmt.Sprintf("upstream returned HTTP %d", status)
	}
	if len(s) > 240 {
		return s[:240] + "..."
	}
	return s
}

func isChatToolContinuation(req *ChatCompletionRequest) bool {
	if req == nil || len(req.Messages) == 0 {
		return false
	}
	lastRole := strings.ToLower(strings.TrimSpace(req.Messages[len(req.Messages)-1].Role))
	return lastRole == "tool" || lastRole == "function"
}

func isResponsesToolContinuation(turnItems []map[string]any) bool {
	if len(turnItems) == 0 {
		return false
	}
	for _, it := range turnItems {
		typ, _ := it["type"].(string)
		if typ != "function_call_output" {
			return false
		}
	}
	return true
}

type reqTelemetry struct {
	RequestedModel  string
	ReasoningEffort string
	Bridged         bool
	SessionHash     string
	RequestID       string
	ClientIP        string
	UserAgent       string
	HasTools        bool
	HasVision       bool
	PreflightMode   string // "preflight" | "tool_skip" | "disabled"
	SciMasterPrompt bool
	UsageMultiplier float64
}

func buildLogEntry(keyID, keyName, endpoint, upstreamAPI, modelID string, decision *RouteDecision, stream bool, status int, start time.Time, firstTokenMs int64, usage UsageMetrics, errStr string, tel reqTelemetry) RequestLogEntry {
	accID := ""
	accLabel := ""
	routeMode := ""
	if decision != nil {
		accID = decision.Account.ID
		accLabel = decision.Account.DisplayName()
		routeMode = decision.Mode
	}
	return RequestLogEntry{
		APIKeyID:         keyID,
		APIKeyName:       keyName,
		Endpoint:         endpoint,
		UpstreamAPI:      upstreamAPI,
		Bridged:          tel.Bridged,
		Model:            modelID,
		RequestedModel:   tel.RequestedModel,
		ReasoningEffort:  tel.ReasoningEffort,
		AccountID:        accID,
		AccountLabel:     accLabel,
		RoutingMode:      routeMode,
		SessionHash:      tel.SessionHash,
		RequestID:        tel.RequestID,
		ClientIP:         tel.ClientIP,
		UserAgent:        tel.UserAgent,
		Stream:           stream,
		HasTools:         tel.HasTools,
		HasVision:        tel.HasVision,
		PreflightMode:    tel.PreflightMode,
		SciMasterPrompt:  tel.SciMasterPrompt,
		UsageMultiplier:  tel.UsageMultiplier,
		Status:           status,
		FirstTokenMs:     firstTokenMs,
		DurationMs:       time.Since(start).Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CachedTokens:     usage.CachedTokens,
		Error:            errStr,
	}
}

func (g *Gateway) recordFailure(ctx context.Context, endpoint, upstreamAPI, model, accID, accLabel, routeMode string, stream bool, status int, start time.Time, errMsg string, tel reqTelemetry) {
	keyID, keyName := apiKeyIdentityFromCtx(ctx)
	reqModel := tel.RequestedModel
	if reqModel == model {
		reqModel = ""
	}
	g.respDir.RecordRequest(RequestLogEntry{
		APIKeyID:        keyID,
		APIKeyName:      keyName,
		Endpoint:        endpoint,
		UpstreamAPI:     upstreamAPI,
		Bridged:         tel.Bridged,
		Model:           model,
		RequestedModel:  reqModel,
		ReasoningEffort: tel.ReasoningEffort,
		AccountID:       accID,
		AccountLabel:    accLabel,
		RoutingMode:     routeMode,
		SessionHash:     tel.SessionHash,
		RequestID:       tel.RequestID,
		ClientIP:        tel.ClientIP,
		UserAgent:       tel.UserAgent,
		Stream:          stream,
		HasTools:        tel.HasTools,
		HasVision:       tel.HasVision,
		PreflightMode:   tel.PreflightMode,
		SciMasterPrompt: tel.SciMasterPrompt,
		UsageMultiplier: tel.UsageMultiplier,
		Status:          status,
		DurationMs:      time.Since(start).Milliseconds(),
		Error:           errMsg,
	})
}

func (g *Gateway) effectivePreflightMode(isToolCont bool) string {
	if g.cfg == nil || !g.cfg.Get().Gateway.PreflightMode {
		return "disabled"
	}
	if isToolCont {
		return "tool_skip"
	}
	return "preflight"
}

func modelUsageMultiplier(m bohrium.SciXModel) float64 {
	if m.Presentation != nil && m.Presentation.UsageMultiplier > 0 {
		return m.Presentation.UsageMultiplier
	}
	return 1
}

func resolveRecordedReasoningEffort(requested string, model bohrium.SciXModel, payloadMap map[string]any) string {
	if payloadMap != nil {
		if s, ok := payloadMap["reasoning_effort"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		if rm, ok := payloadMap["reasoning"].(map[string]any); ok {
			if s, ok := rm["effort"].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "none" || req == "off" {
		return req
	}
	if req != "" {
		if c := model.ClampReasoningEffort(req); c != "" {
			return c
		}
		return req
	}
	return strings.TrimSpace(model.ReasoningEffort)
}

func extractClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			xff = xff[:idx]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func extractUpstreamRequestID(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	for _, h := range []string{"X-Request-Id", "Openai-Request-Id", "X-Litellm-Call-Id"} {
		if v := strings.TrimSpace(resp.Header.Get(h)); v != "" {
			return v
		}
	}
	return ""
}

func contentHasVision(content any) bool {
	parts, ok := content.([]any)
	if !ok {
		return false
	}
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(pm["type"])))
		if typ == "image_url" || typ == "input_image" || typ == "image" {
			return true
		}
	}
	return false
}

func chatHasVision(req *ChatCompletionRequest) bool {
	if req == nil {
		return false
	}
	for _, m := range req.Messages {
		if contentHasVision(m.Content) {
			return true
		}
	}
	return false
}

func responsesHasVision(items []map[string]any) bool {
	for _, it := range items {
		typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(it["type"])))
		if typ == "input_image" || typ == "image_url" || typ == "image" {
			return true
		}
		if contentHasVision(it["content"]) {
			return true
		}
	}
	return false
}

// ttftTrackingReader wraps an upstream response body to measure time-to-first-token (TTFT),
// ignoring Bohrium SSE `: heartbeat` comments and leading whitespace.
type ttftTrackingReader struct {
	r            io.Reader
	isSSE        bool
	recorded     bool
	firstTokenAt time.Time
	linePrefix   []byte
}

func newTTFTTracker(r io.Reader, isSSE bool) *ttftTrackingReader {
	return &ttftTrackingReader{
		r:          r,
		isSSE:      isSSE,
		linePrefix: make([]byte, 0, 16),
	}
}

func (t *ttftTrackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 && !t.recorded {
		if !t.isSSE {
			if len(bytes.TrimSpace(p[:n])) > 0 {
				t.firstTokenAt = time.Now()
				t.recorded = true
			}
		} else {
			for _, b := range p[:n] {
				if b == '\n' {
					t.linePrefix = t.linePrefix[:0]
					continue
				}
				if b == '\r' {
					continue
				}
				if len(t.linePrefix) < 16 {
					t.linePrefix = append(t.linePrefix, b)
				}
				trimmed := bytes.TrimLeft(t.linePrefix, " \t")
				if bytes.HasPrefix(trimmed, []byte("data:")) {
					t.firstTokenAt = time.Now()
					t.recorded = true
					break
				}
			}
		}
	}
	return n, err
}

func (t *ttftTrackingReader) FirstTokenMs(start time.Time) int64 {
	if !t.recorded || t.firstTokenAt.IsZero() {
		return 0
	}
	ms := t.firstTokenAt.Sub(start).Milliseconds()
	if ms < 1 {
		return 1
	}
	return ms
}

func copyUpstreamRequestID(w http.ResponseWriter, resp *http.Response) {
	for _, h := range []string{"X-Request-Id", "Openai-Request-Id", "X-Litellm-Call-Id"} {
		if v := strings.TrimSpace(resp.Header.Get(h)); v != "" {
			w.Header().Set(h, v)
		}
	}
}

func (g *Gateway) sciMasterPromptEnabled() bool {
	if g.cfg == nil {
		return true
	}
	return g.cfg.Get().Gateway.SciMasterPrompt
}

func extractExplicitSessionKey(r *http.Request, promptCacheKey, userField string) string {
	if sid := strings.TrimSpace(r.Header.Get("x-session-id")); sid != "" {
		return "hdr:" + sid
	}
	if rawExec := strings.TrimSpace(r.Header.Get(bohrium.SciXExecutionHeader)); rawExec != "" {
		if dec, err := base64.RawURLEncoding.DecodeString(rawExec); err == nil {
			var p bohrium.SciXExecutionPayload
			if json.Unmarshal(dec, &p) == nil {
				if p.RootSessionID != "" {
					return "scix:" + p.RootSessionID
				}
				if p.SessionID != "" {
					return "scix:" + p.SessionID
				}
			}
		}
	}
	if k := strings.TrimSpace(promptCacheKey); k != "" {
		return "pck:" + k
	}
	if u := strings.TrimSpace(userField); u != "" {
		return "user:" + u
	}
	return ""
}

func computeChatSessionHash(r *http.Request, req *ChatCompletionRequest) string {
	apiScope := apiKeyScopeFromCtx(r.Context())
	explicit := extractExplicitSessionKey(r, req.PromptCacheKey, req.User)
	var firstSys, firstUser string
	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if (role == "system" || role == "developer") && firstSys == "" {
			firstSys = strings.TrimSpace(flattenContentToText(m.Content))
		}
		if role == "user" && firstUser == "" {
			firstUser = strings.TrimSpace(flattenContentToText(m.Content))
			break
		}
	}
	return ComputeSessionHash(apiScope, explicit, firstSys, firstUser, "")
}

func computeResponsesSessionHash(r *http.Request, req *ResponsesRequest, allItems []map[string]any, prevRootID, prevSessionHash string) string {
	if strings.TrimSpace(prevSessionHash) != "" {
		return strings.TrimSpace(prevSessionHash)
	}
	apiScope := apiKeyScopeFromCtx(r.Context())
	explicit := extractExplicitSessionKey(r, req.PromptCacheKey, req.User)
	if explicit == "" && strings.TrimSpace(prevRootID) != "" {
		explicit = "resp-chain:" + strings.TrimSpace(prevRootID)
	}
	var firstSys, firstUser string
	if req.Instructions != nil {
		firstSys = strings.TrimSpace(*req.Instructions)
	}
	for _, it := range allItems {
		typ, _ := it["type"].(string)
		role, _ := it["role"].(string)
		if (typ == "message" || typ == "") && strings.EqualFold(role, "user") {
			if txt := strings.TrimSpace(flattenContentToText(it["content"])); txt != "" {
				firstUser = txt
				break
			}
		}
	}
	return ComputeSessionHash(apiScope, explicit, firstSys, firstUser, "")
}

func lastChatToolCallID(req *ChatCompletionRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	last := req.Messages[len(req.Messages)-1]
	role := strings.ToLower(strings.TrimSpace(last.Role))
	if role == "tool" || role == "function" {
		if last.ToolCallID != "" {
			return last.ToolCallID
		}
		if last.Name != "" {
			return "call_" + last.Name
		}
	}
	return ""
}

func lastResponsesToolCallID(turnItems []map[string]any) string {
	for i := len(turnItems) - 1; i >= 0; i-- {
		if typ, _ := turnItems[i]["type"].(string); typ == "function_call_output" {
			if cid, _ := turnItems[i]["call_id"].(string); cid != "" {
				return cid
			}
		}
	}
	return ""
}
