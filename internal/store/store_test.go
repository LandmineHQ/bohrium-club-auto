package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyStateAndResponsesMigration(t *testing.T) {
	tmp := t.TempDir()

	// Seed legacy single-account data/state.json and data/responses.json
	legacyState := map[string]any{
		"auth": map[string]any{
			"token":       "legacy-jwt-token-xyz",
			"userId":      888001,
			"orgId":       999001,
			"nickname":    "LegacyScientist",
			"loginMethod": "wechat_qr",
			"clubBound":   true,
			"expiresAt":   time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339),
			"lastLoginAt": time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		},
		"lastScheduledDay": "2026-11-24",
		"lastRunSummary":   "3/3 Web tasks done",
		"logs": []map[string]any{
			{
				"time":    time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339),
				"level":   "info",
				"action":  "scheduler",
				"message": "legacy run completed",
			},
		},
	}
	stateRaw, _ := json.Marshal(legacyState)
	if err := os.WriteFile(filepath.Join(tmp, "state.json"), stateRaw, 0o600); err != nil {
		t.Fatalf("write legacy state.json: %v", err)
	}

	legacyResponses := map[string]any{
		"order": []string{"resp_legacy_001"},
		"records": map[string]any{
			"resp_legacy_001": map[string]any{
				"id":        "resp_legacy_001",
				"model":     "dspro-high",
				"createdAt": time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
				"response":  map[string]any{"id": "resp_legacy_001", "status": "completed"},
			},
		},
	}
	respRaw, _ := json.Marshal(legacyResponses)
	if err := os.WriteFile(filepath.Join(tmp, "responses.json"), respRaw, 0o600); err != nil {
		t.Fatalf("write legacy responses.json: %v", err)
	}

	// First open triggers one-time automatic migration into bohrium.db
	st, err := Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	accs, err := st.ListAccounts()
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accs) != 1 {
		t.Fatalf("expected 1 migrated account, got %d", len(accs))
	}
	acc := accs[0]
	if acc.UserID != 888001 || acc.Token != "legacy-jwt-token-xyz" || acc.Nickname != "LegacyScientist" {
		t.Fatalf("migrated account fields mismatch: %+v", acc)
	}
	if acc.PoolEnabled {
		t.Fatalf("migrated legacy account MUST default to poolEnabled=false (strict manual opt-in)")
	}
	if !acc.CheckinEnabled || acc.LastScheduledDay != "2026-11-24" || acc.StoreID == "" {
		t.Fatalf("expected checkinEnabled=true, preserved lastScheduledDay, and generated storeId, got %+v", acc)
	}
	logs := st.GetLogs(10)
	if len(logs) == 0 || logs[0].Message != "legacy run completed" {
		t.Fatalf("expected migrated run log, got %+v", logs)
	}
	rec, ok := st.GetResponse("resp_legacy_001")
	if !ok || rec.Model != "dspro-high" {
		t.Fatalf("expected migrated response record, got ok=%v rec=%+v", ok, rec)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("st.Close: %v", err)
	}

	// Second open must be idempotent (no duplicate accounts or logs)
	st2, err := Open(tmp)
	if err != nil {
		t.Fatalf("store.Open #2: %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	accs2, _ := st2.ListAccounts()
	if len(accs2) != 1 {
		t.Fatalf("expected idempotent 1 account on reopen, got %d", len(accs2))
	}
}

func TestSessionBindingHard7DayTTLAndCascadeDelete(t *testing.T) {
	tmp := t.TempDir()
	st, err := Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	acc, _, err := st.UpsertAccountFromAuth(AuthState{
		Token:     "tok-ttl-1",
		UserID:    55501,
		ExpiresAt: time.Now().Add(48 * time.Hour),
	}, "TTL-Account")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth: %v", err)
	}

	baseTime := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	// Intentionally pass 30-day expiry -> store MUST strictly cap at created_at + 7 days!
	if err := st.UpsertSessionBinding(SessionBinding{
		SessionHash: "sess-hash-7d",
		AccountID:   acc.ID,
		ModelHint:   "dspro-high",
		CreatedAt:   baseTime,
		LastUsedAt:  baseTime,
		ExpiresAt:   baseTime.Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSessionBinding: %v", err)
	}

	got, ok, err := st.GetSessionBinding("sess-hash-7d", baseTime.Add(1*time.Hour))
	if err != nil || !ok {
		t.Fatalf("GetSessionBinding: ok=%v err=%v", ok, err)
	}
	wantExp := baseTime.Add(SessionBindingTTL)
	if !got.ExpiresAt.Equal(wantExp) {
		t.Fatalf("expected ExpiresAt capped at %s, got %s", wantExp, got.ExpiresAt)
	}

	// Touching binding increments HitCount but MUST NOT slide ExpiresAt past 7 days
	if err := st.TouchSessionBinding("sess-hash-7d", "run-2", "kimi-k3", baseTime.Add(3*24*time.Hour)); err != nil {
		t.Fatalf("TouchSessionBinding: %v", err)
	}
	got2, ok, _ := st.GetSessionBinding("sess-hash-7d", baseTime.Add(6*24*time.Hour))
	if !ok || got2.HitCount != 2 || !got2.ExpiresAt.Equal(wantExp) {
		t.Fatalf("expected HitCount=2 and unchanged hard 7d ExpiresAt, got %+v", got2)
	}

	// After 7 days + 1 second, GetSessionBinding returns not found and PurgeExpiredSessionBindings deletes it
	after7d := baseTime.Add(SessionBindingTTL + time.Second)
	if _, ok, _ := st.GetSessionBinding("sess-hash-7d", after7d); ok {
		t.Fatalf("expected binding to be expired after 7 days")
	}
	purged, err := st.PurgeExpiredSessionBindings(after7d)
	if err != nil || purged != 1 {
		t.Fatalf("expected 1 purged expired binding, got purged=%d err=%v", purged, err)
	}

	// Cascade delete: deleting an account deletes its bindings via FOREIGN KEY ON DELETE CASCADE
	if err := st.UpsertSessionBinding(SessionBinding{
		SessionHash: "sess-cascade",
		AccountID:   acc.ID,
		CreatedAt:   baseTime,
		LastUsedAt:  baseTime,
		ExpiresAt:   wantExp,
	}); err != nil {
		t.Fatalf("UpsertSessionBinding cascade: %v", err)
	}
	if err := st.DeleteAccount(acc.ID); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if _, ok, _ := st.GetSessionBinding("sess-cascade", baseTime.Add(time.Minute)); ok {
		t.Fatalf("expected session binding to be cascade-deleted when account is deleted")
	}
}
