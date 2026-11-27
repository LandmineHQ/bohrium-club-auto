package bohrium

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/prompts"
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

func checkExactKeys(label string, gotKeys, wantKeys []string) string {
	got := append([]string(nil), gotKeys...)
	want := append([]string(nil), wantKeys...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		return fmt.Sprintf("%s: key count mismatch: got %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Sprintf("%s: keys mismatch: got %v, want %v", label, got, want)
		}
	}
	return ""
}

func mapKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func queryKeys(q url.Values) []string {
	if len(q) == 0 {
		return nil
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	return keys
}

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
		schemaErrs         []string
	)
	addSchemaErr := func(msg string) {
		if msg != "" {
			schemaErrs = append(schemaErrs, msg)
		}
	}

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
			addSchemaErr(checkExactKeys("GET /models query", queryKeys(r.URL.Query()), []string{"tag"}))
			if r.URL.Query().Get("tag") != SciXModelCatalogTag {
				addSchemaErr(fmt.Sprintf("GET /models tag=%q want %q", r.URL.Query().Get("tag"), SciXModelCatalogTag))
			}
			mu.Unlock()
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"dspro-high","name":"DeepSeek-V4-Pro","api":"chat_completions","reasoning_effort":"high","reasoning_efforts":["low","high","max"],"presentation":{"visible":true,"default":true}}]}`))
			return
		case "/billing/preflight":
			desktopPreflight = true
			preflightTrace = r.Header.Get("traceparent")
			var pfBody map[string]any
			_ = json.NewDecoder(r.Body).Decode(&pfBody)
			addSchemaErr(checkExactKeys("POST /billing/preflight body", mapKeys(pfBody), []string{"model"}))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"allowed":true}`))
			return
		case "/chat/completions":
			desktopLLMCalls++
			addSchemaErr(checkExactKeys("POST /chat/completions query", queryKeys(r.URL.Query()), nil))
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			addSchemaErr(checkExactKeys("POST /chat/completions body", mapKeys(body), []string{
				"messages", "model", "reasoning_effort", "stream", "stream_options", "tools",
			}))
			addSchemaErr(checkExactKeys("POST /chat/completions stream_options", mapKeys(asMap(body["stream_options"])), []string{"include_usage"}))
			desktopModel, _ = body["model"].(string)
			if tools, ok := body["tools"].([]any); ok {
				desktopToolCount = len(tools)
			}
			if msgs, ok := body["messages"].([]any); ok && len(msgs) >= 2 {
				for idx, rawMsg := range msgs {
					addSchemaErr(checkExactKeys(fmt.Sprintf("POST /chat/completions messages[%d]", idx), mapKeys(asMap(rawMsg)), []string{"content", "role"}))
				}
				if sys, ok := msgs[0].(map[string]any); ok {
					content, _ := sys["content"].(string)
					if strings.HasPrefix(content, "You are SciMaster,") && strings.Contains(content, "<environment_context>") {
						desktopSystemOK = true
					}
				}
			}
			if rawExec := r.Header.Get(SciXExecutionHeader); rawExec != "" {
				if dec, err := base64.RawURLEncoding.DecodeString(rawExec); err == nil {
					var execMap map[string]any
					if json.Unmarshal(dec, &execMap) == nil {
						addSchemaErr(checkExactKeys("x-scix-execution", mapKeys(execMap), []string{
							"callId", "purpose", "question", "recoveryAttempt", "rootRunId", "rootSessionId",
							"runId", "scenario", "sessionCreatedAt", "sessionId", "storeId", "version",
						}))
					}
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
			addSchemaErr(checkExactKeys("GET /api/plaza/tasks query", queryKeys(r.URL.Query()), nil))
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
			var claimBody map[string]any
			_ = json.NewDecoder(r.Body).Decode(&claimBody)
			addSchemaErr(checkExactKeys("POST /api/plaza/tasks/claim body", mapKeys(claimBody), []string{"publisher"}))
			claimedAll = true
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/bohrapi/v1/literature-sage/api/v1/folder/create":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			addSchemaErr(checkExactKeys("folder/create body", mapKeys(b), []string{"folderName", "parentId", "scene"}))
			_, _ = w.Write([]byte(`{"code":0,"data":{"folderId":101}}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/multipart":
			addSchemaErr(checkExactKeys("file/multipart query", queryKeys(r.URL.Query()), []string{"fileName", "md5", "parentId", "scene", "size"}))
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
			if spRaw := r.Header.Get("X-Storage-Param"); spRaw != "" {
				binaryUploaded = true
				if dec, err := base64.StdEncoding.DecodeString(spRaw); err == nil {
					var spMap map[string]any
					if json.Unmarshal(dec, &spMap) == nil {
						addSchemaErr(checkExactKeys("X-Storage-Param", mapKeys(spMap), []string{"option", "path"}))
						addSchemaErr(checkExactKeys("X-Storage-Param.option", mapKeys(asMap(spMap["option"])), []string{"contentDisposition", "contentType"}))
					}
				}
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/bohrapi/v1/literature-sage/api/v1/file/submit":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			addSchemaErr(checkExactKeys("file/submit body", mapKeys(b), []string{"fileName", "md5", "mimeType", "parentId", "scene", "size", "url"}))
			_, _ = w.Write([]byte(`{"code":0,"data":{"userResourceId":"res-888"}}`))
		case "/api/v1/scix/reader/entries":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			addSchemaErr(checkExactKeys("reader/entries body", mapKeys(b), []string{"entries"}))
			if entries, ok := b["entries"].([]any); ok && len(entries) > 0 {
				e0 := asMap(entries[0])
				addSchemaErr(checkExactKeys("reader/entries[0]", mapKeys(e0), []string{"source"}))
				addSchemaErr(checkExactKeys("reader/entries[0].source", mapKeys(asMap(e0["source"])), []string{"kind", "userResourceId"}))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"results":[{"index":0,"code":0,"entry":{"id":"entry-777","preparation":{"status":"ready"}}}]}}`))
		case "/api/v1/scix/reader/entries/entry-777/document":
			if r.Method == http.MethodPost {
				var b map[string]any
				_ = json.NewDecoder(r.Body).Decode(&b)
				addSchemaErr(checkExactKeys("reader/document body", mapKeys(b), nil))
				_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"ready"}}`))
				mu.Unlock()
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"kind":"upgrade_required"}}`))
		case "/api/v1/chat":
			chatCalls++
			addSchemaErr(checkExactKeys("POST /api/v1/chat query", queryKeys(r.URL.Query()), []string{"historyCapabilities"}))
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if _, isReader := b["readerEntryId"]; isReader {
				addSchemaErr(checkExactKeys("POST /api/v1/chat(reader) body", mapKeys(b), []string{
					"clientTimeZone", "invocationRequestId", "queryId", "readerEntryId", "text",
				}))
			} else {
				addSchemaErr(checkExactKeys("POST /api/v1/chat(team) body", mapKeys(b), []string{
					"clientTimeZone", "invocationRequestId", "queryId", "target", "text",
				}))
				addSchemaErr(checkExactKeys("POST /api/v1/chat(team) target", mapKeys(asMap(b["target"])), []string{"kind", "teamId"}))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"workflowId": "wf-" + string(rune('0'+chatCalls)),
				"sessionId":  "sess-1",
				"queryId":    "q-" + string(rune('0'+chatCalls)),
			})
		case "/api/v1/query-events":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			addSchemaErr(checkExactKeys("POST /api/v1/query-events body", mapKeys(body), []string{
				"eventKey", "orgId", "queryId", "queryTime", "task_key", "userId",
			}))
			if tk, ok := body["task_key"].(string); ok {
				reportedKeys[tk] = true
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/bohrapi/v1/sigma-search/api/v1/scix/membership/client-activity":
			addSchemaErr(checkExactKeys("client-activity query", queryKeys(r.URL.Query()), []string{"channel"}))
			if r.URL.Query().Get("channel") != DesktopLoginChannel {
				addSchemaErr(fmt.Sprintf("client-activity channel=%q want %q", r.URL.Query().Get("channel"), DesktopLoginChannel))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"newUserBenefitWithinPeriod":false,"newUserBenefitFirstLogin":false,"hasOldActivityLogin":true}}`))
		default:
			if strings.HasPrefix(r.URL.Path, "/api/v1/chat/") && strings.HasSuffix(r.URL.Path, "/cancel") {
				cancelCalls++
				addSchemaErr(checkExactKeys("POST /api/v1/chat/*/cancel query", queryKeys(r.URL.Query()), nil))
				var b map[string]any
				_ = json.NewDecoder(r.Body).Decode(&b)
				addSchemaErr(checkExactKeys("POST /api/v1/chat/*/cancel body", mapKeys(b), []string{"source"}))
				_, _ = w.Write([]byte(`{"durationMs":120}`))
				mu.Unlock()
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
		mu.Unlock()
	}))
	defer srv.Close()
	mu.Lock()
	srvURL = srv.URL
	mu.Unlock()

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

	mu.Lock()
	gotDeprecatedHit := deprecatedHit
	gotWebNonChromeUA := webNonChromeUA
	gotDesktopBrowserLeak := desktopBrowserLeak
	gotClaimedAll := claimedAll
	gotChatCalls := chatCalls
	gotCancelCalls := cancelCalls
	gotDesktopPreflight := desktopPreflight
	gotDesktopLLMCalls := desktopLLMCalls
	gotDesktopModel := desktopModel
	gotDesktopToolCount := desktopToolCount
	gotDesktopSystemOK := desktopSystemOK
	gotDesktopExecOK := desktopExecOK
	gotDesktopTraceMatch := desktopTraceMatch
	gotBinaryUploaded := binaryUploaded
	gotSchemaErrs := append([]string(nil), schemaErrs...)
	gotReportedKeys := make(map[string]bool, len(reportedKeys))
	for k, v := range reportedKeys {
		gotReportedKeys[k] = v
	}
	mu.Unlock()

	if len(gotSchemaErrs) > 0 {
		t.Fatalf("strict request schema mismatches:\n%s", strings.Join(gotSchemaErrs, "\n"))
	}

	if gotDeprecatedHit {
		t.Fatalf("deprecated /integral_receive endpoint MUST NOT be called")
	}
	if gotWebNonChromeUA {
		t.Fatalf("every Web task outgoing request must use Chrome User-Agent")
	}
	if gotDesktopBrowserLeak {
		t.Fatalf("desktop client SciX LLM requests must use native transport (x-scix-runtime=scix_local, no browser UA/Sec-CH-UA)")
	}
	if !gotClaimedAll {
		t.Fatalf("expected claim-all to run")
	}
	if gotChatCalls != 3 || gotCancelCalls != 3 {
		t.Fatalf("expected 3 Web chat calls and 3 immediate cancels, got chat=%d cancel=%d", gotChatCalls, gotCancelCalls)
	}
	if !gotDesktopPreflight || gotDesktopLLMCalls != 1 || gotDesktopModel != "dspro-high" || gotDesktopToolCount != 16 || !gotDesktopSystemOK || !gotDesktopExecOK || !gotDesktopTraceMatch {
		t.Fatalf("desktop client task protocol mismatch: preflight=%v calls=%d model=%q tools=%d sys=%v exec=%v trace=%v",
			gotDesktopPreflight, gotDesktopLLMCalls, gotDesktopModel, gotDesktopToolCount, gotDesktopSystemOK, gotDesktopExecOK, gotDesktopTraceMatch)
	}
	if !gotBinaryUploaded {
		t.Fatalf("expected PDF binary upload with X-Storage-Param for Task 3")
	}
	for _, k := range []string{TaskKeyScienceSpaceWeb, TaskKeyScienceSpaceClient, TaskKeyLiteratureReading, TaskKeyAcademicWriting} {
		if !gotReportedKeys[k] {
			t.Fatalf("expected query-events report for %s, got %+v", k, gotReportedKeys)
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
	mu.Lock()
	gotPost, gotGet := postCalls, getCalls
	mu.Unlock()
	if gotPost != 2 || gotGet != 4 {
		t.Fatalf("expected 2 POST + 4 GET status polls, got post=%d get=%d", gotPost, gotGet)
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
	mu.Lock()
	srvURL = ts1.URL
	mu.Unlock()

	httpCli, _ := chromehttp.New(chromehttp.Options{Timeout: 5 * time.Second})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{WWWBase: ts1.URL})
	cli.readerPollInterval = 5 * time.Millisecond

	resID, err := cli.uploadReaderPDF(context.Background(), "paper.pdf", []byte("%PDF-1.4 sample payload"))
	if err != nil {
		t.Fatalf("expected uploadReaderPDF to recover on retry, got %v", err)
	}
	mu.Lock()
	gotMultipartCalls := multipartCalls
	gotUploadCalls := uploadCalls
	gotUploadTokens := append([]string(nil), uploadTokens...)
	mu.Unlock()
	if resID != "res-retry-ok" || gotMultipartCalls != 2 || gotUploadCalls != 2 {
		t.Fatalf("expected 2 prechecks + 2 uploads -> res-retry-ok, got id=%q mp=%d up=%d", resID, gotMultipartCalls, gotUploadCalls)
	}
	if len(gotUploadTokens) != 2 || gotUploadTokens[0] != "Bearer tok-attempt-1" || gotUploadTokens[1] != "Bearer tok-attempt-2" {
		t.Fatalf("expected refreshed upload token on retry, got %+v", gotUploadTokens)
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
	mu.Lock()
	url2 = ts2.URL
	mu.Unlock()
	cli.SetEndpoints(Endpoints{WWWBase: ts2.URL})

	resID2, err := cli.uploadReaderPDF(context.Background(), "dedup.pdf", []byte("%PDF-1.4 dedup payload"))
	if err != nil {
		t.Fatalf("expected MD5 dedup recovery on retry, got %v", err)
	}
	mu.Lock()
	gotMP2, gotUP2 := mp2, up2
	mu.Unlock()
	if resID2 != "res-dedup-ok" || gotMP2 != 2 || gotUP2 != 1 {
		t.Fatalf("expected 2 prechecks + 1 upload (skipped via fileExist=true), got id=%q mp=%d up=%d", resID2, gotMP2, gotUP2)
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
	mu.Lock()
	url3 = ts3.URL
	mu.Unlock()
	cli.SetEndpoints(Endpoints{WWWBase: ts3.URL})

	if _, err := cli.uploadReaderPDF(context.Background(), "forbidden.pdf", []byte("%PDF-1.4 forbidden")); err == nil {
		t.Fatalf("expected 403 upload to fail")
	}
	mu.Lock()
	gotUP3 := up3
	mu.Unlock()
	if gotUP3 != 1 {
		t.Fatalf("expected 403 to fail fast with 1 attempt, got %d", gotUP3)
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

	// 3. SendVerifyCode forwards user-completed Tencent Captcha proof, Content-Language, channel="scix-desktop"
	proof := &CaptchaProof{Ticket: "t03-user-ticket", RandStr: "@ab12", CaptchaAppID: DefaultTencentCaptchaAppID}
	if err := cli.SendVerifyCode(context.Background(), "email", "user@example.org", "", true, proof); err != nil {
		t.Fatalf("SendVerifyCode(email): %v", err)
	}
	mu.Lock()
	gotSend := lastSendPayload
	gotLang := lastSendLang
	mu.Unlock()
	if asString(gotSend["ticket"]) != "t03-user-ticket" || asString(gotSend["randStr"]) != "@ab12" || asInt64(gotSend["captchaAppId"]) != DefaultTencentCaptchaAppID || asString(gotSend["channel"]) != DesktopLoginChannel || !asBool(gotSend["isOversea"]) || gotLang != "en-us" {
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

func TestAcquireRandomPaperPDFCacheReuseAndContextCancel(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })

	// Seed >= minCachedPDFPool (3) valid PDFs in PDFCacheDir (filenames outside catalog or arbitrary valid PDFs).
	pdfDir := cfgMgr.Get().PDFCacheDir
	sample := buildMinimalScientificPDF(prompts.Paper{
		ID: "2611.99999", Title: "Cached Study", Category: "cs.CL", Filename: "arxiv_cached.pdf", AbstractSnippet: "Cached",
	})
	for _, name := range []string{"cached_a.pdf", "cached_b.pdf", "cached_c.pdf"} {
		if err := os.WriteFile(filepath.Join(pdfDir, name), sample, 0o644); err != nil {
			t.Fatalf("write cached pdf %s: %v", name, err)
		}
	}

	httpCli, _ := chromehttp.New(chromehttp.Options{Timeout: 2 * time.Second})
	cli := NewClient(httpCli, cfgMgr, st)

	// 1. Canceled context must return ctx.Err() immediately without hitting synthetic_fallback.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cli.AcquireRandomPaperPDF(cancelCtx); err != context.Canceled {
		t.Fatalf("expected context.Canceled from canceled AcquireRandomPaperPDF, got %v", err)
	}

	// 2. When 3 valid cached PDFs exist, reuse hits cache cleanly over repeated draws without slow network.
	cacheHits := 0
	for i := 0; i < 15; i++ {
		if c := listLocalPDFs(pdfDir, cfgMgr.Get().MaxPDFSizeBytes); len(c) < 3 {
			t.Fatalf("expected 3 cached pdfs, got %d", len(c))
		}
		picked := loadCachedPaperPDF(listLocalPDFs(pdfDir, cfgMgr.Get().MaxPDFSizeBytes), nil, "cache")
		if picked != nil && picked.Source == "cache" && isValidPDF(picked.Data) {
			cacheHits++
		}
	}
	if cacheHits != 15 {
		t.Fatalf("expected loadCachedPaperPDF to return valid cached PDFs consistently, got %d/15", cacheHits)
	}
}

func TestGetSciXModelCatalogRetryAndSharedFallback(t *testing.T) {
	origDelay := scixCatalogRetryBaseDelay
	origTimeout := scixCatalogAttemptTimeout
	scixCatalogRetryBaseDelay = 10 * time.Millisecond
	scixCatalogAttemptTimeout = 2 * time.Second
	t.Cleanup(func() {
		scixCatalogRetryBaseDelay = origDelay
		scixCatalogAttemptTimeout = origTimeout
	})

	const sampleCatalog = `{
		"object": "list",
		"data": [
			{"id":"dsflash-high","name":"DeepSeek-V4.1-Flash","api":"chat_completions","context_window":1000000,"max_output_tokens":384000,"input":["text"],"owned_by":"sigma"}
		]
	}`

	var (
		mode     atomic.Int32 // 0 = ok, 1 = drop twice then ok, 2 = 503 once then ok, 3 = always drop, 4 = always 401
		attempts atomic.Int32
		mu       sync.Mutex
		lastHdrs http.Header
	)

	resetConn := func(w http.ResponseWriter) {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				if tcpConn, ok := conn.(*net.TCPConn); ok {
					_ = tcpConn.SetLinger(0)
				}
				_ = conn.Close()
				return
			}
		}
		http.Error(w, "hijack unavailable", http.StatusInternalServerError)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		n := attempts.Add(1)
		mu.Lock()
		lastHdrs = r.Header.Clone()
		mu.Unlock()

		switch mode.Load() {
		case 1:
			if n <= 2 {
				resetConn(w)
				return
			}
		case 2:
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"temporary overload"}`))
				return
			}
		case 3:
			resetConn(w)
			return
		case 4:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"token expired"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleCatalog))
	}))
	defer srv.Close()

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

	accA, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token:     makeTestJWT(101, 201, time.Now().Add(24*time.Hour)),
		UserID:    101,
		OrgID:     201,
		Nickname:  "AccA",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, "AccA")
	if err != nil {
		t.Fatalf("upsert A: %v", err)
	}
	accB, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token:     makeTestJWT(102, 202, time.Now().Add(24*time.Hour)),
		UserID:    102,
		OrgID:     202,
		Nickname:  "AccB",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, "AccB")
	if err != nil {
		t.Fatalf("upsert B: %v", err)
	}
	accC, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token:     makeTestJWT(103, 203, time.Now().Add(24*time.Hour)),
		UserID:    103,
		OrgID:     203,
		Nickname:  "AccC",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, "AccC")
	if err != nil {
		t.Fatalf("upsert C: %v", err)
	}
	_ = st.SetPoolMembership([]string{accA.ID})

	httpCli, err := chromehttp.New(chromehttp.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("chromehttp.New: %v", err)
	}
	mgr := NewManager(httpCli, cfgMgr, st)
	mgr.SetEndpoints(Endpoints{SigmaLLMBase: srv.URL, PlatformBase: srv.URL, WWWBase: srv.URL})

	ctx := context.Background()

	// 1. Two TCP connection resets followed by 200 on attempt 3 succeeds cleanly, then uses TTL cache.
	mode.Store(1)
	attempts.Store(0)
	cat, err := mgr.ForAccount(accA.ID).GetSciXModelCatalog(ctx, true)
	if err != nil {
		t.Fatalf("expected catalog retry on connection reset to succeed, got %v", err)
	}
	if attempts.Load() != 3 || len(cat.Data) != 1 || cat.Data[0].ID != "dsflash-high" {
		t.Fatalf("unexpected attempts/catalog: attempts=%d data=%+v", attempts.Load(), cat.Data)
	}
	mu.Lock()
	gotHdrs := lastHdrs
	mu.Unlock()
	if gotHdrs.Get(SciXRuntimeHeader) != SciXRuntimeValue || gotHdrs.Get("traceparent") == "" || gotHdrs.Get("Sec-CH-UA") != "" {
		t.Fatalf("unexpected native headers on /models: %+v", gotHdrs)
	}
	if _, err := mgr.ForAccount(accA.ID).GetSciXModelCatalog(ctx, false); err != nil {
		t.Fatalf("cached GetSciXModelCatalog: %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected TTL cache hit without extra upstream calls, got attempts=%d", attempts.Load())
	}

	// 2. HTTP 503 once followed by 200 succeeds on retry.
	mode.Store(2)
	attempts.Store(0)
	if _, err := mgr.ForAccount(accA.ID).GetSciXModelCatalog(ctx, true); err != nil {
		t.Fatalf("expected catalog retry on 503 to succeed, got %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts on single 503, got %d", attempts.Load())
	}

	// 3. Cold Account B falls back to Manager's sharedCatalog when upstream always resets connections.
	mode.Store(3)
	catB, err := mgr.ForAccount(accB.ID).GetSciXModelCatalog(ctx, false)
	if err != nil {
		t.Fatalf("expected cold Account B to use sharedCatalog fallback, got %v", err)
	}
	if catB == nil || len(catB.Data) != 1 || catB.SciXCatalog == nil || catB.SciXCatalog.ScopeGeneration != "u102-o202" {
		t.Fatalf("unexpected fallback catalog for Account B: %+v", catB)
	}

	// 4. HTTP 401 MUST NOT silently serve sharedCatalog fallback so pool failover still works.
	mode.Store(4)
	if _, err := mgr.ForAccount(accC.ID).GetSciXModelCatalog(ctx, false); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error without shared fallback, got %v", err)
	}

	// 5. Fresh Manager WarmSciXCatalog populates sharedCatalog so a cold account survives upstream drop.
	mgr2 := NewManager(httpCli, cfgMgr, st)
	mgr2.SetEndpoints(Endpoints{SigmaLLMBase: srv.URL, PlatformBase: srv.URL, WWWBase: srv.URL})
	mode.Store(0)
	if err := mgr2.WarmSciXCatalog(ctx); err != nil {
		t.Fatalf("WarmSciXCatalog: %v", err)
	}
	mode.Store(3)
	if catWarmed, err := mgr2.ForAccount(accC.ID).GetSciXModelCatalog(ctx, false); err != nil || len(catWarmed.Data) != 1 {
		t.Fatalf("expected cold account to survive via warmed sharedCatalog, got err=%v cat=%+v", err, catWarmed)
	}
}

func assertExactJSONKeys(t *testing.T, label string, payload map[string]any, expected []string) {
	t.Helper()
	got := make([]string, 0, len(payload))
	for k := range payload {
		got = append(got, k)
	}
	want := append([]string(nil), expected...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s: JSON key count mismatch: got %v, want %v (payload=%+v)", label, got, want, payload)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: JSON keys mismatch: got %v, want %v (payload=%+v)", label, got, want, payload)
		}
	}
}

func assertExactQueryKeys(t *testing.T, label string, q url.Values, expected []string) {
	t.Helper()
	got := make([]string, 0, len(q))
	for k := range q {
		got = append(got, k)
	}
	want := append([]string(nil), expected...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s: query key count mismatch: got %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: query keys mismatch: got %v, want %v", label, got, want)
		}
	}
}

func makeTypedTestJWT(userID, orgID int64, userType string, exp time.Time) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	idMap := map[string]any{
		"userId": userID,
		"orgId":  orgID,
	}
	if userType != "" {
		idMap["userType"] = userType
	}
	payload, _ := json.Marshal(map[string]any{
		"exp":      exp.Unix(),
		"identity": idMap,
	})
	return hdr + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestDesktopChannelLoginStrictRequestSchemas(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })

	tok := makeTypedTestJWT(501, 601, "bohrVersion", time.Now().Add(24*time.Hour))

	type capturedReq struct {
		Method  string
		Path    string
		Query   url.Values
		Headers http.Header
		Body    map[string]any
	}
	var (
		mu         sync.Mutex
		skipUser   = true
		captured   []capturedReq
		schemaErrs []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var bodyMap map[string]any
		rawBody, _ := io.ReadAll(r.Body)
		if len(bytes.TrimSpace(rawBody)) > 0 {
			if err := json.Unmarshal(rawBody, &bodyMap); err != nil {
				mu.Lock()
				schemaErrs = append(schemaErrs, fmt.Sprintf("expected JSON body for %s %s: %v (raw=%s)", r.Method, r.URL.Path, err, string(rawBody)))
				mu.Unlock()
			}
		}
		mu.Lock()
		captured = append(captured, capturedReq{
			Method:  r.Method,
			Path:    r.URL.Path,
			Query:   r.URL.Query(),
			Headers: r.Header.Clone(),
			Body:    bodyMap,
		})
		curSkip := skipUser
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/account/wechat/login_qrcode":
			_, _ = w.Write([]byte(`{"code":0,"data":{"sceneId":"sc-1","qrCode":"https://qr.example/1"}}`))
		case "/api/account/wechat/qr_code/check_login_status":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":1}}`))
		case "/api/oauth/verify", "/api/account/v2/login/phone_code", "/api/account/v2/login/email_code", "/api/account/v3/login", "/platform-gateway/v1/account/refresh_token":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"token": tok}})
		case "/api/account/code/send_by_phone_v3", "/api/account/code/send_by_email_v3":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/api/captcha/check_user":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"skip": curSkip}})
		case "/api/captcha/encrypt_appid":
			_, _ = w.Write([]byte(`{"code":0,"data":{"signAppIdStr":"enc-aid-1"}}`))
		case "/bohrapi/v1/account/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userId":501,"orgId":601,"name":"DesktopLoginUser"}}`))
		case "/api/platform/session":
			_, _ = w.Write([]byte(`{"logged_in":true,"binding":{"nickname":"DesktopLoginUser"}}`))
		case "/bohrapi/v1/sigma-search/api/v1/scix/membership/client-activity":
			_, _ = w.Write([]byte(`{"code":0,"data":{"newUserBenefitWithinPeriod":true,"newUserBenefitFirstLogin":false,"hasOldActivityLogin":true}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	httpCli, _ := chromehttp.New(chromehttp.Options{})
	cli := NewClient(httpCli, cfgMgr, st)
	cli.SetEndpoints(Endpoints{
		ClubBase:     srv.URL,
		PlatformBase: srv.URL,
		WWWBase:      srv.URL,
	})

	lastByPath := func(path string) capturedReq {
		mu.Lock()
		defer mu.Unlock()
		for i := len(captured) - 1; i >= 0; i-- {
			if captured[i].Path == path {
				return captured[i]
			}
		}
		t.Fatalf("no request recorded for path %s", path)
		return capturedReq{}
	}

	ctx := context.Background()

	// 0. StartWeChatQR: exact query ["refer"] == "{}" and empty body
	if _, err := cli.StartWeChatQR(ctx); err != nil {
		t.Fatalf("StartWeChatQR: %v", err)
	}
	qrStart := lastByPath("/api/account/wechat/login_qrcode")
	assertExactQueryKeys(t, "StartWeChatQR query", qrStart.Query, []string{"refer"})
	if qrStart.Query.Get("refer") != "{}" || qrStart.Body != nil {
		t.Fatalf("StartWeChatQR unexpected query/body: query=%v body=%+v", qrStart.Query, qrStart.Body)
	}

	// 1. WeChat QR Poll: strict query fields & device JSON schema
	if _, err := cli.PollWeChatQR(ctx, "scene-xyz"); err != nil {
		t.Fatalf("PollWeChatQR: %v", err)
	}
	qrReq := lastByPath("/api/account/wechat/qr_code/check_login_status")
	assertExactQueryKeys(t, "PollWeChatQR", qrReq.Query, []string{"channel", "device", "sceneId"})
	if qrReq.Query.Get("channel") != DesktopLoginChannel || qrReq.Query.Get("sceneId") != "scene-xyz" {
		t.Fatalf("PollWeChatQR unexpected query values: %v", qrReq.Query)
	}
	devRaw, err := base64.StdEncoding.DecodeString(qrReq.Query.Get("device"))
	if err != nil {
		t.Fatalf("decode PollWeChatQR device param: %v", err)
	}
	var devMap map[string]any
	if err := json.Unmarshal(devRaw, &devMap); err != nil {
		t.Fatalf("unmarshal PollWeChatQR device JSON: %v", err)
	}
	assertExactJSONKeys(t, "PollWeChatQR.device", devMap, []string{"memory", "screenInfo", "userAgent"})
	assertExactJSONKeys(t, "PollWeChatQR.device.screenInfo", asMap(devMap["screenInfo"]), []string{"height", "with"})

	// 2. PrepareCaptcha ("password", skip=false): strict check_user & encrypt_appid schemas
	mu.Lock()
	skipUser = false
	mu.Unlock()
	prep, err := cli.PrepareCaptcha(ctx, "password", "a@example.com")
	if err != nil || prep.Skip || prep.AidEncrypted != "enc-aid-1" {
		t.Fatalf("PrepareCaptcha: err=%v prep=%+v", err, prep)
	}
	chkReq := lastByPath("/api/captcha/check_user")
	assertExactJSONKeys(t, "PrepareCaptcha check_user", chkReq.Body, []string{"account"})
	encReq := lastByPath("/api/captcha/encrypt_appid")
	assertExactJSONKeys(t, "PrepareCaptcha encrypt_appid", encReq.Body, []string{"captchaAppId"})
	mu.Lock()
	skipUser = true
	mu.Unlock()

	// 3. OAuth Verify: strict JSON fields & channel="scix-desktop"
	if _, err := cli.VerifyOAuthCallback(ctx, "google", "https://login.bohrium.com/?code=abc&state=st1"); err != nil {
		t.Fatalf("VerifyOAuthCallback: %v", err)
	}
	oauthReq := lastByPath("/api/oauth/verify")
	assertExactQueryKeys(t, "VerifyOAuthCallback query", oauthReq.Query, nil)
	assertExactJSONKeys(t, "VerifyOAuthCallback body", oauthReq.Body, []string{
		"businessLine", "channel", "code", "isOversea", "method", "refer", "state", "type",
	})
	if asString(oauthReq.Body["channel"]) != DesktopLoginChannel {
		t.Fatalf("VerifyOAuthCallback channel=%q want %q", asString(oauthReq.Body["channel"]), DesktopLoginChannel)
	}

	// 4. SendVerifyCode (phone, without & with captcha proof)
	if err := cli.SendVerifyCode(ctx, "phone", "13800138000", "86", false, nil); err != nil {
		t.Fatalf("SendVerifyCode(phone): %v", err)
	}
	sendPhone := lastByPath("/api/account/code/send_by_phone_v3")
	assertExactJSONKeys(t, "SendVerifyCode(phone)", sendPhone.Body, []string{
		"Version", "businessLine", "captchaSceneId", "channel", "phone", "sceneType", "tokenKey",
	})
	if asString(sendPhone.Body["channel"]) != DesktopLoginChannel || asString(sendPhone.Body["phone"]) != "+8613800138000" {
		t.Fatalf("unexpected sendPhone payload: %+v", sendPhone.Body)
	}
	proof := &CaptchaProof{Ticket: "t-1", RandStr: "@r-1", CaptchaAppID: DefaultTencentCaptchaAppID}
	if err := cli.SendVerifyCode(ctx, "phone", "13800138000", "86", false, proof); err != nil {
		t.Fatalf("SendVerifyCode(phone+proof): %v", err)
	}
	sendPhoneProof := lastByPath("/api/account/code/send_by_phone_v3")
	assertExactJSONKeys(t, "SendVerifyCode(phone+proof)", sendPhoneProof.Body, []string{
		"Version", "businessLine", "captchaAppId", "captchaSceneId", "channel", "phone", "randStr", "sceneType", "ticket", "tokenKey",
	})

	// 5. SendVerifyCode (email, without & with captcha proof)
	if err := cli.SendVerifyCode(ctx, "email", "a@example.com", "", false, nil); err != nil {
		t.Fatalf("SendVerifyCode(email): %v", err)
	}
	sendEmail := lastByPath("/api/account/code/send_by_email_v3")
	assertExactJSONKeys(t, "SendVerifyCode(email)", sendEmail.Body, []string{
		"businessLine", "channel", "email", "isOversea", "sceneType", "tokenKey",
	})
	if asString(sendEmail.Body["channel"]) != DesktopLoginChannel {
		t.Fatalf("unexpected sendEmail channel: %+v", sendEmail.Body)
	}
	if err := cli.SendVerifyCode(ctx, "email", "a@example.com", "", true, proof); err != nil {
		t.Fatalf("SendVerifyCode(email+proof): %v", err)
	}
	sendEmailProof := lastByPath("/api/account/code/send_by_email_v3")
	assertExactJSONKeys(t, "SendVerifyCode(email+proof)", sendEmailProof.Body, []string{
		"businessLine", "captchaAppId", "channel", "email", "isOversea", "randStr", "sceneType", "ticket", "tokenKey",
	})

	// 6. LoginWithCode (phone): exact 7 fields (no sceneType/isOversea)
	if _, err := cli.LoginWithCode(ctx, "phone", "13800138000", "86", "123456", false); err != nil {
		t.Fatalf("LoginWithCode(phone): %v", err)
	}
	phoneLogin := lastByPath("/api/account/v2/login/phone_code")
	assertExactJSONKeys(t, "LoginWithCode(phone)", phoneLogin.Body, []string{
		"autoRegist", "businessLine", "channel", "device", "phone", "phoneCode", "refer",
	})
	if asString(phoneLogin.Body["channel"]) != DesktopLoginChannel || !asBool(phoneLogin.Body["autoRegist"]) {
		t.Fatalf("unexpected phoneLogin payload: %+v", phoneLogin.Body)
	}

	// 7. LoginWithCode (email): exact 7 fields (with ext, no autoRegist/sceneType/isOversea)
	if _, err := cli.LoginWithCode(ctx, "email", "a@example.com", "", "654321", true); err != nil {
		t.Fatalf("LoginWithCode(email): %v", err)
	}
	emailLogin := lastByPath("/api/account/v2/login/email_code")
	assertExactJSONKeys(t, "LoginWithCode(email)", emailLogin.Body, []string{
		"businessLine", "channel", "code", "device", "email", "ext", "refer",
	})
	if asString(emailLogin.Body["channel"]) != DesktopLoginChannel || asString(emailLogin.Body["ext"]) != platformAESExt {
		t.Fatalf("unexpected emailLogin payload: %+v", emailLogin.Body)
	}

	// 8. LoginWithPassword (exempt skip=true, no proof): exact 7 fields (no sceneType)
	if _, err := cli.LoginWithPassword(ctx, "a@example.com", "pw123456", nil); err != nil {
		t.Fatalf("LoginWithPassword(no proof): %v", err)
	}
	pwNoProof := lastByPath("/api/account/v3/login")
	assertExactJSONKeys(t, "LoginWithPassword(no proof)", pwNoProof.Body, []string{
		"account", "businessLine", "channel", "device", "ext", "password", "refer",
	})
	if asString(pwNoProof.Body["channel"]) != DesktopLoginChannel || asString(pwNoProof.Body["ext"]) != platformAESExt {
		t.Fatalf("unexpected pwNoProof payload: %+v", pwNoProof.Body)
	}

	// 9. LoginWithPassword (with proof): exact 10 fields
	if _, err := cli.LoginWithPassword(ctx, "a@example.com", "pw123456", proof); err != nil {
		t.Fatalf("LoginWithPassword(with proof): %v", err)
	}
	pwProof := lastByPath("/api/account/v3/login")
	assertExactJSONKeys(t, "LoginWithPassword(with proof)", pwProof.Body, []string{
		"account", "businessLine", "captchaAppId", "channel", "device", "ext", "password", "randStr", "refer", "ticket",
	})

	// 10. RefreshToken: exact JSON keys ["businessLine", "token"]
	if _, err := cli.RefreshToken(ctx, "manual"); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	refReq := lastByPath("/platform-gateway/v1/account/refresh_token")
	assertExactQueryKeys(t, "RefreshToken query", refReq.Query, nil)
	assertExactJSONKeys(t, "RefreshToken body", refReq.Body, []string{"businessLine", "token"})
	if asString(refReq.Body["businessLine"]) != "bohrium" || asString(refReq.Body["token"]) != tok {
		t.Fatalf("unexpected RefreshToken body: %+v", refReq.Body)
	}

	mu.Lock()
	gotSchemaErrs := append([]string(nil), schemaErrs...)
	mu.Unlock()
	if len(gotSchemaErrs) > 0 {
		t.Fatalf("handler decode errors:\n%s", strings.Join(gotSchemaErrs, "\n"))
	}
}

func TestClientLoginBenefitStrictStatusGateAndSchema(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })

	var (
		mu                  sync.Mutex
		withinPeriod        = true
		firstLogin          = true
		hasOldActivityLogin = false
		activityCalls       int
		receiveCalls        int
		lastActivityQuery   url.Values
		lastReceiveBody     map[string]any
		lastReceiveHeaders  http.Header
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bohrapi/v1/sigma-search/api/v1/scix/membership/client-activity":
			activityCalls++
			lastActivityQuery = r.URL.Query()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"newUserBenefitWithinPeriod": withinPeriod,
					"newUserBenefitFirstLogin":   firstLogin,
					"hasOldActivityLogin":        hasOldActivityLogin,
				},
			})
		case "/platform-gateway/v1/membership/receive_photon":
			receiveCalls++
			lastReceiveHeaders = r.Header.Clone()
			rawBody, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(rawBody, &lastReceiveBody)
			hasOldActivityLogin = true
			firstLogin = false
			_, _ = w.Write([]byte(`{"code":0,"data":{"claimed":true}}`))
		case "/bohrapi/v1/account/info":
			_, _ = w.Write([]byte(`{"code":0,"data":{"userId":801,"orgId":901,"name":"BenefitUser"}}`))
		case "/api/platform/session":
			_, _ = w.Write([]byte(`{"logged_in":true,"binding":{"nickname":"BenefitUser"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	httpCli, _ := chromehttp.New(chromehttp.Options{})
	cli := NewClient(httpCli, cfgMgr, st)
	// PlatformGatewayBase intentionally omitted to verify automatic WWWBase fallback in offline tests.
	cli.SetEndpoints(Endpoints{
		ClubBase:     srv.URL,
		PlatformBase: srv.URL,
		WWWBase:      srv.URL,
	})

	personalTok := makeTypedTestJWT(801, 901, "bohrVersion", time.Now().Add(24*time.Hour))
	if err := st.SetAuth(store.AuthState{Token: personalTok, UserID: 801, OrgID: 901}); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}

	// 1. Eligible personal user checks client-activity first, then posts exact {"eventSubject":"client_register"}.
	ctx := context.Background()
	res, err := cli.ClaimClientLoginPhotons(ctx, "manual")
	if err != nil {
		t.Fatalf("ClaimClientLoginPhotons(eligible): %v", err)
	}
	if !res.Success || !res.Claimed || res.RewardPhotons != 10000 || res.ValidDays != 90 {
		t.Fatalf("unexpected claim result: %+v", res)
	}
	mu.Lock()
	assertExactQueryKeys(t, "client-activity query", lastActivityQuery, []string{"channel"})
	if lastActivityQuery.Get("channel") != DesktopLoginChannel {
		t.Fatalf("unexpected channel query: %v", lastActivityQuery)
	}
	assertExactJSONKeys(t, "receive_photon payload", lastReceiveBody, []string{"eventSubject"})
	if asString(lastReceiveBody["eventSubject"]) != ClientLoginBenefitEventSubject {
		t.Fatalf("unexpected eventSubject: %+v", lastReceiveBody)
	}
	if lastReceiveHeaders.Get("X-Bohrium-Platform") != "web" || lastReceiveHeaders.Get("Authorization") != "Bearer "+personalTok {
		t.Fatalf("unexpected receive_photon headers: %+v", lastReceiveHeaders)
	}
	if activityCalls != 1 || receiveCalls != 1 {
		t.Fatalf("expected 1 activity call and 1 receive call, got activity=%d receive=%d", activityCalls, receiveCalls)
	}
	mu.Unlock()

	// 2. Second claim after already claimed MUST NOT call receive_photon at all.
	res2, err := cli.ClaimClientLoginPhotons(ctx, "manual")
	if err != nil || res2.Claimed || !res2.Skipped || !res2.AlreadyClaimed {
		t.Fatalf("expected skipped alreadyClaimed result, got %+v err=%v", res2, err)
	}
	mu.Lock()
	if receiveCalls != 1 {
		t.Fatalf("receive_photon must not be called when already claimed, got receiveCalls=%d", receiveCalls)
	}
	// 3. Out of period MUST NOT call receive_photon even when firstLogin=true.
	withinPeriod = false
	firstLogin = true
	hasOldActivityLogin = false
	mu.Unlock()
	res3, err := cli.ClaimClientLoginPhotons(ctx, "manual")
	if err != nil || res3.Claimed || !res3.Skipped {
		t.Fatalf("expected out-of-period skip, got %+v err=%v", res3, err)
	}
	mu.Lock()
	if receiveCalls != 1 {
		t.Fatalf("receive_photon must not be called out of period, got receiveCalls=%d", receiveCalls)
	}
	// 4. Org/team account ("teamVersion") MUST NOT call receive_photon even if server flags are all eligible.
	withinPeriod = true
	firstLogin = true
	hasOldActivityLogin = false
	mu.Unlock()
	teamTok := makeTypedTestJWT(802, 902, "teamVersion", time.Now().Add(24*time.Hour))
	teamAcc, _, err := st.UpsertAccountFromAuth(store.AuthState{Token: teamTok, UserID: 802, OrgID: 902}, "TeamAccount")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth(team): %v", err)
	}
	mgr := NewManager(httpCli, cfgMgr, st)
	mgr.SetEndpoints(Endpoints{
		ClubBase:     srv.URL,
		PlatformBase: srv.URL,
		WWWBase:      srv.URL,
	})
	teamCli := mgr.ForAccount(teamAcc.ID)
	res4, err := teamCli.ClaimClientLoginPhotons(ctx, "manual")
	if err != nil || res4.Claimed || !res4.Skipped || res4.Status == nil || res4.Status.PersonalAccount {
		t.Fatalf("expected team account skip with PersonalAccount=false, got %+v err=%v", res4, err)
	}
	mu.Lock()
	if receiveCalls != 1 {
		t.Fatalf("receive_photon must not be called for non-personal account, got receiveCalls=%d", receiveCalls)
	}
	mu.Unlock()

	// 5. CompleteLogin automatically checks client-activity and claims receive_photon when personal + eligible.
	if _, err := cli.CompleteLogin(ctx, personalTok, "test"); err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	mu.Lock()
	gotReceiveAfterLogin := receiveCalls
	mu.Unlock()
	if gotReceiveAfterLogin != 2 {
		t.Fatalf("expected CompleteLogin to auto-claim eligible client login benefit, got receiveCalls=%d", gotReceiveAfterLogin)
	}
}

func TestTaskPreExecutionStatusGateStrictSchema(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	_, _ = cfgMgr.Update(func(c *config.Config) error {
		c.TaskDelaySec = 0
		return nil
	})
	st, _ := store.Open(tmp)
	t.Cleanup(func() { _ = st.Close() })

	tok := makeTypedTestJWT(950, 951, "bohrVersion", time.Now().Add(24*time.Hour))
	if err := st.SetAuth(store.AuthState{Token: tok, UserID: 950, OrgID: 951}); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}

	var (
		mu              sync.Mutex
		taskStatus      = 1
		claimAdvances   = false
		listCalls       int
		claimCalls      int
		chatCalls       int
		lastClaimBody   map[string]any
		lastChatQuery   url.Values
		lastChatBody    map[string]any
		lastCancelQuery url.Values
		lastCancelBody  map[string]any
		lastTaskQuery   url.Values
		lastEventBody   map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/plaza/tasks":
			listCalls++
			lastTaskQuery = r.URL.Query()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"taskDate": "2026-11-27",
					"list": []map[string]any{
						{"id": 1, "taskKey": "bohrium_science_space_web", "titleZh": "体验玻尔科学空间「Web端」", "rewardAmount": 1000, "status": taskStatus},
					},
				},
			})
		case r.URL.Path == "/api/plaza/tasks/claim":
			claimCalls++
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			lastClaimBody = b
			if claimAdvances {
				taskStatus = 0
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"claimedCount":1}}`))
		case r.URL.Path == "/api/v1/chat":
			chatCalls++
			lastChatQuery = r.URL.Query()
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			lastChatBody = b
			_, _ = w.Write([]byte(`{"workflowId":"wf-gate-1","sessionId":"sess-gate-1","queryId":"q-gate-1"}`))
		case r.URL.Path == "/api/v1/query-events":
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			lastEventBody = b
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/chat/") && strings.HasSuffix(r.URL.Path, "/cancel"):
			lastCancelQuery = r.URL.Query()
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			lastCancelBody = b
			_, _ = w.Write([]byte(`{"durationMs":25}`))
		case r.URL.Path == "/bohrapi/v1/sigma-search/api/v1/scix/membership/client-activity":
			_, _ = w.Write([]byte(`{"code":0,"data":{"newUserBenefitWithinPeriod":false,"newUserBenefitFirstLogin":false,"hasOldActivityLogin":true}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

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
	ctx := context.Background()

	// 1. Unknown taskKey NOT in live ListDailyTasks MUST be rejected without calling /api/v1/chat.
	resMissing, err := cli.ExecuteSingleTask(ctx, "non_existent_task", "manual")
	if err != nil || !resMissing.Skipped || resMissing.Success || chatCalls != 0 {
		t.Fatalf("expected missing task rejection without chat call, got res=%+v err=%v chatCalls=%d", resMissing, err, chatCalls)
	}

	// 2. Completed task (status == 1) MUST NOT claim or execute.
	resDone, err := cli.ExecuteSingleTask(ctx, TaskKeyScienceSpaceWeb, "manual")
	if err != nil || !resDone.Skipped || !resDone.Success || claimCalls != 0 || chatCalls != 0 {
		t.Fatalf("expected completed task skip without claim/chat, got res=%+v err=%v claim=%d chat=%d", resDone, err, claimCalls, chatCalls)
	}

	// 3. Unclaimed task (status == -1): autoClaim=false in RunAllWebTasks skips without claim/chat.
	mu.Lock()
	taskStatus = -1
	claimAdvances = false
	mu.Unlock()
	runNoClaim, err := cli.RunAllWebTasks(ctx, "manual", false)
	if err != nil || len(runNoClaim.Results) != 1 || !runNoClaim.Results[0].Skipped || runNoClaim.Results[0].Success || claimCalls != 0 || chatCalls != 0 {
		t.Fatalf("expected autoClaim=false to skip status=-1 task, got %+v err=%v claim=%d chat=%d", runNoClaim, err, claimCalls, chatCalls)
	}

	// 4. Unclaimed task where claim returns OK but refreshed ListDailyTasks still stays status=-1 MUST refuse chat execution.
	resSticky, err := cli.ExecuteSingleTask(ctx, TaskKeyScienceSpaceWeb, "manual")
	if err != nil || !resSticky.Skipped || resSticky.Success || chatCalls != 0 || claimCalls == 0 {
		t.Fatalf("expected sticky status=-1 after claim to abort execution, got res=%+v err=%v claim=%d chat=%d", resSticky, err, claimCalls, chatCalls)
	}

	// 5. Unclaimed task -> claim transitions status -1 -> 0 -> re-query confirms 0 -> executes with exact API schemas.
	mu.Lock()
	taskStatus = -1
	claimAdvances = true
	mu.Unlock()
	resOK, err := cli.ExecuteSingleTask(ctx, TaskKeyScienceSpaceWeb, "manual")
	if err != nil || !resOK.Success || resOK.Skipped || chatCalls != 1 {
		t.Fatalf("expected claimed & confirmed status=0 task to execute cleanly, got res=%+v err=%v chat=%d", resOK, err, chatCalls)
	}
	mu.Lock()
	assertExactQueryKeys(t, "ListDailyTasks", lastTaskQuery, nil)
	assertExactJSONKeys(t, "ClaimTask", lastClaimBody, []string{"publisher", "taskKey"})
	if asString(lastClaimBody["taskKey"]) != TaskKeyScienceSpaceWeb || asString(lastClaimBody["publisher"]) != PublisherCommunity {
		t.Fatalf("unexpected ClaimTask body: %+v", lastClaimBody)
	}
	assertExactQueryKeys(t, "RunScienceSpaceWeb chat query", lastChatQuery, []string{"historyCapabilities"})
	if lastChatQuery.Get("historyCapabilities") != HistoryCapabilitiesValue {
		t.Fatalf("unexpected chat query: %v", lastChatQuery)
	}
	assertExactJSONKeys(t, "RunScienceSpaceWeb chat body", lastChatBody, []string{
		"clientTimeZone", "invocationRequestId", "queryId", "target", "text",
	})
	assertExactJSONKeys(t, "RunScienceSpaceWeb chat target", asMap(lastChatBody["target"]), []string{
		"kind", "teamId",
	})
	assertExactQueryKeys(t, "Cancel chat query", lastCancelQuery, nil)
	assertExactJSONKeys(t, "Cancel chat body", lastCancelBody, []string{"source"})
	if asString(lastCancelBody["source"]) != "api_request" {
		t.Fatalf("unexpected cancel body: %+v", lastCancelBody)
	}
	assertExactJSONKeys(t, "QueryEvents", lastEventBody, []string{
		"eventKey", "orgId", "queryId", "queryTime", "task_key", "userId",
	})
	if asString(lastEventBody["task_key"]) != TaskKeyScienceSpaceWeb || asInt64(lastEventBody["userId"]) != 950 || asInt64(lastEventBody["orgId"]) != 951 || asString(lastEventBody["queryId"]) != "q-gate-1" || asString(lastEventBody["eventKey"]) != "950:951:q-gate-1" {
		t.Fatalf("unexpected query-events payload: %+v", lastEventBody)
	}
	mu.Unlock()

	// 6. ClaimAllTasks exact JSON schema: ["publisher"] == PublisherCommunity
	if _, err := cli.ClaimAllTasks(ctx, "manual"); err != nil {
		t.Fatalf("ClaimAllTasks: %v", err)
	}
	mu.Lock()
	assertExactJSONKeys(t, "ClaimAllTasks", lastClaimBody, []string{"publisher"})
	if asString(lastClaimBody["publisher"]) != PublisherCommunity {
		t.Fatalf("unexpected ClaimAllTasks body: %+v", lastClaimBody)
	}
	mu.Unlock()
}
