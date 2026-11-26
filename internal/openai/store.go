package openai

import (
	"sync"
	"time"

	"bohrium-club-auto/internal/store"
)

const maxRecentRequests = 50

// StoredResponse aliases the SQLite-backed Responses API record with account & session_hash ownership.
type StoredResponse = store.ResponseRecord

// RequestLogEntry captures a detailed record of a gateway API call for dashboard visibility & SQLite analytics.
type RequestLogEntry struct {
	ID               string    `json:"id"`
	Timestamp        time.Time `json:"timestamp"`
	APIKeyID         string    `json:"apiKeyId,omitempty"`
	APIKeyName       string    `json:"apiKeyName,omitempty"`
	Endpoint         string    `json:"endpoint"` // "/v1/chat/completions" | "/v1/responses"
	UpstreamAPI      string    `json:"upstreamApi"`
	Bridged          bool      `json:"bridged,omitempty"`
	Model            string    `json:"model"`
	RequestedModel   string    `json:"requestedModel,omitempty"`
	ReasoningEffort  string    `json:"reasoningEffort,omitempty"`
	AccountID        string    `json:"accountId,omitempty"`
	AccountLabel     string    `json:"accountLabel,omitempty"`
	RoutingMode      string    `json:"routingMode,omitempty"` // "bound" | "new_bind" | "rebound" | "direct"
	SessionHash      string    `json:"sessionHash,omitempty"`
	RequestID        string    `json:"requestId,omitempty"`
	ClientIP         string    `json:"clientIp,omitempty"`
	UserAgent        string    `json:"userAgent,omitempty"`
	Stream           bool      `json:"stream"`
	HasTools         bool      `json:"hasTools,omitempty"`
	HasVision        bool      `json:"hasVision,omitempty"`
	PreflightMode    string    `json:"preflightMode,omitempty"` // "preflight" | "tool_skip" | "disabled"
	SciMasterPrompt  bool      `json:"sciMasterPrompt,omitempty"`
	UsageMultiplier  float64   `json:"usageMultiplier,omitempty"`
	WeightedTokens   float64   `json:"weightedTokens,omitempty"`
	Status           int       `json:"status"`
	FirstTokenMs     int64     `json:"firstTokenMs,omitempty"`
	DurationMs       int64     `json:"durationMs"`
	PromptTokens     int64     `json:"promptTokens"`
	CompletionTokens int64     `json:"completionTokens"`
	ReasoningTokens  int64     `json:"reasoningTokens"`
	CachedTokens     int64     `json:"cachedTokens"`
	Error            string    `json:"error,omitempty"`
}

// StatsSnapshot summarizes gateway traffic since server boot.
type StatsSnapshot struct {
	TotalRequests    int64             `json:"totalRequests"`
	SuccessRequests  int64             `json:"successRequests"`
	FailedRequests   int64             `json:"failedRequests"`
	ChatRequests     int64             `json:"chatRequests"`
	ResponseRequests int64             `json:"responseRequests"`
	StreamRequests   int64             `json:"streamRequests"`
	PromptTokens     int64             `json:"promptTokens"`
	CompletionTokens int64             `json:"completionTokens"`
	ReasoningTokens  int64             `json:"reasoningTokens"`
	CachedTokens     int64             `json:"cachedTokens"`
	StoredResponses  int               `json:"storedResponses"`
	ModelCounts      map[string]int64  `json:"modelCounts"`
	RecentRequests   []RequestLogEntry `json:"recentRequests"`
}

// ResponseStore persists Responses API state in SQLite via *store.Store and tracks live gateway metrics.
type ResponseStore struct {
	mu          sync.RWMutex
	db          *store.Store
	stats       StatsSnapshot
	modelCounts map[string]int64
	recent      []RequestLogEntry
}

// OpenResponseStore wraps the shared SQLite store for Responses API persistence and live telemetry.
func OpenResponseStore(st *store.Store) *ResponseStore {
	rs := &ResponseStore{
		db:          st,
		modelCounts: make(map[string]int64),
		recent:      make([]RequestLogEntry, 0, maxRecentRequests),
	}
	if st != nil {
		for _, rec := range st.RecentGatewayUsage(maxRecentRequests) {
			rs.recent = append(rs.recent, RequestLogEntry{
				ID:               rec.ID,
				Timestamp:        rec.CreatedAt,
				APIKeyID:         rec.APIKeyID,
				APIKeyName:       rec.APIKeyName,
				Endpoint:         rec.Endpoint,
				UpstreamAPI:      rec.UpstreamAPI,
				Bridged:          rec.Bridged,
				Model:            rec.Model,
				RequestedModel:   rec.RequestedModel,
				ReasoningEffort:  rec.ReasoningEffort,
				AccountID:        rec.AccountID,
				AccountLabel:     rec.AccountLabel,
				RoutingMode:      rec.RoutingMode,
				SessionHash:      rec.SessionHash,
				RequestID:        rec.RequestID,
				ClientIP:         rec.ClientIP,
				UserAgent:        rec.UserAgent,
				Stream:           rec.Stream,
				HasTools:         rec.HasTools,
				HasVision:        rec.HasVision,
				PreflightMode:    rec.PreflightMode,
				SciMasterPrompt:  rec.SciMasterPrompt,
				UsageMultiplier:  rec.UsageMultiplier,
				WeightedTokens:   rec.WeightedTokens,
				Status:           rec.Status,
				FirstTokenMs:     rec.FirstTokenMs,
				DurationMs:       rec.DurationMs,
				PromptTokens:     rec.PromptTokens,
				CompletionTokens: rec.CompletionTokens,
				ReasoningTokens:  rec.ReasoningTokens,
				CachedTokens:     rec.CachedTokens,
				Error:            rec.Error,
			})
		}
	}
	return rs
}

// Save stores a completed response record in SQLite.
func (rs *ResponseStore) Save(rec *StoredResponse) {
	if rs == nil || rs.db == nil || rec == nil || rec.ID == "" {
		return
	}
	_ = rs.db.SaveResponse(rec)
}

// Get returns a stored response by ID.
func (rs *ResponseStore) Get(id string) (*StoredResponse, bool) {
	if rs == nil || rs.db == nil {
		return nil, false
	}
	return rs.db.GetResponse(id)
}

// Delete removes a stored response by ID.
func (rs *ResponseStore) Delete(id string) bool {
	if rs == nil || rs.db == nil {
		return false
	}
	return rs.db.DeleteResponse(id)
}

// BuildChainItems collects all prior input + output items up to prevID in chronological order,
// along with last non-empty instructions, root response ID, owning accountID, and sessionHash.
func (rs *ResponseStore) BuildChainItems(prevID string) ([]map[string]any, *string, string, string, string, bool) {
	if rs == nil || rs.db == nil {
		return nil, nil, "", "", "", false
	}
	return rs.db.BuildResponseChain(prevID)
}

// FindItemByID resolves an item_reference against stored response input/output items.
func (rs *ResponseStore) FindItemByID(itemID string) (map[string]any, bool) {
	if rs == nil || rs.db == nil {
		return nil, false
	}
	return rs.db.FindResponseItemByID(itemID)
}

// RecordRequest updates in-memory gateway metrics and recent request ring buffer.
func (rs *ResponseStore) RecordRequest(entry RequestLogEntry) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.ID == "" {
		entry.ID = RandomID("req_")
	}
	rs.stats.TotalRequests++
	if entry.Status >= 200 && entry.Status < 300 && entry.Error == "" {
		rs.stats.SuccessRequests++
	} else {
		rs.stats.FailedRequests++
	}
	switch entry.Endpoint {
	case "/v1/chat/completions":
		rs.stats.ChatRequests++
	case "/v1/responses":
		rs.stats.ResponseRequests++
	}
	if entry.Stream {
		rs.stats.StreamRequests++
	}
	rs.stats.PromptTokens += entry.PromptTokens
	rs.stats.CompletionTokens += entry.CompletionTokens
	rs.stats.ReasoningTokens += entry.ReasoningTokens
	rs.stats.CachedTokens += entry.CachedTokens
	if entry.Model != "" {
		rs.modelCounts[entry.Model]++
	}
	rs.recent = append([]RequestLogEntry{entry}, rs.recent...)
	if len(rs.recent) > maxRecentRequests {
		rs.recent = rs.recent[:maxRecentRequests]
	}
	if rs.db != nil {
		_ = rs.db.RecordGatewayUsage(store.GatewayUsageRecord{
			ID:               entry.ID,
			CreatedAt:        entry.Timestamp,
			APIKeyID:         entry.APIKeyID,
			APIKeyName:       entry.APIKeyName,
			AccountID:        entry.AccountID,
			AccountLabel:     entry.AccountLabel,
			Model:            entry.Model,
			RequestedModel:   entry.RequestedModel,
			ReasoningEffort:  entry.ReasoningEffort,
			Endpoint:         entry.Endpoint,
			UpstreamAPI:      entry.UpstreamAPI,
			Bridged:          entry.Bridged,
			RoutingMode:      entry.RoutingMode,
			SessionHash:      entry.SessionHash,
			RequestID:        entry.RequestID,
			ClientIP:         entry.ClientIP,
			UserAgent:        entry.UserAgent,
			Stream:           entry.Stream,
			HasTools:         entry.HasTools,
			HasVision:        entry.HasVision,
			PreflightMode:    entry.PreflightMode,
			SciMasterPrompt:  entry.SciMasterPrompt,
			UsageMultiplier:  entry.UsageMultiplier,
			WeightedTokens:   entry.WeightedTokens,
			Status:           entry.Status,
			FirstTokenMs:     entry.FirstTokenMs,
			DurationMs:       entry.DurationMs,
			PromptTokens:     entry.PromptTokens,
			CompletionTokens: entry.CompletionTokens,
			ReasoningTokens:  entry.ReasoningTokens,
			CachedTokens:     entry.CachedTokens,
			TotalTokens:      entry.PromptTokens + entry.CompletionTokens,
			Error:            entry.Error,
		})
	}
}

// Stats returns a thread-safe snapshot of gateway metrics.
func (rs *ResponseStore) Stats() StatsSnapshot {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	cp := rs.stats
	if rs.db != nil {
		cp.StoredResponses = rs.db.CountResponses()
	}
	cp.ModelCounts = make(map[string]int64, len(rs.modelCounts))
	for k, v := range rs.modelCounts {
		cp.ModelCounts[k] = v
	}
	cp.RecentRequests = make([]RequestLogEntry, len(rs.recent))
	copy(cp.RecentRequests, rs.recent)
	return cp
}
