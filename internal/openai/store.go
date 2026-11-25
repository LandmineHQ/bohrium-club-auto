package openai

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maxStoredResponses = 200
	maxRecentRequests  = 50
)

// StoredResponse persists an OpenAI Responses API record so previous_response_id,
// GET /v1/responses/{id}, DELETE /v1/responses/{id}, and /input_items work seamlessly.
type StoredResponse struct {
	ID                 string           `json:"id"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
	Model              string           `json:"model"`
	Instructions       *string          `json:"instructions,omitempty"`
	InputItems         []map[string]any `json:"input_items"`
	OutputItems        []map[string]any `json:"output_items"`
	Response           map[string]any   `json:"response"`
	CreatedAt          time.Time        `json:"created_at"`
}

// RequestLogEntry captures a compact record of a gateway API call for dashboard visibility.
type RequestLogEntry struct {
	ID               string    `json:"id"`
	Timestamp        time.Time `json:"timestamp"`
	Endpoint         string    `json:"endpoint"` // "/v1/chat/completions" | "/v1/responses"
	UpstreamAPI      string    `json:"upstreamApi"`
	Model            string    `json:"model"`
	Stream           bool      `json:"stream"`
	Status           int       `json:"status"`
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

// ResponseStore manages persisted Responses API state and live gateway metrics.
type ResponseStore struct {
	mu      sync.RWMutex
	path    string
	order   []string
	records map[string]*StoredResponse

	stats       StatsSnapshot
	modelCounts map[string]int64
	recent      []RequestLogEntry
}

// OpenResponseStore loads or initializes <dataDir>/responses.json.
func OpenResponseStore(dataDir string) *ResponseStore {
	rs := &ResponseStore{
		path:        filepath.Join(dataDir, "responses.json"),
		order:       make([]string, 0, maxStoredResponses),
		records:     make(map[string]*StoredResponse),
		modelCounts: make(map[string]int64),
		recent:      make([]RequestLogEntry, 0, maxRecentRequests),
	}
	if dataDir == "" {
		rs.path = ""
		return rs
	}
	raw, err := os.ReadFile(rs.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return rs
		}
		return rs
	}
	var disk struct {
		Order   []string                   `json:"order"`
		Records map[string]*StoredResponse `json:"records"`
	}
	if err := json.Unmarshal(raw, &disk); err == nil && disk.Records != nil {
		rs.order = disk.Order
		rs.records = disk.Records
	}
	return rs
}

// Save stores a completed response record and prunes oldest entries past maxStoredResponses.
func (rs *ResponseStore) Save(rec *StoredResponse) {
	if rec == nil || rec.ID == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if _, exists := rs.records[rec.ID]; !exists {
		rs.order = append(rs.order, rec.ID)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	rs.records[rec.ID] = rec
	for len(rs.order) > maxStoredResponses {
		oldest := rs.order[0]
		rs.order = rs.order[1:]
		delete(rs.records, oldest)
	}
	rs.persistLocked()
}

// Get returns a stored response by ID.
func (rs *ResponseStore) Get(id string) (*StoredResponse, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	rec, ok := rs.records[id]
	return rec, ok
}

// Delete removes a stored response by ID.
func (rs *ResponseStore) Delete(id string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if _, ok := rs.records[id]; !ok {
		return false
	}
	delete(rs.records, id)
	nextOrder := make([]string, 0, len(rs.order))
	for _, oid := range rs.order {
		if oid != id {
			nextOrder = append(nextOrder, oid)
		}
	}
	rs.order = nextOrder
	rs.persistLocked()
	return true
}

// BuildChainItems recursively collects all prior input + output items up to prevID in chronological order.
func (rs *ResponseStore) BuildChainItems(prevID string) ([]map[string]any, *string, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	var chain []*StoredResponse
	visited := make(map[string]bool)
	cur := prevID
	for cur != "" {
		if visited[cur] {
			break
		}
		visited[cur] = true
		rec, ok := rs.records[cur]
		if !ok {
			if len(chain) == 0 {
				return nil, nil, false
			}
			break
		}
		chain = append(chain, rec)
		cur = rec.PreviousResponseID
	}
	if len(chain) == 0 {
		return nil, nil, false
	}

	// Reverse so oldest turn comes first.
	var items []map[string]any
	var lastInstr *string
	for i := len(chain) - 1; i >= 0; i-- {
		rec := chain[i]
		if rec.Instructions != nil && *rec.Instructions != "" {
			lastInstr = rec.Instructions
		}
		items = append(items, rec.InputItems...)
		items = append(items, rec.OutputItems...)
	}
	return items, lastInstr, true
}

// RootResponseID walks the previous_response_id chain back to the earliest stored response ID,
// allowing multi-turn Responses API chains to share a stable SciX sessionId.
func (rs *ResponseStore) RootResponseID(id string) string {
	if rs == nil || id == "" {
		return id
	}
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	visited := make(map[string]bool)
	cur := id
	root := id
	for cur != "" && !visited[cur] {
		visited[cur] = true
		rec, ok := rs.records[cur]
		if !ok {
			break
		}
		root = rec.ID
		if rec.PreviousResponseID == "" {
			break
		}
		cur = rec.PreviousResponseID
	}
	return root
}

// FindItemByID resolves an item_reference against stored response input/output items.
func (rs *ResponseStore) FindItemByID(itemID string) (map[string]any, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	for i := len(rs.order) - 1; i >= 0; i-- {
		rec := rs.records[rs.order[i]]
		if rec == nil {
			continue
		}
		for _, it := range rec.OutputItems {
			if id, _ := it["id"].(string); id == itemID {
				return it, true
			}
		}
		for _, it := range rec.InputItems {
			if id, _ := it["id"].(string); id == itemID {
				return it, true
			}
		}
	}
	return nil, false
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
}

// Stats returns a thread-safe snapshot of gateway metrics.
func (rs *ResponseStore) Stats() StatsSnapshot {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	cp := rs.stats
	cp.StoredResponses = len(rs.records)
	cp.ModelCounts = make(map[string]int64, len(rs.modelCounts))
	for k, v := range rs.modelCounts {
		cp.ModelCounts[k] = v
	}
	cp.RecentRequests = make([]RequestLogEntry, len(rs.recent))
	copy(cp.RecentRequests, rs.recent)
	return cp
}

func (rs *ResponseStore) persistLocked() {
	if rs.path == "" {
		return
	}
	data, err := json.Marshal(map[string]any{
		"order":   rs.order,
		"records": rs.records,
	})
	if err != nil {
		return
	}
	tmp := rs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, rs.path)
}
