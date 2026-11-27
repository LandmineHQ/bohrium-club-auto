package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/scheduler"
	"bohrium-club-auto/internal/store"
)

func TestMain(m *testing.M) {
	if defTr, ok := http.DefaultTransport.(*http.Transport); ok {
		origDial := defTr.DialContext
		if origDial == nil {
			d := &net.Dialer{Timeout: 5 * time.Second}
			origDial = d.DialContext
		}
		defTr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			host = strings.Trim(strings.TrimSpace(host), "[]")
			ip := net.ParseIP(host)
			if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
				return nil, fmt.Errorf("offline test guard blocked non-loopback network dial to %s", addr)
			}
			return origDial(ctx, network, addr)
		}
	}
	os.Exit(m.Run())
}

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

func TestAsyncManualTaskExecutionAndPolling(t *testing.T) {
	var (
		mu        sync.Mutex
		chatCount int
	)
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/plaza/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"taskDate": "2026-11-26",
					"list": []map[string]any{
						{"id": 1, "taskKey": "bohrium_science_space_web", "titleZh": "体验玻尔科学空间「Web端」", "rewardAmount": 1000, "status": 0},
					},
				},
			})
		case r.URL.Path == "/api/v1/chat":
			// Simulate slow execution (~120ms) after the caller's HTTP POST context would have been canceled.
			time.Sleep(120 * time.Millisecond)
			mu.Lock()
			chatCount++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"workflowId":"wf-async-1","sessionId":"sess-async-1","queryId":"q-async-1"}`))
		case r.URL.Path == "/api/v1/query-events":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/chat/") && strings.HasSuffix(r.URL.Path, "/cancel"):
			_, _ = w.Write([]byte(`{"durationMs":120}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	t.Cleanup(upSrv.Close)

	srv, st, pw := setupTestServer(t)
	srv.mgr.SetEndpoints(bohrium.Endpoints{
		ClubBase:     upSrv.URL,
		PlatformBase: upSrv.URL,
		WWWBase:      upSrv.URL,
		SBCloudBase:  upSrv.URL,
		AgentBase:    upSrv.URL,
		SigmaLLMBase: upSrv.URL,
	})
	acc, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token: "tok-async-1", UserID: 9101, OrgID: 9102, Nickname: "AsyncTester", ExpiresAt: time.Now().Add(24 * time.Hour),
	}, "Async")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth: %v", err)
	}

	// Ensure GET /api/overview always serializes activeJobs as [] (never null) when idle.
	ovReq := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	ovReq.Header.Set("X-Admin-Password", pw)
	ovRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ovRec, ovReq)
	if ovRec.Code != http.StatusOK || !strings.Contains(ovRec.Body.String(), `"activeJobs":[]`) || strings.Contains(ovRec.Body.String(), `"activeJobs":null`) {
		t.Fatalf("GET /api/overview expected non-null activeJobs [], got %d %s", ovRec.Code, ovRec.Body.String())
	}

	// Trigger single manual task with a short HTTP request context that is canceled immediately after 202 returns.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	bodyBytes, _ := json.Marshal(map[string]string{"taskKey": "bohrium_science_space_web"})
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/"+acc.ID+"/tasks/run", bytes.NewReader(bodyBytes)).WithContext(reqCtx)
	req.Header.Set("X-Admin-Password", pw)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	cancelReq() // Simulate CDN/browser closing the POST request immediately after receiving 202.

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for async manual task, got %d: %s", rec.Code, rec.Body.String())
	}
	var startRes struct {
		OK    bool              `json:"ok"`
		Async bool              `json:"async"`
		Job   scheduler.TaskJob `json:"job"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &startRes); err != nil || !startRes.Async || startRes.Job.ID == "" {
		t.Fatalf("unexpected async start payload: err=%v body=%s", err, rec.Body.String())
	}

	// Second concurrent start for the same account while running MUST return 409 Conflict.
	reqConflict := httptest.NewRequest(http.MethodPost, "/api/accounts/"+acc.ID+"/tasks/run", bytes.NewReader(bodyBytes))
	reqConflict.Header.Set("X-Admin-Password", pw)
	reqConflict.Header.Set("Content-Type", "application/json")
	recConflict := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recConflict, reqConflict)
	if recConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict while task running, got %d: %s", recConflict.Code, recConflict.Body.String())
	}

	// Poll GET /api/tasks/jobs/{id} until completed despite reqCtx cancellation.
	var finalJob scheduler.TaskJob
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pollReq := httptest.NewRequest(http.MethodGet, "/api/tasks/jobs/"+startRes.Job.ID, nil)
		pollReq.Header.Set("X-Admin-Password", pw)
		pollRec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(pollRec, pollReq)
		if pollRec.Code != http.StatusOK {
			t.Fatalf("GET /api/tasks/jobs/{id} failed: %d %s", pollRec.Code, pollRec.Body.String())
		}
		var pollRes struct {
			OK  bool              `json:"ok"`
			Job scheduler.TaskJob `json:"job"`
		}
		_ = json.Unmarshal(pollRec.Body.Bytes(), &pollRes)
		if pollRes.Job.Status == "completed" || pollRes.Job.Status == "failed" {
			finalJob = pollRes.Job
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if finalJob.Status != "completed" || finalJob.SingleRes == nil || !finalJob.SingleRes.Success || finalJob.SingleRes.WorkflowID != "wf-async-1" {
		t.Fatalf("expected background job to succeed after caller disconnect, got %+v", finalJob)
	}

	// GET /api/tasks/jobs lists the job.
	listReq := httptest.NewRequest(http.MethodGet, "/api/tasks/jobs", nil)
	listReq.Header.Set("X-Admin-Password", pw)
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), startRes.Job.ID) {
		t.Fatalf("GET /api/tasks/jobs missing job: %d %s", listRec.Code, listRec.Body.String())
	}

	// After job completes, GET /api/overview still returns activeJobs as [] (never null).
	ovDoneReq := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	ovDoneReq.Header.Set("X-Admin-Password", pw)
	ovDoneRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ovDoneRec, ovDoneReq)
	if ovDoneRec.Code != http.StatusOK || !strings.Contains(ovDoneRec.Body.String(), `"activeJobs":[]`) || strings.Contains(ovDoneRec.Body.String(), `"activeJobs":null`) {
		t.Fatalf("GET /api/overview after completion expected activeJobs [], got %d %s", ovDoneRec.Code, ovDoneRec.Body.String())
	}
}

func TestClientLoginBenefitAdminEndpoints(t *testing.T) {
	var (
		mu           sync.Mutex
		receiveCalls int
		firstLogin   = true
		claimedOld   = false
	)
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bohrapi/v1/sigma-search/api/v1/scix/membership/client-activity":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"newUserBenefitWithinPeriod": true,
					"newUserBenefitFirstLogin":   firstLogin,
					"hasOldActivityLogin":        claimedOld,
				},
			})
		case "/platform-gateway/v1/membership/receive_photon":
			receiveCalls++
			firstLogin = false
			claimedOld = true
			_, _ = w.Write([]byte(`{"code":0,"data":{"claimed":true}}`))
		case "/bohrapi/v1/account/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userId":9201,"orgId":9202,"name":"BenefitAdmin"}}`))
		case "/api/platform/session":
			_, _ = w.Write([]byte(`{"logged_in":true,"binding":{"nickname":"BenefitAdmin"}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		}
	}))
	t.Cleanup(upSrv.Close)

	srv, st, pw := setupTestServer(t)
	srv.mgr.SetEndpoints(bohrium.Endpoints{
		ClubBase:            upSrv.URL,
		PlatformBase:        upSrv.URL,
		WWWBase:             upSrv.URL,
		SBCloudBase:         upSrv.URL,
		AgentBase:           upSrv.URL,
		SigmaLLMBase:        upSrv.URL,
		PlatformGatewayBase: upSrv.URL,
	})
	// Construct a personal-account JWT ("bohrVersion") since ClaimClientLoginPhotons checks token claims offline.
	hdrB64 := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
	payB64 := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":4102444800,"identity":{"userId":9201,"orgId":9202,"userType":"bohrVersion"}}`))
	personalTok := hdrB64 + "." + payB64 + ".sig"
	acc, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token: personalTok, UserID: 9201, OrgID: 9202, Nickname: "BenefitAdmin", ExpiresAt: time.Now().Add(24 * time.Hour),
	}, "BenefitAdmin")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth: %v", err)
	}

	// 1. Check status returns eligible=true
	stReq := httptest.NewRequest(http.MethodGet, "/api/accounts/"+acc.ID+"/benefits/client-login", nil)
	stReq.Header.Set("X-Admin-Password", pw)
	stRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stRec, stReq)
	if stRec.Code != http.StatusOK || !strings.Contains(stRec.Body.String(), `"eligible":true`) {
		t.Fatalf("GET client-login benefit status failed: %d %s", stRec.Code, stRec.Body.String())
	}

	// 2. Claim calls receive_photon once
	clReq := httptest.NewRequest(http.MethodPost, "/api/accounts/"+acc.ID+"/benefits/client-login/claim", nil)
	clReq.Header.Set("X-Admin-Password", pw)
	clRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(clRec, clReq)
	if clRec.Code != http.StatusOK || !strings.Contains(clRec.Body.String(), `"claimed":true`) {
		t.Fatalf("POST client-login benefit claim failed: %d %s", clRec.Code, clRec.Body.String())
	}

	// 3. Second claim MUST skip without hitting receive_photon again
	cl2Req := httptest.NewRequest(http.MethodPost, "/api/accounts/"+acc.ID+"/benefits/client-login/claim", nil)
	cl2Req.Header.Set("X-Admin-Password", pw)
	cl2Rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(cl2Rec, cl2Req)
	if cl2Rec.Code != http.StatusOK || !strings.Contains(cl2Rec.Body.String(), `"skipped":true`) {
		t.Fatalf("second POST client-login benefit claim should skip: %d %s", cl2Rec.Code, cl2Rec.Body.String())
	}
	mu.Lock()
	gotCalls := receiveCalls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("expected receiveCalls=1, got %d", gotCalls)
	}
}
