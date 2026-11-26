package openai_test

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

type capturedUpstream struct {
	mu                  sync.Mutex
	path                string
	auth                string
	headers             http.Header
	modelsHeaders       http.Header
	preflightHeaders    http.Header
	execRaw             string
	execPayload         bohrium.SciXExecutionPayload
	traceparent         string
	body                map[string]any
	preflightCount      int
	preflightTrace      string
	preflightModel      string
	preflightDenyReason string
}

func setupTestGatewayEnv(t *testing.T, autoPool bool, upstreamHandler http.HandlerFunc) (*openai.Gateway, *httptest.Server, *store.Store, *bohrium.Manager, string, *capturedUpstream) {
	t.Helper()

	capUp := &capturedUpstream{}
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			capUp.mu.Lock()
			capUp.modelsHeaders = r.Header.Clone()
			capUp.mu.Unlock()
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
			capUp.preflightHeaders = r.Header.Clone()
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
		capUp.headers = r.Header.Clone()
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
	t.Cleanup(func() { _ = st.Close() })

	if autoPool {
		acc, _, err := st.UpsertAccountFromAuth(store.AuthState{
			Token:     "fake-brm-jwt-token",
			UserID:    100001,
			OrgID:     200001,
			Nickname:  "TestScientist",
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}, "Primary")
		if err != nil {
			t.Fatalf("UpsertAccountFromAuth: %v", err)
		}
		_ = st.UpdateAccountBalance(acc.ID, 5000, 500, 10)
		if err := st.SetPoolMembership([]string{acc.ID}); err != nil {
			t.Fatalf("SetPoolMembership: %v", err)
		}
	}

	httpCli, err := chromehttp.New(chromehttp.Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("chromehttp.New: %v", err)
	}
	mgr := bohrium.NewManager(httpCli, cfgMgr, st)
	mgr.SetEndpoints(bohrium.Endpoints{
		SigmaLLMBase: upSrv.URL,
		WWWBase:      upSrv.URL,
	})

	gw := openai.NewGateway(cfgMgr, mgr)
	mux := http.NewServeMux()
	gw.RegisterRoutes(mux)
	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return gw, gwSrv, st, mgr, "sk-brm-test-key-001", capUp
}

func setupTestGateway(t *testing.T, upstreamHandler http.HandlerFunc) (*openai.Gateway, *httptest.Server, string, *capturedUpstream) {
	t.Helper()
	gw, gwSrv, _, _, apiKey, capUp := setupTestGatewayEnv(t, true, upstreamHandler)
	return gw, gwSrv, apiKey, capUp
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

	// 2. Valid Bearer key -> 200 standard OpenAI model list
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
	rawCat, _ := io.ReadAll(resp.Body)
	var cat openai.OpenAIModelList
	if err := json.Unmarshal(rawCat, &cat); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if cat.Object != "list" || len(cat.Data) != 5 {
		t.Fatalf("expected object=list and 5 models, got object=%q len=%d", cat.Object, len(cat.Data))
	}
	var rawMap map[string]any
	if err := json.Unmarshal(rawCat, &rawMap); err != nil || len(rawMap) != 2 {
		t.Fatalf("expected strictly {object, data} top-level keys on /v1/models, got %v", rawMap)
	}
	if items, ok := rawMap["data"].([]any); !ok || len(items) != 5 {
		t.Fatalf("unexpected data array: %v", rawMap["data"])
	} else {
		first, _ := items[0].(map[string]any)
		if len(first) != 4 {
			t.Fatalf("expected strictly 4 OpenAI model fields {id, object, created, owned_by}, got %v", first)
		}
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
	var model openai.OpenAIModel
	_ = json.NewDecoder(resp2.Body).Decode(&model)
	if model.ID != "dspro-high" || model.Object != "model" || model.OwnedBy == "" {
		t.Fatalf("expected dspro-high standard OpenAI model, got %+v", model)
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
	var turn atomic.Int32
	_, gwSrv, apiKey, capUp := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		tNum := turn.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Gateway always requests stream=true upstream (matching brain.mjs), even when client requested stream=false.
		if body["stream"] != true {
			t.Errorf("turn %d: expected upstream stream=true, got %v", tNum, body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		if tNum == 1 {
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"Let me check."},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Beijing\"}"}}]},"finish_reason":null}]}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"id":"s1","object":"chat.completion.chunk","created":1700000001,"model":"dspro-high","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":40,"completion_tokens":20,"total_tokens":60,"completion_tokens_details":{"reasoning_tokens":8}}}`+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"id":"s2","object":"chat.completion.chunk","created":1700000002,"model":"dspro-high","choices":[{"index":0,"delta":{"role":"assistant","content":"Beijing is sunny, 22C."},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"s2","object":"chat.completion.chunk","created":1700000002,"model":"dspro-high","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":10,"total_tokens":90}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
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
	var upstreamInsufficient atomic.Bool
	_, gwSrv, st, _, apiKey, capUp := setupTestGatewayEnv(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if upstreamInsufficient.Load() {
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
	// Reset account health from step 3's cooldown so the single pooled test account can exercise direct upstream 402.
	if accs, _ := st.ListAccounts(); len(accs) > 0 {
		_ = st.UpdateAccountHealth(accs[0].ID, "active", "", time.Time{})
	}
	capUp.mu.Lock()
	capUp.preflightDenyReason = ""
	capUp.mu.Unlock()
	upstreamInsufficient.Store(true)
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

func assertNoBrowserHeaders(t *testing.T, label string, h http.Header) {
	t.Helper()
	for _, forbidden := range []string{"User-Agent", "Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform", "Accept-Language"} {
		if v := h.Get(forbidden); v != "" {
			t.Fatalf("%s: expected no browser/Go header %s, got %q", label, forbidden, v)
		}
	}
	if rt := h.Get(bohrium.SciXRuntimeHeader); rt != bohrium.SciXRuntimeValue {
		t.Fatalf("%s: expected %s=%q, got %q", label, bohrium.SciXRuntimeHeader, bohrium.SciXRuntimeValue, rt)
	}
}

func TestDesktopClientFingerprintAndSciMasterPromptToggle(t *testing.T) {
	// Part 1: Default SciMasterPrompt=true + native desktop headers + always-SSE aggregation + tool strict + session/run affinity
	_, gwSrv, apiKey, capUp := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/responses" {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_vis1\",\"model\":\"vision\",\"status\":\"in_progress\"}}\n\n")
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Described image\"}\n\n")
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_vis1\",\"object\":\"response\",\"model\":\"vision\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Described image\"}]}],\"usage\":{\"input_tokens\":50,\"output_tokens\":4,\"total_tokens\":54}}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		_, _ = io.WriteString(w, `data: {"id":"c_agg","object":"chat.completion.chunk","created":1700000100,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"step 1"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"c_agg","object":"chat.completion.chunk","created":1700000100,"model":"dsflash-high","choices":[{"index":0,"delta":{"content":"Hello!"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"c_agg","object":"chat.completion.chunk","created":1700000100,"model":"dsflash-high","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":8,"total_tokens":108,"completion_tokens_details":{"reasoning_tokens":3}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})

	// 1a. Non-streaming Chat Completions with custom system prompt & tools (without strict)
	req1, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [
			{"role": "system", "content": "Custom instruction."},
			{"role": "user", "content": "Explain DFT briefly"}
		],
		"tools": [
			{"type": "function", "function": {"name": "calc", "description": "math", "parameters": {"type": "object"}}},
			{"type": "function", "function": {"name": "strict_fn", "description": "strict", "parameters": {"type": "object"}, "strict": true}}
		]
	}`))
	req1.Header.Set("Authorization", "Bearer "+apiKey)
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("req1: %v", err)
	}
	var chat1 openai.ChatCompletionResponse
	if err := json.NewDecoder(resp1.Body).Decode(&chat1); err != nil {
		t.Fatalf("decode chat1: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp1.StatusCode)
	}
	if chat1.Choices[0].Message.Content != "Hello!" || chat1.Choices[0].Message.ReasoningContent == nil || *chat1.Choices[0].Message.ReasoningContent != "step 1" {
		t.Fatalf("unexpected aggregated SSE chat response: %+v", chat1)
	}
	if chat1.Usage == nil || chat1.Usage.PromptTokens != 100 || chat1.Usage.CompletionTokens != 8 {
		t.Fatalf("expected aggregated usage, got %+v", chat1.Usage)
	}

	capUp.mu.Lock()
	assertNoBrowserHeaders(t, "/models", capUp.modelsHeaders)
	assertNoBrowserHeaders(t, "/billing/preflight", capUp.preflightHeaders)
	assertNoBrowserHeaders(t, "/chat/completions", capUp.headers)
	if capUp.preflightHeaders.Get(bohrium.SciXExecutionHeader) != "" {
		t.Fatalf("expected /billing/preflight to omit synthetic x-scix-execution, got %q", capUp.preflightHeaders.Get(bohrium.SciXExecutionHeader))
	}
	if capUp.body["stream"] != true {
		t.Fatalf("expected upstream stream=true even when caller requested stream=false")
	}
	msgs1, _ := capUp.body["messages"].([]any)
	sys1, _ := msgs1[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(sys1, "You are SciMaster,") || !strings.Contains(sys1, "Custom instruction.") {
		t.Fatalf("expected SciMaster prefix merged with caller system prompt, got prefix=%q", sys1[:min(80, len(sys1))])
	}
	if capUp.execPayload.Question != "Explain DFT briefly" {
		t.Fatalf("expected x-scix-execution.question to stay user question, got %q", capUp.execPayload.Question)
	}
	tools1, _ := capUp.body["tools"].([]any)
	fn0 := tools1[0].(map[string]any)["function"].(map[string]any)
	fn1 := tools1[1].(map[string]any)["function"].(map[string]any)
	if fn0["strict"] != false || fn1["strict"] != true {
		t.Fatalf("expected default strict=false and explicit strict=true, got %v / %v", fn0["strict"], fn1["strict"])
	}
	turn1Sess := capUp.execPayload.SessionID
	turn1Run := capUp.execPayload.RunID
	turn1CreatedAt := capUp.execPayload.SessionCreatedAt
	capUp.mu.Unlock()

	// 1b. Tool continuation in same conversation MUST keep sessionId, rootRunId, runId, sessionCreatedAt and attach toolCallId
	reqTool, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [
			{"role": "system", "content": "Custom instruction."},
			{"role": "user", "content": "Explain DFT briefly"},
			{"role": "assistant", "content": null, "tool_calls": [{"id":"call_42","type":"function","function":{"name":"calc","arguments":"{}"}}]},
			{"role": "tool", "tool_call_id": "call_42", "content": "42"}
		]
	}`))
	reqTool.Header.Set("Authorization", "Bearer "+apiKey)
	reqTool.Header.Set("Content-Type", "application/json")
	respTool, err := http.DefaultClient.Do(reqTool)
	if err != nil {
		t.Fatalf("reqTool: %v", err)
	}
	respTool.Body.Close()
	capUp.mu.Lock()
	if capUp.execPayload.SessionID != turn1Sess || capUp.execPayload.RunID != turn1Run || capUp.execPayload.SessionCreatedAt != turn1CreatedAt {
		t.Fatalf("expected tool continuation to preserve session/run affinity: turn1=(%s,%s,%s) tool=(%s,%s,%s)",
			turn1Sess, turn1Run, turn1CreatedAt, capUp.execPayload.SessionID, capUp.execPayload.RunID, capUp.execPayload.SessionCreatedAt)
	}
	if capUp.execPayload.ToolCallID != "call_42" {
		t.Fatalf("expected toolCallId=call_42 on tool continuation, got %q", capUp.execPayload.ToolCallID)
	}
	capUp.mu.Unlock()

	// 1c. No duplication if caller already starts system prompt with "You are SciMaster"
	reqDup, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [
			{"role": "system", "content": "You are SciMaster, custom edition."},
			{"role": "user", "content": "hi"}
		]
	}`))
	reqDup.Header.Set("Authorization", "Bearer "+apiKey)
	reqDup.Header.Set("Content-Type", "application/json")
	respDup, _ := http.DefaultClient.Do(reqDup)
	respDup.Body.Close()
	capUp.mu.Lock()
	dupSys, _ := capUp.body["messages"].([]any)[0].(map[string]any)["content"].(string)
	capUp.mu.Unlock()
	if dupSys != "You are SciMaster, custom edition." {
		t.Fatalf("expected no duplicate SciMaster injection, got %q", dupSys)
	}

	// 1d. Native /responses model ("vision") places SciMaster + instructions in input[0] with role="developer", NO top-level instructions, WITH prompt_cache_key
	reqVis, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/responses", strings.NewReader(`{
		"model": "vision",
		"stream": false,
		"instructions": "Describe charts accurately.",
		"input": "What is in this plot?"
	}`))
	reqVis.Header.Set("Authorization", "Bearer "+apiKey)
	reqVis.Header.Set("Content-Type", "application/json")
	respVis, err := http.DefaultClient.Do(reqVis)
	if err != nil {
		t.Fatalf("reqVis: %v", err)
	}
	var visOut map[string]any
	_ = json.NewDecoder(respVis.Body).Decode(&visOut)
	respVis.Body.Close()
	if respVis.StatusCode != http.StatusOK || visOut["status"] != "completed" {
		t.Fatalf("expected 200 completed from SSE-aggregated vision response, got %d %+v", respVis.StatusCode, visOut)
	}
	capUp.mu.Lock()
	if _, hasTopInstr := capUp.body["instructions"]; hasTopInstr {
		t.Fatalf("expected native /responses to place instructions in input[0], not top-level instructions")
	}
	if capUp.body["prompt_cache_key"] == nil || capUp.body["prompt_cache_key"] == "" {
		t.Fatalf("expected prompt_cache_key on native /responses upstream")
	}
	visInput, _ := capUp.body["input"].([]any)
	visSys := visInput[0].(map[string]any)
	if visSys["role"] != "developer" || !strings.HasPrefix(visSys["content"].(string), "You are SciMaster,") || !strings.Contains(visSys["content"].(string), "Describe charts accurately.") {
		t.Fatalf("unexpected vision input[0]: %+v", visSys)
	}
	capUp.mu.Unlock()

	// 1e. Auxiliary "title" model MUST skip SciMaster injection
	reqTitle, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/responses", strings.NewReader(`{
		"model": "title",
		"stream": false,
		"instructions": "Generate a short title.",
		"input": "quantum error correction"
	}`))
	reqTitle.Header.Set("Authorization", "Bearer "+apiKey)
	reqTitle.Header.Set("Content-Type", "application/json")
	respTitle, _ := http.DefaultClient.Do(reqTitle)
	respTitle.Body.Close()
	capUp.mu.Lock()
	titleInput, _ := capUp.body["input"].([]any)
	titleSys := titleInput[0].(map[string]any)
	capUp.mu.Unlock()
	if titleSys["role"] != "system" || titleSys["content"] != "Generate a short title." {
		t.Fatalf("expected title auxiliary model to skip SciMaster and use role=system, got %+v", titleSys)
	}

	// Part 2: BOHRIUM_GATEWAY_SCIMASTER_PROMPT=false disables SciMaster prompt injection cleanly
	t.Setenv("BOHRIUM_GATEWAY_SCIMASTER_PROMPT", "false")
	_, gwSrvOff, apiKeyOff, capOff := setupTestGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, `data: {"id":"c_off","object":"chat.completion.chunk","created":1700000200,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","content":"Raw"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	reqOff, _ := http.NewRequest(http.MethodPost, gwSrvOff.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "user", "content": "plain message"}]
	}`))
	reqOff.Header.Set("Authorization", "Bearer "+apiKeyOff)
	reqOff.Header.Set("Content-Type", "application/json")
	respOff, err := http.DefaultClient.Do(reqOff)
	if err != nil {
		t.Fatalf("reqOff: %v", err)
	}
	respOff.Body.Close()
	capOff.mu.Lock()
	msgsOff, _ := capOff.body["messages"].([]any)
	capOff.mu.Unlock()
	if len(msgsOff) != 1 || msgsOff[0].(map[string]any)["role"] != "user" {
		t.Fatalf("expected raw pass-through without SciMaster when toggle is off, got %+v", msgsOff)
	}
}

func TestDefaultAccountNotInPoolRequiresManualOptIn(t *testing.T) {
	_, gwSrv, st, _, apiKey, _ := setupTestGatewayEnv(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, `data: {"id":"c_opt","object":"chat.completion.chunk","created":1700000300,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","content":"Pooled!"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})

	// Add an authenticated account WITHOUT dragging it into the routing pool
	acc, _, err := st.UpsertAccountFromAuth(store.AuthState{
		Token:     "tok-unpooled-1",
		UserID:    300001,
		OrgID:     400001,
		Nickname:  "UnpooledUser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, "Unpooled")
	if err != nil {
		t.Fatalf("UpsertAccountFromAuth: %v", err)
	}
	if acc.PoolEnabled {
		t.Fatalf("expected newly added account to default poolEnabled=false")
	}
	_ = st.UpdateAccountBalance(acc.ID, 5000, 0, 0)

	// /v1/models, /v1/balance, and /v1/chat/completions MUST all reject with 503 empty_pool
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/models", ""},
		{http.MethodGet, "/v1/balance", ""},
		{http.MethodPost, "/v1/chat/completions", `{"model":"dsflash-high","messages":[{"role":"user","content":"hi"}]}`},
	} {
		req, _ := http.NewRequest(tc.method, gwSrv.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		var env openai.ErrorEnvelope
		_ = json.NewDecoder(resp.Body).Decode(&env)
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || env.Error.Code != "empty_pool" {
			t.Fatalf("%s %s: expected 503 empty_pool before manual opt-in, got status=%d env=%+v", tc.method, tc.path, resp.StatusCode, env)
		}
	}

	// Manually drag account into routing pool -> /v1/chat/completions now succeeds!
	if err := st.SetPoolMembership([]string{acc.ID}); err != nil {
		t.Fatalf("SetPoolMembership: %v", err)
	}
	reqOK, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "user", "content": "now in pool"}]
	}`))
	reqOK.Header.Set("Authorization", "Bearer "+apiKey)
	reqOK.Header.Set("Content-Type", "application/json")
	respOK, err := http.DefaultClient.Do(reqOK)
	if err != nil {
		t.Fatalf("POST after opt-in: %v", err)
	}
	respOK.Body.Close()
	if respOK.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after manual opt-in, got %d", respOK.StatusCode)
	}
}

func TestWeightedBalanceProbabilisticRoutingAnd7DayStickyBinding(t *testing.T) {
	var (
		mu             sync.Mutex
		authHits       = map[string]int{}
		storeByAuth    = map[string]string{}
		lastSessByAuth = map[string]string{}
	)
	_, gwSrv, st, _, apiKey, _ := setupTestGatewayEnv(t, false, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		execRaw := r.Header.Get(bohrium.SciXExecutionHeader)
		var execObj bohrium.SciXExecutionPayload
		if dec, err := base64.RawURLEncoding.DecodeString(execRaw); err == nil {
			_ = json.Unmarshal(dec, &execObj)
		}
		mu.Lock()
		authHits[auth]++
		storeByAuth[auth] = execObj.StoreID
		lastSessByAuth[auth] = execObj.SessionID
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, `data: {"id":"c_w","object":"chat.completion.chunk","created":1700000400,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})

	accHigh, _, _ := st.UpsertAccountFromAuth(store.AuthState{
		Token:     "tok-high-90k",
		UserID:    500001,
		Nickname:  "RichAccount",
		ExpiresAt: time.Now().Add(48 * time.Hour),
	}, "High")
	accLow, _, _ := st.UpsertAccountFromAuth(store.AuthState{
		Token:     "tok-low-10k",
		UserID:    500002,
		Nickname:  "SmallAccount",
		ExpiresAt: time.Now().Add(48 * time.Hour),
	}, "Low")
	// 90,000 + 10,000 = 100,000 vs 10,000 + 0 = 10,000 (~90.9% vs ~9.1%)
	_ = st.UpdateAccountBalance(accHigh.ID, 90000, 10000, 0)
	_ = st.UpdateAccountBalance(accLow.ID, 10000, 0, 0)
	_ = st.SetPoolMembership([]string{accHigh.ID, accLow.ID})

	const totalSessions = 100
	for i := 0; i < totalSessions; i++ {
		body := fmt.Sprintf(`{"model":"dsflash-high","stream":false,"messages":[{"role":"user","content":"Weighted routing test prompt #%d"}]}`, i)
		req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("session %d status=%d", i, resp.StatusCode)
		}
		if mode := resp.Header.Get("X-Bohrium-Routing-Mode"); mode != "new_bind" {
			t.Fatalf("session %d: expected X-Bohrium-Routing-Mode=new_bind, got %q", i, mode)
		}
		resp.Body.Close()
	}

	mu.Lock()
	highCount := authHits["Bearer tok-high-90k"]
	lowCount := authHits["Bearer tok-low-10k"]
	highStore := storeByAuth["Bearer tok-high-90k"]
	lowStore := storeByAuth["Bearer tok-low-10k"]
	mu.Unlock()

	// With P(high)=0.909 over 100 trials, expected ~91; threshold >=72 has <1e-7 false failure rate.
	if highCount < 72 || highCount+lowCount != totalSessions {
		t.Fatalf("expected high-balance account (~90.9%%) to strongly dominate routing, got high=%d low=%d", highCount, lowCount)
	}
	// Verify per-account isolated StoreID when both accounts saw traffic
	if lowCount > 0 && (highStore == "" || lowStore == "" || highStore == lowStore) {
		t.Fatalf("expected isolated per-account storeId, got high=%q low=%q", highStore, lowStore)
	}

	// Verify sticky binding: follow-up turn in conversation #0 hits the exact bound account & scixSessionId, incrementing hitCount to 2
	bindingsBefore, _ := st.ListSessionBindings(time.Now().UTC(), 200)
	if len(bindingsBefore) != totalSessions {
		t.Fatalf("expected %d SQLite bindings, got %d", totalSessions, len(bindingsBefore))
	}
	reqFollow, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [
			{"role": "user", "content": "Weighted routing test prompt #0"},
			{"role": "assistant", "content": "ok"},
			{"role": "user", "content": "Follow-up turn"}
		]
	}`))
	reqFollow.Header.Set("Authorization", "Bearer "+apiKey)
	reqFollow.Header.Set("Content-Type", "application/json")
	respFollow, err := http.DefaultClient.Do(reqFollow)
	if err != nil {
		t.Fatalf("follow-up turn: %v", err)
	}
	if mode := respFollow.Header.Get("X-Bohrium-Routing-Mode"); mode != "bound" {
		t.Fatalf("expected follow-up turn X-Bohrium-Routing-Mode=bound, got %q", mode)
	}
	respFollow.Body.Close()

	// Find the binding with HitCount == 2
	foundHit2 := false
	bindingsAfter, _ := st.ListSessionBindings(time.Now().UTC(), 200)
	for _, b := range bindingsAfter {
		if b.HitCount == 2 {
			foundHit2 = true
			if b.ExpiresAt.Sub(b.CreatedAt) > store.SessionBindingTTL {
				t.Fatalf("binding TTL exceeded 7 days: %s", b.ExpiresAt.Sub(b.CreatedAt))
			}
			break
		}
	}
	if !foundHit2 {
		t.Fatalf("expected bound conversation's SQLite hit_count to increment to 2")
	}

	// Purging 7 days + 1 min into the future deletes all 100 bindings
	purged, err := st.PurgeExpiredSessionBindings(time.Now().UTC().Add(store.SessionBindingTTL + time.Minute))
	if err != nil || purged != totalSessions {
		t.Fatalf("expected %d bindings purged after 7d, got purged=%d err=%v", totalSessions, purged, err)
	}
}

func TestPreFirstByteAutomaticFailoverAndRebind(t *testing.T) {
	var failA atomic.Bool
	_, gwSrv, st, _, apiKey, _ := setupTestGatewayEnv(t, false, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if failA.Load() && auth == "Bearer tok-fail-A" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = io.WriteString(w, `{"error":{"code":"insufficient_balance","message":"BILLING_PHOTON_NOT_ENOUGH"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, `data: {"id":"c_fb","object":"chat.completion.chunk","created":1700000500,"model":"dsflash-high","choices":[{"index":0,"delta":{"role":"assistant","content":"Handled via `+auth+`"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})

	accA, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-fail-A", UserID: 600001, Nickname: "FailA", ExpiresAt: time.Now().Add(48 * time.Hour)}, "A")
	accB, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-ok-B", UserID: 600002, Nickname: "HealthyB", ExpiresAt: time.Now().Add(48 * time.Hour)}, "B")
	_ = st.UpdateAccountBalance(accA.ID, 50000, 0, 0)
	_ = st.UpdateAccountBalance(accB.ID, 10000, 0, 0)
	// Start with only accA in pool so Turn 1 genuinely binds "failover-conv-1" -> accA in SQLite
	_ = st.SetPoolMembership([]string{accA.ID})

	reqInit, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "user", "content": "bind to accA first"}]
	}`))
	reqInit.Header.Set("Authorization", "Bearer "+apiKey)
	reqInit.Header.Set("Content-Type", "application/json")
	reqInit.Header.Set("x-session-id", "failover-conv-1")
	respInit, err := http.DefaultClient.Do(reqInit)
	if err != nil {
		t.Fatalf("init request: %v", err)
	}
	respInit.Body.Close()
	if respInit.StatusCode != http.StatusOK || respInit.Header.Get("X-Bohrium-Routing-Mode") != "new_bind" {
		t.Fatalf("expected 200 new_bind on turn 1, got %d %q", respInit.StatusCode, respInit.Header.Get("X-Bohrium-Routing-Mode"))
	}
	initBindings, _ := st.ListSessionBindings(time.Now().UTC(), 10)
	if len(initBindings) != 1 || initBindings[0].AccountID != accA.ID {
		t.Fatalf("expected 1 SQLite binding to accA, got %+v", initBindings)
	}
	sessHash := initBindings[0].SessionHash

	// Now add accB to pool and make accA fail with 402 insufficient_balance -> Turn 2 MUST failover & rebind!
	_ = st.SetPoolMembership([]string{accA.ID, accB.ID})
	failA.Store(true)

	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "user", "content": "trigger failover"}]
	}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-session-id", "failover-conv-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failover request: %v", err)
	}
	var out openai.ChatCompletionResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after automatic failover, got %d", resp.StatusCode)
	}
	if mode := resp.Header.Get("X-Bohrium-Routing-Mode"); mode != "rebound" {
		t.Fatalf("expected X-Bohrium-Routing-Mode=rebound, got %q", mode)
	}
	if !strings.Contains(out.Choices[0].Message.Content.(string), "tok-ok-B") {
		t.Fatalf("expected response from HealthyB, got %+v", out.Choices[0].Message.Content)
	}

	// Verify SQLite binding was updated to accB and accA was marked insufficient/cooling down
	rebound, ok, _ := st.GetSessionBinding(sessHash, time.Now().UTC())
	if !ok || rebound.AccountID != accB.ID {
		t.Fatalf("expected SQLite binding rebound to accB (%s), got ok=%v binding=%+v", accB.ID, ok, rebound)
	}
	afterA, _, _ := st.GetAccount(accA.ID)
	if afterA.HealthStatus != "insufficient" || !afterA.CooldownUntil.After(time.Now().UTC()) {
		t.Fatalf("expected accA marked insufficient with cooldown, got status=%q cooldown=%s", afterA.HealthStatus, afterA.CooldownUntil)
	}

	// Next: remove accB from pool and add accC -> bound session fails over cleanly to accC!
	accC, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-ok-C", UserID: 600003, Nickname: "HealthyC", ExpiresAt: time.Now().Add(48 * time.Hour)}, "C")
	_ = st.UpdateAccountBalance(accC.ID, 20000, 0, 0)
	_ = st.SetPoolMembership([]string{accC.ID})

	req2, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{
		"model": "dsflash-high",
		"stream": false,
		"messages": [{"role": "user", "content": "after accB removed"}]
	}`))
	req2.Header.Set("Authorization", "Bearer "+apiKey)
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("x-session-id", "failover-conv-1")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("post-removal request: %v", err)
	}
	var out2 openai.ChatCompletionResponse
	_ = json.NewDecoder(resp2.Body).Decode(&out2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK || !strings.Contains(out2.Choices[0].Message.Content.(string), "tok-ok-C") {
		t.Fatalf("expected automatic failover to accC after accB removed from pool, got status=%d content=%v", resp2.StatusCode, out2.Choices[0].Message.Content)
	}
	reboundC, ok, _ := st.GetSessionBinding(sessHash, time.Now().UTC())
	if !ok || reboundC.AccountID != accC.ID {
		t.Fatalf("expected SQLite binding rebound to accC (%s), got ok=%v binding=%+v", accC.ID, ok, reboundC)
	}
}

func TestMultiAccountAggregatedBalance(t *testing.T) {
	_, gwSrv, st, _, apiKey, _ := setupTestGatewayEnv(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/bohrapi/v1/account/user/integral":
			switch auth {
			case "Bearer tok-bal-1":
				_, _ = io.WriteString(w, `{"code":0,"data":{"balance":2000,"orgBalance":0,"orgBalanceLimit":0}}`)
			case "Bearer tok-bal-2":
				_, _ = io.WriteString(w, `{"code":0,"data":{"balance":7000,"orgBalance":1000,"orgBalanceLimit":2000}}`)
			default:
				_, _ = io.WriteString(w, `{"code":0,"data":{"balance":50000,"orgBalance":0,"orgBalanceLimit":0}}`)
			}
		case "/brm/v1/wallet/info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"balance":5}}`)
		case "/platform-gateway/v1/benefits/quota/info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"extraExpire7DayAmount":100}}`)
		case "/platform-gateway/v1/benefits/quota/expire_page", "/bohrapi/v1/account/user/integral_page":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[]}}`)
		case "/platform-gateway/v1/membership/info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"level":1,"photons":500}}`)
		case "/bohrapi/v1/literature-sage/api/v1/file/capacity":
			_, _ = io.WriteString(w, `{"code":0,"data":{"remainingCapacity":1000,"usedCapacity":100,"totalCapacity":1100}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	a1, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-bal-1", UserID: 700001, Nickname: "Pool1", ExpiresAt: time.Now().Add(48 * time.Hour)}, "P1")
	a2, _, _ := st.UpsertAccountFromAuth(store.AuthState{Token: "tok-bal-2", UserID: 700002, Nickname: "Pool2", ExpiresAt: time.Now().Add(48 * time.Hour)}, "P2")
	_, _, _ = st.UpsertAccountFromAuth(store.AuthState{Token: "tok-bal-3", UserID: 700003, Nickname: "OutOfPool", ExpiresAt: time.Now().Add(48 * time.Hour)}, "Out")
	_ = st.SetPoolMembership([]string{a1.ID, a2.ID})

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
		t.Fatalf("decode: %v", err)
	}
	// Pool1 (2000+0) + Pool2 (7000+1000) = 9000 photon + 1000 org = 10000 effective (OutOfPool 50000 excluded!)
	if bal.PhotonBalance != 9000 || bal.OrgBalance != 1000 || bal.PoolAccountCount != 2 || bal.HealthyPoolCount != 2 {
		t.Fatalf("unexpected aggregated pool balance: %+v", bal)
	}
	if len(bal.PoolAccounts) != 2 || bal.PoolAccounts[0].WeightPercent != 20.0 || bal.PoolAccounts[1].WeightPercent != 80.0 {
		t.Fatalf("expected 20%% / 80%% pool weights, got %+v", bal.PoolAccounts)
	}
}

func TestGatewayDetailedUsageTelemetryAndTTFT(t *testing.T) {
	_, gwSrv, st, _, apiKey, _ := setupTestGatewayEnv(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "up-req-telemetry-99")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		// Send Bohrium keepalive heartbeat first, then sleep briefly so TTFT measures real first token arrival
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(25 * time.Millisecond)
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-tel1","object":"chat.completion.chunk","created":1700000000,"model":"kimi-k3","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"id":"chatcmpl-tel1","object":"chat.completion.chunk","created":1700000000,"model":"kimi-k3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens_details":{"reasoning_tokens":10}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})

	// Send /v1/responses targeting kimi-k3 (native chat_completions -> bridged=true, multiplier=2.0) via alias "Kimi-K3"
	body := `{
		"model": "Kimi-K3",
		"stream": true,
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],
		"reasoning": {"effort": "high"},
		"tools": [{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]
	}`
	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OpenAI-TestClient/9.9")
	req.Header.Set("X-Forwarded-For", "203.0.113.42, 10.0.0.1")
	req.Header.Set("x-session-id", "telemetry-sess-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	page, err := st.ListGatewayUsageLogs(store.UsageLogsQueryOptions{RangeDays: 1, Page: 1, PageSize: 10})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected 1 logged usage record, got page=%+v err=%v", page, err)
	}
	rec := page.Items[0]
	if rec.Model != "kimi-k3" || rec.RequestedModel != "Kimi-K3" {
		t.Fatalf("unexpected model alias recording: model=%q requested=%q", rec.Model, rec.RequestedModel)
	}
	if rec.ReasoningEffort != "high" || !rec.Bridged || !rec.Stream || !rec.HasTools || !rec.HasVision {
		t.Fatalf("unexpected capability/bridge flags: %+v", rec)
	}
	if rec.ClientIP != "203.0.113.42" || rec.UserAgent != "OpenAI-TestClient/9.9" || rec.RequestID != "up-req-telemetry-99" {
		t.Fatalf("unexpected caller/upstream metadata: ip=%q ua=%q reqId=%q", rec.ClientIP, rec.UserAgent, rec.RequestID)
	}
	if rec.PreflightMode != "preflight" || !rec.SciMasterPrompt || rec.UsageMultiplier != 2 || rec.WeightedTokens != 300 {
		t.Fatalf("unexpected billing/policy telemetry: pf=%q sci=%v mult=%.2f weighted=%.2f", rec.PreflightMode, rec.SciMasterPrompt, rec.UsageMultiplier, rec.WeightedTokens)
	}
	if rec.FirstTokenMs < 20 || rec.DurationMs < rec.FirstTokenMs {
		t.Fatalf("expected realistic TTFT >= 20ms and duration >= TTFT, got ttft=%d dur=%d", rec.FirstTokenMs, rec.DurationMs)
	}
}

func TestModelVisibilityToggleAndStandardOpenAIModels(t *testing.T) {
	upSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"object": "list",
				"data": [
					{"id":"standard","name":"标准","api":"chat_completions","context_window":1000000,"max_output_tokens":384000,"input":["text"],"reasoning_effort":"high","reasoning_efforts":["low","medium","high"],"presentation":{"visible":false,"sortOrder":0}},
					{"id":"advanced","name":"高级","api":"chat_completions","context_window":1050000,"max_output_tokens":128000,"input":["text","image"],"reasoning_effort":"high","reasoning_efforts":["low","medium","high"],"presentation":{"visible":false,"sortOrder":0}},
					{"id":"vision","name":"读图","hidden":true,"context_window":1050000,"max_output_tokens":128000,"input":["text","image"],"reasoning_effort":"high","reasoning_efforts":["low","medium","high"],"presentation":{"visible":false,"sortOrder":0}},
					{"id":"title","name":"标题","hidden":true,"context_window":128000,"max_output_tokens":256,"input":["text"],"presentation":{"visible":false,"sortOrder":0}},
					{"id":"summary","name":"工具批次摘要","hidden":true,"context_window":128000,"max_output_tokens":512,"input":["text"],"presentation":{"visible":false,"sortOrder":0}},
					{"id":"dspro-high","name":"DeepSeek-V4-Pro","api":"chat_completions","context_window":1000000,"max_output_tokens":384000,"input":["text"],"reasoning_effort":"high","reasoning_efforts":["low","high","max"],"presentation":{"visible":true,"name":"DeepSeek-V4-Pro","sortOrder":20,"usageMultiplier":1,"default":true}},
					{"id":"dsflash-high","name":"DeepSeek-V4.1-Flash","api":"chat_completions","context_window":1000000,"max_output_tokens":384000,"input":["text"],"reasoning_effort":"high","reasoning_efforts":["low","high","max"],"presentation":{"visible":true,"name":"DeepSeek-V4.1-Flash","sortOrder":10,"usageMultiplier":0.3}},
					{"id":"kimi-k3","name":"Kimi-K3","api":"chat_completions","context_window":1050000,"max_output_tokens":128000,"input":["text","image"],"reasoning_effort":"high","reasoning_efforts":["low","high","max"],"presentation":{"visible":true,"name":"Kimi-K3","sortOrder":30,"usageMultiplier":2}}
				],
				"image_describe": {"model":"vision","reasoning_effort":"low"},
				"session_purposes": {"session-title":{"model":"title"},"tool-batch-summary":{"model":"summary"}}
			}`)
		case "/billing/preflight":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"allowed":true}`)
		case "/chat/completions", "/responses":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mID, _ := body["model"].(string)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			if r.URL.Path == "/chat/completions" {
				_, _ = io.WriteString(w, `data: {"id":"c_vis","object":"chat.completion.chunk","created":1700000000,"model":"`+mID+`","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			} else {
				_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_vis\",\"object\":\"response\",\"model\":\""+mID+"\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upSrv.Close)

	tmpDir := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmpDir)
	t.Setenv("BOHRIUM_API_KEYS", "sk-brm-vis-test")
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "testpass123")
	cfgMgr, _ := config.Load()
	st, _ := store.Open(tmpDir)
	t.Cleanup(func() { _ = st.Close() })
	acc, _, _ := st.UpsertAccountFromAuth(store.AuthState{
		Token: "tok-vis-1", UserID: 8801, OrgID: 8802, Nickname: "VisTester", ExpiresAt: time.Now().Add(24 * time.Hour),
	}, "Vis")
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

	doReq := func(method, path, body string) (*http.Response, string) {
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, gwSrv.URL+path, rdr)
		req.Header.Set("Authorization", "Bearer sk-brm-vis-test")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(raw)
	}

	// 1. Default GET /v1/models excludes standard & advanced, keeps vision/title/summary, and returns strict standard OpenAI fields ONLY
	rList, rawList := doReq(http.MethodGet, "/v1/models", "")
	if rList.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models status=%d body=%s", rList.StatusCode, rawList)
	}
	var cat openai.OpenAIModelList
	if err := json.Unmarshal([]byte(rawList), &cat); err != nil {
		t.Fatalf("unmarshal /v1/models: %v", err)
	}
	if cat.Object != "list" || len(cat.Data) != 6 {
		t.Fatalf("expected object=list and 6 models by default (excluding standard/advanced), got object=%q len=%d", cat.Object, len(cat.Data))
	}
	var rawMap map[string]any
	if err := json.Unmarshal([]byte(rawList), &rawMap); err != nil || len(rawMap) != 2 {
		t.Fatalf("expected strictly {object, data} top-level keys on /v1/models, got %v", rawMap)
	}
	byID := map[string]openai.OpenAIModel{}
	for _, m := range cat.Data {
		byID[m.ID] = m
	}
	if _, hasStd := byID["standard"]; hasStd {
		t.Fatalf("standard must be hidden from /v1/models by default")
	}
	if _, hasAdv := byID["advanced"]; hasAdv {
		t.Fatalf("advanced must be hidden from /v1/models by default")
	}
	for _, keep := range []string{"vision", "title", "summary", "dspro-high", "dsflash-high", "kimi-k3"} {
		if _, ok := byID[keep]; !ok {
			t.Fatalf("expected model %q to remain enabled by default", keep)
		}
	}
	for _, item := range rawMap["data"].([]any) {
		obj := item.(map[string]any)
		if len(obj) != 4 || obj["id"] == nil || obj["object"] != "model" || obj["created"] == nil || obj["owned_by"] == nil {
			t.Fatalf("expected strictly 4 OpenAI model fields {id, object, created, owned_by}, got %v", obj)
		}
	}

	// GET /v1/models/dspro-high also returns strictly standard OpenAI fields ONLY
	rOne, rawOne := doReq(http.MethodGet, "/v1/models/dspro-high", "")
	if rOne.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models/dspro-high status=%d body=%s", rOne.StatusCode, rawOne)
	}
	var oneMap map[string]any
	if err := json.Unmarshal([]byte(rawOne), &oneMap); err != nil || len(oneMap) != 4 || oneMap["id"] != "dspro-high" || oneMap["object"] != "model" {
		t.Fatalf("expected strictly {id, object, created, owned_by} on GET /v1/models/dspro-high, got %v", oneMap)
	}

	// 2. Disabled standard & advanced cannot be queried or invoked (including aliases)
	if r, raw := doReq(http.MethodGet, "/v1/models/standard", ""); r.StatusCode != http.StatusNotFound || !strings.Contains(raw, "model_disabled") {
		t.Fatalf("expected 404 model_disabled for GET /v1/models/standard, got %d %s", r.StatusCode, raw)
	}
	for _, mName := range []string{"standard", "标准"} {
		if r, raw := doReq(http.MethodPost, "/v1/chat/completions", `{"model":"`+mName+`","stream":false,"messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != http.StatusForbidden || !strings.Contains(raw, "model_disabled") {
			t.Fatalf("expected 403 model_disabled for chat %q, got %d %s", mName, r.StatusCode, raw)
		}
	}
	if r, raw := doReq(http.MethodPost, "/v1/responses", `{"model":"advanced","stream":false,"input":"hi"}`); r.StatusCode != http.StatusForbidden || !strings.Contains(raw, "model_disabled") {
		t.Fatalf("expected 403 model_disabled for responses advanced, got %d %s", r.StatusCode, raw)
	}

	// 3. Enable standard & disable vision via GatewayConfig overrides -> /v1/models & invocation update immediately
	_, _ = cfgMgr.Update(func(c *config.Config) error {
		c.Gateway.ModelEnabled = map[string]bool{
			"standard": true,
			"vision":   false,
		}
		return nil
	})
	_, rawList2 := doReq(http.MethodGet, "/v1/models", "")
	var cat2 openai.OpenAIModelList
	_ = json.Unmarshal([]byte(rawList2), &cat2)
	ids2 := map[string]bool{}
	for _, m := range cat2.Data {
		ids2[m.ID] = true
	}
	if len(cat2.Data) != 6 || !ids2["standard"] || ids2["vision"] {
		t.Fatalf("expected 6 models (standard added, vision removed), got ids=%v", ids2)
	}
	if r, raw := doReq(http.MethodPost, "/v1/chat/completions", `{"model":"standard","stream":false,"messages":[{"role":"user","content":"hi"}]}`); r.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for enabled standard, got %d %s", r.StatusCode, raw)
	}
	if r, raw := doReq(http.MethodPost, "/v1/responses", `{"model":"vision","stream":false,"input":"hi"}`); r.StatusCode != http.StatusForbidden || !strings.Contains(raw, "model_disabled") {
		t.Fatalf("expected 403 model_disabled for disabled vision, got %d %s", r.StatusCode, raw)
	}
}
