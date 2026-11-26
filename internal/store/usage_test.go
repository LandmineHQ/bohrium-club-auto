package store

import (
	"testing"
	"time"
)

func TestGatewayUsageAnalyticsAndRetentionPurge(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	records := []GatewayUsageRecord{
		{
			ID:               "u1",
			CreatedAt:        now.Add(-2 * time.Hour),
			APIKeyID:         "key_cursor",
			APIKeyName:       "Cursor",
			AccountID:        "acc_1",
			AccountLabel:     "Main",
			Model:            "dspro-high",
			RequestedModel:   "DeepSeek-V4-Pro",
			ReasoningEffort:  "high",
			Endpoint:         "/v1/chat/completions",
			UpstreamAPI:      "chat_completions",
			Bridged:          false,
			RoutingMode:      "bound",
			SessionHash:      "abc1234567890def",
			RequestID:        "req-up-001",
			ClientIP:         "10.0.0.15",
			UserAgent:        "Cursor/0.49 OpenAI-JS",
			Stream:           true,
			HasTools:         true,
			HasVision:        false,
			PreflightMode:    "preflight",
			SciMasterPrompt:  true,
			UsageMultiplier:  1.0,
			Status:           200,
			FirstTokenMs:     320,
			DurationMs:       1200,
			PromptTokens:     1000,
			CompletionTokens: 500,
			ReasoningTokens:  200,
			CachedTokens:     300,
		},
		{
			ID:               "u2",
			CreatedAt:        now.Add(-26 * time.Hour),
			APIKeyID:         "key_codex",
			APIKeyName:       "Codex",
			AccountID:        "acc_2",
			AccountLabel:     "Lab",
			Model:            "kimi-k3",
			ReasoningEffort:  "max",
			Endpoint:         "/v1/responses",
			UpstreamAPI:      "chat_completions",
			Bridged:          true,
			RoutingMode:      "new_bind",
			SessionHash:      "fedcba9876543210",
			RequestID:        "req-up-002",
			ClientIP:         "192.168.1.88",
			UserAgent:        "CodexCLI/1.2",
			Stream:           false,
			HasTools:         false,
			HasVision:        true,
			PreflightMode:    "tool_skip",
			SciMasterPrompt:  true,
			UsageMultiplier:  2.0,
			Status:           200,
			FirstTokenMs:     180,
			DurationMs:       800,
			PromptTokens:     2000,
			CompletionTokens: 1000,
			ReasoningTokens:  400,
			CachedTokens:     100,
		},
		{
			ID:               "u3",
			CreatedAt:        now.Add(-1 * time.Hour),
			APIKeyID:         "key_cursor",
			APIKeyName:       "Cursor",
			AccountID:        "acc_1",
			AccountLabel:     "Main",
			Model:            "dsflash-high",
			Endpoint:         "/v1/chat/completions",
			UpstreamAPI:      "chat_completions",
			RoutingMode:      "bound",
			ClientIP:         "10.0.0.15",
			UserAgent:        "Cursor/0.49 OpenAI-JS",
			Stream:           true,
			PreflightMode:    "disabled",
			UsageMultiplier:  0.3,
			Status:           502,
			DurationMs:       400,
			Error:            "upstream timeout error",
		},
		{
			ID:               "u_old_200d",
			CreatedAt:        now.AddDate(0, 0, -200),
			APIKeyID:         "key_old",
			APIKeyName:       "Legacy",
			AccountID:        "acc_1",
			AccountLabel:     "Main",
			Model:            "dspro-high",
			Endpoint:         "/v1/chat/completions",
			Status:           200,
			DurationMs:       500,
			PromptTokens:     9000,
			CompletionTokens: 1000,
		},
	}
	for _, r := range records {
		if err := st.RecordGatewayUsage(r); err != nil {
			t.Fatalf("RecordGatewayUsage %s: %v", r.ID, err)
		}
	}

	// Default 180-day purge removes the 200-day-old record while keeping the recent 3.
	purged, err := st.PurgeExpiredGatewayUsage(now, 180)
	if err != nil {
		t.Fatalf("PurgeExpiredGatewayUsage(180): %v", err)
	}
	if purged != 1 {
		t.Fatalf("expected 1 record purged (>180d), got %d", purged)
	}

	// Query 7-day analytics (daily granularity)
	an, err := st.QueryGatewayAnalytics(UsageQueryOptions{
		RangeDays:   7,
		Granularity: "day",
		Timezone:    "Asia/Shanghai",
		Now:         now,
	})
	if err != nil {
		t.Fatalf("QueryGatewayAnalytics: %v", err)
	}
	if an.Summary.TotalRequests != 3 || an.Summary.SuccessRequests != 2 || an.Summary.FailedRequests != 1 {
		t.Fatalf("unexpected request counts: %+v", an.Summary)
	}
	if an.Summary.TotalTokens != 4500 || an.Summary.PromptTokens != 3000 || an.Summary.CompletionTokens != 1500 || an.Summary.ReasoningTokens != 600 || an.Summary.CachedTokens != 400 {
		t.Fatalf("unexpected token totals: %+v", an.Summary)
	}
	// Weighted tokens: u1=1500*1 + u2=3000*2 = 7500; AvgFirstTokenMs: (320+180)/2 = 250
	if an.Summary.WeightedTokens != 7500 {
		t.Fatalf("expected weightedTokens=7500, got %.2f", an.Summary.WeightedTokens)
	}
	if an.Summary.AvgFirstTokenMs != 250 {
		t.Fatalf("expected avgFirstTokenMs=250, got %.2f", an.Summary.AvgFirstTokenMs)
	}
	if len(an.TimeSeries) != 7 {
		t.Fatalf("expected 7 dense daily buckets, got %d", len(an.TimeSeries))
	}
	if len(an.ByKey) != 2 || an.ByKey[0].ID != "key_codex" || an.ByKey[0].TotalTokens != 3000 {
		t.Fatalf("expected key_codex ranked first by tokens, got %+v", an.ByKey)
	}
	if len(an.ByModel) != 3 || an.ByModel[0].ID != "kimi-k3" {
		t.Fatalf("unexpected ByModel breakdown: %+v", an.ByModel)
	}

	// Detailed paginated logs queries (sub2api-style filters & search)
	page1, err := st.ListGatewayUsageLogs(UsageLogsQueryOptions{
		RangeDays: 7,
		Now:       now,
		Page:      1,
		PageSize:  2,
	})
	if err != nil {
		t.Fatalf("ListGatewayUsageLogs page1: %v", err)
	}
	if page1.Total != 3 || page1.TotalPages != 2 || len(page1.Items) != 2 || page1.Items[0].ID != "u3" || page1.Items[1].ID != "u1" {
		t.Fatalf("unexpected page1 result: %+v", page1)
	}
	if page1.Items[1].RequestedModel != "DeepSeek-V4-Pro" || page1.Items[1].ReasoningEffort != "high" || !page1.Items[1].HasTools || page1.Items[1].ClientIP != "10.0.0.15" || page1.Items[1].FirstTokenMs != 320 {
		t.Fatalf("missing detailed columns on u1: %+v", page1.Items[1])
	}

	errOnly, err := st.ListGatewayUsageLogs(UsageLogsQueryOptions{
		RangeDays: 7,
		Status:    "error",
		Now:       now,
	})
	if err != nil || errOnly.Total != 1 || errOnly.Items[0].ID != "u3" {
		t.Fatalf("expected 1 error record u3, got %+v err=%v", errOnly, err)
	}

	nonStreamSearch, err := st.ListGatewayUsageLogs(UsageLogsQueryOptions{
		RangeDays: 7,
		Stream:    "false",
		Query:     "CodexCLI",
		Now:       now,
	})
	if err != nil || nonStreamSearch.Total != 1 || nonStreamSearch.Items[0].ID != "u2" || !nonStreamSearch.Items[0].Bridged || !nonStreamSearch.Items[0].HasVision || nonStreamSearch.Items[0].WeightedTokens != 6000 {
		t.Fatalf("unexpected nonStreamSearch result: %+v err=%v", nonStreamSearch, err)
	}

	// Filter by APIKeyID = key_cursor
	filtered, err := st.QueryGatewayAnalytics(UsageQueryOptions{
		RangeDays:   1,
		Granularity: "hour",
		APIKeyID:    "key_cursor",
		Timezone:    "Asia/Shanghai",
		Now:         now,
	})
	if err != nil {
		t.Fatalf("QueryGatewayAnalytics filtered: %v", err)
	}
	if filtered.Summary.TotalRequests != 2 || filtered.Summary.TotalTokens != 1500 {
		t.Fatalf("unexpected filtered totals: %+v", filtered.Summary)
	}
	if len(filtered.TimeSeries) != 24 {
		t.Fatalf("expected 24 dense hourly buckets, got %d", len(filtered.TimeSeries))
	}

	// Custom retention purge: 1 day removes u2 (26h old)
	purged2, err := st.PurgeExpiredGatewayUsage(now, 1)
	if err != nil {
		t.Fatalf("PurgeExpiredGatewayUsage(1): %v", err)
	}
	if purged2 != 1 {
		t.Fatalf("expected 1 record purged (>1d), got %d", purged2)
	}
}
