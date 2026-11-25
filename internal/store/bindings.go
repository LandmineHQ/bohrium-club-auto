package store

import (
	"encoding/json"
	"strings"
	"time"
)

// GetSessionBinding returns an active (unexpired) session-to-account routing binding from SQLite.
func (s *Store) GetSessionBinding(sessionHash string, now time.Time) (*SessionBinding, bool, error) {
	row := s.db.QueryRow(`
		SELECT session_hash, account_id, scix_session_id, scix_run_id, session_created_at,
		       model_hint, hit_count, created_at, last_used_at, expires_at
		FROM session_bindings
		WHERE session_hash = ? AND expires_at > ?
	`, sessionHash, now.UTC().Unix())

	var (
		b                          SessionBinding
		createdSec, usedSec, expSec int64
	)
	err := row.Scan(
		&b.SessionHash, &b.AccountID, &b.SciXSessionID, &b.SciXRunID, &b.SessionCreatedAt,
		&b.ModelHint, &b.HitCount, &createdSec, &usedSec, &expSec,
	)
	if isErrNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	b.CreatedAt = fromUnix(createdSec)
	b.LastUsedAt = fromUnix(usedSec)
	b.ExpiresAt = fromUnix(expSec)
	return &b, true, nil
}

// UpsertSessionBinding inserts or replaces a session binding, strictly capping ExpiresAt to CreatedAt + 7 days.
func (s *Store) UpsertSessionBinding(b SessionBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	if b.LastUsedAt.IsZero() {
		b.LastUsedAt = now
	}
	maxExp := b.CreatedAt.Add(SessionBindingTTL)
	if b.ExpiresAt.IsZero() || b.ExpiresAt.After(maxExp) {
		b.ExpiresAt = maxExp
	}
	if b.HitCount <= 0 {
		b.HitCount = 1
	}
	if b.SciXSessionID == "" {
		b.SciXSessionID = newUUIDv4()
	}
	if b.SessionCreatedAt == "" {
		b.SessionCreatedAt = b.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO session_bindings (
			session_hash, account_id, scix_session_id, scix_run_id, session_created_at,
			model_hint, hit_count, created_at, last_used_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		b.SessionHash, b.AccountID, b.SciXSessionID, b.SciXRunID, b.SessionCreatedAt,
		b.ModelHint, b.HitCount, toUnix(b.CreatedAt), toUnix(b.LastUsedAt), toUnix(b.ExpiresAt),
	)
	return err
}

// TouchSessionBinding updates last_used_at, hit_count, and optionally scix_run_id / model_hint
// WITHOUT extending expires_at past the strict 7-day hard cap.
func (s *Store) TouchSessionBinding(sessionHash, scixRunID, modelHint string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE session_bindings SET
			hit_count = hit_count + 1,
			last_used_at = ?,
			scix_run_id = CASE WHEN ? != '' THEN ? ELSE scix_run_id END,
			model_hint = CASE WHEN ? != '' THEN ? ELSE model_hint END
		WHERE session_hash = ? AND expires_at > ?
	`, now.UTC().Unix(), scixRunID, scixRunID, modelHint, modelHint, sessionHash, now.UTC().Unix())
	return err
}

// RebindSession reassigns an existing session_hash to a new healthy pooled account (e.g., on failover),
// generating a fresh scix_session_id for the new account while keeping the 7-day hard expiry cap.
func (s *Store) RebindSession(sessionHash, newAccountID, newSciXSessionID, newSciXRunID, modelHint string, now time.Time) (*SessionBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now = now.UTC()
	var createdSec, expSec, hits int64
	err := s.db.QueryRow(`SELECT created_at, expires_at, hit_count FROM session_bindings WHERE session_hash = ?`, sessionHash).Scan(&createdSec, &expSec, &hits)
	if isErrNoRows(err) || expSec <= now.Unix() {
		createdSec = now.Unix()
		expSec = now.Add(SessionBindingTTL).Unix()
		hits = 1
	} else {
		hits++
		maxExp := fromUnix(createdSec).Add(SessionBindingTTL).Unix()
		if expSec > maxExp {
			expSec = maxExp
		}
	}
	if newSciXSessionID == "" {
		newSciXSessionID = newUUIDv4()
	}
	sessCreatedAt := now.Format("2006-01-02T15:04:05.000Z")

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO session_bindings (
			session_hash, account_id, scix_session_id, scix_run_id, session_created_at,
			model_hint, hit_count, created_at, last_used_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, sessionHash, newAccountID, newSciXSessionID, newSciXRunID, sessCreatedAt, modelHint, hits, createdSec, now.Unix(), expSec)
	if err != nil {
		return nil, err
	}
	return &SessionBinding{
		SessionHash:      sessionHash,
		AccountID:        newAccountID,
		SciXSessionID:    newSciXSessionID,
		SciXRunID:        newSciXRunID,
		SessionCreatedAt: sessCreatedAt,
		ModelHint:        modelHint,
		HitCount:         hits,
		CreatedAt:        fromUnix(createdSec),
		LastUsedAt:       now,
		ExpiresAt:        fromUnix(expSec),
	}, nil
}

// DeleteSessionBinding removes a single session binding by hash.
func (s *Store) DeleteSessionBinding(sessionHash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM session_bindings WHERE session_hash = ?`, strings.TrimSpace(sessionHash))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClearSessionBindings removes all cached session bindings.
func (s *Store) ClearSessionBindings() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM session_bindings`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeExpiredSessionBindings permanently deletes all bindings older than their 7-day TTL.
func (s *Store) PurgeExpiredSessionBindings(now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM session_bindings WHERE expires_at <= ?`, now.UTC().Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListSessionBindings lists active (non-expired) bindings enriched with account labels, newest-used first.
func (s *Store) ListSessionBindings(now time.Time, limit int) ([]SessionBinding, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT b.session_hash, b.account_id, b.scix_session_id, b.scix_run_id, b.session_created_at,
		       b.model_hint, b.hit_count, b.created_at, b.last_used_at, b.expires_at,
		       COALESCE(a.label, ''), COALESCE(a.nickname, ''), COALESCE(a.user_id, 0)
		FROM session_bindings b
		LEFT JOIN accounts a ON a.id = b.account_id
		WHERE b.expires_at > ?
		ORDER BY b.last_used_at DESC
		LIMIT ?
	`, now.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SessionBinding, 0)
	for rows.Next() {
		var (
			b                          SessionBinding
			createdSec, usedSec, expSec int64
		)
		if err := rows.Scan(
			&b.SessionHash, &b.AccountID, &b.SciXSessionID, &b.SciXRunID, &b.SessionCreatedAt,
			&b.ModelHint, &b.HitCount, &createdSec, &usedSec, &expSec,
			&b.AccountLabel, &b.AccountNickname, &b.AccountUID,
		); err != nil {
			return nil, err
		}
		b.CreatedAt = fromUnix(createdSec)
		b.LastUsedAt = fromUnix(usedSec)
		b.ExpiresAt = fromUnix(expSec)
		out = append(out, b)
	}
	return out, rows.Err()
}

// --- OpenAI Responses API SQLite persistence ---

// SaveResponse persists a Responses API turn record and prunes oldest records past maxStoredResponses.
func (s *Store) SaveResponse(rec *ResponseRecord) error {
	if rec == nil || rec.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	inJSON, _ := json.Marshal(rec.InputItems)
	outJSON, _ := json.Marshal(rec.OutputItems)
	respJSON, _ := json.Marshal(rec.Response)

	var instr any
	if rec.Instructions != nil {
		instr = *rec.Instructions
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO responses (
			id, previous_response_id, account_id, session_hash, model,
			instructions, input_items_json, output_items_json, response_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		rec.ID, rec.PreviousResponseID, rec.AccountID, rec.SessionHash, rec.Model,
		instr, string(inJSON), string(outJSON), string(respJSON), rec.CreatedAt.UTC().UnixNano(),
	)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM responses WHERE id NOT IN (SELECT id FROM responses ORDER BY created_at DESC LIMIT ?)`, maxStoredResponses)
	return nil
}

// GetResponse fetches a stored response record by ID.
func (s *Store) GetResponse(id string) (*ResponseRecord, bool) {
	row := s.db.QueryRow(`
		SELECT id, previous_response_id, account_id, session_hash, model,
		       instructions, input_items_json, output_items_json, response_json, created_at
		FROM responses WHERE id = ?
	`, strings.TrimSpace(id))

	var (
		rec                              ResponseRecord
		instrPtr                         *string
		inStr, outStr, respStr           string
		createdNano                      int64
	)
	if err := row.Scan(
		&rec.ID, &rec.PreviousResponseID, &rec.AccountID, &rec.SessionHash, &rec.Model,
		&instrPtr, &inStr, &outStr, &respStr, &createdNano,
	); err != nil {
		return nil, false
	}
	rec.Instructions = instrPtr
	rec.CreatedAt = time.Unix(0, createdNano).UTC()
	_ = json.Unmarshal([]byte(inStr), &rec.InputItems)
	_ = json.Unmarshal([]byte(outStr), &rec.OutputItems)
	_ = json.Unmarshal([]byte(respStr), &rec.Response)
	return &rec, true
}

// DeleteResponse deletes a stored response by ID.
func (s *Store) DeleteResponse(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM responses WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// CountResponses returns the count of locally stored Responses API records.
func (s *Store) CountResponses() int {
	var cnt int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM responses`).Scan(&cnt)
	return cnt
}

// BuildResponseChain walks previous_response_id links chronologically and returns all prior items,
// last non-empty instructions, root response ID, owning accountID, and sessionHash.
func (s *Store) BuildResponseChain(prevID string) (items []map[string]any, lastInstr *string, rootID, accountID, sessionHash string, found bool) {
	var chain []*ResponseRecord
	visited := make(map[string]bool)
	cur := strings.TrimSpace(prevID)
	for cur != "" && !visited[cur] {
		visited[cur] = true
		rec, ok := s.GetResponse(cur)
		if !ok {
			if len(chain) == 0 {
				return nil, nil, "", "", "", false
			}
			break
		}
		chain = append(chain, rec)
		cur = rec.PreviousResponseID
	}
	if len(chain) == 0 {
		return nil, nil, "", "", "", false
	}
	rootID = chain[len(chain)-1].ID
	// Prefer the most recent turn's accountID/sessionHash (in case a failover rebound it), falling back to older turns.
	for _, r := range chain {
		if accountID == "" && r.AccountID != "" {
			accountID = r.AccountID
		}
		if sessionHash == "" && r.SessionHash != "" {
			sessionHash = r.SessionHash
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		r := chain[i]
		if r.Instructions != nil && *r.Instructions != "" {
			lastInstr = r.Instructions
		}
		items = append(items, r.InputItems...)
		items = append(items, r.OutputItems...)
	}
	return items, lastInstr, rootID, accountID, sessionHash, true
}

// FindResponseItemByID searches recent stored responses for an input/output item matching itemID.
func (s *Store) FindResponseItemByID(itemID string) (map[string]any, bool) {
	rows, err := s.db.Query(`SELECT input_items_json, output_items_json FROM responses ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	for rows.Next() {
		var inStr, outStr string
		if err := rows.Scan(&inStr, &outStr); err != nil {
			continue
		}
		var outItems, inItems []map[string]any
		_ = json.Unmarshal([]byte(outStr), &outItems)
		for _, it := range outItems {
			if id, _ := it["id"].(string); id == itemID {
				return it, true
			}
		}
		_ = json.Unmarshal([]byte(inStr), &inItems)
		for _, it := range inItems {
			if id, _ := it["id"].(string); id == itemID {
				return it, true
			}
		}
	}
	return nil, false
}
