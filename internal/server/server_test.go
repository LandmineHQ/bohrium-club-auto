package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/scheduler"
	"bohrium-club-auto/internal/store"
)

func setupTestServer(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "super-secret-admin")

	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	httpCli, _ := chromehttp.New(chromehttp.Options{})
	mgr := bohrium.NewManager(httpCli, cfgMgr, st)
	sched := scheduler.New(mgr, cfgMgr, st)
	srv := New("test-1.0", cfgMgr, st, httpCli, mgr, sched)
	return srv, st, "super-secret-admin"
}

func TestAdminPasswordProtectionAndHealthz(t *testing.T) {
	srv, _, pw := setupTestServer(t)

	// 1. /healthz is public and reports Chrome UA
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Chrome/") {
		t.Fatalf("unexpected /healthz: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Protected API rejects unauthenticated access
	req2 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/settings, got %d", rec2.Code)
	}

	// 3. Wrong admin password rejected
	badBody, _ := json.Marshal(map[string]string{"password": "wrong"})
	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(badBody))
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", rec3.Code)
	}

	// 4. Correct admin password issues HttpOnly cookie that grants access
	goodBody, _ := json.Marshal(map[string]string{"password": pw})
	req4 := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(goodBody))
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("expected 200 login, got %d: %s", rec4.Code, rec4.Body.String())
	}
	cookies := rec4.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != adminCookieName || !cookies[0].HttpOnly {
		t.Fatalf("expected HttpOnly session cookie, got %+v", cookies)
	}

	req5 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req5.AddCookie(cookies[0])
	rec5 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", rec5.Code)
	}

	// 5. Embedded dashboard HTML renders cleanly
	req6 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec6 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec6, req6)
	if rec6.Code != http.StatusOK || !strings.Contains(rec6.Body.String(), "Bohrium Club Auto") {
		t.Fatalf("expected embedded dashboard HTML, got code=%d", rec6.Code)
	}
}

func TestMultiAccountPoolAndBindingsAdminAPI(t *testing.T) {
	srv, st, pw := setupTestServer(t)

	acc1, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token:       "tok-secret-alpha-111",
		UserID:      1001,
		OrgID:       2001,
		Nickname:    "Alpha",
		ExpiresAt:   time.Now().Add(48 * time.Hour),
		LoginMethod: "token",
	}, "Main")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth acc1: %v", err)
	}
	acc2, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token:       "tok-secret-beta-222",
		UserID:      1002,
		OrgID:       2002,
		Nickname:    "Beta",
		ExpiresAt:   time.Now().Add(48 * time.Hour),
		LoginMethod: "wechat_qr",
	}, "Spare")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth acc2: %v", err)
	}
	if acc1.PoolEnabled || acc2.PoolEnabled {
		t.Fatalf("newly logged-in accounts must default to poolEnabled=false")
	}
	if acc1.StoreID == "" || acc2.StoreID == "" || acc1.StoreID == acc2.StoreID {
		t.Fatalf("expected distinct non-empty storeIds, got %q vs %q", acc1.StoreID, acc2.StoreID)
	}
	_ = st.UpdateAccountBalance(acc1.ID, 8000, 2000, 10)
	_ = st.UpdateAccountBalance(acc2.ID, 2500, 0, 5)

	doAdmin := func(method, path string, body any) *httptest.ResponseRecorder {
		var rdr *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rdr)
		req.Header.Set("X-Admin-Password", pw)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// 1. GET /api/accounts lists both accounts and NEVER leaks raw JWT tokens
	rec := doAdmin(http.MethodGet, "/api/accounts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/accounts: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tok-secret-alpha-111") || strings.Contains(rec.Body.String(), "tok-secret-beta-222") {
		t.Fatalf("raw JWT token leaked in /api/accounts response!")
	}
	var listRes struct {
		OK       bool            `json:"ok"`
		Accounts []store.Account `json:"accounts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listRes)
	if len(listRes.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(listRes.Accounts))
	}

	// 2. PATCH /api/accounts/{id} updates label & checkinEnabled
	chkFalse := false
	lbl := "Lab-A"
	recPatch := doAdmin(http.MethodPatch, "/api/accounts/"+acc1.ID, map[string]any{
		"label":          lbl,
		"checkinEnabled": chkFalse,
	})
	if recPatch.Code != http.StatusOK {
		t.Fatalf("PATCH account: code=%d body=%s", recPatch.Code, recPatch.Body.String())
	}
	got1, _, _ := st.GetAccount(acc1.ID)
	if got1.Label != "Lab-A" || got1.CheckinEnabled {
		t.Fatalf("expected updated label/checkin, got %+v", got1)
	}

	// 3. PUT /api/gateway/pool drags acc1 into the OpenAI pool (leaving acc2 out)
	recPool := doAdmin(http.MethodPut, "/api/gateway/pool", map[string]any{
		"accountIds": []string{acc1.ID},
	})
	if recPool.Code != http.StatusOK {
		t.Fatalf("PUT /api/gateway/pool: code=%d body=%s", recPool.Code, recPool.Body.String())
	}
	poolAccs, _ := st.ListPoolAccounts()
	if len(poolAccs) != 1 || poolAccs[0].ID != acc1.ID {
		t.Fatalf("expected only acc1 in pool, got %+v", poolAccs)
	}

	// 4. SQLite session bindings endpoints (list, delete one, clear all)
	now := time.Now().UTC()
	_ = st.UpsertSessionBinding(store.SessionBinding{
		SessionHash: "hash-session-one",
		AccountID:   acc1.ID,
		ModelHint:   "dspro-high",
		CreatedAt:   now,
		LastUsedAt:  now,
		ExpiresAt:   now.Add(store.SessionBindingTTL),
	})
	_ = st.UpsertSessionBinding(store.SessionBinding{
		SessionHash: "hash-session-two",
		AccountID:   acc1.ID,
		ModelHint:   "kimi-k3",
		CreatedAt:   now,
		LastUsedAt:  now,
		ExpiresAt:   now.Add(store.SessionBindingTTL),
	})

	recB := doAdmin(http.MethodGet, "/api/gateway/bindings", nil)
	if recB.Code != http.StatusOK || !strings.Contains(recB.Body.String(), "hash-session-one") {
		t.Fatalf("GET /api/gateway/bindings failed: %d %s", recB.Code, recB.Body.String())
	}

	recDel1 := doAdmin(http.MethodDelete, "/api/gateway/bindings/hash-session-one", nil)
	if recDel1.Code != http.StatusOK {
		t.Fatalf("DELETE single binding failed: %d %s", recDel1.Code, recDel1.Body.String())
	}
	if _, found, _ := st.GetSessionBinding("hash-session-one", now); found {
		t.Fatalf("expected hash-session-one to be deleted")
	}

	recDelAll := doAdmin(http.MethodDelete, "/api/gateway/bindings", nil)
	if recDelAll.Code != http.StatusOK {
		t.Fatalf("DELETE all bindings failed: %d %s", recDelAll.Code, recDelAll.Body.String())
	}
	rem, _ := st.ListSessionBindings(now, 50)
	if len(rem) != 0 {
		t.Fatalf("expected 0 remaining bindings, got %d", len(rem))
	}

	// 5. DELETE /api/accounts/{id} removes acc2 cleanly
	recDelAcc := doAdmin(http.MethodDelete, "/api/accounts/"+acc2.ID, nil)
	if recDelAcc.Code != http.StatusOK {
		t.Fatalf("DELETE account failed: %d %s", recDelAcc.Code, recDelAcc.Body.String())
	}
	afterAccs, _ := st.ListAccounts()
	if len(afterAccs) != 1 || afterAccs[0].ID != acc1.ID {
		t.Fatalf("expected only acc1 remaining, got %+v", afterAccs)
	}

	// 6. OpenAI usage analytics & configurable retention (default 180d -> custom 90d)
	_ = st.RecordGatewayUsage(store.GatewayUsageRecord{
		CreatedAt:        now.Add(-1 * time.Hour),
		APIKeyID:         "key_test",
		APIKeyName:       "Codex",
		AccountID:        acc1.ID,
		AccountLabel:     "Lab-A",
		Model:            "dspro-high",
		ReasoningEffort:  "high",
		Endpoint:         "/v1/chat/completions",
		UpstreamAPI:      "chat_completions",
		RoutingMode:      "bound",
		ClientIP:         "10.1.2.3",
		UserAgent:        "CodexCLI/1.5",
		Stream:           true,
		PreflightMode:    "preflight",
		UsageMultiplier:  1.0,
		Status:           200,
		FirstTokenMs:     240,
		DurationMs:       950,
		PromptTokens:     1200,
		CompletionTokens: 300,
		ReasoningTokens:  100,
	})
	_ = st.RecordGatewayUsage(store.GatewayUsageRecord{
		CreatedAt:        now.AddDate(0, 0, -120),
		APIKeyID:         "key_test",
		APIKeyName:       "Codex",
		AccountID:        acc1.ID,
		AccountLabel:     "Lab-A",
		Model:            "dspro-high",
		Endpoint:         "/v1/chat/completions",
		Status:           200,
		PromptTokens:     5000,
		CompletionTokens: 1000,
	})

	recAn := doAdmin(http.MethodGet, "/api/gateway/analytics?days=180", nil)
	if recAn.Code != http.StatusOK || !strings.Contains(recAn.Body.String(), `"analyticsRetentionDays":180`) || !strings.Contains(recAn.Body.String(), `"totalTokens":7500`) {
		t.Fatalf("GET /api/gateway/analytics failed: %d %s", recAn.Code, recAn.Body.String())
	}

	recLogs := doAdmin(http.MethodGet, "/api/gateway/analytics/logs?days=180&q=CodexCLI&page=1&pageSize=10", nil)
	if recLogs.Code != http.StatusOK || !strings.Contains(recLogs.Body.String(), `"total":1`) || !strings.Contains(recLogs.Body.String(), `"clientIp":"10.1.2.3"`) || !strings.Contains(recLogs.Body.String(), `"firstTokenMs":240`) {
		t.Fatalf("GET /api/gateway/analytics/logs failed: %d %s", recLogs.Code, recLogs.Body.String())
	}

	// Update retention to 90 days -> immediately purges the 120-day-old record
	recSet := doAdmin(http.MethodPut, "/api/gateway/settings", map[string]any{
		"analyticsRetentionDays": 90,
	})
	if recSet.Code != http.StatusOK || !strings.Contains(recSet.Body.String(), `"purged":1`) {
		t.Fatalf("PUT /api/gateway/settings retention update failed: %d %s", recSet.Code, recSet.Body.String())
	}
	recAn2 := doAdmin(http.MethodGet, "/api/gateway/analytics?days=180", nil)
	if recAn2.Code != http.StatusOK || !strings.Contains(recAn2.Body.String(), `"analyticsRetentionDays":90`) || !strings.Contains(recAn2.Body.String(), `"totalTokens":1500`) {
		t.Fatalf("GET /api/gateway/analytics after 90d purge failed: %d %s", recAn2.Code, recAn2.Body.String())
	}

	// 7. Per-model visibility toggle endpoint PATCH /api/gateway/models/{id}
	recModOn := doAdmin(http.MethodPatch, "/api/gateway/models/standard", map[string]any{"enabled": true})
	if recModOn.Code != http.StatusOK || !strings.Contains(recModOn.Body.String(), `"enabled":true`) {
		t.Fatalf("PATCH /api/gateway/models/standard failed: %d %s", recModOn.Code, recModOn.Body.String())
	}
	recModOff := doAdmin(http.MethodPatch, "/api/gateway/models/vision", map[string]any{"enabled": false})
	if recModOff.Code != http.StatusOK || !strings.Contains(recModOff.Body.String(), `"enabled":false`) {
		t.Fatalf("PATCH /api/gateway/models/vision failed: %d %s", recModOff.Code, recModOff.Body.String())
	}
	recGwSt := doAdmin(http.MethodGet, "/api/gateway/status", nil)
	if recGwSt.Code != http.StatusOK || !strings.Contains(recGwSt.Body.String(), `"standard":true`) || !strings.Contains(recGwSt.Body.String(), `"vision":false`) {
		t.Fatalf("GET /api/gateway/status missing modelEnabled overrides: %d %s", recGwSt.Code, recGwSt.Body.String())
	}
}
