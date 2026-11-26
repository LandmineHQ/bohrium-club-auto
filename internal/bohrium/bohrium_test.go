package bohrium

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/store"
)

func makeTestJWT(uid, orgID int64, exp time.Time) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"identity": map[string]any{
			"userId":   uid,
			"orgId":    orgID,
			"userType": 1,
		},
		"exp": exp.Unix(),
	})
	pl := base64.RawURLEncoding.EncodeToString(payload)
	return hdr + "." + pl + ".sigplaceholder"
}

func TestParseJWTAndCleanToken(t *testing.T) {
	exp := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	tok := makeTestJWT(123456, 7890, exp)
	cleaned := CleanToken("foo=bar; brmToken=" + tok + "; theme=dark")
	if cleaned != tok {
		t.Fatalf("CleanToken mismatch: got %q", cleaned)
	}
	claims, err := ParseJWTClaims(cleaned)
	if err != nil {
		t.Fatalf("ParseJWTClaims: %v", err)
	}
	if claims.UserID != 123456 || claims.OrgID != 7890 || claims.ExpiresAt.Unix() != exp.Unix() {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestEncryptPlatformPasswordRoundTrip(t *testing.T) {
	enc, err := EncryptPlatformPassword("MySecret#2026")
	if err != nil {
		t.Fatalf("EncryptPlatformPassword: %v", err)
	}
	// Double-base64 decode and AES-128-CBC decrypt to verify exact match with platform.bohrium.com
	firstB64, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("outer base64: %v", err)
	}
	rawCipher, err := base64.StdEncoding.DecodeString(string(firstB64))
	if err != nil {
		t.Fatalf("inner base64: %v", err)
	}
	block, _ := aes.NewCipher([]byte(platformAESKey))
	plain := make([]byte, len(rawCipher))
	cipher.NewCBCDecrypter(block, []byte(platformAESKey)).CryptBlocks(plain, rawCipher)
	pad := int(plain[len(plain)-1])
	got := string(plain[:len(plain)-pad])
	if got != "MySecret#2026" {
		t.Fatalf("roundtrip mismatch: got %q", got)
	}
}

func TestRunAllTasksIncludingDesktopClientAndImmediateCancel(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "test-pw-1234")
	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_, _ = cfgMgr.Update(func(c *config.Config) error {
		c.TaskDelaySec = 0
		c.CancelAfterMs = 15
		return nil
	})
	// Place a valid local PDF so test runs offline without touching arXiv
	samplePDF := buildMinimalScientificPDF(struct {
		ID              string   `json:"id"`
		Title           string   `json:"title"`
		Authors         []string `json:"authors"`
		Category        string   `json:"category"`
		Published       string   `json:"published"`
		PDFURL          string   `json:"pdfUrl"`
		MirrorPDFURL    string   `json:"mirrorPdfUrl"`
		Filename        string   `json:"filename"`
		Pages           int      `json:"pages"`
		AbstractSnippet string   `json:"abstractSnippet"`
	}{ID: "2611.00001", Title: "Test Paper", Category: "cs.AI", Filename: "test.pdf", AbstractSnippet: "A test abstract."})
	if err := os.WriteFile(filepath.Join(cfgMgr.Get().LocalPDFDir, "test_paper.pdf"), samplePDF, 0o644); err != nil {
		t.Fatalf("write sample pdf: %v", err)
	}

	st, err := store.Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tok := makeTestJWT(4242, 99, time.Now().Add(72*time.Hour))
	_ = st.SetAuth(store.AuthState{Token: tok, UserID: 4242, OrgID: 99, ExpiresAt: time.Now().Add(72 * time.Hour)})

	var (
		mu                 sync.Mutex
		claimedAll         bool
		chatCalls          int
		cancelCalls        int
		binaryUploaded     bool
		reportedKeys       = map[string]bool{}
		deprecatedHit      bool
		webNonChromeUA     bool
		desktopBrowserLeak bool
		desktopPreflight   bool
		desktopLLMCalls    int
		desktopToolCount   int
		desktopModel       string
		desktopSystemOK    bool
		desktopExecOK      bool
		desktopTraceMatch  bool
		preflightTrace     string
	)

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		isSciXLLM := r.URL.Path == "/models" || r.URL.Path == "/billing/preflight" || r.URL.Path == "/chat/completions"
		if isSciXLLM {
			// Desktop SciX LLM requests MUST NOT leak Chrome UA or Sec-CH-UA headers and MUST carry x-scix-runtime=scix_local
			if r.Header.Get("User-Agent") != "" || r.Header.Get("Sec-Ch-Ua") != "" || r.Header.Get(SciXRuntimeHeader) != SciXRuntimeValue {
				desktopBrowserLeak = true
			}
		} else {
			if !strings.Contains(r.Header.Get("User-Agent"), "Chrome/") {
				webNonChromeUA = true
			}
		}
		if strings.Contains(r.URL.Path, "integral_receive") {
			deprecatedHit = true
			mu.Unlock()
			w.WriteHeader(http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			mu.Unlock()
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"dspro-high","name":"DeepSeek-V4-Pro","api":"chat_completions","reasoning_effort":"high","reasoning_efforts":["low","high","max"],"presentation":{"visible":true,"default":true}}]}`))
			return
		case "/billing/preflight":
			desktopPreflight = true
			preflightTrace = r.Header.Get("traceparent")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"allowed":true}`))
			return
		case "/chat/completions":
			desktopLLMCalls++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			desktopModel, _ = body["model"].(string)
			if tools, ok := body["tools"].([]any); ok {
				desktopToolCount = len(tools)
			}
			if msgs, ok := body["messages"].([]any); ok && len(msgs) >= 2 {
				if sys, ok := msgs[0].(map[string]any); ok {
					content, _ := sys["content"].(string)
					if strings.HasPrefix(content, "You are SciMaster,") && strings.Contains(content, "<environment_context>") {
						desktopSystemOK = true
					}
				}
			}
			if rawExec := r.Header.Get(SciXExecutionHeader); rawExec != "" {
				if dec, err := base64.RawURLEncoding.DecodeString(rawExec); err == nil {
					var ep SciXExecutionPayload
					if json.Unmarshal(dec, &ep) == nil && ep.Version == 1 && ep.Scenario == "local_agent" && ep.Purpose == "agent" && ep.StoreID == st.StoreID() {
						desktopExecOK = true
					}
				}
			}
			if preflightTrace != "" && r.Header.Get("traceparent") == preflightTrace {
				desktopTraceMatch = true
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(": heartbeat\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Wait until client cancels the SSE stream immediately
			<-r.Context().Done()
			return
		case "/api/plaza/tasks":
			if r.Header.Get(PlazaGatewayHeader) != PlazaGatewayProd {
				mu.Unlock()
				w.WriteHeader(http.StatusForbidden)
				return
			}
			stVal := -1
			if claimedAll {
				stVal = 0
			}
			if len(reportedKeys) == 4 {
				stVal = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"taskDate": "2026-11-26",
					"list": []map[string]any{
						{"id": 1, "taskKey": "bohrium_science_space_web", "titleZh": "体验玻尔科学空间「Web端」", "rewardAmount": 1000, "status": stVal},
						{"id": 2, "taskKey": "bohrium_science_space_client", "titleZh": "体验玻尔科学空间「客户端」", "rewardAmount": 1000, "status": stVal},
						{"id": 3, "taskKey": "bohrium_literature_reading", "titleZh": "体验玻尔「文献阅读」", "rewardAmount": 1000, "status": stVal},
						{"id": 4, "taskKey": "bohrium_academic_writing", "titleZh": "体验玻尔「学术写作」", "rewardAmount": 1000, "status": stVal},
					},
				},
			})
		case "/api/plaza/tasks/claim":
			claimedAll = true
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/bohrapi/v1/literature-sage/api/v1/folder/create":
			_, _ = w.Write([]byte(`{"code":0,"data":{"folderId":101}}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/multipart":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"fileExist": false,
					"host":      srvURL,
					"token":     "upload-tok",
					"path":      "/reader/test_paper.pdf",
				},
			})
		case "/api/upload/binary":
			if r.Header.Get("X-Storage-Param") != "" {
				binaryUploaded = true
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/submit":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userResourceId":"res-888"}}`))
		case "/api/v1/scix/reader/entries":
			_, _ = w.Write([]byte(`{"code":0,"data":{"results":[{"index":0,"code":0,"entry":{"id":"entry-777","preparation":{"status":"ready"}}}]}}`))
		case "/api/v1/scix/reader/entries/entry-777/document":
			if r.Method == http.MethodPost {
				_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"ready"}}`))
				mu.Unlock()
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"upgrade_required"}}`))
		case "/api/v1/chat":
			chatCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workflowId": "wf-" + string(rune('0'+chatCalls)),
				"sessionId":  "sess-1",
				"queryId":    "q-" + string(rune('0'+chatCalls)),
			})
		case "/api/v1/query-events":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if tk, ok := body["task_key"].(string); ok {
				reportedKeys[tk] = true
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			if strings.HasPrefix(r.URL.Path, "/api/v1/chat/") && strings.HasSuffix(r.URL.Path, "/cancel") {
				cancelCalls++
				_, _ = w.Write([]byte(`{"durationMs":120}`))
				mu.Unlock()
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
		mu.Unlock()
	}))
	defer srv.Close()
	srvURL = srv.URL

	httpCli, _ := chromehttp.New(chromehttp.Options{})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{
		ClubBase:     srv.URL,
		PlatformBase: srv.URL,
		WWWBase:      srv.URL,
		SBCloudBase:  srv.URL,
		AgentBase:    srv.URL,
		SigmaLLMBase: srv.URL,
	})

	res, err := cli.RunAllWebTasks(context.Background(), "manual", true)
	if err != nil {
		t.Fatalf("RunAllWebTasks: %v", err)
	}

	if deprecatedHit {
		t.Fatalf("deprecated /integral_receive endpoint MUST NOT be called")
	}
	if webNonChromeUA {
		t.Fatalf("every Web task outgoing request must use Chrome User-Agent")
	}
	if desktopBrowserLeak {
		t.Fatalf("desktop client SciX LLM requests must use native transport (x-scix-runtime=scix_local, no browser UA/Sec-CH-UA)")
	}
	if !claimedAll {
		t.Fatalf("expected claim-all to run")
	}
	if chatCalls != 3 || cancelCalls != 3 {
		t.Fatalf("expected 3 Web chat calls and 3 immediate cancels, got chat=%d cancel=%d", chatCalls, cancelCalls)
	}
	if !desktopPreflight || desktopLLMCalls != 1 || desktopModel != "dspro-high" || desktopToolCount != 16 || !desktopSystemOK || !desktopExecOK || !desktopTraceMatch {
		t.Fatalf("desktop client task protocol mismatch: preflight=%v calls=%d model=%q tools=%d sys=%v exec=%v trace=%v",
			desktopPreflight, desktopLLMCalls, desktopModel, desktopToolCount, desktopSystemOK, desktopExecOK, desktopTraceMatch)
	}
	if !binaryUploaded {
		t.Fatalf("expected PDF binary upload with X-Storage-Param for Task 3")
	}
	for _, k := range []string{TaskKeyScienceSpaceWeb, TaskKeyScienceSpaceClient, TaskKeyLiteratureReading, TaskKeyAcademicWriting} {
		if !reportedKeys[k] {
			t.Fatalf("expected query-events report for %s, got %+v", k, reportedKeys)
		}
	}
	if !res.AllWebDone {
		t.Fatalf("expected AllWebDone=true (all 4 tasks complete), got %+v", res)
	}
}

func TestGetPointsAndCapacity(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })
	_ = st.SetAuth(store.AuthState{Token: makeTestJWT(1, 2, time.Now().Add(time.Hour))})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bohrapi/v1/account/user/integral":
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":2560,"orgBalance":500,"orgBalanceLimit":1000}}`))
		case "/brm/v1/wallet/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"balance":88.5}}`))
		case "/platform-gateway/v1/benefits/quota/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"extraExpire7DayAmount":300}}`))
		case "/platform-gateway/v1/benefits/quota/expire_page":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"remaining":300,"expireTime":"2026-12-01"}]}}`))
		case "/bohrapi/v1/account/user/integral_page":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":9,"changeNum":1000,"eventType":1,"expireAt":"2026-12-25","remark":"玻尔社区每日任务","createTime":"2026-11-25T19:03:53+08:00"},{"id":10,"changeNum":50,"eventType":2,"remark":"科学空间提问消费","createTime":"2026-11-25T18:50:00+08:00"}]}}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/capacity":
			_, _ = w.Write([]byte(`{"code":0,"data":{"remainingCapacity":8589934592,"usedCapacity":2147483648,"totalCapacity":10737418240}}`))
		case "/platform-gateway/v1/membership/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"level":2,"expireDate":"2027-01-01","photonNumber":1000}}`))
		case "/platform-gateway/v1/benefits/seat/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"application":{"sn":{"paper":{"remaining":45,"total":50},"deep_research":{"remaining":8,"total":10}},"knowledge_base":{"chat":{"remaining":90,"total":100}}}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	httpCli, _ := chromehttp.New(chromehttp.Options{})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{WWWBase: srv.URL})

	assets, err := cli.GetPointsAndCapacity(context.Background())
	if err != nil {
		t.Fatalf("GetPointsAndCapacity: %v", err)
	}
	if assets.PhotonBalance != 2560 || assets.WalletBalance != 88.5 || assets.Expiring7DayPhotons != 300 {
		t.Fatalf("unexpected balances: %+v", assets)
	}
	if assets.Storage.UsedHuman != "2.00 GB" || assets.Storage.TotalHuman != "10.00 GB" {
		t.Fatalf("unexpected storage formatting: %+v", assets.Storage)
	}
	if assets.PaperQuota.Remaining == nil || *assets.PaperQuota.Remaining != 45 {
		t.Fatalf("unexpected paper quota: %+v", assets.PaperQuota)
	}
	if len(assets.RecentIntegralHistory) != 2 || assets.RecentIntegralHistory[0].Amount != 1000 || assets.RecentIntegralHistory[1].Amount != -50 {
		t.Fatalf("unexpected integral ledger parsing: %+v", assets.RecentIntegralHistory)
	}
}

func TestOAuthURLAndVerifyCallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })
	tok := makeTestJWT(777, 888, time.Now().Add(24*time.Hour))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/account/oauth_url":
			if r.URL.Query().Get("type") != "4" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"url":"https://accounts.google.com/o/oauth2/auth?client_id=abc&state=st1"}}`))
		case "/api/oauth/verify":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if asString(body["code"]) != "4/0A_test_code" || int(asInt64(body["type"])) != 4 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"token": tok}})
		case "/bohrapi/v1/account/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userId":777,"orgId":888,"name":"GoogleTester"}}`))
		case "/api/platform/session":
			_, _ = w.Write([]byte(`{"logged_in":true,"binding":{"nickname":"GoogleTester"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	httpCli, _ := chromehttp.New(chromehttp.Options{})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{ClubBase: srv.URL, PlatformBase: srv.URL, WWWBase: srv.URL})

	urlRes, err := cli.GetOAuthURL(context.Background(), "google")
	if err != nil || !strings.Contains(urlRes.AuthURL, "accounts.google.com") {
		t.Fatalf("GetOAuthURL(google): %v %+v", err, urlRes)
	}

	auth, err := cli.VerifyOAuthCallback(context.Background(), "google", "https://login.bohrium.com/?code=4%2F0A_test_code&state=st1")
	if err != nil {
		t.Fatalf("VerifyOAuthCallback: %v", err)
	}
	if auth.UserID != 777 || auth.LoginMethod != "oauth_google" {
		t.Fatalf("unexpected oauth auth state: %+v", auth)
	}
}

func TestAccountProfileNicknameParsingAndBackfill(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "test-pw-1234")
	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var (
		mu             sync.Mutex
		extendNick     string
		clubCustomNick any
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/bohrapi/v1/account/info", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		en := extendNick
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"userId":    4748512,
				"orgId":     4741448,
				"userName":  "bohr68859c",
				"avatarUrl": "https://cdn.example/avatar.png",
				"email":     "researcher@example.test",
				"extend": map[string]any{
					"nickName": en,
				},
			},
		})
	})
	mux.HandleFunc("/api/platform/session", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cn := clubCustomNick
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"logged_in": true,
			"binding": map[string]any{
				"name":              "bohr68859c",
				"nickname":          cn,
				"platform_uid":      "4748512",
				"platform_username": "bohr68859c",
			},
			"profile": map[string]any{
				"name":     "bohr68859c",
				"username": "bohr68859c",
				"email":    "researcher@example.test",
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	httpCli, err := chromehttp.New(chromehttp.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("chromehttp.New: %v", err)
	}
	mgr := NewManager(httpCli, cfgMgr, st)
	mgr.SetEndpoints(Endpoints{
		ClubBase: ts.URL,
		WWWBase:  ts.URL,
	})

	// 1. Existing migrated account with empty nickname gets backfilled by Manager.SyncMissingProfiles
	tok := makeTestJWT(4748512, 4741448, time.Now().Add(48*time.Hour))
	acc, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token:       tok,
		UserID:      4748512,
		OrgID:       4741448,
		ExpiresAt:   time.Now().Add(48 * time.Hour),
		LoginMethod: "manual_token",
	}, "")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth: %v", err)
	}
	if acc.Nickname != "" {
		t.Fatalf("expected empty initial nickname, got %q", acc.Nickname)
	}
	if synced := mgr.SyncMissingProfiles(context.Background()); synced != 1 {
		t.Fatalf("expected SyncMissingProfiles to update 1 account, got %d", synced)
	}
	updated, ok, _ := st.GetAccount(acc.ID)
	if !ok || updated.Nickname != "bohr68859c" || updated.Avatar != "https://cdn.example/avatar.png" {
		t.Fatalf("unexpected synced account: nick=%q avatar=%q", updated.Nickname, updated.Avatar)
	}

	// 2. Club custom binding.nickname overrides default userName when extend.nickName is empty
	mu.Lock()
	clubCustomNick = "科研小助手"
	mu.Unlock()
	if _, changed, err := mgr.ForAccount(acc.ID).SyncProfile(context.Background()); err != nil || !changed {
		t.Fatalf("SyncProfile with club custom nick failed: changed=%v err=%v", changed, err)
	}
	updated, _, _ = st.GetAccount(acc.ID)
	if updated.Nickname != "科研小助手" {
		t.Fatalf("expected club custom nickname, got %q", updated.Nickname)
	}

	// 3. Bohrium extend.nickName takes top priority over userName and club binding.nickname
	mu.Lock()
	extendNick = "玻尔实验员"
	mu.Unlock()
	if _, changed, err := mgr.ForAccount(acc.ID).SyncProfile(context.Background()); err != nil || !changed {
		t.Fatalf("SyncProfile with extend.nickName failed: changed=%v err=%v", changed, err)
	}
	updated, _, _ = st.GetAccount(acc.ID)
	if updated.Nickname != "玻尔实验员" {
		t.Fatalf("expected extend.nickName priority, got %q", updated.Nickname)
	}
}

func TestWaitForReaderDocumentReadyStatusDrivenPolling(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })
	_ = st.SetAuth(store.AuthState{Token: makeTestJWT(1, 2, time.Now().Add(time.Hour))})

	// Case 1: upgrade_required -> preparing -> transient 502 -> processing -> ready (no timeout)
	var (
		mu         sync.Mutex
		postCalls  int
		getCalls   int
		statusSeq  = []string{"preparing", "502", "processing", "ready"}
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/api/v1/scix/reader/entries/entry-1/document" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost {
			postCalls++
			if postCalls == 1 {
				_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"upgrade_required"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"preparing"}}`))
			return
		}
		idx := getCalls
		getCalls++
		if idx >= len(statusSeq) {
			idx = len(statusSeq) - 1
		}
		next := statusSeq[idx]
		if next == "502" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`upstream blip`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"` + next + `"}}`))
	}))
	defer ts.Close()

	httpCli, _ := chromehttp.New(chromehttp.Options{Timeout: 5 * time.Second})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{SBCloudBase: ts.URL, WWWBase: ts.URL})
	cli.readerPollInterval = 5 * time.Millisecond

	if err := cli.waitForReaderDocumentReady(context.Background(), "entry-1", "rev-1"); err != nil {
		t.Fatalf("expected status polling to recover and succeed, got %v", err)
	}
	if postCalls != 2 || getCalls != 4 {
		t.Fatalf("expected 2 POST + 4 GET status polls, got post=%d get=%d", postCalls, getCalls)
	}

	// Case 2: upstream reports terminal failed status -> abort immediately with detail
	tsFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"failed","error":"corrupted pdf"}}`))
	}))
	defer tsFail.Close()
	cli.SetEndpoints(Endpoints{SBCloudBase: tsFail.URL, WWWBase: tsFail.URL})
	err := cli.waitForReaderDocumentReady(context.Background(), "entry-bad", "")
	if err == nil || !strings.Contains(err.Error(), "corrupted pdf") {
		t.Fatalf("expected terminal failure error with detail, got %v", err)
	}

	// Case 3: caller context cancellation exits cleanly while status stays preparing
	tsPrep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"preparing"}}`))
	}))
	defer tsPrep.Close()
	cli.SetEndpoints(Endpoints{SBCloudBase: tsPrep.URL, WWWBase: tsPrep.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	err = cli.waitForReaderDocumentReady(ctx, "entry-slow", "")
	if err == nil || err != context.DeadlineExceeded {
		t.Fatalf("expected caller context cancellation, got %v", err)
	}
}

func TestUploadReaderPDFRetryAndMD5Dedup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })
	_ = st.SetAuth(store.AuthState{Token: makeTestJWT(1, 2, time.Now().Add(time.Hour))})

	// Case 1: First upload hits transient 502; retry re-queries file/multipart (refreshing token) and succeeds.
	var (
		mu             sync.Mutex
		multipartCalls int
		uploadCalls    int
		uploadTokens   []string
		srvURL         string
	)
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/bohrapi/v1/literature-sage/api/v1/folder/create":
			_, _ = w.Write([]byte(`{"code":0,"data":{"folderId":10}}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/multipart":
			multipartCalls++
			tok := "tok-attempt-" + string(rune('0'+multipartCalls))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"fileExist": false,
					"host":      srvURL,
					"token":     tok,
					"path":      "reader/test.pdf",
				},
			})
		case "/api/upload/binary":
			uploadCalls++
			uploadTokens = append(uploadTokens, r.Header.Get("Authorization"))
			if uploadCalls == 1 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("tiefblue transient 502"))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/submit":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userResourceId":"res-retry-ok"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts1.Close()
	srvURL = ts1.URL

	httpCli, _ := chromehttp.New(chromehttp.Options{Timeout: 5 * time.Second})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{WWWBase: ts1.URL})
	cli.readerPollInterval = 5 * time.Millisecond

	resID, err := cli.uploadReaderPDF(context.Background(), "paper.pdf", []byte("%PDF-1.4 sample payload"))
	if err != nil {
		t.Fatalf("expected uploadReaderPDF to recover on retry, got %v", err)
	}
	if resID != "res-retry-ok" || multipartCalls != 2 || uploadCalls != 2 {
		t.Fatalf("expected 2 prechecks + 2 uploads -> res-retry-ok, got id=%q mp=%d up=%d", resID, multipartCalls, uploadCalls)
	}
	if len(uploadTokens) != 2 || uploadTokens[0] != "Bearer tok-attempt-1" || uploadTokens[1] != "Bearer tok-attempt-2" {
		t.Fatalf("expected refreshed upload token on retry, got %+v", uploadTokens)
	}

	// Case 2: First upload returns 503 after data already landed; retry precheck sees fileExist=true and skips re-upload.
	var (
		mp2   int
		up2   int
		url2  string
	)
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/bohrapi/v1/literature-sage/api/v1/folder/create":
			_, _ = w.Write([]byte(`{"code":0,"data":{"folderId":10}}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/multipart":
			mp2++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"fileExist": mp2 >= 2,
					"host":      url2,
					"token":     "tok-dedup",
					"path":      "reader/dedup.pdf",
				},
			})
		case "/api/upload/binary":
			up2++
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("tiefblue gateway timeout"))
		case "/bohrapi/v1/literature-sage/api/v1/file/submit":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userResourceId":"res-dedup-ok"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts2.Close()
	url2 = ts2.URL
	cli.SetEndpoints(Endpoints{WWWBase: ts2.URL})

	resID2, err := cli.uploadReaderPDF(context.Background(), "dedup.pdf", []byte("%PDF-1.4 dedup payload"))
	if err != nil {
		t.Fatalf("expected MD5 dedup recovery on retry, got %v", err)
	}
	if resID2 != "res-dedup-ok" || mp2 != 2 || up2 != 1 {
		t.Fatalf("expected 2 prechecks + 1 upload (skipped via fileExist=true), got id=%q mp=%d up=%d", resID2, mp2, up2)
	}

	// Case 3: Non-retryable 403 fails fast on first attempt.
	var up3 int
	var url3 string
	ts3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/bohrapi/v1/literature-sage/api/v1/folder/create":
			_, _ = w.Write([]byte(`{"code":0,"data":{"folderId":10}}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/multipart":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"fileExist": false,
					"host":      url3,
					"token":     "tok-403",
					"path":      "reader/forbidden.pdf",
				},
			})
		case "/api/upload/binary":
			up3++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("forbidden"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts3.Close()
	url3 = ts3.URL
	cli.SetEndpoints(Endpoints{WWWBase: ts3.URL})

	if _, err := cli.uploadReaderPDF(context.Background(), "forbidden.pdf", []byte("%PDF-1.4 forbidden")); err == nil {
		t.Fatalf("expected 403 upload to fail")
	}
	if up3 != 1 {
		t.Fatalf("expected 403 to fail fast with 1 attempt, got %d", up3)
	}
}

func TestPrepareCaptchaAndLoginWithProof(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })
	tok := makeTestJWT(901, 902, time.Now().Add(24*time.Hour))

	var (
		mu              sync.Mutex
		skipUser        bool
		lastSendPayload map[string]any
		lastSendLang    string
		lastPwPayload   map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/captcha/check_user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"skip": skipUser},
			})
		case "/api/captcha/encrypt_appid":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if int64(asInt64(b["captchaAppId"])) != DefaultTencentCaptchaAppID {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"signAppIdStr":"enc-aid-xyz"}}`))
		case "/api/account/code/send_by_phone_v3", "/api/account/code/send_by_email_v3":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			lastSendPayload = b
			lastSendLang = r.Header.Get("Content-Language")
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/api/account/v3/login":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			lastPwPayload = b
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"token": tok},
			})
		case "/bohrapi/v1/account/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userId":901,"orgId":902,"name":"CaptchaUser"}}`))
		case "/api/platform/session":
			_, _ = w.Write([]byte(`{"logged_in":true,"binding":{"nickname":"CaptchaUser"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	httpCli, _ := chromehttp.New(chromehttp.Options{})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{ClubBase: srv.URL, PlatformBase: srv.URL, WWWBase: srv.URL})

	// 1. PrepareCaptcha for password when account is NOT exempt -> returns encrypted AppID
	prep, err := cli.PrepareCaptcha(context.Background(), "password", "user@example.com")
	if err != nil {
		t.Fatalf("PrepareCaptcha(password, !skip): %v", err)
	}
	if prep.Skip || prep.CaptchaAppID != "194611140" || prep.AidEncrypted != "enc-aid-xyz" {
		t.Fatalf("unexpected PrepareCaptcha result: %+v", prep)
	}

	// 2. PrepareCaptcha for password when account IS exempt -> skip=true
	mu.Lock()
	skipUser = true
	mu.Unlock()
	prepSkip, err := cli.PrepareCaptcha(context.Background(), "password", "exempt@example.com")
	if err != nil || !prepSkip.Skip {
		t.Fatalf("expected skip=true for exempt account, got %+v err=%v", prepSkip, err)
	}

	// 3. SendVerifyCode forwards user-completed Tencent Captcha proof, Content-Language, channel="pc"
	proof := &CaptchaProof{Ticket: "t03-user-ticket", RandStr: "@ab12", CaptchaAppID: DefaultTencentCaptchaAppID}
	if err := cli.SendVerifyCode(context.Background(), "email", "user@example.org", "", true, proof); err != nil {
		t.Fatalf("SendVerifyCode(email): %v", err)
	}
	mu.Lock()
	gotSend := lastSendPayload
	gotLang := lastSendLang
	mu.Unlock()
	if asString(gotSend["ticket"]) != "t03-user-ticket" || asString(gotSend["randStr"]) != "@ab12" || asInt64(gotSend["captchaAppId"]) != DefaultTencentCaptchaAppID || asString(gotSend["channel"]) != "pc" || !asBool(gotSend["isOversea"]) || gotLang != "en-us" {
		t.Fatalf("SendVerifyCode did not forward expected fields: payload=%+v lang=%q", gotSend, gotLang)
	}

	// 4. SendVerifyCode MUST detect Aliyun WAF HTML 200 responses and return a clean user-facing error (no raw HTML leak)
	tsBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<textarea id="renderData" style="display:none">{}</textarea><textarea id="aliyun_waf_fa9faf9f45" style="display:none">{}</textarea>`))
	}))
	defer tsBad.Close()
	cli.SetEndpoints(Endpoints{ClubBase: srv.URL, PlatformBase: tsBad.URL, WWWBase: srv.URL})
	wafErr := cli.SendVerifyCode(context.Background(), "email", "user@example.org", "", false, proof)
	if wafErr == nil || !strings.Contains(wafErr.Error(), "阿里云 WAF") || strings.Contains(wafErr.Error(), "<textarea") {
		t.Fatalf("expected clean Aliyun WAF error without raw HTML, got %v", wafErr)
	}
	cli.SetEndpoints(Endpoints{ClubBase: srv.URL, PlatformBase: srv.URL, WWWBase: srv.URL})

	// 4. LoginWithPassword with proof bypasses check_user rejection and forwards proof to /v3/login
	mu.Lock()
	skipUser = false
	mu.Unlock()
	if _, err := cli.LoginWithPassword(context.Background(), "user@example.com", "secret123", nil); err == nil {
		t.Fatalf("expected password login without proof to fail when check_user skip=false")
	}
	auth, err := cli.LoginWithPassword(context.Background(), "user@example.com", "secret123", proof)
	if err != nil {
		t.Fatalf("LoginWithPassword with proof: %v", err)
	}
	if auth.UserID != 901 || auth.Nickname != "CaptchaUser" {
		t.Fatalf("unexpected auth state: %+v", auth)
	}
	mu.Lock()
	gotPw := lastPwPayload
	mu.Unlock()
	if asString(gotPw["ticket"]) != "t03-user-ticket" || asString(gotPw["randStr"]) != "@ab12" || asInt64(gotPw["captchaAppId"]) != DefaultTencentCaptchaAppID {
		t.Fatalf("LoginWithPassword did not forward captcha proof: %+v", gotPw)
	}
}
