package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// SessionBindingTTL is the strict hard upper bound (7 days) for OpenAI session-to-account bindings.
	SessionBindingTTL = 7 * 24 * time.Hour

	maxLogs            = 1000
	maxStoredResponses = 500
)

// AuthState persists a single Bohrium account's login session and JWT metadata.
type AuthState struct {
	Token         string    `json:"token"`
	UserID        int64     `json:"userId"`
	OrgID         int64     `json:"orgId"`
	UserType      int       `json:"userType"`
	Nickname      string    `json:"nickname"`
	Phone         string    `json:"phone,omitempty"`
	Email         string    `json:"email,omitempty"`
	Avatar        string    `json:"avatar,omitempty"`
	ClubBound     bool      `json:"clubBound"`
	ExpiresAt     time.Time `json:"expiresAt"`
	LastLoginAt   time.Time `json:"lastLoginAt"`
	LastRefreshAt time.Time `json:"lastRefreshAt"`
	LoginMethod   string    `json:"loginMethod"` // "wechat_qr" | "oauth_google" | ... | "manual_token" | "password"
}

// Account represents a managed Bohrium account stored in SQLite.
// Newly logged-in accounts default to PoolEnabled=false and must be explicitly added to the OpenAI routing pool.
type Account struct {
	ID               string    `json:"id"`
	UserID           int64     `json:"userId"`
	OrgID            int64     `json:"orgId"`
	UserType         int       `json:"userType"`
	Label            string    `json:"label"`
	Nickname         string    `json:"nickname"`
	Phone            string    `json:"phone,omitempty"`
	Email            string    `json:"email,omitempty"`
	Avatar           string    `json:"avatar,omitempty"`
	Token            string    `json:"token,omitempty"`
	LoginMethod      string    `json:"loginMethod"`
	ClubBound        bool      `json:"clubBound"`
	ExpiresAt        time.Time `json:"expiresAt"`
	LastLoginAt      time.Time `json:"lastLoginAt"`
	LastRefreshAt    time.Time `json:"lastRefreshAt"`
	StoreID          string    `json:"storeId"`
	PoolEnabled      bool      `json:"poolEnabled"`
	PoolOrder        int       `json:"poolOrder"`
	CheckinEnabled   bool      `json:"checkinEnabled"`
	PhotonBalance    float64   `json:"photonBalance"`
	OrgBalance       float64   `json:"orgBalance"`
	WalletBalanceCNY float64   `json:"walletBalanceCny"`
	BalanceUpdatedAt time.Time `json:"balanceUpdatedAt,omitempty"`
	HealthStatus     string    `json:"healthStatus"` // "active" | "expired" | "insufficient" | "cooldown"
	CooldownUntil    time.Time `json:"cooldownUntil,omitempty"`
	LastError        string    `json:"lastError,omitempty"`
	LastScheduledDay string    `json:"lastScheduledDay,omitempty"`
	LastRunAt        time.Time `json:"lastRunAt,omitempty"`
	LastRunSummary   string    `json:"lastRunSummary,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// AuthState projects Account credentials into AuthState.
func (a Account) AuthState() AuthState {
	return AuthState{
		Token:         a.Token,
		UserID:        a.UserID,
		OrgID:         a.OrgID,
		UserType:      a.UserType,
		Nickname:      a.Nickname,
		Phone:         a.Phone,
		Email:         a.Email,
		Avatar:        a.Avatar,
		ClubBound:     a.ClubBound,
		ExpiresAt:     a.ExpiresAt,
		LastLoginAt:   a.LastLoginAt,
		LastRefreshAt: a.LastRefreshAt,
		LoginMethod:   a.LoginMethod,
	}
}

// EffectiveBalance returns total spendable photons (personal photons + usable org balance) used for weighted routing.
func (a Account) EffectiveBalance() float64 {
	total := 0.0
	if a.PhotonBalance > 0 {
		total += a.PhotonBalance
	}
	if a.OrgBalance > 0 {
		total += a.OrgBalance
	}
	return total
}

// DisplayName returns the user-friendly name for UI & logs.
func (a Account) DisplayName() string {
	if s := strings.TrimSpace(a.Label); s != "" {
		return s
	}
	if s := strings.TrimSpace(a.Nickname); s != "" {
		return s
	}
	if a.UserID > 0 {
		return fmt.Sprintf("UID %d", a.UserID)
	}
	return a.ID
}

// IsHealthyForPool reports whether this account is currently eligible to receive OpenAI gateway traffic.
func (a Account) IsHealthyForPool(now time.Time) bool {
	if !a.PoolEnabled || strings.TrimSpace(a.Token) == "" {
		return false
	}
	if a.HealthStatus == "expired" {
		return false
	}
	if !a.ExpiresAt.IsZero() && !a.ExpiresAt.After(now) {
		return false
	}
	if !a.CooldownUntil.IsZero() && a.CooldownUntil.After(now) {
		return false
	}
	return true
}

// Redacted returns a copy of Account with its raw JWT token masked for safe API exposure.
func (a Account) Redacted() Account {
	cp := a
	if len(cp.Token) > 16 {
		cp.Token = cp.Token[:8] + "..." + cp.Token[len(cp.Token)-6:]
	} else if cp.Token != "" {
		cp.Token = "***"
	}
	return cp
}

// RunLog records a single automation action or task execution (optionally tagged to a specific account).
type RunLog struct {
	ID         string         `json:"id"`
	AccountID  string         `json:"accountId,omitempty"`
	AccountUID int64          `json:"accountUid,omitempty"`
	Nickname   string         `json:"nickname,omitempty"`
	Timestamp  time.Time      `json:"timestamp"`
	Trigger    string         `json:"trigger"` // "manual" | "schedule" | "system"
	Action     string         `json:"action"`  // "claim_all" | "claim_one" | "task_run" | "run_all" | "refresh_token" | "login" | "recon"
	TaskKey    string         `json:"taskKey,omitempty"`
	TaskTitle  string         `json:"taskTitle,omitempty"`
	Success    bool           `json:"success"`
	Skipped    bool           `json:"skipped,omitempty"`
	Message    string         `json:"message"`
	DurationMs int64          `json:"durationMs"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// SessionBinding stores a 7-day-TTL OpenAI session-hash to account routing binding in SQLite.
type SessionBinding struct {
	SessionHash      string    `json:"sessionHash"`
	AccountID        string    `json:"accountId"`
	SciXSessionID    string    `json:"scixSessionId"`
	SciXRunID        string    `json:"scixRunId,omitempty"`
	SessionCreatedAt string    `json:"sessionCreatedAt"`
	ModelHint        string    `json:"modelHint,omitempty"`
	HitCount         int64     `json:"hitCount"`
	CreatedAt        time.Time `json:"createdAt"`
	LastUsedAt       time.Time `json:"lastUsedAt"`
	ExpiresAt        time.Time `json:"expiresAt"`

	// Enriched metadata populated when listing bindings for the Web Console.
	AccountLabel    string `json:"accountLabel,omitempty"`
	AccountNickname string `json:"accountNickname,omitempty"`
	AccountUID      int64  `json:"accountUid,omitempty"`
}

// ResponseRecord stores an OpenAI Responses API turn in SQLite, linked to its owning account and session hash.
type ResponseRecord struct {
	ID                 string           `json:"id"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
	AccountID          string           `json:"account_id,omitempty"`
	SessionHash        string           `json:"session_hash,omitempty"`
	Model              string           `json:"model"`
	Instructions       *string          `json:"instructions,omitempty"`
	InputItems         []map[string]any `json:"input_items"`
	OutputItems        []map[string]any `json:"output_items"`
	Response           map[string]any   `json:"response"`
	CreatedAt          time.Time        `json:"created_at"`
}

// Store manages thread-safe SQLite persistence for multi-account state, OpenAI session bindings,
// Responses API chains, and execution logs.
type Store struct {
	mu      sync.Mutex
	db      *sql.DB
	dbPath  string
	dataDir string
}

// Open initializes (or opens) <dataDir>/bohrium.db in WAL mode, creates schema idempotently,
// migrates any legacy single-account state.json / responses.json once, and purges expired bindings.
func Open(dataDir string) (*Store, error) {
	var dsn, dbPath string
	if strings.TrimSpace(dataDir) == "" {
		dsn = "file::memory:?cache=shared&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	} else {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return nil, fmt.Errorf("create store dir: %w", err)
		}
		dbPath = filepath.Join(dataDir, "bohrium.db")
		// Forward slashes keep SQLite URI clean on Windows.
		uriPath := filepath.ToSlash(dbPath)
		dsn = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", uriPath)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	// Single connection serializes writers cleanly in pure-Go SQLite without SQLITE_BUSY contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", dbPath, err)
	}
	if dbPath != "" {
		_ = os.Chmod(dbPath, 0o600)
	}

	s := &Store{
		db:      db,
		dbPath:  dbPath,
		dataDir: dataDir,
	}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if dataDir != "" {
		_ = s.migrateLegacyFiles(dataDir)
	}
	_, _ = s.PurgeExpiredSessionBindings(time.Now().UTC())
	return s, nil
}

// Close cleanly closes the underlying SQLite database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DBPath returns the SQLite database file path (empty for in-memory stores).
func (s *Store) DBPath() string {
	return s.dbPath
}

func (s *Store) initSchema() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS accounts (
    id                 TEXT PRIMARY KEY,
    user_id            INTEGER NOT NULL UNIQUE,
    org_id             INTEGER NOT NULL DEFAULT 0,
    user_type          INTEGER NOT NULL DEFAULT 0,
    label              TEXT NOT NULL DEFAULT '',
    nickname           TEXT NOT NULL DEFAULT '',
    phone              TEXT NOT NULL DEFAULT '',
    email              TEXT NOT NULL DEFAULT '',
    avatar             TEXT NOT NULL DEFAULT '',
    token              TEXT NOT NULL,
    login_method       TEXT NOT NULL DEFAULT '',
    club_bound         INTEGER NOT NULL DEFAULT 0,
    expires_at         INTEGER NOT NULL DEFAULT 0,
    last_login_at      INTEGER NOT NULL DEFAULT 0,
    last_refresh_at    INTEGER NOT NULL DEFAULT 0,
    store_id           TEXT NOT NULL,
    pool_enabled       INTEGER NOT NULL DEFAULT 0,
    pool_order         INTEGER NOT NULL DEFAULT 0,
    checkin_enabled    INTEGER NOT NULL DEFAULT 1,
    photon_balance     REAL NOT NULL DEFAULT 0,
    org_balance        REAL NOT NULL DEFAULT 0,
    wallet_balance_cny REAL NOT NULL DEFAULT 0,
    balance_updated_at INTEGER NOT NULL DEFAULT 0,
    health_status      TEXT NOT NULL DEFAULT 'active',
    cooldown_until     INTEGER NOT NULL DEFAULT 0,
    last_error         TEXT NOT NULL DEFAULT '',
    last_scheduled_day TEXT NOT NULL DEFAULT '',
    last_run_at        INTEGER NOT NULL DEFAULT 0,
    last_run_summary   TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS session_bindings (
    session_hash       TEXT PRIMARY KEY,
    account_id         TEXT NOT NULL,
    scix_session_id    TEXT NOT NULL,
    scix_run_id        TEXT NOT NULL DEFAULT '',
    session_created_at TEXT NOT NULL,
    model_hint         TEXT NOT NULL DEFAULT '',
    hit_count          INTEGER NOT NULL DEFAULT 1,
    created_at         INTEGER NOT NULL,
    last_used_at       INTEGER NOT NULL,
    expires_at         INTEGER NOT NULL,
    FOREIGN KEY(account_id) REFERENCES accounts(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_session_bindings_expires ON session_bindings(expires_at);
CREATE INDEX IF NOT EXISTS idx_session_bindings_account ON session_bindings(account_id);

CREATE TABLE IF NOT EXISTS responses (
    id                   TEXT PRIMARY KEY,
    previous_response_id TEXT NOT NULL DEFAULT '',
    account_id           TEXT NOT NULL DEFAULT '',
    session_hash         TEXT NOT NULL DEFAULT '',
    model                TEXT NOT NULL,
    instructions         TEXT,
    input_items_json     TEXT NOT NULL,
    output_items_json    TEXT NOT NULL,
    response_json        TEXT NOT NULL,
    created_at           INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_responses_created ON responses(created_at);

CREATE TABLE IF NOT EXISTS run_logs (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL DEFAULT '',
    account_uid INTEGER NOT NULL DEFAULT 0,
    nickname    TEXT NOT NULL DEFAULT '',
    timestamp   INTEGER NOT NULL,
    trigger     TEXT NOT NULL,
    action      TEXT NOT NULL,
    task_key    TEXT NOT NULL DEFAULT '',
    task_title  TEXT NOT NULL DEFAULT '',
    success     INTEGER NOT NULL DEFAULT 0,
    skipped     INTEGER NOT NULL DEFAULT 0,
    message     TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    meta_json   TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_run_logs_ts ON run_logs(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_run_logs_acc ON run_logs(account_id, timestamp DESC);

CREATE TABLE IF NOT EXISTS gateway_usage_logs (
    id                TEXT PRIMARY KEY,
    created_at        INTEGER NOT NULL,
    api_key_id        TEXT NOT NULL DEFAULT '',
    api_key_name      TEXT NOT NULL DEFAULT '',
    account_id        TEXT NOT NULL DEFAULT '',
    account_label     TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    requested_model   TEXT NOT NULL DEFAULT '',
    reasoning_effort  TEXT NOT NULL DEFAULT '',
    endpoint          TEXT NOT NULL DEFAULT '',
    upstream_api      TEXT NOT NULL DEFAULT '',
    bridged           INTEGER NOT NULL DEFAULT 0,
    routing_mode      TEXT NOT NULL DEFAULT '',
    session_hash      TEXT NOT NULL DEFAULT '',
    request_id        TEXT NOT NULL DEFAULT '',
    client_ip         TEXT NOT NULL DEFAULT '',
    user_agent        TEXT NOT NULL DEFAULT '',
    stream            INTEGER NOT NULL DEFAULT 0,
    has_tools         INTEGER NOT NULL DEFAULT 0,
    has_vision        INTEGER NOT NULL DEFAULT 0,
    preflight_mode    TEXT NOT NULL DEFAULT '',
    sci_master_prompt INTEGER NOT NULL DEFAULT 0,
    usage_multiplier  REAL NOT NULL DEFAULT 0,
    weighted_tokens   REAL NOT NULL DEFAULT 0,
    status            INTEGER NOT NULL DEFAULT 0,
    first_token_ms    INTEGER NOT NULL DEFAULT 0,
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
    cached_tokens     INTEGER NOT NULL DEFAULT 0,
    total_tokens      INTEGER NOT NULL DEFAULT 0,
    error             TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_usage_created ON gateway_usage_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_key_created ON gateway_usage_logs(api_key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_model_created ON gateway_usage_logs(model, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_account_created ON gateway_usage_logs(account_id, created_at DESC);

CREATE TABLE IF NOT EXISTS gateway_key_usage_totals (
    api_key_id    TEXT PRIMARY KEY,
    request_count INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    last_used_at  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS gateway_key_daily_baselines (
    api_key_id    TEXT NOT NULL,
    day_start     INTEGER NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (api_key_id, day_start)
);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}
	if err := s.ensureUsageColumns(); err != nil {
		return fmt.Errorf("migrate gateway_usage_logs columns: %w", err)
	}
	if err := s.backfillGatewayKeyUsageTotals(); err != nil {
		return fmt.Errorf("backfill gateway key usage totals: %w", err)
	}
	return nil
}

// migrateLegacyFiles performs a one-time lossless import of legacy data/state.json and data/responses.json.
// Migrated legacy accounts always start with PoolEnabled=false (requiring explicit opt-in into the OpenAI pool).
func (s *Store) migrateLegacyFiles(dataDir string) error {
	var done string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'migrated_legacy_v1'`).Scan(&done)
	if err == nil && done == "1" {
		return nil
	}

	var migratedAccID string
	legacyStatePath := filepath.Join(dataDir, "state.json")
	if raw, rErr := os.ReadFile(legacyStatePath); rErr == nil && len(raw) > 0 {
		var legacy struct {
			Auth             AuthState `json:"auth"`
			LastScheduledDay string    `json:"lastScheduledDay"`
			LastRunAt        time.Time `json:"lastRunAt"`
			LastRunSummary   string    `json:"lastRunSummary"`
			Logs             []RunLog  `json:"logs"`
		}
		if json.Unmarshal(raw, &legacy) == nil {
			if strings.TrimSpace(legacy.Auth.Token) != "" {
				// Preserve legacy gateway.storeId if available in data/config.json.
				legacyStoreID := readLegacyStoreID(filepath.Join(dataDir, "config.json"))
				acc, _, uErr := s.upsertAccountInternal(legacy.Auth, "", legacyStoreID)
				if uErr == nil && acc != nil {
					migratedAccID = acc.ID
					if legacy.LastScheduledDay != "" || !legacy.LastRunAt.IsZero() {
						_, _ = s.db.Exec(
							`UPDATE accounts SET last_scheduled_day = ?, last_run_at = ?, last_run_summary = ? WHERE id = ?`,
							legacy.LastScheduledDay, toUnix(legacy.LastRunAt), legacy.LastRunSummary, acc.ID,
						)
					}
				}
			}
			// Import legacy run logs (oldest -> newest so timestamps stay intact).
			for i := len(legacy.Logs) - 1; i >= 0; i-- {
				entry := legacy.Logs[i]
				if entry.AccountID == "" && migratedAccID != "" {
					entry.AccountID = migratedAccID
					entry.AccountUID = legacy.Auth.UserID
					entry.Nickname = legacy.Auth.Nickname
				}
				_ = s.AppendLog(entry)
			}
		}
	}

	legacyRespPath := filepath.Join(dataDir, "responses.json")
	if raw, rErr := os.ReadFile(legacyRespPath); rErr == nil && len(raw) > 0 {
		var disk struct {
			Order   []string                   `json:"order"`
			Records map[string]*ResponseRecord `json:"records"`
		}
		if json.Unmarshal(raw, &disk) == nil && len(disk.Records) > 0 {
			for _, id := range disk.Order {
				rec := disk.Records[id]
				if rec == nil || rec.ID == "" {
					continue
				}
				if rec.AccountID == "" && migratedAccID != "" {
					rec.AccountID = migratedAccID
				}
				_ = s.SaveResponse(rec)
			}
		}
	}

	_, _ = s.db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('migrated_legacy_v1', '1')`)
	return nil
}

func readLegacyStoreID(cfgPath string) string {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	var partial struct {
		Gateway struct {
			StoreID string `json:"storeId"`
		} `json:"gateway"`
	}
	if json.Unmarshal(raw, &partial) == nil {
		return strings.TrimSpace(partial.Gateway.StoreID)
	}
	return ""
}

func newUUIDv4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func toUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().Unix()
}

func fromUnix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isErrNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
