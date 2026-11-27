package store

import (
	"testing"
	"time"
)

func TestGatewayKeyLifetimeUsageSurvivesRetentionPurge(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, rec := range []GatewayUsageRecord{
		{ID: "lifetime_old", CreatedAt: now.AddDate(0, 0, -200), APIKeyID: "key_quota", TotalTokens: 500, Status: 200},
		{ID: "lifetime_today", CreatedAt: now, APIKeyID: "key_quota", TotalTokens: 100, Status: 200},
		{ID: "lifetime_other", CreatedAt: now, APIKeyID: "key_other", TotalTokens: 999, Status: 200},
	} {
		if err := st.RecordGatewayUsage(rec); err != nil {
			t.Fatal(err)
		}
		// A repeated delivery of the same request ID must not double-bill the key.
		if err := st.RecordGatewayUsage(rec); err != nil {
			t.Fatal(err)
		}
	}

	summary, err := st.GatewayKeyUsageSummary(now, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if got := summary["key_quota"]; got.RequestCount != 2 || got.TotalTokens != 600 || got.TodayRequests != 1 || got.TodayTokens != 100 || got.LastUsedAt == nil {
		t.Fatalf("unexpected lifetime summary: %+v", got)
	}
	purged, err := st.PurgeExpiredGatewayUsage(now, 180)
	if err != nil || purged != 1 {
		t.Fatalf("expected one old analytics row purged, purged=%d err=%v", purged, err)
	}
	total, today, err := st.GatewayKeyUsageForAdmission("key_quota", now, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if total != 600 || today != 100 {
		t.Fatalf("retention purge must not reduce lifetime quota: total=%d today=%d", total, today)
	}

	cleared, err := st.ClearGatewayUsageInTimezone(now, "Asia/Shanghai")
	if err != nil || cleared != 2 {
		t.Fatalf("expected two remaining analytics rows cleared, cleared=%d err=%v", cleared, err)
	}
	total, today, err = st.GatewayKeyUsageForAdmission("key_quota", now, "Asia/Shanghai")
	if err != nil || total != 600 || today != 100 {
		t.Fatalf("clearing analytics must preserve quota counters: total=%d today=%d err=%v", total, today, err)
	}
	if err := st.RecordGatewayUsage(GatewayUsageRecord{ID: "lifetime_after_clear", CreatedAt: now, APIKeyID: "key_quota", TotalTokens: 50, Status: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClearGatewayUsageInTimezone(now, "Asia/Shanghai"); err != nil {
		t.Fatal(err)
	}
	total, today, err = st.GatewayKeyUsageForAdmission("key_quota", now, "Asia/Shanghai")
	if err != nil || total != 650 || today != 150 {
		t.Fatalf("repeated clear must keep baseline and new usage exactly once: total=%d today=%d err=%v", total, today, err)
	}
}

func TestGatewayKeyLifetimeUsageBackfillsLegacyLogs(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	legacyTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := st.db.Exec(`INSERT INTO gateway_usage_logs (id, created_at, api_key_id, api_key_name, total_tokens, status) VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy_quota_row", legacyTime.Unix(), "key_legacy", "Legacy", 1234, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM meta WHERE key = 'gateway_key_usage_totals_backfilled_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := st.backfillGatewayKeyUsageTotals(); err != nil {
		t.Fatal(err)
	}
	total, _, err := st.GatewayKeyUsageForAdmission("key_legacy", time.Now().UTC(), "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1234 {
		t.Fatalf("expected backfilled total 1234, got %d", total)
	}
}
