package openai_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/openai"
	"bohrium-club-auto/internal/store"
)

type capturedUpstream struct {
	mu                  sync.Mutex
	path                string
	auth                string
	execRaw             string
	execPayload         bohrium.SciXExecutionPayload
	traceparent         string
	body                map[string]any
	preflightCount      int
	preflightTrace      string
	preflightModel      string
	preflightDenyReason string
}

func setupTestGateway(t *testing.T, upstreamHandler http.HandlerFunc) (*openai.Gateway, *httptest.Server, string, *capturedUpstream) {
	t.Helper()

	capUp := &capturedUpstream{}
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			if r.URL.Query().Get("tag") != bohrium.SciXModelCatalogTag {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":"tag_required","message":"query parameter tag is required"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"object": "list",
				"data": [
					{"id":"dspro-high","name":"DeepSeek-V4-Pro","api":"chat_completions","context_window":1000000,"max_output_tokens":384000,"input":["text"],"reasoning_effort":"high","reasoning_efforts":["low","high","max"],"owned_by":"sigma","presentation":{"visible":true,"name":"DeepSeek-V4-Pro","sortOrder":20,"usageMultiplier":1,"default":true}},
					{"id":"dsflash-high","name":"DeepSeek-V4.1-Flash","api":"chat_completions","context_window":1000000,"max_output_tokens":384000,"input":["text"],"reasoning_effort":"high","reasoning_efforts":["low","high","max"],"owned_by":"sigma","presentation":{"visible":true,"name":"DeepSeek-V4.1-Flash","sortOrder":10,"usageMultiplier":0.3}},
					{"id":"kimi-k3","name":"Kimi-K3","api":"chat_completions","context_window":1050000,"max_output_tokens":128000,"input":["text","image"],"reasoning_effort":"high","reasoning_efforts":["low","high","max"],"owned_by":"sigma","presentation":{"visible":true,"name":"Kimi-K3","sortOrder":30,"usageMultiplier":2}},
					{"id":"vision","name":"读图","hidden":true,"context_window":1050000,"max_output_tokens":128000,"input":["text","image"],"reasoning_effort":"high","reasoning_efforts":["low","medium","high"],"owned_by":"sigma"},
					{"id":"title","name":"标题","hidden":true,"context_window":128000,"max_output_tokens":256,"input":["text"],"owned_by":"sigma"}
				],
				"image_describe": {"model":"vision","reasoning_effort":"low"},
				"session_purposes": {"session-title":{"model":"title"}}
			}`)
			return
		}
		if r.URL.Path == "/billing/preflight" {
			var pb map[string]string
			_ = json.NewDecoder(r.Body).Decode(&pb)
			capUp.mu.Lock()
			capUp.preflightCount++
			capUp.preflightTrace = r.Header.Get("traceparent")
			capUp.preflightModel = pb["model"]
			denyReason := capUp.preflightDenyReason
			capUp.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if denyReason != "" {
				_, _ = io.WriteString(w, `{"allowed":false,"reason":"`+denyReason+`"}`)
				return
			}
			_, _ = io.WriteString(w, `{"allowed":true}`)
			return
		}

		bodyBytes, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		var bodyMap map[string]any
		_ = json.Unmarshal(bodyBytes, &bodyMap)

		execRaw := r.Header.Get(bohrium.SciXExecutionHeader)
		var execObj bohrium.SciXExecutionPayload
		if dec, err := base64.RawURLEncoding.DecodeString(execRaw); err == nil {
			_ = json.Unmarshal(dec, &execObj)
		}

		capUp.mu.Lock()
		capUp.path = r.URL.Path
		capUp.auth = r.Header.Get("Authorization")
		capUp.execRaw = execRaw
		capUp.execPayload = execObj
		capUp.traceparent = r.Header.Get("traceparent")
		capUp.body = bodyMap
		capUp.mu.Unlock()

		if upstreamHandler != nil {
			upstreamHandler(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(upSrv.Close)

	tmpDir := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmpDir)
	t.Setenv("BOHRIUM_API_KEYS", "sk-brm-test-key-001")
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "testpass123")

	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(tmpDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.SetAuth(store.AuthState{
		Token:     "fake-brm-jwt-token",
		UserID:    100001,
		OrgID:     200001,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("SetAuth: %v", err)
	}

	httpCli, err := chromehttp.New(chromehttp.Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("chromehttp.New: %v", err)
	}
	bClient := bohrium.NewClient(httpCli, cfgMgr, st)
	bClient.SetEndpoints(bohrium.Endpoints{
		SigmaLLMBase: upSrv.URL,
		WWWBase:      upSrv.URL,
	})

	gw := openai.NewGateway(cfgMgr, bClient)
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return gw, gwSrv, "sk-brm-test-key-001", capUp
}

func TestAPIKeyAuthAndModels(t *testing.T) {
	_, gwSrv, apiKey, _ := setupTestGateway(t, nil)

	// 1. Missing API key -> 401 OpenAI error
	resp, err := http.Get(gwSrv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", resp.StatusCode)
	}

	// 2. Valid Bearer key -> 200 catalog with scix_catalog metadata
	req, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/models auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var cat bohrium.SciXModelCatalog
	if err := json.NewDecoder(resp.Body).Decode(&cat); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(cat.Data) != 5 {
		t.Fatalf("expected 5 models, got %d", len(cat.Data))
	}
	if cat.SciXCatalog == nil || cat.SciXCatalog.ScopeGeneration == "" || cat.SciXCatalog.ContentVersion == "" {
		t.Fatalf("expected scix_catalog scopeGeneration & contentVersion, got %+v", cat.SciXCatalog)
	}

	// 3. Alias lookup via x-api-key header ("DeepSeek-V4-Pro" -> "dspro-high")
	req2, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/models/DeepSeek-V4-Pro", nil)
	req2.Header.Set("x-api-key", apiKey)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("GET /v1/models/DeepSeek-V4-Pro: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for alias, got %d", resp2.StatusCode)
	}
	var model bohrium.SciXModel
	_ = json.NewDecoder(resp2.Body).Decode(&model)
	if model.ID != "dspro-high" {
		t.Fatalf("expected dspro-high, got %q", model.ID)
	}
}

func TestChatCompletionsNativeCompatAndStreaming(t *testing.T) {
	_, gwSrv, apiKey, capUp := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think"},"finish_reason":null}],"usage":null}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":null}],"usage":null}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":3}}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c2","object":"chat.completion","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"message":{"role":"assistant","content":"OK","reasoning_content":"brief"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`)
	})

	// Send OpenAI request using developer role, max_completion_tokens, store=true, reasoning_effort="medium"
	reqBody := `{
		"model": "scix/dsflash-high",
		"stream": true,
		"store": true,
		"max_completion_tokens": 128,
		"reasoning_effort": "medium",
		"messages": [
			{"role": "developer", "content": "Be brief."},
			{"role": "user", "content": "Say OK"}
		]
	}`
	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(raw))
	}

	rawSSE, _ := io.ReadAll(resp.Body)
	sseText := string(rawSSE)
	if strings.Contains(sseText, ": heartbeat") {
		t.Fatalf("expected heartbeat comments to be filtered, got: %s", sseText)
	}
	if !strings.Contains(sseText, `"reasoning_content":"think"`) || !strings.Contains(sseText, `"content":"OK"`) || !strings.Contains(sseText, "data: [DONE]") {
		t.Fatalf("unexpected SSE output: %s", sseText)
	}

	// Verify Bohrium desktop client upstream fidelity!
	capUp.mu.Lock()
	defer capUp.mu.Unlock()
	if capUp.path != "/chat/completions" {
		t.Fatalf("expected upstream /chat/completions, got %s", capUp.path)
	}
	if capUp.auth != "Bearer fake-brm-jwt-token" {
		t.Fatalf("expected Bearer brmToken, got %q", capUp.auth)
	}
	if capUp.execPayload.Version != 1 || capUp.execPayload.Scenario != "local_agent" || capUp.execPayload.Purpose != "agent" {
		t.Fatalf("invalid x-scix-execution payload: %+v", capUp.execPayload)
	}
	if !strings.HasPrefix(capUp.traceparent, "00-") {
		t.Fatalf("expected W3C traceparent, got %q", capUp.traceparent)
	}
	if _, hasStore := capUp.body["store"]; hasStore {
		t.Fatalf("expected store to be stripped for Bohrium chat_completions")
	}
	if _, hasMCT := capUp.body["max_completion_tokens"]; hasMCT {
		t.Fatalf("expected max_completion_tokens normalized to max_tokens")
	}
	if capUp.body["max_tokens"] != float64(128) {
		t.Fatalf("expected max_tokens=128, got %v", capUp.body["max_tokens"])
	}
	// medium -> high on ["low","high","max"] model
	if capUp.body["reasoning_effort"] != "high" {
		t.Fatalf("expected reasoning_effort clamped to high, got %v", capUp.body["reasoning_effort"])
	}
	msgs, _ := capUp.body["messages"].([]any)
	firstMsg, _ := msgs[0].(map[string]any)
	if firstMsg["role"] != "system" {
		t.Fatalf("expected developer role mapped to system, got %v", firstMsg["role"])
	}
}

func TestResponsesBridgeToChatCompletionsStreamingAndChaining(t *testing.T) {
	turn := 0
	_, gwSrv, apiKey, capUp := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		turn++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me check."},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Beijing\"}"}}]},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":40,"completion_tokens":20,"total_tokens":60,"completion_tokens_details":{"reasoning_tokens":8}}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"s2","object":"chat.completion","created":1700000002,"model":"dspro-high","choices":[{"index":0,"message":{"role":"assistant","content":"Beijing is sunny, 22C."},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":10,"total_tokens":90}}`)
	})

	// Turn 1: Streaming /v1/responses on chat_completions-native dspro-high with tools!
	req1Body := `{
		"model": "dspro-high",
		"stream": true,
		"instructions": "Use tools when asked.",
		"input": "Check Beijing weather",
		"reasoning": {"effort": "low"},
		"tools": [{
			"type": "function",
			"name": "get_weather",
			"description": "Get city weather",
			"parameters": {"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}
		}],
		"tool_choice": {"type":"function","name":"get_weather"}
	}`
	req1, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/responses", strings.NewReader(req1Body))
	req1.Header.Set("Authorization", "Bearer "+apiKey)
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("POST /v1/responses turn 1: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp1.Body)
		t.Fatalf("turn 1 HTTP %d: %s", resp1.StatusCode, string(raw))
	}

	var events []map[string]any
	var eventNames []string
	var resp1ID string
	sc := bufio.NewScanner(resp1.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			eventNames = append(eventNames, strings.TrimPrefix(line, "event: "))
		} else if strings.HasPrefix(line, "data: ") {
			var ev map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("invalid SSE JSON: %v", err)
			}
			events = append(events, ev)
			if ev["type"] == "response.completed" {
				if rObj, ok := ev["response"].(map[string]any); ok {
					resp1ID, _ = rObj["id"].(string)
				}
			}
		}
	}

	// Verify monotonic sequence_number and required lifecycle events
	for i, ev := range events {
		if int(ev["sequence_number"].(float64)) != i {
			t.Fatalf("sequence_number mismatch at %d: got %v", i, ev["sequence_number"])
		}
	}
	joined := strings.Join(eventNames, ",")
	for _, want := range []string{
		"response.created",
		"response.in_progress",
		"response.reasoning_text.delta",
		"response.reasoning_text.done",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing Responses SSE event %q in %s", want, joined)
		}
	}
	if resp1ID == "" {
		t.Fatalf("expected non-empty response ID from turn 1")
	}

	// Verify GET /v1/responses/{id} retrieves stored response
	getReq, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/responses/"+resp1ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+apiKey)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GET /v1/responses/{id}: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on GET response, got %d", getResp.StatusCode)
	}

	// Turn 2: Chain with previous_response_id + function_call_output (non-streaming)!
	req2Body := `{
		"model": "dspro-high",
		"stream": false,
		"previous_response_id": "` + resp1ID + `",
		"input": [{
			"type": "function_call_output",
			"call_id": "call_abc",
			"output": "{\"temp\":22,\"cond\":\"sunny\"}"
		}]
	}`
	req2, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/responses", strings.NewReader(req2Body))
	req2.Header.Set("Authorization", "Bearer "+apiKey)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST /v1/responses turn 2: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("turn 2 HTTP %d: %s", resp2.StatusCode, string(raw))
	}
	var out2 openai.ResponseObject
	if err := json.NewDecoder(resp2.Body).Decode(&out2); err != nil {
		t.Fatalf("decode turn 2: %v", err)
	}
	if out2.Status != "completed" || out2.PreviousResponseID == nil || *out2.PreviousResponseID != resp1ID {
		t.Fatalf("unexpected turn 2 response: %+v", out2)
	}
	if len(out2.Output) != 1 || out2.Output[0]["type"] != "message" {
		t.Fatalf("expected single message output, got %+v", out2.Output)
	}

	// Verify upstream received full replayed conversation (system + user + assistant tool_call + tool output)!
	capUp.mu.Lock()
	msgs, _ := capUp.body["messages"].([]any)
	capUp.mu.Unlock()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 replayed messages (system, user, assistant+tool_calls, tool), got %d: %+v", len(msgs), msgs)
	}

	// Verify DELETE /v1/responses/{id}
	delReq, _ := http.NewRequest(http.MethodDelete, gwSrv.URL+"/v1/responses/"+resp1ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+apiKey)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE response: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on DELETE, got %d", delResp.StatusCode)
	}
}

func TestReverseBridgeChatToResponsesNativeModel(t *testing.T) {
	_, gwSrv, apiKey, capUp := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_t1\"}}\n\n")
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Quantum Gravity\"}\n\n")
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_t1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":3,\"total_tokens\":13}}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_t2","object":"response","created_at":1700000010,"model":"title","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Quantum Gravity"}]}],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}`)
	})

	// Call /v1/chat/completions targeting responses-native "title" model (streaming)!
	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "title",
		"stream": true,
		"messages": [{"role":"user","content":"Title for quantum gravity"}]
	}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions (title): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, string(raw))
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), `"content":"Quantum Gravity"`) || !strings.Contains(string(raw), "data: [DONE]") {
		t.Fatalf("unexpected reverse-bridged SSE: %s", string(raw))
	}

	capUp.mu.Lock()
	defer capUp.mu.Unlock()
	if capUp.path != "/responses" {
		t.Fatalf("expected upstream /responses for title model, got %s", capUp.path)
	}
	if capUp.execPayload.Purpose != "session-title" {
		t.Fatalf("expected purpose=session-title for title model, got %q", capUp.execPayload.Purpose)
	}
	if capUp.body["store"] != false {
		t.Fatalf("expected store=false on stateless Bohrium /responses upstream, got %v", capUp.body["store"])
	}
}

func TestBalanceEndpoint(t *testing.T) {
	_, gwSrv, apiKey, _ := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bohrapi/v1/account/user/integral":
			_, _ = io.WriteString(w, `{"code":0,"data":{"balance":2450.5,"orgBalance":500,"orgBalanceLimit":2000}}`)
		case "/brm/v1/wallet/info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"balance":18.5}}`)
		case "/platform-gateway/v1/benefits/quota/info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"extraExpire7DayAmount":300,"quotas":{"sn.paper":{"remaining":50,"total":100},"knowledge_base.chat":{"remaining":30,"total":50},"sn.deep_research":{"remaining":5,"total":10}}}}`)
		case "/platform-gateway/v1/benefits/quota/expire_page":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"id":1,"remaining":300,"createdTime":"2026-11-20","expireTime":"2026-11-27","sourceType":"daily"}]}}`)
		case "/bohrapi/v1/account/user/integral_page":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[]}}`)
		case "/platform-gateway/v1/membership/info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"level":2,"expireTime":"2027-01-01","photons":1000}}`)
		case "/bohrapi/v1/literature-sage/api/v1/file/capacity":
			_, _ = io.WriteString(w, `{"code":0,"data":{"remainingCapacity":103809024,"usedCapacity":1048576,"totalCapacity":104857600}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// Unauthorized without API key
	unauthResp, err := http.Get(gwSrv.URL + "/v1/balance")
	if err != nil {
		t.Fatalf("GET /v1/balance unauth: %v", err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthResp.StatusCode)
	}

	// Authorized balance query
	req, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/balance", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/balance: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(raw))
	}

	var bal openai.BalanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&bal); err != nil {
		t.Fatalf("decode balance: %v", err)
	}
	if bal.Object != "balance" || !bal.IsAvailable || bal.PhotonBalance != 2450.5 || bal.OrgBalance != 500 || bal.WalletBalanceCNY != 18.5 {
		t.Fatalf("unexpected balance payload: %+v", bal)
	}
	if bal.Expiring7DPhotons != 300 || len(bal.BalanceInfos) != 2 || bal.BalanceInfos[0].Currency != "PHOTON" {
		t.Fatalf("unexpected balance_infos/expiring: %+v", bal)
	}
	if bal.Storage.UsedBytes != 1048576 || bal.Storage.TotalBytes != 104857600 {
		t.Fatalf("unexpected storage: %+v", bal.Storage)
	}
}

func TestBillingPreflightClientFidelityAndInsufficientBalance(t *testing.T) {
	upstreamInsufficient := false
	_, gwSrv, apiKey, capUp := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if upstreamInsufficient {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":{"code":"insufficient_balance","message":"BILLING_PHOTON_NOT_ENOUGH: photon balance exhausted","type":"insufficient_quota"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"c_pf","object":"chat.completion","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	})

	// 1. New user turn MUST trigger /billing/preflight with same traceparent as /chat/completions
	req1, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	req1.Header.Set("Authorization", "Bearer "+apiKey)
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on turn 1, got %d", resp1.StatusCode)
	}
	capUp.mu.Lock()
	pfCount1 := capUp.preflightCount
	pfTrace := capUp.preflightTrace
	llmTrace := capUp.traceparent
	pfModel := capUp.preflightModel
	capUp.mu.Unlock()
	if pfCount1 != 1 || pfModel != "dsflash-high" {
		t.Fatalf("expected 1 preflight for dsflash-high, got count=%d model=%q", pfCount1, pfModel)
	}
	if pfTrace == "" || pfTrace != llmTrace {
		t.Fatalf("expected preflight and LLM request to share traceparent, got pf=%q llm=%q", pfTrace, llmTrace)
	}

	// 2. Mid-loop tool continuation (last message role=tool) MUST skip redundant /billing/preflight
	req2, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": null, "tool_calls": [{"id":"call_1","type":"function","function":{"name":"get_w","arguments":"{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "22C"}
		]
	}`))
	req2.Header.Set("Authorization", "Bearer "+apiKey)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on tool continuation, got %d", resp2.StatusCode)
	}
	capUp.mu.Lock()
	pfCount2 := capUp.preflightCount
	capUp.mu.Unlock()
	if pfCount2 != 1 {
		t.Fatalf("expected tool continuation to skip preflight (still 1), got %d", pfCount2)
	}

	// 3. When /billing/preflight rejects (allowed=false), return 402 insufficient_balance BEFORE calling LLM
	capUp.mu.Lock()
	capUp.preflightDenyReason = "光子余额不足，请充值"
	capUp.mu.Unlock()
	req3, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "user", "content": "another turn"}]
	}`))
	req3.Header.Set("Authorization", "Bearer "+apiKey)
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("turn 3: %v", err)
	}
	var errEnv3 openai.ErrorEnvelope
	_ = json.NewDecoder(resp3.Body).Decode(&errEnv3)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusPaymentRequired || errEnv3.Error.Code != "insufficient_balance" {
		t.Fatalf("expected 402 insufficient_balance from preflight deny, got status=%d env=%+v", resp3.StatusCode, errEnv3)
	}

	// 4. Mid-run tool step hitting upstream insufficient_balance directly -> mapped cleanly to 402 insufficient_balance
	capUp.mu.Lock()
	capUp.preflightDenyReason = ""
	capUp.mu.Unlock()
	upstreamInsufficient = true
	req4, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "tool", "tool_call_id": "call_1", "content": "done"}]
	}`))
	req4.Header.Set("Authorization", "Bearer "+apiKey)
	req4.Header.Set("Content-Type", "application/json")
	resp4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatalf("turn 4: %v", err)
	}
	var errEnv4 openai.ErrorEnvelope
	_ = json.NewDecoder(resp4.Body).Decode(&errEnv4)
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusPaymentRequired || errEnv4.Error.Code != "insufficient_balance" || errEnv4.Error.Type != "insufficient_quota" {
		t.Fatalf("expected 402 insufficient_balance from upstream rejection, got status=%d env=%+v", resp4.StatusCode, errEnv4)
	}
}
