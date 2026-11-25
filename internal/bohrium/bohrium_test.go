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

func TestRunAllWebTasksSkipsClientAndCancelsImmediately(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "test-pw-1234")
	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_, _ = cfgMgr.Update(func(c *config.Config) error {
		c.TaskDelaySec = 0
		c.CancelAfterMs = 10
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
	tok := makeTestJWT(4242, 99, time.Now().Add(72*time.Hour))
	_ = st.SetAuth(store.AuthState{Token: tok, UserID: 4242, OrgID: 99, ExpiresAt: time.Now().Add(72 * time.Hour)})

	var (
		mu             sync.Mutex
		claimedAll     bool
		chatCalls      int
		cancelCalls    int
		binaryUploaded bool
		reportedKeys   = map[string]bool{}
		deprecatedHit  bool
		nonChromeUA    bool
	)

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(r.Header.Get("User-Agent"), "Chrome/") {
			nonChromeUA = true
		}
		if strings.Contains(r.URL.Path, "integral_receive") {
			deprecatedHit = true
			w.WriteHeader(http.StatusGone)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/plaza/tasks":
			if r.Header.Get(PlazaGatewayHeader) != PlazaGatewayProd {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			stVal := -1
			if claimedAll {
				stVal = 0
			}
			if len(reportedKeys) == 3 {
				stVal = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"taskDate": "2026-11-25",
					"list": []map[string]any{
						{"id": 1, "taskKey": "bohrium_science_space_web", "titleZh": "体验玻尔科学空间「Web端」", "rewardAmount": 500, "status": stVal},
						{"id": 2, "taskKey": "bohrium_science_space_client", "titleZh": "体验玻尔科学空间「客户端」", "rewardAmount": 500, "status": stVal},
						{"id": 3, "taskKey": "bohrium_literature_reading", "titleZh": "体验玻尔「文献阅读」", "rewardAmount": 300, "status": stVal},
						{"id": 4, "taskKey": "bohrium_academic_writing", "titleZh": "体验玻尔「学术写作」", "rewardAmount": 300, "status": stVal},
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
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
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
	})

	res, err := cli.RunAllWebTasks(context.Background(), "manual", true)
	if err != nil {
		t.Fatalf("RunAllWebTasks: %v", err)
	}

	if deprecatedHit {
		t.Fatalf("deprecated /integral_receive endpoint MUST NOT be called")
	}
	if nonChromeUA {
		t.Fatalf("every outgoing request must use Chrome User-Agent")
	}
	if !claimedAll {
		t.Fatalf("expected claim-all to run")
	}
	if chatCalls != 3 || cancelCalls != 3 {
		t.Fatalf("expected 3 chat calls and 3 immediate cancels, got chat=%d cancel=%d", chatCalls, cancelCalls)
	}
	if !binaryUploaded {
		t.Fatalf("expected PDF binary upload with X-Storage-Param for Task 3")
	}
	for _, k := range []string{TaskKeyScienceSpaceWeb, TaskKeyLiteratureReading, TaskKeyAcademicWriting} {
		if !reportedKeys[k] {
			t.Fatalf("expected query-events report for %s, got %+v", k, reportedKeys)
		}
	}
	if reportedKeys[TaskKeyClientSkipped] {
		t.Fatalf("desktop client task must never be executed or reported")
	}
	if !res.AllWebDone {
		t.Fatalf("expected AllWebDone=true, got %+v", res)
	}
}

func TestGetPointsAndCapacity(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmp)
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
