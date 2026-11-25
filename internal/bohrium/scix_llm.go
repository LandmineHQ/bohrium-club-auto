package bohrium

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const scixCatalogTTL = 60 * time.Second

var canonicalUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ErrBohriumNotLoggedIn indicates that no Bohrium brmToken is currently stored.
var ErrBohriumNotLoggedIn = errors.New("Bohrium account is not logged in; please sign in via the Web Console first")

// SciXModelPresentation mirrors the desktop client UI metadata returned by Sigma.
type SciXModelPresentation struct {
	Visible         bool    `json:"visible"`
	Name            string  `json:"name,omitempty"`
	Description     string  `json:"description,omitempty"`
	SortOrder       int     `json:"sortOrder"`
	UsageMultiplier float64 `json:"usageMultiplier,omitempty"`
	Default         bool    `json:"default,omitempty"`
}

// SciXModel represents a single model entry from Bohrium desktop client's /models?tag=shrimp-brain-v2 catalog.
type SciXModel struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name,omitempty"`
	Object           string                 `json:"object,omitempty"`
	Created          int64                  `json:"created,omitempty"`
	OwnedBy          string                 `json:"owned_by,omitempty"`
	API              string                 `json:"api,omitempty"` // "chat_completions" | "responses" | "" (empty defaults to responses)
	ContextWindow    int                    `json:"context_window,omitempty"`
	MaxOutputTokens  int                    `json:"max_output_tokens,omitempty"`
	Input            []string               `json:"input,omitempty"`
	ReasoningEffort  string                 `json:"reasoning_effort,omitempty"`
	ReasoningEfforts []string               `json:"reasoning_efforts,omitempty"`
	Hidden           bool                   `json:"hidden,omitempty"`
	Presentation     *SciXModelPresentation `json:"presentation,omitempty"`
}

// EffectiveAPI returns the native Bohrium upstream protocol for this model ("chat_completions" or "responses").
// Per brain.mjs (createScixProfile), omitted api defaults to "responses".
func (m SciXModel) EffectiveAPI() string {
	if strings.EqualFold(strings.TrimSpace(m.API), "chat_completions") {
		return "chat_completions"
	}
	return "responses"
}

// SupportsImage reports whether this model accepts image inputs.
func (m SciXModel) SupportsImage() bool {
	for _, mod := range m.Input {
		if strings.EqualFold(mod, "image") {
			return true
		}
	}
	return false
}

// SupportsReasoning reports whether this model supports reasoning_effort control.
func (m SciXModel) SupportsReasoning() bool {
	return len(m.ReasoningEfforts) > 0 || m.ReasoningEffort != ""
}

// ClampReasoningEffort maps an arbitrary OpenAI reasoning effort onto one of the model's supported values.
// Returns "" if the model does not support reasoning or if requested is empty/"none"/"off".
func (m SciXModel) ClampReasoningEffort(requested string) string {
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "" || req == "none" || req == "off" {
		return ""
	}
	if len(m.ReasoningEfforts) == 0 {
		return ""
	}
	for _, allowed := range m.ReasoningEfforts {
		if strings.EqualFold(allowed, req) {
			return allowed
		}
	}
	// Nearest-neighbor mapping across OpenAI's effort ladder (minimal -> low -> medium -> high -> xhigh -> max).
	fallbacks := map[string][]string{
		"minimal": {"low", "medium", "high"},
		"low":     {"low", "minimal", "medium", "high"},
		"medium":  {"medium", "high", "low"},
		"high":    {"high", "max", "medium", "low"},
		"xhigh":   {"max", "high", "medium"},
		"max":     {"max", "high", "medium"},
	}
	if prefs, ok := fallbacks[req]; ok {
		for _, p := range prefs {
			for _, allowed := range m.ReasoningEfforts {
				if strings.EqualFold(allowed, p) {
					return allowed
				}
			}
		}
	}
	if m.ReasoningEffort != "" {
		return m.ReasoningEffort
	}
	return m.ReasoningEfforts[0]
}

// SciXImageDescribe matches the image_describe catalog field.
type SciXImageDescribe struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// SciXSessionPurpose matches session_purposes entries (e.g. session-title, tool-batch-summary).
type SciXSessionPurpose struct {
	Model string `json:"model"`
}

// SciXCatalogVersion mirrors the desktop client's scix_catalog metadata required by brain.mjs.
type SciXCatalogVersion struct {
	ScopeGeneration string `json:"scopeGeneration"`
	ContentVersion  string `json:"contentVersion"`
}

// SciXModelCatalog is the decoded response from GET /api/v1/scix/llm/models?tag=shrimp-brain-v2.
type SciXModelCatalog struct {
	Object          string                        `json:"object"`
	Data            []SciXModel                   `json:"data"`
	ImageDescribe   *SciXImageDescribe            `json:"image_describe,omitempty"`
	SessionPurposes map[string]SciXSessionPurpose `json:"session_purposes,omitempty"`
	SciXCatalog     *SciXCatalogVersion           `json:"scix_catalog,omitempty"`
	FetchedAt       time.Time                     `json:"-"`
}

// Clone returns a shallow-safe copy of the model catalog.
func (cat *SciXModelCatalog) Clone() *SciXModelCatalog {
	if cat == nil {
		return nil
	}
	cp := *cat
	if cat.Data != nil {
		cp.Data = make([]SciXModel, len(cat.Data))
		copy(cp.Data, cat.Data)
	}
	if cat.ImageDescribe != nil {
		id := *cat.ImageDescribe
		cp.ImageDescribe = &id
	}
	if cat.SessionPurposes != nil {
		cp.SessionPurposes = make(map[string]SciXSessionPurpose, len(cat.SessionPurposes))
		for k, v := range cat.SessionPurposes {
			cp.SessionPurposes[k] = v
		}
	}
	if cat.SciXCatalog != nil {
		v := *cat.SciXCatalog
		cp.SciXCatalog = &v
	}
	return &cp
}

// DefaultModelID returns the catalog's default model ID, falling back to fallbackID or first visible model.
func (cat *SciXModelCatalog) DefaultModelID(fallbackID string) string {
	if cat != nil {
		if fallbackID != "" {
			if m, ok := cat.FindModel(fallbackID); ok {
				return m.ID
			}
		}
		for _, m := range cat.Data {
			if m.Presentation != nil && m.Presentation.Default {
				return m.ID
			}
		}
		for _, m := range cat.Data {
			if !m.Hidden {
				return m.ID
			}
		}
		if len(cat.Data) > 0 {
			return cat.Data[0].ID
		}
	}
	if fallbackID != "" {
		return fallbackID
	}
	return "dspro-high"
}

// FindModel resolves a model by exact ID, scix/<id> prefix, case-insensitive ID, or display name alias.
func (cat *SciXModelCatalog) FindModel(requested string) (SciXModel, bool) {
	if cat == nil {
		return SciXModel{}, false
	}
	q := strings.TrimSpace(requested)
	q = strings.TrimPrefix(q, "scix/")
	if q == "" {
		return SciXModel{}, false
	}
	// 1. Exact ID
	for _, m := range cat.Data {
		if m.ID == q {
			return m, true
		}
	}
	// 2. Case-insensitive ID
	ql := strings.ToLower(q)
	for _, m := range cat.Data {
		if strings.ToLower(m.ID) == ql {
			return m, true
		}
	}
	// 3. Display name or presentation name (normalized spaces/hyphens)
	qNorm := normalizeModelAlias(q)
	for _, m := range cat.Data {
		if m.Name != "" && normalizeModelAlias(m.Name) == qNorm {
			return m, true
		}
		if m.Presentation != nil && m.Presentation.Name != "" && normalizeModelAlias(m.Presentation.Name) == qNorm {
			return m, true
		}
	}
	return SciXModel{}, false
}

func normalizeModelAlias(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	return s
}

// GetSciXModelCatalog fetches (with 60s TTL cache) Bohrium desktop client's live model catalog.
func (c *Client) GetSciXModelCatalog(ctx context.Context, forceRefresh bool) (*SciXModelCatalog, error) {
	auth := c.store.GetAuth()
	if strings.TrimSpace(auth.Token) == "" {
		return nil, ErrBohriumNotLoggedIn
	}

	c.catalogMu.RLock()
	cached := c.catalogCache
	fetchedAt := c.catalogFetchedAt
	cachedUID := c.catalogUserID
	c.catalogMu.RUnlock()

	if !forceRefresh && cached != nil && cachedUID == auth.UserID && time.Since(fetchedAt) < scixCatalogTTL {
		return cached.Clone(), nil
	}

	rawURL := c.endpoints.SigmaLLMBase + "/models?tag=" + url.QueryEscape(SciXModelCatalogTag)
	doCatalog := func(tok string) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("traceparent", NewTraceparent())
		req.Header.Set(SciXRuntimeHeader, SciXRuntimeValue)
		resp, err := c.http.DoNative(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return body, resp.StatusCode, err
	}

	body, statusCode, err := doCatalog(auth.Token)
	if err == nil && statusCode == http.StatusUnauthorized {
		if refreshed, rErr := c.RefreshToken(ctx, "system"); rErr == nil && refreshed.Token != "" {
			body, statusCode, err = doCatalog(refreshed.Token)
		}
	}
	if err != nil {
		if cached != nil && cachedUID == auth.UserID {
			return cached.Clone(), nil
		}
		return nil, fmt.Errorf("fetch SciX model catalog: %w", err)
	}
	if statusCode < 200 || statusCode >= 300 {
		if cached != nil && cachedUID == auth.UserID && statusCode >= 500 {
			return cached.Clone(), nil
		}
		return nil, fmt.Errorf("SciX model catalog returned HTTP %d: %s", statusCode, truncateText(string(body), 240))
	}

	var cat SciXModelCatalog
	if err := json.Unmarshal(body, &cat); err != nil {
		return nil, fmt.Errorf("parse SciX model catalog: %w", err)
	}
	if cat.Object == "" {
		cat.Object = "list"
	}
	nowUnix := time.Now().UTC().Unix()
	for i := range cat.Data {
		if cat.Data[i].Object == "" {
			cat.Data[i].Object = "model"
		}
		if cat.Data[i].OwnedBy == "" {
			cat.Data[i].OwnedBy = "sigma"
		}
		if cat.Data[i].Created == 0 {
			cat.Data[i].Created = nowUnix
		}
	}
	digest := sha256.Sum256(body)
	cat.SciXCatalog = &SciXCatalogVersion{
		ScopeGeneration: fmt.Sprintf("u%d-o%d", auth.UserID, auth.OrgID),
		ContentVersion:  hex.EncodeToString(digest[:8]),
	}
	cat.FetchedAt = time.Now().UTC()

	c.catalogMu.Lock()
	c.catalogCache = cat.Clone()
	c.catalogFetchedAt = cat.FetchedAt
	c.catalogUserID = auth.UserID
	c.catalogMu.Unlock()

	return cat.Clone(), nil
}

// ResolveSciXModel looks up a requested model ID/alias against the live SciX catalog.
func (c *Client) ResolveSciXModel(ctx context.Context, requested string) (SciXModel, *SciXModelCatalog, error) {
	cat, err := c.GetSciXModelCatalog(ctx, false)
	if err != nil {
		return SciXModel{}, nil, err
	}
	target := strings.TrimSpace(requested)
	if target == "" {
		target = cat.DefaultModelID(c.cfg.Get().Gateway.DefaultModel)
	}
	if m, ok := cat.FindModel(target); ok {
		return m, cat, nil
	}
	// If cache is older than 5 seconds, try refreshing once in case Bohrium rolled out new models.
	if time.Since(cat.FetchedAt) > 5*time.Second {
		if fresh, rErr := c.GetSciXModelCatalog(ctx, true); rErr == nil {
			cat = fresh
			if m, ok := cat.FindModel(target); ok {
				return m, cat, nil
			}
		}
	}
	return SciXModel{}, cat, fmt.Errorf("model %q not found in Bohrium SciX catalog", requested)
}

// SciXExecOptions controls the x-scix-execution envelope generated for Bohrium desktop compatibility.
type SciXExecOptions struct {
	IncomingHeader   string
	SessionSeed      string
	SessionID        string
	RootSessionID    string
	RunID            string
	RootRunID        string
	SessionCreatedAt string
	Purpose          string
	Question         string
	ToolCallID       string
	ModelID          string
}

// SciXExecutionPayload mirrors the exact JSON schema sent in the x-scix-execution header by scix.exe v0.10.25.
type SciXExecutionPayload struct {
	Version          int    `json:"version"`
	Scenario         string `json:"scenario"`
	StoreID          string `json:"storeId"`
	RootSessionID    string `json:"rootSessionId"`
	SessionID        string `json:"sessionId"`
	CallID           string `json:"callId"`
	Purpose          string `json:"purpose"`
	RecoveryAttempt  int    `json:"recoveryAttempt"`
	RootRunID        string `json:"rootRunId,omitempty"`
	RunID            string `json:"runId,omitempty"`
	ToolCallID       string `json:"toolCallId,omitempty"`
	Visibility       string `json:"visibility,omitempty"`
	SessionCreatedAt string `json:"sessionCreatedAt"`
	Question         string `json:"question"`
}

// BuildSciXExecutionHeader produces an authentic unpadded-base64url x-scix-execution header matching scix.exe.
// If opts.IncomingHeader is provided (e.g. from brain.mjs), it preserves caller metadata while ensuring
// scenario="local_agent" and canonical UUIDs, mirroring scix.exe's src/llm/execution.rs behavior.
func (c *Client) BuildSciXExecutionHeader(opts SciXExecOptions) string {
	storeID := ""
	if c.store != nil {
		storeID = strings.TrimSpace(c.store.StoreID())
	}
	if !isCanonicalUUID(storeID) && c.cfg != nil {
		storeID = strings.TrimSpace(c.cfg.Get().Gateway.StoreID)
	}
	if !isCanonicalUUID(storeID) {
		storeID = newUUIDv4()
	}

	purpose := strings.TrimSpace(opts.Purpose)
	if purpose == "" {
		switch strings.ToLower(strings.TrimSpace(opts.ModelID)) {
		case "title":
			purpose = "session-title"
		case "summary":
			purpose = "tool-batch-summary"
		default:
			purpose = "agent"
		}
	}

	var p SciXExecutionPayload
	if raw := strings.TrimSpace(opts.IncomingHeader); raw != "" {
		if decoded, err := decodeBase64URLFlexible(raw); err == nil {
			_ = json.Unmarshal(decoded, &p)
		}
	}

	p.Version = 1
	if strings.TrimSpace(p.Scenario) == "" {
		p.Scenario = SciXExecutionScenario
	}
	if !isCanonicalUUID(p.StoreID) {
		p.StoreID = storeID
	}
	sessionID := strings.TrimSpace(p.SessionID)
	if !isCanonicalUUID(sessionID) {
		if isCanonicalUUID(opts.SessionID) {
			sessionID = strings.ToLower(strings.TrimSpace(opts.SessionID))
		} else if strings.TrimSpace(opts.SessionSeed) != "" {
			sessionID = deterministicUUID("scix-session:" + opts.SessionSeed)
		} else {
			sessionID = newUUIDv4()
		}
	}
	p.SessionID = sessionID
	if !isCanonicalUUID(p.RootSessionID) {
		if isCanonicalUUID(opts.RootSessionID) {
			p.RootSessionID = strings.ToLower(strings.TrimSpace(opts.RootSessionID))
		} else {
			p.RootSessionID = sessionID
		}
	}
	if !isCanonicalUUID(p.CallID) {
		p.CallID = newUUIDv4()
	}
	if strings.TrimSpace(p.Purpose) == "" {
		p.Purpose = purpose
	}
	if p.RecoveryAttempt < 0 || p.RecoveryAttempt > 2 {
		p.RecoveryAttempt = 0
	}
	runID := strings.TrimSpace(p.RunID)
	if !isCanonicalUUID(runID) {
		if isCanonicalUUID(opts.RunID) {
			runID = strings.ToLower(strings.TrimSpace(opts.RunID))
		} else {
			runID = newUUIDv4()
		}
	}
	p.RunID = runID
	if !isCanonicalUUID(p.RootRunID) {
		if isCanonicalUUID(opts.RootRunID) {
			p.RootRunID = strings.ToLower(strings.TrimSpace(opts.RootRunID))
		} else {
			p.RootRunID = runID
		}
	}
	if p.ToolCallID == "" && strings.TrimSpace(opts.ToolCallID) != "" {
		p.ToolCallID = strings.TrimSpace(opts.ToolCallID)
	}
	if strings.TrimSpace(p.SessionCreatedAt) == "" {
		if strings.TrimSpace(opts.SessionCreatedAt) != "" {
			p.SessionCreatedAt = strings.TrimSpace(opts.SessionCreatedAt)
		} else {
			p.SessionCreatedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	if p.Question == "" && strings.TrimSpace(opts.Question) != "" {
		p.Question = clampRunes(strings.TrimSpace(opts.Question), 512)
	}

	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DeterministicSessionUUID converts a stable conversation seed into a canonical lowercase UUIDv5.
func DeterministicSessionUUID(seed string) string {
	if strings.TrimSpace(seed) == "" {
		return newUUIDv4()
	}
	return deterministicUUID("scix-session:" + seed)
}

// NewCanonicalUUID returns a fresh canonical lowercase RFC 4122 UUIDv4.
func NewCanonicalUUID() string {
	return newUUIDv4()
}

// NewTraceparent returns a valid W3C traceparent header ("00-<32hex>-<16hex>-01"), matching scix.exe.
func NewTraceparent() string {
	var traceID [16]byte
	var spanID [8]byte
	_, _ = rand.Read(traceID[:])
	_, _ = rand.Read(spanID[:])
	// Ensure non-zero per W3C Trace Context spec
	traceID[0] |= 1
	spanID[0] |= 1
	return fmt.Sprintf("00-%s-%s-01", hex.EncodeToString(traceID[:]), hex.EncodeToString(spanID[:]))
}

// SciXBillingPreflight calls POST /api/v1/scix/llm/billing/preflight to verify account admission for a model,
// mirroring Bohrium desktop client's preflight_brain_model check (Authorization + traceparent + x-scix-runtime,
// without browser headers or synthetic x-scix-execution).
func (c *Client) SciXBillingPreflight(ctx context.Context, modelID, traceparent string) (bool, string, error) {
	auth := c.store.GetAuth()
	token := strings.TrimSpace(auth.Token)
	if token == "" {
		return false, "", ErrBohriumNotLoggedIn
	}
	bodyBytes, err := json.Marshal(map[string]string{"model": modelID})
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(traceparent) == "" {
		traceparent = NewTraceparent()
	}
	targetURL := c.endpoints.SigmaLLMBase + "/billing/preflight"
	doPreflight := func(tok string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("traceparent", traceparent)
		req.Header.Set(SciXRuntimeHeader, SciXRuntimeValue)
		return c.http.DoNative(req)
	}
	resp, err := doPreflight(token)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		if refreshed, rErr := c.RefreshToken(ctx, "system"); rErr == nil && refreshed.Token != "" && refreshed.Token != token {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			resp, err = doPreflight(refreshed.Token)
		}
	}
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errEnv struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(raw, &errEnv)
		msg := errEnv.Error.Message
		if msg == "" {
			msg = errEnv.Message
		}
		if msg == "" {
			msg = errEnv.Reason
		}
		if resp.StatusCode == http.StatusPaymentRequired ||
			strings.Contains(strings.ToLower(string(raw)), "insufficient_balance") ||
			strings.Contains(string(raw), "BILLING_PHOTON_NOT_ENOUGH") {
			if msg == "" {
				msg = "[insufficient_balance] Insufficient balance"
			}
			return false, msg, nil
		}
		return false, msg, fmt.Errorf("billing preflight HTTP %d: %s", resp.StatusCode, truncateText(string(raw), 240))
	}
	var parsed struct {
		Allowed bool   `json:"allowed"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false, "", fmt.Errorf("parse billing preflight response: %w", err)
	}
	msg := parsed.Reason
	if msg == "" {
		msg = parsed.Message
	}
	if msg == "" && !parsed.Allowed {
		msg = "[insufficient_balance] Insufficient balance"
	}
	return parsed.Allowed, msg, nil
}

// DoSciXLLM performs a streaming-capable POST request to Bohrium desktop client's Sigma LLM gateway.
// Caller is responsible for closing resp.Body.
func (c *Client) DoSciXLLM(ctx context.Context, subPath string, payload []byte, stream bool, execHeader string, traceparent string) (*http.Response, error) {
	auth := c.store.GetAuth()
	token := strings.TrimSpace(auth.Token)
	if token == "" {
		return nil, ErrBohriumNotLoggedIn
	}
	if traceparent == "" {
		traceparent = NewTraceparent()
	}
	if execHeader == "" {
		execHeader = c.BuildSciXExecutionHeader(SciXExecOptions{})
	}
	targetURL := c.endpoints.SigmaLLMBase + "/" + strings.TrimLeft(subPath, "/")

	doReq := func(tok string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		} else {
			req.Header.Set("Accept", "application/json")
		}
		req.Header.Set("traceparent", traceparent)
		req.Header.Set(SciXExecutionHeader, execHeader)
		req.Header.Set(SciXRuntimeHeader, SciXRuntimeValue)
		return c.http.DoNativeStream(req)
	}

	resp, err := doReq(token)
	if err != nil {
		return nil, err
	}
	// If upstream returned 401 Unauthorized, try refreshing brmToken once and replaying.
	if resp.StatusCode == http.StatusUnauthorized {
		if refreshed, rErr := c.RefreshToken(ctx, "system"); rErr == nil && refreshed.Token != "" && refreshed.Token != token {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			return doReq(refreshed.Token)
		}
	}
	return resp, nil
}

func isCanonicalUUID(s string) bool {
	return canonicalUUIDRe.MatchString(strings.ToLower(strings.TrimSpace(s)))
}

func deterministicUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50 // version 5
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func decodeBase64URLFlexible(raw string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(raw)
}

func clampRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
