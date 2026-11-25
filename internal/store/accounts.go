package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const accountColumns = `
	id, user_id, org_id, user_type, label, nickname, phone, email, avatar,
	token, login_method, club_bound, expires_at, last_login_at, last_refresh_at,
	store_id, pool_enabled, pool_order, checkin_enabled,
	photon_balance, org_balance, wallet_balance_cny, balance_updated_at,
	health_status, cooldown_until, last_error,
	last_scheduled_day, last_run_at, last_run_summary,
	created_at, updated_at
`

func scanAccount(scanner interface{ Scan(dest ...any) error }) (*Account, error) {
	var (
		a                                                                        Account
		clubBound, poolEnabled, checkinEnabled                                   int
		expSec, loginSec, refreshSec, balSec, coolSec, runSec, createdSec, updSec int64
	)
	err := scanner.Scan(
		&a.ID, &a.UserID, &a.OrgID, &a.UserType, &a.Label, &a.Nickname, &a.Phone, &a.Email, &a.Avatar,
		&a.Token, &a.LoginMethod, &clubBound, &expSec, &loginSec, &refreshSec,
		&a.StoreID, &poolEnabled, &a.PoolOrder, &checkinEnabled,
		&a.PhotonBalance, &a.OrgBalance, &a.WalletBalanceCNY, &balSec,
		&a.HealthStatus, &coolSec, &a.LastError,
		&a.LastScheduledDay, &runSec, &a.LastRunSummary,
		&createdSec, &updSec,
	)
	if err != nil {
		return nil, err
	}
	a.ClubBound = clubBound != 0
	a.PoolEnabled = poolEnabled != 0
	a.CheckinEnabled = checkinEnabled != 0
	a.ExpiresAt = fromUnix(expSec)
	a.LastLoginAt = fromUnix(loginSec)
	a.LastRefreshAt = fromUnix(refreshSec)
	a.BalanceUpdatedAt = fromUnix(balSec)
	a.CooldownUntil = fromUnix(coolSec)
	a.LastRunAt = fromUnix(runSec)
	a.CreatedAt = fromUnix(createdSec)
	a.UpdatedAt = fromUnix(updSec)
	if a.HealthStatus == "" {
		a.HealthStatus = "active"
	}
	return &a, nil
}

// ListAccounts returns all stored accounts ordered by pool status, pool order, and creation time.
func (s *Store) ListAccounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountColumns + ` FROM accounts ORDER BY pool_enabled DESC, pool_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ListPoolAccounts returns accounts that have been explicitly dragged into the OpenAI routing pool.
func (s *Store) ListPoolAccounts() ([]Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountColumns + ` FROM accounts WHERE pool_enabled = 1 ORDER BY pool_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Account, 0)
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// GetAccount fetches a single account by internal ID.
func (s *Store) GetAccount(id string) (*Account, bool, error) {
	row := s.db.QueryRow(`SELECT `+accountColumns+` FROM accounts WHERE id = ?`, strings.TrimSpace(id))
	a, err := scanAccount(row)
	if isErrNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return a, true, nil
}

// GetAccountByUserID fetches an account by its Bohrium numeric UID.
func (s *Store) GetAccountByUserID(uid int64) (*Account, bool, error) {
	row := s.db.QueryRow(`SELECT `+accountColumns+` FROM accounts WHERE user_id = ?`, uid)
	a, err := scanAccount(row)
	if isErrNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return a, true, nil
}

// UpsertAccountFromAuth inserts a brand-new Bohrium account (with PoolEnabled=false and a fresh UUIDv4 StoreID)
// or refreshes credentials of an existing account matching auth.UserID while preserving pool/check-in settings.
func (s *Store) UpsertAccountFromAuth(auth AuthState, defaultLabel string) (*Account, bool, error) {
	return s.upsertAccountInternal(auth, defaultLabel, "")
}

func (s *Store) upsertAccountInternal(auth AuthState, defaultLabel, preferredStoreID string) (*Account, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	nowSec := now.Unix()

	var existing *Account
	if auth.UserID > 0 {
		row := s.db.QueryRow(`SELECT `+accountColumns+` FROM accounts WHERE user_id = ?`, auth.UserID)
		if a, err := scanAccount(row); err == nil {
			existing = a
		}
	}

	if existing != nil {
		label := existing.Label
		if strings.TrimSpace(defaultLabel) != "" {
			label = strings.TrimSpace(defaultLabel)
		}
		nick := auth.Nickname
		if nick == "" {
			nick = existing.Nickname
		}
		phone := auth.Phone
		if phone == "" {
			phone = existing.Phone
		}
		email := auth.Email
		if email == "" {
			email = existing.Email
		}
		avatar := auth.Avatar
		if avatar == "" {
			avatar = existing.Avatar
		}
		method := auth.LoginMethod
		if method == "" {
			method = existing.LoginMethod
		}
		lastLogin := auth.LastLoginAt
		if lastLogin.IsZero() {
			lastLogin = existing.LastLoginAt
		}
		lastRefresh := auth.LastRefreshAt
		if lastRefresh.IsZero() {
			lastRefresh = existing.LastRefreshAt
		}
		storeID := existing.StoreID
		if storeID == "" {
			storeID = newUUIDv4()
		}

		_, err := s.db.Exec(`
			UPDATE accounts SET
				org_id = ?, user_type = ?, label = ?, nickname = ?, phone = ?, email = ?, avatar = ?,
				token = ?, login_method = ?, club_bound = ?, expires_at = ?, last_login_at = ?, last_refresh_at = ?,
				store_id = ?, health_status = 'active', cooldown_until = 0, last_error = '', updated_at = ?
			WHERE id = ?
		`,
			auth.OrgID, auth.UserType, label, nick, phone, email, avatar,
			auth.Token, method, boolToInt(auth.ClubBound), toUnix(auth.ExpiresAt), toUnix(lastLogin), toUnix(lastRefresh),
			storeID, nowSec, existing.ID,
		)
		if err != nil {
			return nil, false, fmt.Errorf("update account %s: %w", existing.ID, err)
		}
		updated, _, err := s.GetAccount(existing.ID)
		return updated, false, err
	}

	// New account: unique ID, unique StoreID, PoolEnabled=false by default!
	id := ""
	if auth.UserID > 0 {
		id = fmt.Sprintf("acc_u%d", auth.UserID)
	} else {
		id = "acc_" + strings.ReplaceAll(newUUIDv4()[:12], "-", "")
	}
	storeID := strings.TrimSpace(preferredStoreID)
	if storeID == "" {
		storeID = newUUIDv4()
	}
	label := strings.TrimSpace(defaultLabel)
	if auth.LastLoginAt.IsZero() {
		auth.LastLoginAt = now
	}

	_, err := s.db.Exec(`
		INSERT INTO accounts (
			id, user_id, org_id, user_type, label, nickname, phone, email, avatar,
			token, login_method, club_bound, expires_at, last_login_at, last_refresh_at,
			store_id, pool_enabled, pool_order, checkin_enabled,
			health_status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 1, 'active', ?, ?)
	`,
		id, auth.UserID, auth.OrgID, auth.UserType, label, auth.Nickname, auth.Phone, auth.Email, auth.Avatar,
		auth.Token, auth.LoginMethod, boolToInt(auth.ClubBound), toUnix(auth.ExpiresAt), toUnix(auth.LastLoginAt), toUnix(auth.LastRefreshAt),
		storeID, nowSec, nowSec,
	)
	if err != nil {
		return nil, false, fmt.Errorf("insert account %s: %w", id, err)
	}
	created, _, err := s.GetAccount(id)
	return created, true, err
}

// UpdateAccountSettings updates mutable per-account settings (label, poolEnabled, poolOrder, checkinEnabled).
func (s *Store) UpdateAccountSettings(id string, label *string, poolEnabled *bool, poolOrder *int, checkinEnabled *bool) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	acc, ok, err := s.GetAccount(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if label != nil {
		acc.Label = strings.TrimSpace(*label)
	}
	if poolEnabled != nil {
		acc.PoolEnabled = *poolEnabled
	}
	if poolOrder != nil {
		acc.PoolOrder = *poolOrder
	}
	if checkinEnabled != nil {
		acc.CheckinEnabled = *checkinEnabled
	}
	_, err = s.db.Exec(`
		UPDATE accounts SET label = ?, pool_enabled = ?, pool_order = ?, checkin_enabled = ?, updated_at = ?
		WHERE id = ?
	`, acc.Label, boolToInt(acc.PoolEnabled), acc.PoolOrder, boolToInt(acc.CheckinEnabled), time.Now().UTC().Unix(), acc.ID)
	if err != nil {
		return nil, err
	}
	updated, _, err := s.GetAccount(acc.ID)
	return updated, err
}

// SetPoolMembership atomically sets which accounts are in the OpenAI routing pool and their display order.
// Accounts not listed in poolIDs are removed from the pool (pool_enabled=0) and will no longer receive OpenAI traffic.
func (s *Store) SetPoolMembership(poolIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	nowSec := time.Now().UTC().Unix()
	if _, err := tx.Exec(`UPDATE accounts SET pool_enabled = 0, pool_order = 0, updated_at = ?`, nowSec); err != nil {
		return err
	}
	seen := make(map[string]bool, len(poolIDs))
	order := 1
	for _, rawID := range poolIDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.Exec(`UPDATE accounts SET pool_enabled = 1, pool_order = ?, updated_at = ? WHERE id = ?`, order, nowSec, id); err != nil {
			return err
		}
		order++
	}
	return tx.Commit()
}

// UpdateAccountBalance persists the latest photon/org/wallet balance snapshot for an account.
// If the account was marked insufficient/cooldown and now has spendable balance, health is restored to active.
func (s *Store) UpdateAccountBalance(id string, photon, org, walletCNY float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowSec := time.Now().UTC().Unix()
	effective := photon
	if org > 0 {
		effective += org
	}
	if effective > 0 {
		_, err := s.db.Exec(`
			UPDATE accounts SET
				photon_balance = ?, org_balance = ?, wallet_balance_cny = ?, balance_updated_at = ?,
				health_status = CASE WHEN health_status IN ('insufficient','cooldown') THEN 'active' ELSE health_status END,
				cooldown_until = CASE WHEN health_status IN ('insufficient','cooldown') THEN 0 ELSE cooldown_until END,
				last_error = CASE WHEN health_status IN ('insufficient','cooldown') THEN '' ELSE last_error END,
				updated_at = ?
			WHERE id = ?
		`, photon, org, walletCNY, nowSec, nowSec, id)
		return err
	}
	_, err := s.db.Exec(`
		UPDATE accounts SET
			photon_balance = ?, org_balance = ?, wallet_balance_cny = ?, balance_updated_at = ?, updated_at = ?
		WHERE id = ?
	`, photon, org, walletCNY, nowSec, nowSec, id)
	return err
}

// UpdateAccountHealth marks an account's health status, error note, and optional cooldown window.
func (s *Store) UpdateAccountHealth(id, healthStatus, lastError string, cooldownUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if healthStatus == "" {
		healthStatus = "active"
	}
	_, err := s.db.Exec(`
		UPDATE accounts SET health_status = ?, last_error = ?, cooldown_until = ?, updated_at = ?
		WHERE id = ?
	`, healthStatus, lastError, toUnix(cooldownUntil), time.Now().UTC().Unix(), id)
	return err
}

// UpdateAccountCheckin records the latest daily check-in completion date and summary for an account.
func (s *Store) UpdateAccountCheckin(id, day, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	nowSec := time.Now().UTC().Unix()
	_, err := s.db.Exec(`
		UPDATE accounts SET last_scheduled_day = ?, last_run_at = ?, last_run_summary = ?, updated_at = ?
		WHERE id = ?
	`, day, nowSec, summary, nowSec, id)
	return err
}

// DeleteAccount removes an account and cascades deletion of its session bindings.
func (s *Store) DeleteAccount(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("账号不存在: %s", id)
	}
	return nil
}

// AccountBindingCounts returns the number of active (non-expired) session bindings per account ID.
func (s *Store) AccountBindingCounts(now time.Time) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT account_id, COUNT(*) FROM session_bindings WHERE expires_at > ? GROUP BY account_id`, now.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var id string
		var cnt int
		if err := rows.Scan(&id, &cnt); err == nil {
			out[id] = cnt
		}
	}
	return out, rows.Err()
}

// AppendLog persists a new run/action log entry and trims old entries beyond maxLogs.
func (s *Store) AppendLog(entry RunLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("log_%d_%s", entry.Timestamp.UnixNano(), newUUIDv4()[:6])
	}
	metaBytes := []byte("{}")
	if len(entry.Meta) > 0 {
		if b, err := json.Marshal(entry.Meta); err == nil {
			metaBytes = b
		}
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO run_logs (
			id, account_id, account_uid, nickname, timestamp, trigger, action,
			task_key, task_title, success, skipped, message, duration_ms, meta_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		entry.ID, entry.AccountID, entry.AccountUID, entry.Nickname, entry.Timestamp.UTC().UnixNano(),
		entry.Trigger, entry.Action, entry.TaskKey, entry.TaskTitle,
		boolToInt(entry.Success), boolToInt(entry.Skipped), entry.Message, entry.DurationMs, string(metaBytes),
	)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM run_logs WHERE id NOT IN (SELECT id FROM run_logs ORDER BY timestamp DESC LIMIT ?)`, maxLogs)
	return nil
}

// GetLogs returns up to limit recent logs across all accounts (newest first).
func (s *Store) GetLogs(limit int) []RunLog {
	return s.GetLogsFiltered("", limit)
}

// GetLogsFiltered returns up to limit recent logs optionally scoped to a single accountID.
func (s *Store) GetLogsFiltered(accountID string, limit int) []RunLog {
	if limit <= 0 || limit > maxLogs {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if strings.TrimSpace(accountID) != "" {
		rows, err = s.db.Query(`
			SELECT id, account_id, account_uid, nickname, timestamp, trigger, action,
			       task_key, task_title, success, skipped, message, duration_ms, meta_json
			FROM run_logs WHERE account_id = ? ORDER BY timestamp DESC LIMIT ?
		`, strings.TrimSpace(accountID), limit)
	} else {
		rows, err = s.db.Query(`
			SELECT id, account_id, account_uid, nickname, timestamp, trigger, action,
			       task_key, task_title, success, skipped, message, duration_ms, meta_json
			FROM run_logs ORDER BY timestamp DESC LIMIT ?
		`, limit)
	}
	if err != nil {
		return []RunLog{}
	}
	defer rows.Close()

	out := make([]RunLog, 0, limit)
	for rows.Next() {
		var (
			e        RunLog
			tsNano   int64
			succ, sk int
			metaStr  string
		)
		if err := rows.Scan(
			&e.ID, &e.AccountID, &e.AccountUID, &e.Nickname, &tsNano, &e.Trigger, &e.Action,
			&e.TaskKey, &e.TaskTitle, &succ, &sk, &e.Message, &e.DurationMs, &metaStr,
		); err != nil {
			continue
		}
		e.Timestamp = time.Unix(0, tsNano).UTC()
		e.Success = succ != 0
		e.Skipped = sk != 0
		if metaStr != "" && metaStr != "{}" {
			_ = json.Unmarshal([]byte(metaStr), &e.Meta)
		}
		out = append(out, e)
	}
	return out
}

// --- Backward-compatible single-account helpers on Store (operate on first account) ---

func (s *Store) firstAccount() *Account {
	accounts, err := s.ListAccounts()
	if err != nil || len(accounts) == 0 {
		return nil
	}
	return &accounts[0]
}

// GetAuth returns the credentials of the primary/first account (backward compatibility for tests).
func (s *Store) GetAuth() AuthState {
	if a := s.firstAccount(); a != nil {
		return a.AuthState()
	}
	return AuthState{}
}

// StoreID returns the primary/first account's StoreID.
func (s *Store) StoreID() string {
	if a := s.firstAccount(); a != nil {
		return a.StoreID
	}
	return ""
}

// SetAuth upserts credentials into the accounts table (backward compatibility for tests & login flows).
func (s *Store) SetAuth(auth AuthState) error {
	_, _, err := s.UpsertAccountFromAuth(auth, "")
	return err
}

// ClearAuth removes the primary/first account (backward compatibility).
func (s *Store) ClearAuth() error {
	if a := s.firstAccount(); a != nil {
		return s.DeleteAccount(a.ID)
	}
	return nil
}

// GetLastScheduledDay returns the last scheduled check-in day of the primary/first account.
func (s *Store) GetLastScheduledDay() string {
	if a := s.firstAccount(); a != nil {
		return a.LastScheduledDay
	}
	return ""
}

// SetLastScheduledDay updates the primary/first account's last scheduled check-in day.
func (s *Store) SetLastScheduledDay(day, summary string) error {
	if a := s.firstAccount(); a != nil {
		return s.UpdateAccountCheckin(a.ID, day, summary)
	}
	return nil
}

// GetSummary returns the primary/first account's last scheduled check-in metadata.
func (s *Store) GetSummary() (lastDay string, lastAt time.Time, summary string) {
	if a := s.firstAccount(); a != nil {
		return a.LastScheduledDay, a.LastRunAt, a.LastRunSummary
	}
	return "", time.Time{}, ""
}

// --- Per-Account Scoped Store Views ---

// AccountScope binds Store operations to one specific account ID.
type AccountScope struct {
	root      *Store
	accountID string
}

// ForAccount returns a scoped view for a specific persisted account.
func (s *Store) ForAccount(accountID string) *AccountScope {
	return &AccountScope{root: s, accountID: strings.TrimSpace(accountID)}
}

func (a *AccountScope) AccountID() string { return a.accountID }

func (a *AccountScope) GetAuth() AuthState {
	acc, ok, err := a.root.GetAccount(a.accountID)
	if err != nil || !ok {
		return AuthState{}
	}
	return acc.AuthState()
}

func (a *AccountScope) StoreID() string {
	acc, ok, err := a.root.GetAccount(a.accountID)
	if err != nil || !ok {
		return ""
	}
	return acc.StoreID
}

func (a *AccountScope) SetAuth(auth AuthState) error {
	acc, ok, err := a.root.GetAccount(a.accountID)
	if err == nil && ok && auth.UserID == 0 {
		auth.UserID = acc.UserID
	}
	_, _, err = a.root.UpsertAccountFromAuth(auth, "")
	return err
}

func (a *AccountScope) ClearAuth() error {
	return a.root.DeleteAccount(a.accountID)
}

func (a *AccountScope) AppendLog(entry RunLog) error {
	if entry.AccountID == "" {
		entry.AccountID = a.accountID
	}
	if acc, ok, err := a.root.GetAccount(a.accountID); err == nil && ok {
		if entry.AccountUID == 0 {
			entry.AccountUID = acc.UserID
		}
		if entry.Nickname == "" {
			entry.Nickname = acc.DisplayName()
		}
	}
	return a.root.AppendLog(entry)
}

// LoginScope is a short-lived AccountStore used exclusively during new/re-login flows.
// Calling SetAuth upserts the authenticated user into accounts (PoolEnabled=false for brand-new accounts).
type LoginScope struct {
	mu      sync.Mutex
	root    *Store
	label   string
	result  *Account
	created bool
}

// ForLogin creates a temporary LoginScope to capture an incoming Bohrium login into SQLite.
func (s *Store) ForLogin(label string) *LoginScope {
	return &LoginScope{root: s, label: strings.TrimSpace(label)}
}

func (l *LoginScope) GetAuth() AuthState {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.result != nil {
		return l.result.AuthState()
	}
	return AuthState{}
}

func (l *LoginScope) StoreID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.result != nil {
		return l.result.StoreID
	}
	return ""
}

func (l *LoginScope) SetAuth(auth AuthState) error {
	acc, created, err := l.root.UpsertAccountFromAuth(auth, l.label)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.result = acc
	l.created = created
	l.mu.Unlock()
	return nil
}

func (l *LoginScope) ClearAuth() error { return nil }

func (l *LoginScope) AppendLog(entry RunLog) error {
	l.mu.Lock()
	res := l.result
	l.mu.Unlock()
	if res != nil {
		if entry.AccountID == "" {
			entry.AccountID = res.ID
		}
		if entry.AccountUID == 0 {
			entry.AccountUID = res.UserID
		}
		if entry.Nickname == "" {
			entry.Nickname = res.DisplayName()
		}
	}
	return l.root.AppendLog(entry)
}

// Result returns the upserted account and whether it was newly created.
func (l *LoginScope) Result() (*Account, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.result == nil {
		return nil, false
	}
	cp := *l.result
	return &cp, l.created
}
