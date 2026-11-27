package store

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// GatewayUsageRecord persists a detailed OpenAI /v1/* gateway request record in SQLite.
type GatewayUsageRecord struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"createdAt"`
	APIKeyID         string    `json:"apiKeyId"`
	APIKeyName       string    `json:"apiKeyName"`
	AccountID        string    `json:"accountId"`
	AccountLabel     string    `json:"accountLabel"`
	Model            string    `json:"model"`
	RequestedModel   string    `json:"requestedModel,omitempty"`
	ReasoningEffort  string    `json:"reasoningEffort,omitempty"`
	Endpoint         string    `json:"endpoint"`
	UpstreamAPI      string    `json:"upstreamApi"`
	Bridged          bool      `json:"bridged"`
	RoutingMode      string    `json:"routingMode"`
	SessionHash      string    `json:"sessionHash,omitempty"`
	RequestID        string    `json:"requestId,omitempty"`
	ClientIP         string    `json:"clientIp,omitempty"`
	UserAgent        string    `json:"userAgent,omitempty"`
	Stream           bool      `json:"stream"`
	HasTools         bool      `json:"hasTools"`
	HasVision        bool      `json:"hasVision"`
	PreflightMode    string    `json:"preflightMode,omitempty"` // "preflight" | "tool_skip" | "disabled"
	SciMasterPrompt  bool      `json:"sciMasterPrompt"`
	UsageMultiplier  float64   `json:"usageMultiplier"`
	WeightedTokens   float64   `json:"weightedTokens"`
	Status           int       `json:"status"`
	FirstTokenMs     int64     `json:"firstTokenMs"`
	DurationMs       int64     `json:"durationMs"`
	PromptTokens     int64     `json:"promptTokens"`
	CompletionTokens int64     `json:"completionTokens"`
	ReasoningTokens  int64     `json:"reasoningTokens"`
	CachedTokens     int64     `json:"cachedTokens"`
	TotalTokens      int64     `json:"totalTokens"`
	Error            string    `json:"error,omitempty"`
}

// GatewayKeyUsage contains persisted per-key counters used by the key-management
// page and by admission-time quota checks.
type GatewayKeyUsage struct {
	APIKeyID      string     `json:"apiKeyId"`
	RequestCount  int64      `json:"requestCount"`
	TotalTokens   int64      `json:"totalTokens"`
	TodayRequests int64      `json:"todayRequests"`
	TodayTokens   int64      `json:"todayTokens"`
	LastUsedAt    *time.Time `json:"lastUsedAt,omitempty"`
}

// GatewayKeyUsageSummary combines durable all-time counters with the retained
// local-calendar-day rows used for daily quota enforcement.
func (s *Store) GatewayKeyUsageSummary(now time.Time, timezone string) (map[string]GatewayKeyUsage, error) {
	out := make(map[string]GatewayKeyUsage)
	if s == nil || s.db == nil {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT api_key_id, request_count, total_tokens, last_used_at FROM gateway_key_usage_totals`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var keyID string
		var requests, tokens, lastSec int64
		if err := rows.Scan(&keyID, &requests, &tokens, &lastSec); err != nil {
			_ = rows.Close()
			return nil, err
		}
		u := GatewayKeyUsage{APIKeyID: keyID, RequestCount: requests, TotalTokens: tokens}
		if lastSec > 0 {
			t := time.Unix(lastSec, 0).UTC()
			u.LastUsedAt = &t
		}
		out[keyID] = u
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	daily, err := s.gatewayDailyUsage(now, timezone, "")
	if err != nil {
		return nil, err
	}
	for keyID, counter := range daily {
		u := out[keyID]
		u.APIKeyID = keyID
		u.TodayRequests = counter.requests
		u.TodayTokens = counter.tokens
		out[keyID] = u
	}
	return out, nil
}

// GatewayKeyUsageForAdmission returns durable total usage and retained daily
// usage without loading individual request rows.
func (s *Store) GatewayKeyUsageForAdmission(apiKeyID string, now time.Time, timezone string) (totalTokens, todayTokens int64, err error) {
	if s == nil || s.db == nil || strings.TrimSpace(apiKeyID) == "" {
		return 0, 0, nil
	}
	if err = s.db.QueryRow(`SELECT COALESCE(total_tokens, 0) FROM gateway_key_usage_totals WHERE api_key_id = ?`, apiKeyID).Scan(&totalTokens); err != nil && !isErrNoRows(err) {
		return 0, 0, err
	}
	daily, err := s.gatewayDailyUsage(now, timezone, apiKeyID)
	if err != nil {
		return 0, 0, err
	}
	todayTokens = daily[apiKeyID].tokens
	return totalTokens, todayTokens, nil
}

type gatewayDailyCounter struct {
	requests int64
	tokens   int64
}

func gatewayDayStart(now time.Time, timezone string) int64 {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(timezone) == "" {
		timezone = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc).UTC().Unix()
}

// gatewayDailyUsage combines the retained current-day logs with the durable
// baseline captured before an administrator clears analytics history.
func (s *Store) gatewayDailyUsage(now time.Time, timezone, apiKeyID string) (map[string]gatewayDailyCounter, error) {
	out := make(map[string]gatewayDailyCounter)
	dayStart := gatewayDayStart(now, timezone)
	baselineQuery := `SELECT api_key_id, request_count, total_tokens FROM gateway_key_daily_baselines WHERE day_start = ?`
	baselineArgs := []any{dayStart}
	if apiKeyID != "" {
		baselineQuery += ` AND api_key_id = ?`
		baselineArgs = append(baselineArgs, apiKeyID)
	}
	baselineRows, err := s.db.Query(baselineQuery, baselineArgs...)
	if err != nil {
		return nil, err
	}
	for baselineRows.Next() {
		var keyID string
		var counter gatewayDailyCounter
		if err := baselineRows.Scan(&keyID, &counter.requests, &counter.tokens); err != nil {
			_ = baselineRows.Close()
			return nil, err
		}
		out[keyID] = counter
	}
	if err := baselineRows.Err(); err != nil {
		_ = baselineRows.Close()
		return nil, err
	}
	_ = baselineRows.Close()

	logQuery := `
		SELECT api_key_id, COUNT(*),
		       COALESCE(SUM(CASE WHEN total_tokens > 0 THEN total_tokens ELSE prompt_tokens + completion_tokens END), 0)
		FROM gateway_usage_logs WHERE created_at >= ?`
	logArgs := []any{dayStart}
	if apiKeyID != "" {
		logQuery += ` AND api_key_id = ?`
		logArgs = append(logArgs, apiKeyID)
	}
	logQuery += ` GROUP BY api_key_id`
	logRows, err := s.db.Query(logQuery, logArgs...)
	if err != nil {
		return nil, err
	}
	defer logRows.Close()
	for logRows.Next() {
		var keyID string
		var counter gatewayDailyCounter
		if err := logRows.Scan(&keyID, &counter.requests, &counter.tokens); err != nil {
			return nil, err
		}
		if strings.TrimSpace(keyID) == "" {
			keyID = "default"
		}
		baseline := out[keyID]
		baseline.requests += counter.requests
		baseline.tokens += counter.tokens
		out[keyID] = baseline
	}
	return out, logRows.Err()
}

// backfillGatewayKeyUsageTotals imports pre-existing analytics exactly once.
// Subsequent writes use transactional upserts, so retention purges never reduce
// the lifetime token balance used for quota admission.
func (s *Store) backfillGatewayKeyUsageTotals() error {
	var done string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'gateway_key_usage_totals_backfilled_v1'`).Scan(&done)
	if err == nil && done == "1" {
		return nil
	}
	if err != nil && !isErrNoRows(err) {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`
		INSERT INTO gateway_key_usage_totals (api_key_id, request_count, total_tokens, last_used_at)
		SELECT CASE WHEN api_key_id = '' THEN 'default' ELSE api_key_id END,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN total_tokens > 0 THEN total_tokens ELSE prompt_tokens + completion_tokens END), 0),
		       COALESCE(MAX(created_at), 0)
		FROM gateway_usage_logs GROUP BY CASE WHEN api_key_id = '' THEN 'default' ELSE api_key_id END
		ON CONFLICT(api_key_id) DO NOTHING`)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('gateway_key_usage_totals_backfilled_v1', '1')`); err != nil {
		return err
	}
	return tx.Commit()
}

// UsageQueryOptions filters and groups gateway analytics queries.
type UsageQueryOptions struct {
	RangeDays   int    // 1 (24h), 7, 30, 90, 180, or <=0 for all retained records
	Granularity string // "auto" | "hour" | "day"
	APIKeyID    string
	Model       string
	AccountID   string
	Timezone    string // e.g. "Asia/Shanghai"
	Now         time.Time
}

// UsageLogsQueryOptions filters and paginates detailed usage records (sub2api-style table).
type UsageLogsQueryOptions struct {
	RangeDays int
	APIKeyID  string
	Model     string
	AccountID string
	Status    string // "" | "all" | "success" | "error"
	Stream    string // "" | "all" | "true" | "false"
	Query     string // substring search across IP, UA, model, key, account, requestId, sessionHash, error
	Timezone  string
	Now       time.Time
	Page      int
	PageSize  int
}

// UsageLogsPage holds one page of detailed gateway usage records.
type UsageLogsPage struct {
	Items      []GatewayUsageRecord `json:"items"`
	Total      int64                `json:"total"`
	Page       int                  `json:"page"`
	PageSize   int                  `json:"pageSize"`
	TotalPages int                  `json:"totalPages"`
}

// UsageSummary totals token consumption and call reliability over the selected window.
type UsageSummary struct {
	TotalRequests    int64   `json:"totalRequests"`
	SuccessRequests  int64   `json:"successRequests"`
	FailedRequests   int64   `json:"failedRequests"`
	SuccessRate      float64 `json:"successRate"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	ReasoningTokens  int64   `json:"reasoningTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	WeightedTokens   float64 `json:"weightedTokens"`
	AvgFirstTokenMs  float64 `json:"avgFirstTokenMs"`
	AvgDurationMs    float64 `json:"avgDurationMs"`
	ActiveKeys       int     `json:"activeKeys"`
	ActiveModels     int     `json:"activeModels"`
	ActiveAccounts   int     `json:"activeAccounts"`
}

// TimeSeriesBucket aggregates usage within a single hour or calendar day.
type TimeSeriesBucket struct {
	Bucket           string  `json:"bucket"` // "2026-11-26" or "2026-11-26 14:00"
	Label            string  `json:"label"`  // Compact axis label "11-26" or "14:00"
	Requests         int64   `json:"requests"`
	SuccessRequests  int64   `json:"successRequests"`
	FailedRequests   int64   `json:"failedRequests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	ReasoningTokens  int64   `json:"reasoningTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	WeightedTokens   float64 `json:"weightedTokens"`
}

// DimensionUsage summarizes consumption grouped by API key, model, or Bohrium account.
type DimensionUsage struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Requests         int64   `json:"requests"`
	SuccessRequests  int64   `json:"successRequests"`
	FailedRequests   int64   `json:"failedRequests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	ReasoningTokens  int64   `json:"reasoningTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	WeightedTokens   float64 `json:"weightedTokens"`
	AvgDurationMs    float64 `json:"avgDurationMs"`
	SharePercent     float64 `json:"sharePercent"`
}

// KeyModelUsage summarizes token usage for a specific (API Key, Model) pair.
type KeyModelUsage struct {
	APIKeyID         string  `json:"apiKeyId"`
	APIKeyName       string  `json:"apiKeyName"`
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	ReasoningTokens  int64   `json:"reasoningTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	WeightedTokens   float64 `json:"weightedTokens"`
}

// FilterOption lists a selectable key, model, or account present in usage history.
type FilterOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GatewayAnalytics is the full dashboard payload returned by GET /api/gateway/analytics.
type GatewayAnalytics struct {
	RangeDays          int                `json:"rangeDays"`
	Granularity        string             `json:"granularity"`
	Timezone           string             `json:"timezone"`
	From               time.Time          `json:"from"`
	To                 time.Time          `json:"to"`
	Summary            UsageSummary       `json:"summary"`
	TimeSeries         []TimeSeriesBucket `json:"timeSeries"`
	ByKey              []DimensionUsage   `json:"byKey"`
	ByModel            []DimensionUsage   `json:"byModel"`
	ByAccount          []DimensionUsage   `json:"byAccount"`
	ByKeyModel         []KeyModelUsage    `json:"byKeyModel"`
	AvailableKeys      []FilterOption     `json:"availableKeys"`
	AvailableModels    []FilterOption     `json:"availableModels"`
	AvailableAccounts  []FilterOption     `json:"availableAccounts"`
	TotalStoredRecords int64              `json:"totalStoredRecords"`
	OldestRecordAt     *time.Time         `json:"oldestRecordAt,omitempty"`
}

// ensureUsageColumns idempotently migrates existing SQLite databases that created
// gateway_usage_logs prior to detailed sub2api-style columns.
func (s *Store) ensureUsageColumns() error {
	rows, err := s.db.Query(`PRAGMA table_info(gateway_usage_logs)`)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name, typ string
			notnull   int
			dflt      any
			pk        int
		)
		if rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk) == nil {
			existing[strings.ToLower(name)] = true
		}
	}
	_ = rows.Close()

	migrations := []struct {
		col string
		def string
	}{
		{"requested_model", "TEXT NOT NULL DEFAULT ''"},
		{"reasoning_effort", "TEXT NOT NULL DEFAULT ''"},
		{"bridged", "INTEGER NOT NULL DEFAULT 0"},
		{"session_hash", "TEXT NOT NULL DEFAULT ''"},
		{"request_id", "TEXT NOT NULL DEFAULT ''"},
		{"client_ip", "TEXT NOT NULL DEFAULT ''"},
		{"user_agent", "TEXT NOT NULL DEFAULT ''"},
		{"has_tools", "INTEGER NOT NULL DEFAULT 0"},
		{"has_vision", "INTEGER NOT NULL DEFAULT 0"},
		{"preflight_mode", "TEXT NOT NULL DEFAULT ''"},
		{"sci_master_prompt", "INTEGER NOT NULL DEFAULT 0"},
		{"usage_multiplier", "REAL NOT NULL DEFAULT 0"},
		{"weighted_tokens", "REAL NOT NULL DEFAULT 0"},
		{"first_token_ms", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, m := range migrations {
		if existing[m.col] {
			continue
		}
		if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE gateway_usage_logs ADD COLUMN %s %s`, m.col, m.def)); err != nil {
			return err
		}
	}
	return nil
}

const usageSelectColumns = `
	id, created_at, api_key_id, api_key_name, account_id, account_label,
	model, requested_model, reasoning_effort, endpoint, upstream_api, bridged,
	routing_mode, session_hash, request_id, client_ip, user_agent,
	stream, has_tools, has_vision, preflight_mode, sci_master_prompt,
	usage_multiplier, weighted_tokens, status, first_token_ms, duration_ms,
	prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens, total_tokens, error
`

func scanUsageRecord(scanner interface{ Scan(dest ...any) error }) (GatewayUsageRecord, error) {
	var (
		r                                                        GatewayUsageRecord
		tsSec                                                    int64
		bridged, stream, hasTools, hasVision, sciMasterPromptInt int
	)
	err := scanner.Scan(
		&r.ID, &tsSec, &r.APIKeyID, &r.APIKeyName, &r.AccountID, &r.AccountLabel,
		&r.Model, &r.RequestedModel, &r.ReasoningEffort, &r.Endpoint, &r.UpstreamAPI, &bridged,
		&r.RoutingMode, &r.SessionHash, &r.RequestID, &r.ClientIP, &r.UserAgent,
		&stream, &hasTools, &hasVision, &r.PreflightMode, &sciMasterPromptInt,
		&r.UsageMultiplier, &r.WeightedTokens, &r.Status, &r.FirstTokenMs, &r.DurationMs,
		&r.PromptTokens, &r.CompletionTokens, &r.ReasoningTokens, &r.CachedTokens, &r.TotalTokens, &r.Error,
	)
	if err != nil {
		return r, err
	}
	r.CreatedAt = time.Unix(tsSec, 0).UTC()
	r.Bridged = bridged != 0
	r.Stream = stream != 0
	r.HasTools = hasTools != 0
	r.HasVision = hasVision != 0
	r.SciMasterPrompt = sciMasterPromptInt != 0
	if r.TotalTokens == 0 && (r.PromptTokens > 0 || r.CompletionTokens > 0) {
		r.TotalTokens = r.PromptTokens + r.CompletionTokens
	}
	if r.WeightedTokens == 0 && r.TotalTokens > 0 {
		mult := r.UsageMultiplier
		if mult <= 0 {
			mult = 1
		}
		r.WeightedTokens = round1(float64(r.TotalTokens) * mult)
	}
	return r, nil
}

// RecordGatewayUsage writes a single OpenAI gateway request record to SQLite.
func (s *Store) RecordGatewayUsage(rec GatewayUsageRecord) error {
	if s == nil || s.db == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.ID == "" {
		rec.ID = fmt.Sprintf("u_%d_%s", rec.CreatedAt.UnixNano(), newUUIDv4()[:8])
	}
	if rec.TotalTokens == 0 && (rec.PromptTokens > 0 || rec.CompletionTokens > 0) {
		rec.TotalTokens = rec.PromptTokens + rec.CompletionTokens
	}
	if rec.WeightedTokens == 0 && rec.TotalTokens > 0 {
		mult := rec.UsageMultiplier
		if mult <= 0 {
			mult = 1
		}
		rec.WeightedTokens = round1(float64(rec.TotalTokens) * mult)
	}
	if strings.TrimSpace(rec.APIKeyID) == "" {
		rec.APIKeyID = "default"
	}
	if strings.TrimSpace(rec.APIKeyName) == "" {
		rec.APIKeyName = rec.APIKeyID
	}
	if len(rec.UserAgent) > 256 {
		rec.UserAgent = rec.UserAgent[:256]
	}
	if len(rec.Error) > 512 {
		rec.Error = rec.Error[:512]
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`
		INSERT OR IGNORE INTO gateway_usage_logs (
			id, created_at, api_key_id, api_key_name, account_id, account_label,
			model, requested_model, reasoning_effort, endpoint, upstream_api, bridged,
			routing_mode, session_hash, request_id, client_ip, user_agent,
			stream, has_tools, has_vision, preflight_mode, sci_master_prompt,
			usage_multiplier, weighted_tokens, status, first_token_ms, duration_ms,
			prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens, total_tokens, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.ID, rec.CreatedAt.UTC().Unix(), rec.APIKeyID, rec.APIKeyName, rec.AccountID, rec.AccountLabel,
		rec.Model, rec.RequestedModel, rec.ReasoningEffort, rec.Endpoint, rec.UpstreamAPI, boolToInt(rec.Bridged),
		rec.RoutingMode, rec.SessionHash, rec.RequestID, rec.ClientIP, rec.UserAgent,
		boolToInt(rec.Stream), boolToInt(rec.HasTools), boolToInt(rec.HasVision), rec.PreflightMode, boolToInt(rec.SciMasterPrompt),
		rec.UsageMultiplier, rec.WeightedTokens, rec.Status, rec.FirstTokenMs, rec.DurationMs,
		rec.PromptTokens, rec.CompletionTokens, rec.ReasoningTokens, rec.CachedTokens, rec.TotalTokens, rec.Error,
	)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(`
		INSERT INTO gateway_key_usage_totals (api_key_id, request_count, total_tokens, last_used_at)
		VALUES (?, 1, ?, ?)
		ON CONFLICT(api_key_id) DO UPDATE SET
			request_count = request_count + 1,
			total_tokens = total_tokens + excluded.total_tokens,
			last_used_at = MAX(last_used_at, excluded.last_used_at)
	`, rec.APIKeyID, rec.TotalTokens, rec.CreatedAt.UTC().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeExpiredGatewayUsage removes usage rows older than retentionDays (relative to now).
func (s *Store) PurgeExpiredGatewayUsage(now time.Time, retentionDays int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	if retentionDays <= 0 {
		retentionDays = 180
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.UTC().AddDate(0, 0, -retentionDays).Unix()

	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM gateway_usage_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ClearGatewayUsage deletes analytics history without resetting key quota accounting.
func (s *Store) ClearGatewayUsage() (int64, error) {
	return s.ClearGatewayUsageInTimezone(time.Now().UTC(), "Asia/Shanghai")
}

// ClearGatewayUsageInTimezone durably captures the current-day counters before
// deleting all request logs. Repeating the operation is idempotent per log row.
func (s *Store) ClearGatewayUsageInTimezone(now time.Time, timezone string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	dayStart := gatewayDayStart(now, timezone)
	if _, err := tx.Exec(`
		INSERT INTO gateway_key_daily_baselines (api_key_id, day_start, request_count, total_tokens)
		SELECT CASE WHEN api_key_id = '' THEN 'default' ELSE api_key_id END,
		       ?, COUNT(*),
		       COALESCE(SUM(CASE WHEN total_tokens > 0 THEN total_tokens ELSE prompt_tokens + completion_tokens END), 0)
		FROM gateway_usage_logs WHERE created_at >= ?
		GROUP BY CASE WHEN api_key_id = '' THEN 'default' ELSE api_key_id END
		ON CONFLICT(api_key_id, day_start) DO UPDATE SET
			request_count = request_count + excluded.request_count,
			total_tokens = total_tokens + excluded.total_tokens
	`, dayStart, dayStart); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM gateway_usage_logs`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// RecentGatewayUsage returns the latest N usage records from SQLite (newest first).
func (s *Store) RecentGatewayUsage(limit int) []GatewayUsageRecord {
	if s == nil || s.db == nil {
		return nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+usageSelectColumns+` FROM gateway_usage_logs ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]GatewayUsageRecord, 0, limit)
	for rows.Next() {
		r, err := scanUsageRecord(rows)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ListGatewayUsageLogs returns a filtered, paginated slice of detailed usage logs (newest first).
func (s *Store) ListGatewayUsageLogs(opts UsageLogsQueryOptions) (*UsageLogsPage, error) {
	nowUTC := opts.Now
	if nowUTC.IsZero() {
		nowUTC = time.Now().UTC()
	}
	tzName := strings.TrimSpace(opts.Timezone)
	if tzName == "" {
		tzName = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	nowLocal := nowUTC.In(loc)

	page := opts.Page
	if page < 1 {
		page = 1
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var (
		clauses []string
		args    []any
	)
	if opts.RangeDays == 1 {
		fromSec := nowLocal.Truncate(time.Hour).Add(-23 * time.Hour).UTC().Unix()
		clauses = append(clauses, "created_at >= ?")
		args = append(args, fromSec)
	} else if opts.RangeDays > 1 {
		y, m, d := nowLocal.Date()
		fromSec := time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -(opts.RangeDays - 1)).UTC().Unix()
		clauses = append(clauses, "created_at >= ?")
		args = append(args, fromSec)
	}
	if k := strings.TrimSpace(opts.APIKeyID); k != "" {
		clauses = append(clauses, "api_key_id = ?")
		args = append(args, k)
	}
	if m := strings.TrimSpace(opts.Model); m != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, m)
	}
	if a := strings.TrimSpace(opts.AccountID); a != "" {
		clauses = append(clauses, "account_id = ?")
		args = append(args, a)
	}
	switch strings.ToLower(strings.TrimSpace(opts.Status)) {
	case "success":
		clauses = append(clauses, "status >= 200 AND status < 300 AND error = ''")
	case "error":
		clauses = append(clauses, "(status < 200 OR status >= 300 OR error != '')")
	}
	switch strings.ToLower(strings.TrimSpace(opts.Stream)) {
	case "true", "1", "sse":
		clauses = append(clauses, "stream = 1")
	case "false", "0", "json":
		clauses = append(clauses, "stream = 0")
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		like := "%" + q + "%"
		clauses = append(clauses, `(
			client_ip LIKE ? OR user_agent LIKE ? OR model LIKE ? OR requested_model LIKE ? OR
			api_key_name LIKE ? OR account_label LIKE ? OR request_id LIKE ? OR session_hash LIKE ? OR error LIKE ?
		)`)
		for i := 0; i < 9; i++ {
			args = append(args, like)
		}
	}

	whereSQL := ""
	if len(clauses) > 0 {
		whereSQL = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM gateway_usage_logs`+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	offset := (page - 1) * pageSize

	queryArgs := append(append([]any{}, args...), pageSize, offset)
	rows, err := s.db.Query(
		`SELECT `+usageSelectColumns+` FROM gateway_usage_logs`+whereSQL+` ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]GatewayUsageRecord, 0, pageSize)
	for rows.Next() {
		r, err := scanUsageRecord(rows)
		if err != nil {
			continue
		}
		items = append(items, r)
	}
	return &UsageLogsPage{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, rows.Err()
}

// QueryGatewayAnalytics computes time-series, per-key, per-model, per-account, and key×model
// token consumption analytics over the requested window and filters.
func (s *Store) QueryGatewayAnalytics(opts UsageQueryOptions) (*GatewayAnalytics, error) {
	nowUTC := opts.Now
	if nowUTC.IsZero() {
		nowUTC = time.Now().UTC()
	}
	tzName := strings.TrimSpace(opts.Timezone)
	if tzName == "" {
		tzName = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
		tzName = "Asia/Shanghai"
	}
	nowLocal := nowUTC.In(loc)

	var (
		totalStored int64
		oldestSec   int64
	)
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(created_at), 0) FROM gateway_usage_logs`).Scan(&totalStored, &oldestSec)

	granularity := strings.ToLower(strings.TrimSpace(opts.Granularity))
	rangeDays := opts.RangeDays
	var fromLocal time.Time
	if rangeDays == 1 {
		if granularity != "day" {
			granularity = "hour"
		}
		fromLocal = nowLocal.Truncate(time.Hour).Add(-23 * time.Hour)
	} else if rangeDays > 1 {
		if granularity != "hour" {
			granularity = "day"
		}
		if granularity == "hour" && rangeDays > 14 {
			granularity = "day"
		}
		if granularity == "hour" {
			fromLocal = nowLocal.Truncate(time.Hour).Add(-time.Duration(rangeDays*24-1) * time.Hour)
		} else {
			y, m, d := nowLocal.Date()
			fromLocal = time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -(rangeDays - 1))
		}
	} else {
		rangeDays = 0
		if granularity != "hour" {
			granularity = "day"
		}
		if oldestSec > 0 {
			oldestLocal := time.Unix(oldestSec, 0).In(loc)
			y, m, d := oldestLocal.Date()
			fromLocal = time.Date(y, m, d, 0, 0, 0, 0, loc)
			if granularity == "hour" && nowLocal.Sub(fromLocal) > 14*24*time.Hour {
				granularity = "day"
			}
		} else {
			y, m, d := nowLocal.Date()
			fromLocal = time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -6)
		}
	}

	out := &GatewayAnalytics{
		RangeDays:          rangeDays,
		Granularity:        granularity,
		Timezone:           tzName,
		From:               fromLocal.UTC(),
		To:                 nowUTC,
		TimeSeries:         make([]TimeSeriesBucket, 0),
		ByKey:              make([]DimensionUsage, 0),
		ByModel:            make([]DimensionUsage, 0),
		ByAccount:          make([]DimensionUsage, 0),
		ByKeyModel:         make([]KeyModelUsage, 0),
		AvailableKeys:      s.distinctUsageOptions(`api_key_id`, `api_key_name`),
		AvailableModels:    s.distinctUsageOptions(`model`, `model`),
		AvailableAccounts:  s.distinctUsageOptions(`account_id`, `account_label`),
		TotalStoredRecords: totalStored,
	}
	if oldestSec > 0 {
		t := time.Unix(oldestSec, 0).UTC()
		out.OldestRecordAt = &t
	}

	bucketIdx := make(map[string]int)
	if granularity == "hour" {
		cur := fromLocal.Truncate(time.Hour)
		end := nowLocal.Truncate(time.Hour)
		for !cur.After(end) {
			key := cur.Format("2006-01-02 15:00")
			label := cur.Format("01-02 15:00")
			if rangeDays == 1 {
				label = cur.Format("15:00")
			}
			bucketIdx[key] = len(out.TimeSeries)
			out.TimeSeries = append(out.TimeSeries, TimeSeriesBucket{
				Bucket: key,
				Label:  label,
			})
			cur = cur.Add(time.Hour)
		}
	} else {
		y, m, d := fromLocal.Date()
		cur := time.Date(y, m, d, 0, 0, 0, 0, loc)
		ey, em, ed := nowLocal.Date()
		end := time.Date(ey, em, ed, 0, 0, 0, 0, loc)
		for !cur.After(end) {
			key := cur.Format("2006-01-02")
			label := cur.Format("01-02")
			bucketIdx[key] = len(out.TimeSeries)
			out.TimeSeries = append(out.TimeSeries, TimeSeriesBucket{
				Bucket: key,
				Label:  label,
			})
			cur = cur.AddDate(0, 0, 1)
		}
	}

	var (
		clauses []string
		args    []any
	)
	if rangeDays > 0 {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, fromLocal.UTC().Unix())
	}
	if k := strings.TrimSpace(opts.APIKeyID); k != "" {
		clauses = append(clauses, "api_key_id = ?")
		args = append(args, k)
	}
	if m := strings.TrimSpace(opts.Model); m != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, m)
	}
	if a := strings.TrimSpace(opts.AccountID); a != "" {
		clauses = append(clauses, "account_id = ?")
		args = append(args, a)
	}

	query := `
		SELECT created_at, api_key_id, api_key_name, account_id, account_label,
		       model, status, first_token_ms, duration_ms, prompt_tokens, completion_tokens,
		       reasoning_tokens, cached_tokens, total_tokens, usage_multiplier, weighted_tokens, error
		FROM gateway_usage_logs
	`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at ASC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dimAcc struct {
		DimensionUsage
		durationSum int64
	}
	keyMap := make(map[string]*dimAcc)
	modelMap := make(map[string]*dimAcc)
	accMap := make(map[string]*dimAcc)
	kmMap := make(map[string]*KeyModelUsage)

	var (
		totalDuration int64
		totalTTFT     int64
		ttftCount     int64
	)

	for rows.Next() {
		var (
			tsSec                                             int64
			keyID, keyName, accID, accLabel, model            string
			status                                            int
			ttftMs, durMs, pTok, cTok, rTok, cacheTok, totTok int64
			usageMult, wTok                                   float64
			errStr                                            string
		)
		if err := rows.Scan(
			&tsSec, &keyID, &keyName, &accID, &accLabel,
			&model, &status, &ttftMs, &durMs, &pTok, &cTok,
			&rTok, &cacheTok, &totTok, &usageMult, &wTok, &errStr,
		); err != nil {
			continue
		}
		if totTok == 0 && (pTok > 0 || cTok > 0) {
			totTok = pTok + cTok
		}
		if wTok == 0 && totTok > 0 {
			m := usageMult
			if m <= 0 {
				m = 1
			}
			wTok = float64(totTok) * m
		}
		okCall := status >= 200 && status < 300 && errStr == ""

		out.Summary.TotalRequests++
		if okCall {
			out.Summary.SuccessRequests++
		} else {
			out.Summary.FailedRequests++
		}
		out.Summary.PromptTokens += pTok
		out.Summary.CompletionTokens += cTok
		out.Summary.ReasoningTokens += rTok
		out.Summary.CachedTokens += cacheTok
		out.Summary.TotalTokens += totTok
		out.Summary.WeightedTokens += wTok
		totalDuration += durMs
		if ttftMs > 0 {
			totalTTFT += ttftMs
			ttftCount++
		}

		tLocal := time.Unix(tsSec, 0).In(loc)
		bKey := tLocal.Format("2006-01-02")
		if granularity == "hour" {
			bKey = tLocal.Format("2006-01-02 15:00")
		}
		if idx, exists := bucketIdx[bKey]; exists {
			b := &out.TimeSeries[idx]
			b.Requests++
			if okCall {
				b.SuccessRequests++
			} else {
				b.FailedRequests++
			}
			b.PromptTokens += pTok
			b.CompletionTokens += cTok
			b.ReasoningTokens += rTok
			b.CachedTokens += cacheTok
			b.TotalTokens += totTok
			b.WeightedTokens = round1(b.WeightedTokens + wTok)
		}

		if keyID == "" {
			keyID = "default"
		}
		if keyName == "" {
			keyName = keyID
		}
		kd, exists := keyMap[keyID]
		if !exists {
			kd = &dimAcc{DimensionUsage: DimensionUsage{ID: keyID, Name: keyName}}
			keyMap[keyID] = kd
		} else if keyName != "" && keyName != keyID {
			kd.Name = keyName
		}
		kd.Requests++
		if okCall {
			kd.SuccessRequests++
		} else {
			kd.FailedRequests++
		}
		kd.PromptTokens += pTok
		kd.CompletionTokens += cTok
		kd.ReasoningTokens += rTok
		kd.CachedTokens += cacheTok
		kd.TotalTokens += totTok
		kd.WeightedTokens = round1(kd.WeightedTokens + wTok)
		kd.durationSum += durMs

		mID := strings.TrimSpace(model)
		if mID == "" {
			mID = "(unknown)"
		}
		md, exists := modelMap[mID]
		if !exists {
			md = &dimAcc{DimensionUsage: DimensionUsage{ID: mID, Name: mID}}
			modelMap[mID] = md
		}
		md.Requests++
		if okCall {
			md.SuccessRequests++
		} else {
			md.FailedRequests++
		}
		md.PromptTokens += pTok
		md.CompletionTokens += cTok
		md.ReasoningTokens += rTok
		md.CachedTokens += cacheTok
		md.TotalTokens += totTok
		md.WeightedTokens = round1(md.WeightedTokens + wTok)
		md.durationSum += durMs

		aID := strings.TrimSpace(accID)
		aName := strings.TrimSpace(accLabel)
		if aID == "" {
			aID = "_unrouted"
			aName = "未路由/失败"
		} else if aName == "" {
			aName = aID
		}
		ad, exists := accMap[aID]
		if !exists {
			ad = &dimAcc{DimensionUsage: DimensionUsage{ID: aID, Name: aName}}
			accMap[aID] = ad
		} else if aName != "" && aName != aID {
			ad.Name = aName
		}
		ad.Requests++
		if okCall {
			ad.SuccessRequests++
		} else {
			ad.FailedRequests++
		}
		ad.PromptTokens += pTok
		ad.CompletionTokens += cTok
		ad.ReasoningTokens += rTok
		ad.CachedTokens += cacheTok
		ad.TotalTokens += totTok
		ad.WeightedTokens = round1(ad.WeightedTokens + wTok)
		ad.durationSum += durMs

		kmKey := keyID + "\x00" + mID
		km, exists := kmMap[kmKey]
		if !exists {
			km = &KeyModelUsage{APIKeyID: keyID, APIKeyName: keyName, Model: mID}
			kmMap[kmKey] = km
		}
		km.Requests++
		km.PromptTokens += pTok
		km.CompletionTokens += cTok
		km.ReasoningTokens += rTok
		km.CachedTokens += cacheTok
		km.TotalTokens += totTok
		km.WeightedTokens = round1(km.WeightedTokens + wTok)
	}

	out.Summary.WeightedTokens = round1(out.Summary.WeightedTokens)
	if out.Summary.TotalRequests > 0 {
		out.Summary.SuccessRate = round1(float64(out.Summary.SuccessRequests) * 100 / float64(out.Summary.TotalRequests))
		out.Summary.AvgDurationMs = round1(float64(totalDuration) / float64(out.Summary.TotalRequests))
	}
	if ttftCount > 0 {
		out.Summary.AvgFirstTokenMs = round1(float64(totalTTFT) / float64(ttftCount))
	}
	out.Summary.ActiveKeys = len(keyMap)
	out.Summary.ActiveModels = len(modelMap)
	out.Summary.ActiveAccounts = len(accMap)

	finalizeDims := func(m map[string]*dimAcc) []DimensionUsage {
		list := make([]DimensionUsage, 0, len(m))
		for _, item := range m {
			d := item.DimensionUsage
			if d.Requests > 0 {
				d.AvgDurationMs = round1(float64(item.durationSum) / float64(d.Requests))
			}
			if out.Summary.TotalTokens > 0 {
				d.SharePercent = round1(float64(d.TotalTokens) * 100 / float64(out.Summary.TotalTokens))
			} else if out.Summary.TotalRequests > 0 {
				d.SharePercent = round1(float64(d.Requests) * 100 / float64(out.Summary.TotalRequests))
			}
			list = append(list, d)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].TotalTokens != list[j].TotalTokens {
				return list[i].TotalTokens > list[j].TotalTokens
			}
			if list[i].Requests != list[j].Requests {
				return list[i].Requests > list[j].Requests
			}
			return list[i].ID < list[j].ID
		})
		return list
	}

	out.ByKey = finalizeDims(keyMap)
	out.ByModel = finalizeDims(modelMap)
	out.ByAccount = finalizeDims(accMap)

	for _, km := range kmMap {
		out.ByKeyModel = append(out.ByKeyModel, *km)
	}
	sort.Slice(out.ByKeyModel, func(i, j int) bool {
		if out.ByKeyModel[i].TotalTokens != out.ByKeyModel[j].TotalTokens {
			return out.ByKeyModel[i].TotalTokens > out.ByKeyModel[j].TotalTokens
		}
		return out.ByKeyModel[i].Requests > out.ByKeyModel[j].Requests
	})
	if len(out.ByKeyModel) > 50 {
		out.ByKeyModel = out.ByKeyModel[:50]
	}

	return out, rows.Err()
}

func (s *Store) distinctUsageOptions(idCol, nameCol string) []FilterOption {
	q := fmt.Sprintf(`
		SELECT %s, MAX(%s)
		FROM gateway_usage_logs
		WHERE %s != ''
		GROUP BY %s
		ORDER BY MAX(created_at) DESC
		LIMIT 100
	`, idCol, nameCol, idCol, idCol)
	rows, err := s.db.Query(q)
	if err != nil {
		return []FilterOption{}
	}
	defer rows.Close()
	out := make([]FilterOption, 0)
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil && id != "" {
			if name == "" {
				name = id
			}
			out = append(out, FilterOption{ID: id, Name: name})
		}
	}
	return out
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
