package openai_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/openai"
	"bohrium-club-auto/internal/store"
)

func hardeningPost(t *testing.T, gwURL, apiKey, path, body string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, gwURL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw)
}

func TestOpenAIGatewayClientCompatHardening(t *testing.T) {
	gw, gwSrv, st, _, apiKey, capUp := setupTestGatewayEnv(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, `data: {"id":"c_h","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"c_h","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"c_h","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	_ = gw

	// 1. include_usage=false: usage still requested upstream + metered, but not emitted downstream.
	// reasoning object is honored; prompt_cache_key is not forwarded to /chat/completions.
	code, _, sse := hardeningPost(t, gwSrv.URL, apiKey, "/v1/chat/completions", `{
		"model":"dsflash-high","stream":true,"stream_options":{"include_usage":false},
		"reasoning":{"effort":"low"},"prompt_cache_key":"pck-1","user":"alice",
		"messages":[{"role":"user","content":"usage test"}]
	}`, nil)
	if code != http.StatusOK || !strings.Contains(sse, `"content":"hi"`) || !strings.Contains(sse, "data: [DONE]") {
		t.Fatalf("unexpected stream: %d %q", code, sse)
	}
	if strings.Contains(sse, "prompt_tokens") {
		t.Fatalf("usage chunk must not be emitted when include_usage=false: %q", sse)
	}
	capUp.mu.Lock()
	so, _ := capUp.body["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Fatalf("expected upstream stream_options.include_usage=true, got %v", capUp.body["stream_options"])
	}
	if capUp.body["reasoning_effort"] != "low" {
		t.Fatalf("expected reasoning object mapped to reasoning_effort=low, got %v", capUp.body["reasoning_effort"])
	}
	if _, has := capUp.body["prompt_cache_key"]; has {
		t.Fatalf("prompt_cache_key must not be forwarded to /chat/completions")
	}
	capUp.mu.Unlock()

	var rec store.GatewayUsageRecord
	var found bool
	for i := 0; i < 40 && !found; i++ {
		page, err := st.ListGatewayUsageLogs(store.UsageLogsQueryOptions{RangeDays: 1, Page: 1, PageSize: 10})
		if err == nil && len(page.Items) > 0 {
			rec, found = page.Items[0], true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !found || rec.PromptTokens != 11 || rec.CompletionTokens != 7 || rec.ReasoningEffort != "low" {
		t.Fatalf("expected metered usage 11/7 with effort=low, got found=%v rec=%+v", found, rec)
	}

	// 2. A static `user` no longer collapses unrelated conversations into one session binding.
	for _, msg := range []string{"conversation one", "conversation two"} {
		if c, _, b := hardeningPost(t, gwSrv.URL, apiKey, "/v1/chat/completions", `{"model":"dsflash-high","stream":false,"user":"alice","messages":[{"role":"user","content":"`+msg+`"}]}`, nil); c != http.StatusOK {
			t.Fatalf("user conv %q: %d %s", msg, c, b)
		}
	}
	bindings, _ := st.ListSessionBindings(time.Now().UTC(), 200)
	if len(bindings) != 3 {
		t.Fatalf("expected 3 distinct bindings (1 pck + 2 user convs), got %d", len(bindings))
	}

	// 3. Invalid traceparent is replaced; padded x-scix-execution drives session affinity but never leaks storeId.
	foreignStore := bohrium.NewCanonicalUUID()
	foreignRoot := bohrium.NewCanonicalUUID()
	payload, _ := json.Marshal(bohrium.SciXExecutionPayload{Version: 1, Scenario: "cloud", StoreID: foreignStore, RootSessionID: foreignRoot, SessionID: foreignRoot})
	padded := base64.URLEncoding.EncodeToString(payload)
	for _, msg := range []string{"exec one", "exec two"} {
		if c, _, b := hardeningPost(t, gwSrv.URL, apiKey, "/v1/chat/completions", `{"model":"dsflash-high","stream":false,"messages":[{"role":"user","content":"`+msg+`"}]}`,
			map[string]string{"traceparent": "garbage", "x-scix-execution": padded}); c != http.StatusOK {
			t.Fatalf("exec hdr %q: %d %s", msg, c, b)
		}
	}
	bindings, _ = st.ListSessionBindings(time.Now().UTC(), 200)
	if len(bindings) != 4 {
		t.Fatalf("expected padded x-scix-execution to share one session (4 total bindings), got %d", len(bindings))
	}
	capUp.mu.Lock()
	defer capUp.mu.Unlock()
	if !bohrium.IsValidTraceparent(capUp.traceparent) || capUp.traceparent == "garbage" {
		t.Fatalf("expected fresh valid traceparent, got %q", capUp.traceparent)
	}
	if capUp.execPayload.StoreID == foreignStore || capUp.execPayload.Scenario != "local_agent" || capUp.execPayload.RootSessionID != foreignRoot {
		t.Fatalf("unexpected execution payload isolation: %+v", capUp.execPayload)
	}
}

func TestPreflight401TriggersMultiAccountFailover(t *testing.T) {
	var (
		fail401 atomic.Bool
		mu      sync.Mutex
		llmTP   = map[string]string{}
	)
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"dsflash-high","name":"Flash","api":"chat_completions","context_window":1000000,"max_output_tokens":1000,"input":["text"],"presentation":{"visible":true,"default":true}}]}`)
		case "/billing/preflight":
			if fail401.Load() && auth == "Bearer tok-pf-A" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"message":"token expired"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"allowed":true}`)
		case "/chat/completions":
			mu.Lock()
			llmTP[auth] = r.Header.Get("traceparent")
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = io.WriteString(w, `data: {"id":"c_pf","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upSrv.Close)

	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	t.Setenv("BOHRIUM_API_KEYS", "sk-brm-pf-test")
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "testpass123")
	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	accA, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-pf-A", UserID: 910001, Nickname: "A", ExpiresAt: time.Now().Add(48 * time.Hour)}, "A")
	accB, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-pf-B", UserID: 910002, Nickname: "B", ExpiresAt: time.Now().Add(48 * time.Hour)}, "B")
	_ = st.UpdateAccountBalance(accA.ID, 50000, 0, 0)
	_ = st.UpdateAccountBalance(accB.ID, 10000, 0, 0)

	httpCli, _ := chromehttp.New(chromehttp.Options{Timeout: 5 * time.Second})
	mgr := bohrium.NewManager(httpCli, cfgMgr, st)
	mgr.SetEndpoints(bohrium.Endpoints{SigmaLLMBase: upSrv.URL, WWWBase: upSrv.URL})
	gw := openai.NewGateway(cfgMgr, mgr)
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	const apiKey = "sk-brm-pf-test"
	const callerTP = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"

	for _, tc := range []struct {
		path, body, sess string
	}{
		{"/v1/chat/completions", `{"model":"dsflash-high","stream":false,"messages":[{"role":"user","content":"hi"}]}`, "pf401-chat"},
		{"/v1/responses", `{"model":"dsflash-high","stream":false,"input":"hi"}`, "pf401-resp"},
	} {
		// Bind the session to A while A is healthy.
		fail401.Store(false)
		_ = st.UpdateAccountHealth(accA.ID, "active", "", time.Time{})
		_ = st.SetPoolMembership([]string{accA.ID})
		if c, h, b := hardeningPost(t, gwSrv.URL, apiKey, tc.path, tc.body, map[string]string{"x-session-id": tc.sess}); c != http.StatusOK || h.Get("X-Bohrium-Routing-Mode") != "new_bind" {
			t.Fatalf("%s bind turn: %d mode=%q %s", tc.path, c, h.Get("X-Bohrium-Routing-Mode"), b)
		}

		// A's preflight now returns 401 -> must fail over to B, not surface 502 billing_unavailable.
		_ = st.SetPoolMembership([]string{accA.ID, accB.ID})
		fail401.Store(true)
		c, h, b := hardeningPost(t, gwSrv.URL, apiKey, tc.path, tc.body, map[string]string{"x-session-id": tc.sess, "traceparent": callerTP})
		if c != http.StatusOK || h.Get("X-Bohrium-Routing-Mode") != "rebound" {
			t.Fatalf("%s failover turn: %d mode=%q %s", tc.path, c, h.Get("X-Bohrium-Routing-Mode"), b)
		}
		afterA, _, _ := st.GetAccount(accA.ID)
		if afterA.HealthStatus != "expired" {
			t.Fatalf("%s: expected accA expired after preflight 401, got %q", tc.path, afterA.HealthStatus)
		}
		mu.Lock()
		tpB := llmTP["Bearer tok-pf-B"]
		mu.Unlock()
		if !bohrium.IsValidTraceparent(tpB) || tpB == callerTP {
			t.Fatalf("%s: expected fresh traceparent for failover account, got %q", tc.path, tpB)
		}
	}
}

func TestNonAuxiliaryResponsesReasoningModelGetsSciMasterAndBridgeStripsCacheKey(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies = map[string]map[string]any{}
		execs  = map[string]bohrium.SciXExecutionPayload{}
	)
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[
				{"id":"resp-pro","name":"Resp-Pro","context_window":100000,"max_output_tokens":1000,"input":["text"],"reasoning_effort":"high","reasoning_efforts":["low","high"],"presentation":{"visible":true}},
				{"id":"dsflash-high","name":"Flash","api":"chat_completions","context_window":100000,"max_output_tokens":1000,"input":["text"],"presentation":{"visible":true,"default":true}}
			]}`)
		case "/billing/preflight":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"allowed":true}`)
		case "/responses", "/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			ep, _ := bohrium.ParseSciXExecutionHeader(r.Header.Get(bohrium.SciXExecutionHeader))
			mu.Lock()
			bodies[r.URL.Path] = body
			execs[r.URL.Path] = ep
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			if r.URL.Path == "/responses" {
				_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"object\":\"response\",\"model\":\"resp-pro\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
				return
			}
			_, _ = io.WriteString(w, `data: {"id":"c_x","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upSrv.Close)

	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	t.Setenv("BOHRIUM_API_KEYS", "sk-brm-sm-test")
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "testpass123")
	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	acc, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-sm", UserID: 920001, Nickname: "SM", ExpiresAt: time.Now().Add(48 * time.Hour)}, "SM")
	_ = st.UpdateAccountBalance(acc.ID, 5000, 0, 0)
	_ = st.SetPoolMembership([]string{acc.ID})

	httpCli, _ := chromehttp.New(chromehttp.Options{Timeout: 5 * time.Second})
	mgr := bohrium.NewManager(httpCli, cfgMgr, st)
	mgr.SetEndpoints(bohrium.Endpoints{SigmaLLMBase: upSrv.URL, WWWBase: upSrv.URL})
	gw := openai.NewGateway(cfgMgr, mgr)
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)
	const apiKey = "sk-brm-sm-test"

	// 1. Non-auxiliary /responses-native reasoning model keeps SciMaster in input[0] (developer role).
	if c, _, b := hardeningPost(t, gwSrv.URL, apiKey, "/v1/responses", `{"model":"resp-pro","stream":false,"input":"hello"}`, nil); c != http.StatusOK {
		t.Fatalf("resp-pro: %d %s", c, b)
	}
	mu.Lock()
	rb := bodies["/responses"]
	re := execs["/responses"]
	mu.Unlock()
	if _, has := rb["instructions"]; has {
		t.Fatalf("native /responses must not carry top-level instructions")
	}
	if rb["prompt_cache_key"] == nil || rb["prompt_cache_key"] == "" {
		t.Fatalf("expected prompt_cache_key on native /responses upstream")
	}
	input, _ := rb["input"].([]any)
	if len(input) == 0 {
		t.Fatalf("empty input: %+v", rb)
	}
	first, _ := input[0].(map[string]any)
	content, _ := first["content"].(string)
	if first["role"] != "developer" || !strings.HasPrefix(content, "You are SciMaster,") {
		t.Fatalf("expected SciMaster developer message in input[0], got %+v", first)
	}
	if re.Purpose != "agent" {
		t.Fatalf("expected purpose=agent, got %q", re.Purpose)
	}

	// 2. /responses bridged to a chat_completions model must not forward prompt_cache_key upstream.
	if c, _, b := hardeningPost(t, gwSrv.URL, apiKey, "/v1/responses", `{"model":"dsflash-high","stream":false,"prompt_cache_key":"pck-bridge","input":"hello"}`, nil); c != http.StatusOK {
		t.Fatalf("bridge: %d %s", c, b)
	}
	mu.Lock()
	cb := bodies["/chat/completions"]
	mu.Unlock()
	if _, has := cb["prompt_cache_key"]; has {
		t.Fatalf("prompt_cache_key must not be forwarded to /chat/completions, got %+v", cb)
	}
}
