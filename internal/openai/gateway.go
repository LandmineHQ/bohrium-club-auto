package openai

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/config"
)

const (
	maxRequestBodyBytes = 32 * 1024 * 1024 // 32 MiB to accommodate multimodal base64 images
	maxTrackedSessions  = 256
)

type trackedSession struct {
	sessionID        string
	rootSessionID    string
	runID            string
	rootRunID        string
	sessionCreatedAt string
	updatedAt        time.Time
}

// Gateway exposes full OpenAI-compatible /v1/models, /v1/chat/completions, and /v1/responses endpoints
// backed faithfully by Bohrium desktop client's native Sigma LLM protocol.
type Gateway struct {
	cfg      *config.Manager
	client   *bohrium.Client
	respDir  *ResponseStore
	sessMu   sync.Mutex
	sessions map[string]*trackedSession
}

// NewGateway constructs a new OpenAI-compatible Bohrium gateway.
func NewGateway(cfg *config.Manager, client *bohrium.Client) *Gateway {
	dataDir := ""
	if cfg != nil {
		dataDir = cfg.Get().DataDir
	}
	return &Gateway{
		cfg:      cfg,
		client:   client,
		respDir:  OpenResponseStore(dataDir),
		sessions: make(map[string]*trackedSession),
	}
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
	return g.client.GetSciXModelCatalog(ctx, forceRefresh)
}

// Preflight runs Bohrium's /billing/preflight admission check for a model.
func (g *Gateway) Preflight(ctx context.Context, modelID string) (bool, string, error) {
	resolved, _, err := g.client.ResolveSciXModel(ctx, modelID)
	if err != nil {
		return false, "", err
	}
	return g.client.SciXBillingPreflight(ctx, resolved.ID, bohrium.NewTraceparent())
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
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, x-api-key, api-key, OpenAI-Beta, x-scix-execution, traceparent, X-Refresh-Models")
	w.Header().Set("Access-Control-Expose-Headers", "x-request-id, openai-request-id")
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
		if !gwCfg.AllowNoAuth {
			key := extractAPIKey(r)
			if key == "" || !validateAPIKey(key, gwCfg.APIKeys) {
				WriteError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Invalid or missing API key; provide Authorization: Bearer <sk-brm-...>")
				return
			}
		}
		next(w, r)
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

func validateAPIKey(candidate string, keys []config.APIKey) bool {
	cBytes := []byte(candidate)
	matched := false
	for _, k := range keys {
		if !k.Enabled || k.Key == "" {
			continue
		}
		if subtle.ConstantTimeCompare(cBytes, []byte(k.Key)) == 1 {
			matched = true
		}
	}
	return matched
}

func (g *Gateway) handleListModels(w http.ResponseWriter, r *http.Request) {
	force := strings.EqualFold(r.Header.Get("X-Refresh-Models"), "true") || r.URL.Query().Get("refresh") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	cat, err := g.client.GetSciXModelCatalog(ctx, force)
	if err != nil {
		if errors.Is(err, bohrium.ErrBohriumNotLoggedIn) {
			WriteError(w, http.StatusServiceUnavailable, "authentication_error", "bohrium_not_logged_in", err.Error())
			return
		}
		WriteError(w, http.StatusBadGateway, "upstream_error", "catalog_unavailable", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, cat)
}

func (g *Gateway) handleGetModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	m, _, err := g.client.ResolveSciXModel(ctx, id)
	if err != nil {
		if errors.Is(err, bohrium.ErrBohriumNotLoggedIn) {
			WriteError(w, http.StatusServiceUnavailable, "authentication_error", "bohrium_not_logged_in", err.Error())
			return
		}
		WriteError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, m)
}

// BalanceResponse represents the OpenAI-gateway /v1/balance response payload.
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
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	auth := g.client.RedactedAuth()
	if auth.UserID == 0 && auth.ExpiresAt.IsZero() && auth.LoginMethod == "" {
		// Check session accurately before querying assets.
		sess := g.client.CheckSession(ctx)
		if !sess.HasToken {
			WriteError(w, http.StatusServiceUnavailable, "authentication_error", "bohrium_not_logged_in", bohrium.ErrBohriumNotLoggedIn.Error())
			return
		}
	}

	assets, err := g.client.GetPointsAndCapacity(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "请先登录") {
			WriteError(w, http.StatusServiceUnavailable, "authentication_error", "bohrium_not_logged_in", bohrium.ErrBohriumNotLoggedIn.Error())
			return
		}
		WriteError(w, http.StatusBadGateway, "upstream_error", "balance_query_failed", err.Error())
		return
	}

	totalPhotons := assets.PhotonBalance + assets.OrgBalance
	resp := BalanceResponse{
		Object:            "balance",
		IsAvailable:       totalPhotons > 0 || assets.WalletBalance > 0,
		Currency:          "photon",
		Balance:           assets.PhotonBalance,
		PhotonBalance:     assets.PhotonBalance,
		OrgBalance:        assets.OrgBalance,
		OrgBalanceLimit:   assets.OrgBalanceLimit,
		WalletBalanceCNY:  assets.WalletBalance,
		Expiring7DPhotons: assets.Expiring7DayPhotons,
		Membership: BalanceMembership{
			Level:           assets.MembershipLevel,
			ExpireAt:        assets.MembershipExpire,
			Photons:         assets.MembershipPhotons,
			NextBenefitDate: assets.NextBenefitDate,
		},
		Quotas: BalanceQuotas{
			PaperSearch:   assets.PaperQuota,
			KnowledgeChat: assets.KnowledgeChat,
			DeepResearch:  assets.DeepResearch,
		},
		Storage: BalanceStorage{
			RemainingBytes: assets.Storage.RemainingBytes,
			UsedBytes:      assets.Storage.UsedBytes,
			TotalBytes:     assets.Storage.TotalBytes,
			UsedPercent:    assets.Storage.UsedPercent,
			RemainingHuman: assets.Storage.RemainingHuman,
			UsedHuman:      assets.Storage.UsedHuman,
			TotalHuman:     assets.Storage.TotalHuman,
		},
		ExpiringLots: assets.ExpiringLots,
		BalanceInfos: []BalanceCurrencyInfo{
			{
				Currency:        "PHOTON",
				TotalBalance:    fmt.Sprintf("%.2f", totalPhotons),
				GrantedBalance:  fmt.Sprintf("%.2f", assets.PhotonBalance),
				ToppedUpBalance: fmt.Sprintf("%.2f", assets.OrgBalance),
			},
			{
				Currency:        "CNY",
				TotalBalance:    fmt.Sprintf("%.2f", assets.WalletBalance),
				GrantedBalance:  "0.00",
				ToppedUpBalance: fmt.Sprintf("%.2f", assets.WalletBalance),
			},
		},
		FetchedAt: assets.FetchedAt.UTC().Format(time.RFC3339),
		Warnings:  assets.Warnings,
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
	model, _, err := g.client.ResolveSciXModel(ctx, req.Model)
	if err != nil {
		status := http.StatusBadRequest
		errType := "invalid_request_error"
		code := "model_not_found"
		if errors.Is(err, bohrium.ErrBohriumNotLoggedIn) {
			status = http.StatusServiceUnavailable
			errType = "authentication_error"
			code = "bohrium_not_logged_in"
		}
		WriteError(w, status, errType, code, err.Error())
		g.recordFailure("/v1/chat/completions", "", req.Model, req.Stream, status, start, err.Error())
		return
	}

	upstreamAPI := model.EffectiveAPI()
	traceparent := strings.TrimSpace(r.Header.Get("traceparent"))
	if traceparent == "" {
		traceparent = bohrium.NewTraceparent()
	}

	// Mirror Bohrium desktop client: run /billing/preflight on new user turns,
	// skipping redundant preflights during mid-loop tool continuation steps.
	if !isChatToolContinuation(&req) {
		if status, errType, code, reason, ok := g.runPreflightIfEnabled(ctx, model.ID, traceparent); !ok {
			WriteError(w, status, errType, code, reason)
			g.recordFailure("/v1/chat/completions", upstreamAPI, model.ID, req.Stream, status, start, reason)
			return
		}
	}

	if upstreamAPI == "chat_completions" {
		g.execChatViaChatUpstream(w, r, &req, model, traceparent, start)
		return
	}
	g.execChatViaResponsesUpstream(w, r, &req, model, traceparent, start)
}

func (g *Gateway) execChatViaChatUpstream(w http.ResponseWriter, r *http.Request, req *ChatCompletionRequest, model bohrium.SciXModel, traceparent string, start time.Time) {
	isToolCont := isChatToolContinuation(req)
	sessionID, execHdr := g.resolveExecEnvelope(
		r.Header.Get(bohrium.SciXExecutionHeader),
		sessionSeedForChat(req),
		model.ID,
		"",
		lastChatToolCallID(req),
		isToolCont,
	)

	payloadMap, question, err := NormalizeChatRequestForSciX(req, model, SciXRequestOptions{
		SciMasterPrompt: g.sciMasterPromptEnabled(),
		ForceStream:     true, // Match brain.mjs which always streams SSE from Bohrium Sigma
		SessionID:       sessionID,
	})
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		g.recordFailure("/v1/chat/completions", "chat_completions", model.ID, req.Stream, http.StatusBadRequest, start, err.Error())
		return
	}
	if question != "" {
		_, execHdr = g.resolveExecEnvelopeWithQuestion(
			r.Header.Get(bohrium.SciXExecutionHeader),
			sessionID,
			model.ID,
			question,
			lastChatToolCallID(req),
		)
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "marshal_error", err.Error())
		return
	}

	upResp, err := g.client.DoSciXLLM(r.Context(), "/chat/completions", payloadBytes, true, execHdr, traceparent)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_error", "upstream_transport_error", err.Error())
		g.recordFailure("/v1/chat/completions", "chat_completions", model.ID, req.Stream, http.StatusBadGateway, start, err.Error())
		return
	}
	defer upResp.Body.Close()

	copyUpstreamRequestID(w, upResp)
	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(upResp.Body, 256*1024))
		errMsg := forwardUpstreamError(w, upResp.StatusCode, errBody)
		g.recordFailure("/v1/chat/completions", "chat_completions", model.ID, req.Stream, upResp.StatusCode, start, errMsg)
		return
	}

	if req.Stream {
		usage, sErr := PipeChatSSE(w, upResp.Body)
		errStr := ""
		if sErr != nil {
			errStr = sErr.Error()
		}
		g.respDir.RecordRequest(RequestLogEntry{
			Endpoint:         "/v1/chat/completions",
			UpstreamAPI:      "chat_completions",
			Model:            model.ID,
			Stream:           true,
			Status:           http.StatusOK,
			DurationMs:       time.Since(start).Milliseconds(),
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			ReasoningTokens:  usage.ReasoningTokens,
			CachedTokens:     usage.CachedTokens,
			Error:            errStr,
		})
		return
	}

	var parsed *ChatCompletionResponse
	if isSSEContentType(upResp) {
		parsed, err = AggregateChatSSE(upResp.Body, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure("/v1/chat/completions", "chat_completions", model.ID, false, http.StatusBadGateway, start, err.Error())
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(upResp.Body, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure("/v1/chat/completions", "chat_completions", model.ID, false, http.StatusBadGateway, start, rErr.Error())
			return
		}
		var direct ChatCompletionResponse
		if json.Unmarshal(rawResp, &direct) != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rawResp)
			g.respDir.RecordRequest(RequestLogEntry{
				Endpoint:    "/v1/chat/completions",
				UpstreamAPI: "chat_completions",
				Model:       model.ID,
				Stream:      false,
				Status:      http.StatusOK,
				DurationMs:  time.Since(start).Milliseconds(),
			})
			return
		}
		parsed = &direct
	}

	var usage UsageMetrics
	if parsed != nil && parsed.Usage != nil {
		usage = usageFromChat(parsed.Usage)
	}
	WriteJSON(w, http.StatusOK, parsed)

	g.respDir.RecordRequest(RequestLogEntry{
		Endpoint:         "/v1/chat/completions",
		UpstreamAPI:      "chat_completions",
		Model:            model.ID,
		Stream:           false,
		Status:           http.StatusOK,
		DurationMs:       time.Since(start).Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CachedTokens:     usage.CachedTokens,
	})
}

func (g *Gateway) execChatViaResponsesUpstream(w http.ResponseWriter, r *http.Request, req *ChatCompletionRequest, model bohrium.SciXModel, traceparent string, start time.Time) {
	isToolCont := isChatToolContinuation(req)
	sessionID, execHdr := g.resolveExecEnvelope(
		r.Header.Get(bohrium.SciXExecutionHeader),
		sessionSeedForChat(req),
		model.ID,
		"",
		lastChatToolCallID(req),
		isToolCont,
	)

	payloadMap, question, err := ChatToResponsesRequest(req, model, SciXRequestOptions{
		SciMasterPrompt: g.sciMasterPromptEnabled(),
		ForceStream:     true,
		SessionID:       sessionID,
	})
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		g.recordFailure("/v1/chat/completions", "responses", model.ID, req.Stream, http.StatusBadRequest, start, err.Error())
		return
	}
	if question != "" {
		_, execHdr = g.resolveExecEnvelopeWithQuestion(
			r.Header.Get(bohrium.SciXExecutionHeader),
			sessionID,
			model.ID,
			question,
			lastChatToolCallID(req),
		)
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "marshal_error", err.Error())
		return
	}

	upResp, err := g.client.DoSciXLLM(r.Context(), "/responses", payloadBytes, true, execHdr, traceparent)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_error", "upstream_transport_error", err.Error())
		g.recordFailure("/v1/chat/completions", "responses", model.ID, req.Stream, http.StatusBadGateway, start, err.Error())
		return
	}
	defer upResp.Body.Close()

	copyUpstreamRequestID(w, upResp)
	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(upResp.Body, 256*1024))
		errMsg := forwardUpstreamError(w, upResp.StatusCode, errBody)
		g.recordFailure("/v1/chat/completions", "responses", model.ID, req.Stream, upResp.StatusCode, start, errMsg)
		return
	}

	if req.Stream {
		includeUsage := req.StreamOptions == nil || req.StreamOptions.IncludeUsage
		usage, sErr := StreamResponsesToChatSSE(w, upResp.Body, model.ID, includeUsage)
		errStr := ""
		if sErr != nil {
			errStr = sErr.Error()
		}
		g.respDir.RecordRequest(RequestLogEntry{
			Endpoint:         "/v1/chat/completions",
			UpstreamAPI:      "responses",
			Model:            model.ID,
			Stream:           true,
			Status:           http.StatusOK,
			DurationMs:       time.Since(start).Milliseconds(),
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			ReasoningTokens:  usage.ReasoningTokens,
			CachedTokens:     usage.CachedTokens,
			Error:            errStr,
		})
		return
	}

	var respMap map[string]any
	if isSSEContentType(upResp) {
		respMap, err = AggregateResponsesSSE(upResp.Body, nil, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure("/v1/chat/completions", "responses", model.ID, false, http.StatusBadGateway, start, err.Error())
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(upResp.Body, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure("/v1/chat/completions", "responses", model.ID, false, http.StatusBadGateway, start, rErr.Error())
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

	g.respDir.RecordRequest(RequestLogEntry{
		Endpoint:         "/v1/chat/completions",
		UpstreamAPI:      "responses",
		Model:            model.ID,
		Stream:           false,
		Status:           http.StatusOK,
		DurationMs:       time.Since(start).Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CachedTokens:     usage.CachedTokens,
	})
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

	// Resolve previous_response_id chain locally since Bohrium upstream Responses is stateless.
	var allItems []map[string]any
	effectiveInstr := req.Instructions
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		prevItems, prevInstr, found := g.respDir.BuildChainItems(strings.TrimSpace(req.PreviousResponseID))
		if !found {
			WriteError(w, http.StatusNotFound, "invalid_request_error", "previous_response_not_found", fmt.Sprintf("previous_response_id %q was not found", req.PreviousResponseID))
			return
		}
		allItems = append(allItems, prevItems...)
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
	model, _, err := g.client.ResolveSciXModel(ctx, req.Model)
	if err != nil {
		status := http.StatusBadRequest
		errType := "invalid_request_error"
		code := "model_not_found"
		if errors.Is(err, bohrium.ErrBohriumNotLoggedIn) {
			status = http.StatusServiceUnavailable
			errType = "authentication_error"
			code = "bohrium_not_logged_in"
		}
		WriteError(w, status, errType, code, err.Error())
		g.recordFailure("/v1/responses", "", req.Model, req.Stream, status, start, err.Error())
		return
	}

	traceparent := strings.TrimSpace(r.Header.Get("traceparent"))
	if traceparent == "" {
		traceparent = bohrium.NewTraceparent()
	}

	// Mirror Bohrium desktop client: run /billing/preflight on new user turns,
	// skipping redundant preflights on pure function_call_output continuation turns.
	if !isResponsesToolContinuation(turnItems) {
		if status, errType, code, reason, ok := g.runPreflightIfEnabled(ctx, model.ID, traceparent); !ok {
			WriteError(w, status, errType, code, reason)
			g.recordFailure("/v1/responses", model.EffectiveAPI(), model.ID, req.Stream, status, start, reason)
			return
		}
	}

	shouldStore := req.Store == nil || *req.Store
	if model.EffectiveAPI() == "responses" {
		g.execResponsesViaResponsesUpstream(w, r, &req, model, turnItems, allItems, effectiveInstr, shouldStore, traceparent, start)
		return
	}
	g.execResponsesViaChatUpstream(w, r, &req, model, turnItems, allItems, effectiveInstr, shouldStore, traceparent, start)
}

func (g *Gateway) execResponsesViaResponsesUpstream(w http.ResponseWriter, r *http.Request, req *ResponsesRequest, model bohrium.SciXModel, turnItems, allItems []map[string]any, effectiveInstr *string, shouldStore bool, traceparent string, start time.Time) {
	isToolCont := isResponsesToolContinuation(turnItems)
	toolCallID := lastResponsesToolCallID(turnItems)
	sessionID, execHdr := g.resolveExecEnvelope(
		r.Header.Get(bohrium.SciXExecutionHeader),
		sessionSeedForResponses(req, g.respDir, allItems),
		model.ID,
		"",
		toolCallID,
		isToolCont,
	)

	payloadMap, question, err := NormalizeResponsesRequestForSciX(req, model, allItems, effectiveInstr, SciXRequestOptions{
		SciMasterPrompt: g.sciMasterPromptEnabled(),
		ForceStream:     true,
		SessionID:       sessionID,
	})
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		g.recordFailure("/v1/responses", "responses", model.ID, req.Stream, http.StatusBadRequest, start, err.Error())
		return
	}
	if question != "" {
		_, execHdr = g.resolveExecEnvelopeWithQuestion(
			r.Header.Get(bohrium.SciXExecutionHeader),
			sessionID,
			model.ID,
			question,
			toolCallID,
		)
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "marshal_error", err.Error())
		return
	}

	upResp, err := g.client.DoSciXLLM(r.Context(), "/responses", payloadBytes, true, execHdr, traceparent)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_error", "upstream_transport_error", err.Error())
		g.recordFailure("/v1/responses", "responses", model.ID, req.Stream, http.StatusBadGateway, start, err.Error())
		return
	}
	defer upResp.Body.Close()

	copyUpstreamRequestID(w, upResp)
	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(upResp.Body, 256*1024))
		errMsg := forwardUpstreamError(w, upResp.StatusCode, errBody)
		g.recordFailure("/v1/responses", "responses", model.ID, req.Stream, upResp.StatusCode, start, errMsg)
		return
	}

	if req.Stream {
		terminalResp, usage, sErr := PipeResponsesSSE(w, upResp.Body, req)
		if shouldStore && terminalResp != nil {
			if id, _ := terminalResp["id"].(string); id != "" {
				g.respDir.Save(&StoredResponse{
					ID:                 id,
					PreviousResponseID: req.PreviousResponseID,
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
		g.respDir.RecordRequest(RequestLogEntry{
			Endpoint:         "/v1/responses",
			UpstreamAPI:      "responses",
			Model:            model.ID,
			Stream:           true,
			Status:           http.StatusOK,
			DurationMs:       time.Since(start).Milliseconds(),
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			ReasoningTokens:  usage.ReasoningTokens,
			CachedTokens:     usage.CachedTokens,
			Error:            errStr,
		})
		return
	}

	var respMap map[string]any
	if isSSEContentType(upResp) {
		respMap, err = AggregateResponsesSSE(upResp.Body, req, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure("/v1/responses", "responses", model.ID, false, http.StatusBadGateway, start, err.Error())
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(upResp.Body, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure("/v1/responses", "responses", model.ID, false, http.StatusBadGateway, start, rErr.Error())
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
				Model:              model.ID,
				Instructions:       effectiveInstr,
				InputItems:         turnItems,
				OutputItems:        extractOutputItems(respMap),
				Response:           respMap,
			})
		}
	}
	WriteJSON(w, http.StatusOK, respMap)

	g.respDir.RecordRequest(RequestLogEntry{
		Endpoint:         "/v1/responses",
		UpstreamAPI:      "responses",
		Model:            model.ID,
		Stream:           false,
		Status:           http.StatusOK,
		DurationMs:       time.Since(start).Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CachedTokens:     usage.CachedTokens,
	})
}

func (g *Gateway) execResponsesViaChatUpstream(w http.ResponseWriter, r *http.Request, req *ResponsesRequest, model bohrium.SciXModel, turnItems, allItems []map[string]any, effectiveInstr *string, shouldStore bool, traceparent string, start time.Time) {
	isToolCont := isResponsesToolContinuation(turnItems)
	toolCallID := lastResponsesToolCallID(turnItems)
	sessionID, execHdr := g.resolveExecEnvelope(
		r.Header.Get(bohrium.SciXExecutionHeader),
		sessionSeedForResponses(req, g.respDir, allItems),
		model.ID,
		"",
		toolCallID,
		isToolCont,
	)

	payloadMap, question, err := ResponsesToChatRequest(req, model, allItems, effectiveInstr, SciXRequestOptions{
		SciMasterPrompt: g.sciMasterPromptEnabled(),
		ForceStream:     true,
		SessionID:       sessionID,
	})
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		g.recordFailure("/v1/responses", "chat_completions", model.ID, req.Stream, http.StatusBadRequest, start, err.Error())
		return
	}
	if question != "" {
		_, execHdr = g.resolveExecEnvelopeWithQuestion(
			r.Header.Get(bohrium.SciXExecutionHeader),
			sessionID,
			model.ID,
			question,
			toolCallID,
		)
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "marshal_error", err.Error())
		return
	}

	upResp, err := g.client.DoSciXLLM(r.Context(), "/chat/completions", payloadBytes, true, execHdr, traceparent)
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_error", "upstream_transport_error", err.Error())
		g.recordFailure("/v1/responses", "chat_completions", model.ID, req.Stream, http.StatusBadGateway, start, err.Error())
		return
	}
	defer upResp.Body.Close()

	copyUpstreamRequestID(w, upResp)
	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(upResp.Body, 256*1024))
		errMsg := forwardUpstreamError(w, upResp.StatusCode, errBody)
		g.recordFailure("/v1/responses", "chat_completions", model.ID, req.Stream, upResp.StatusCode, start, errMsg)
		return
	}

	respID := RandomID("resp_")

	if req.Stream {
		finalResp, usage, sErr := StreamChatToResponsesSSE(w, upResp.Body, req, model.ID, respID)
		if shouldStore && finalResp != nil && finalResp.Status != "failed" {
			respMap := structToMap(finalResp)
			g.respDir.Save(&StoredResponse{
				ID:                 finalResp.ID,
				PreviousResponseID: req.PreviousResponseID,
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
		g.respDir.RecordRequest(RequestLogEntry{
			Endpoint:         "/v1/responses",
			UpstreamAPI:      "chat_completions",
			Model:            model.ID,
			Stream:           true,
			Status:           http.StatusOK,
			DurationMs:       time.Since(start).Milliseconds(),
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			ReasoningTokens:  usage.ReasoningTokens,
			CachedTokens:     usage.CachedTokens,
			Error:            errStr,
		})
		return
	}

	var chatResp *ChatCompletionResponse
	if isSSEContentType(upResp) {
		chatResp, err = AggregateChatSSE(upResp.Body, model.ID)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "stream_aggregate_error", err.Error())
			g.recordFailure("/v1/responses", "chat_completions", model.ID, false, http.StatusBadGateway, start, err.Error())
			return
		}
	} else {
		rawResp, rErr := io.ReadAll(io.LimitReader(upResp.Body, 32*1024*1024))
		if rErr != nil {
			WriteError(w, http.StatusBadGateway, "upstream_error", "read_error", rErr.Error())
			g.recordFailure("/v1/responses", "chat_completions", model.ID, false, http.StatusBadGateway, start, rErr.Error())
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
			Model:              model.ID,
			Instructions:       effectiveInstr,
			InputItems:         turnItems,
			OutputItems:        openAIResp.Output,
			Response:           structToMap(openAIResp),
		})
	}
	WriteJSON(w, http.StatusOK, openAIResp)

	g.respDir.RecordRequest(RequestLogEntry{
		Endpoint:         "/v1/responses",
		UpstreamAPI:      "chat_completions",
		Model:            model.ID,
		Stream:           false,
		Status:           http.StatusOK,
		DurationMs:       time.Since(start).Milliseconds(),
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CachedTokens:     usage.CachedTokens,
	})
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

func (g *Gateway) runPreflightIfEnabled(ctx context.Context, modelID, traceparent string) (status int, errType, code, msg string, ok bool) {
	if g.cfg == nil || !g.cfg.Get().Gateway.PreflightMode {
		return 0, "", "", "", true
	}
	allowed, reason, err := g.client.SciXBillingPreflight(ctx, modelID, traceparent)
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

// isChatToolContinuation reports whether a Chat Completions request is resuming an agent loop
// after local tool execution (last message is a tool/function output), matching brain.mjs which
// does not re-run /billing/preflight on mid-run tool steps.
func isChatToolContinuation(req *ChatCompletionRequest) bool {
	if req == nil || len(req.Messages) == 0 {
		return false
	}
	lastRole := strings.ToLower(strings.TrimSpace(req.Messages[len(req.Messages)-1].Role))
	return lastRole == "tool" || lastRole == "function"
}

// isResponsesToolContinuation reports whether the current turn consists solely of function_call_output items.
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

func (g *Gateway) recordFailure(endpoint, upstreamAPI, model string, stream bool, status int, start time.Time, errMsg string) {
	g.respDir.RecordRequest(RequestLogEntry{
		Endpoint:    endpoint,
		UpstreamAPI: upstreamAPI,
		Model:       model,
		Stream:      stream,
		Status:      status,
		DurationMs:  time.Since(start).Milliseconds(),
		Error:       errMsg,
	})
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

// resolveExecEnvelope maintains authentic Bohrium desktop-client session & run affinity:
// - same conversation keeps stable sessionId/rootSessionId/sessionCreatedAt;
// - new user turn starts a fresh runId/rootRunId;
// - tool continuation turn reuses the active runId/rootRunId and attaches toolCallId.
func (g *Gateway) resolveExecEnvelope(incomingHeader, sessionSeed, modelID, question, toolCallID string, isToolContinuation bool) (string, string) {
	sessionID := bohrium.DeterministicSessionUUID(sessionSeed)

	g.sessMu.Lock()
	if g.sessions == nil {
		g.sessions = make(map[string]*trackedSession)
	}
	now := time.Now().UTC()
	st, exists := g.sessions[sessionID]
	if !exists {
		if len(g.sessions) >= maxTrackedSessions {
			var oldestKey string
			var oldestTime time.Time
			for k, v := range g.sessions {
				if oldestKey == "" || v.updatedAt.Before(oldestTime) {
					oldestKey = k
					oldestTime = v.updatedAt
				}
			}
			if oldestKey != "" {
				delete(g.sessions, oldestKey)
			}
		}
		runID := bohrium.NewCanonicalUUID()
		st = &trackedSession{
			sessionID:        sessionID,
			rootSessionID:    sessionID,
			runID:            runID,
			rootRunID:        runID,
			sessionCreatedAt: now.Format("2006-01-02T15:04:05.000Z"),
			updatedAt:        now,
		}
		g.sessions[sessionID] = st
	} else {
		if !isToolContinuation || st.runID == "" {
			runID := bohrium.NewCanonicalUUID()
			st.runID = runID
			st.rootRunID = runID
		}
		st.updatedAt = now
	}
	cp := *st
	g.sessMu.Unlock()

	hdr := g.client.BuildSciXExecutionHeader(bohrium.SciXExecOptions{
		IncomingHeader:   incomingHeader,
		SessionID:        cp.sessionID,
		RootSessionID:    cp.rootSessionID,
		RunID:            cp.runID,
		RootRunID:        cp.rootRunID,
		SessionCreatedAt: cp.sessionCreatedAt,
		Question:         question,
		ToolCallID:       toolCallID,
		ModelID:          modelID,
	})
	return cp.sessionID, hdr
}

func (g *Gateway) resolveExecEnvelopeWithQuestion(incomingHeader, sessionID, modelID, question, toolCallID string) (string, string) {
	g.sessMu.Lock()
	var cp trackedSession
	if g.sessions != nil {
		if st, ok := g.sessions[sessionID]; ok {
			cp = *st
		}
	}
	g.sessMu.Unlock()
	hdr := g.client.BuildSciXExecutionHeader(bohrium.SciXExecOptions{
		IncomingHeader:   incomingHeader,
		SessionID:        cp.sessionID,
		RootSessionID:    cp.rootSessionID,
		RunID:            cp.runID,
		RootRunID:        cp.rootRunID,
		SessionCreatedAt: cp.sessionCreatedAt,
		Question:         question,
		ToolCallID:       toolCallID,
		ModelID:          modelID,
	})
	return sessionID, hdr
}

func sessionSeedForChat(req *ChatCompletionRequest) string {
	if req == nil {
		return ""
	}
	if req.PromptCacheKey != "" {
		return "pck:" + req.PromptCacheKey
	}
	if req.User != "" {
		return "user:" + req.User
	}
	// Derive a deterministic conversation seed from the earliest user message (plus optional leading system prompt)
	// so multi-turn follow-ups and tool-call loops stay bound to the same SciX sessionId.
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
	if firstUser != "" {
		if len(firstSys) > 256 {
			firstSys = firstSys[:256]
		}
		if len(firstUser) > 512 {
			firstUser = firstUser[:512]
		}
		return "chat:" + firstSys + "\x00" + firstUser
	}
	return ""
}

func sessionSeedForResponses(req *ResponsesRequest, rs *ResponseStore, allItems []map[string]any) string {
	if req == nil {
		return ""
	}
	if req.PromptCacheKey != "" {
		return "pck:" + req.PromptCacheKey
	}
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		rootID := strings.TrimSpace(req.PreviousResponseID)
		if rs != nil {
			rootID = rs.RootResponseID(rootID)
		}
		return "resp-chain:" + rootID
	}
	if req.User != "" {
		return "user:" + req.User
	}
	for _, it := range allItems {
		typ, _ := it["type"].(string)
		role, _ := it["role"].(string)
		if (typ == "message" || typ == "") && strings.EqualFold(role, "user") {
			txt := strings.TrimSpace(flattenContentToText(it["content"]))
			if txt != "" {
				if len(txt) > 512 {
					txt = txt[:512]
				}
				return "resp-first:" + txt
			}
		}
	}
	return ""
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
