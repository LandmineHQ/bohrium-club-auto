package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxSSELineBytes               = 16 * 1024 * 1024
	defaultKeepaliveInterval      = 10 * time.Second
	defaultPreHeaderKeepaliveDelay = 8 * time.Second
)

func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func setNonStreamKeepaliveHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func flushWriter(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

type preHeaderKeepalive struct {
	mu        sync.Mutex
	done      chan struct{}
	once      sync.Once
	wg        sync.WaitGroup
	committed bool
}

// startPreHeaderKeepalive waits `delay` while waiting for slow upstream response headers
// (such as large-context LLM prefill). If the upstream still hasn't sent headers when `delay`
// expires, it commits HTTP 200 headers with an initial keepalive byte/comment and continues
// emitting keepalives every `interval` until Stop() or ctx cancellation.
func startPreHeaderKeepalive(ctx context.Context, w http.ResponseWriter, stream bool, routingMode string, delay, interval time.Duration) *preHeaderKeepalive {
	p := &preHeaderKeepalive{
		done: make(chan struct{}),
	}
	if delay <= 0 {
		return p
	}
	if interval <= 0 {
		interval = defaultKeepaliveInterval
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-timer.C:
		}

		payload := " "
		if stream {
			payload = ": keepalive\n\n"
		}

		p.mu.Lock()
		select {
		case <-ctx.Done():
			p.mu.Unlock()
			return
		case <-p.done:
			p.mu.Unlock()
			return
		default:
		}
		if routingMode != "" {
			w.Header().Set("X-Bohrium-Routing-Mode", routingMode)
		}
		if stream {
			setSSEHeaders(w)
		} else {
			setNonStreamKeepaliveHeaders(w)
		}
		_, _ = io.WriteString(w, payload)
		flushWriter(w)
		p.committed = true
		p.mu.Unlock()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.done:
				return
			case <-ticker.C:
				p.mu.Lock()
				select {
				case <-ctx.Done():
					p.mu.Unlock()
					return
				case <-p.done:
					p.mu.Unlock()
					return
				default:
				}
				_, _ = io.WriteString(w, payload)
				flushWriter(w)
				p.mu.Unlock()
			}
		}
	}()
	return p
}

// Stop cancels pre-header keepalive emission, waits for its goroutine to exit,
// and reports whether HTTP response headers were already committed to w.
func (p *preHeaderKeepalive) Stop() bool {
	if p == nil {
		return false
	}
	p.once.Do(func() {
		close(p.done)
	})
	p.wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.committed
}

type syncKeepaliveWriter struct {
	mu        sync.Mutex
	w         http.ResponseWriter
	lastWrite time.Time
}

func newSyncKeepaliveWriter(w http.ResponseWriter) *syncKeepaliveWriter {
	return &syncKeepaliveWriter{
		w:         w,
		lastWrite: time.Now(),
	}
}

func (sw *syncKeepaliveWriter) writeString(s string) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	_, err := io.WriteString(sw.w, s)
	flushWriter(sw.w)
	sw.lastWrite = time.Now()
	return err
}

func (sw *syncKeepaliveWriter) writeKeepalive(minIdle time.Duration, payload string) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if time.Since(sw.lastWrite) < minIdle {
		return
	}
	_, _ = io.WriteString(sw.w, payload)
	flushWriter(sw.w)
	sw.lastWrite = time.Now()
}

func startKeepalive(ctx context.Context, sw *syncKeepaliveWriter, interval time.Duration, payload string) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				sw.writeKeepalive(interval, payload)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
		})
		wg.Wait()
	}
}

// beginNonStreamJSONKeepalive ensures 200 OK headers with application/json (unless already committed
// by pre-header keepalive) and emits a single leading whitespace byte every interval while waiting on
// slow upstream aggregation, preventing Cloudflare (100s) / Aliyun ESA (30s) origin read timeouts.
// JSON allows arbitrary leading whitespace before the root object. Callers MUST invoke stop() before writing the body.
func beginNonStreamJSONKeepalive(ctx context.Context, w http.ResponseWriter, interval time.Duration, headersCommitted bool) (stop func()) {
	if !headersCommitted {
		setNonStreamKeepaliveHeaders(w)
		flushWriter(w)
	}
	return startKeepalive(ctx, newSyncKeepaliveWriter(w), interval, " ")
}

func writeCommittedJSONError(w http.ResponseWriter, errType, code, message string) {
	var codeVal any
	if code != "" {
		codeVal = code
	}
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{
		Error: ErrorDetail{
			Message: message,
			Type:    errType,
			Param:   nil,
			Code:    codeVal,
		},
	})
	flushWriter(w)
}

func writeCommittedChatSSEError(w http.ResponseWriter, errType, code, message string) {
	var codeVal any
	if code != "" {
		codeVal = code
	}
	b, _ := json.Marshal(ErrorEnvelope{
		Error: ErrorDetail{
			Message: message,
			Type:    errType,
			Param:   nil,
			Code:    codeVal,
		},
	})
	_, _ = io.WriteString(w, "data: "+string(b)+"\n\ndata: [DONE]\n\n")
	flushWriter(w)
}

func writeCommittedResponsesSSEError(w http.ResponseWriter, req *ResponsesRequest, modelID, errType, code, message string) {
	now := time.Now().UTC().Unix()
	failedResp := buildResponseEnvelope(RandomID("resp_"), modelID, now, &now, "failed", nil, []map[string]any{}, nil, req)
	var codeVal any
	if code != "" {
		codeVal = code
	}
	failedResp.Error = &ErrorDetail{
		Message: message,
		Type:    errType,
		Param:   nil,
		Code:    codeVal,
	}
	b, _ := json.Marshal(map[string]any{
		"type":            "response.failed",
		"sequence_number": 0,
		"response":        failedResp,
	})
	_, _ = io.WriteString(w, formatSSEEvent("response.failed", b))
	flushWriter(w)
}

func newSSEScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxSSELineBytes)
	return sc
}

// PipeChatSSE streams Bohrium's native /chat/completions SSE directly to an OpenAI client,
// filtering `: heartbeat` keepalive comments, guaranteeing a trailing `data: [DONE]`, and capturing usage.
func PipeChatSSE(w http.ResponseWriter, body io.Reader) (UsageMetrics, error) {
	return pipeChatSSE(context.Background(), w, body, defaultKeepaliveInterval, false)
}

func pipeChatSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, keepaliveInterval time.Duration, headersCommitted bool) (UsageMetrics, error) {
	if !headersCommitted {
		setSSEHeaders(w)
		flushWriter(w)
	}
	sw := newSyncKeepaliveWriter(w)
	stopKA := startKeepalive(ctx, sw, keepaliveInterval, ": keepalive\n\n")
	defer stopKA()

	var usage UsageMetrics
	sawDone := false
	sc := newSSEScanner(body)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ":") {
			// Skip Bohrium `: heartbeat` comments and blank separators between comments.
			continue
		}
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if data == "[DONE]" {
			sawDone = true
			_ = sw.writeString("data: [DONE]\n\n")
			break
		}
		if c, isErr := parseInlineChatError(data); isErr {
			_ = sw.writeString("data: " + string(marshalErrorEnvelopeBytes(c.Type, c.Code, c.PublicMessage)) + "\n\ndata: [DONE]\n\n")
			return usage, fmt.Errorf("upstream stream error: %s", c.RawDetail)
		}
		// Extract token usage if present on chunk
		var chunk ChatCompletionChunk
		if json.Unmarshal([]byte(data), &chunk) == nil && chunk.Usage != nil {
			usage = usageFromChat(chunk.Usage)
		}
		_ = sw.writeString("data: " + data + "\n\n")
	}
	if err := sc.Err(); err != nil {
		return usage, err
	}
	if !sawDone {
		_ = sw.writeString("data: [DONE]\n\n")
	}
	return usage, nil
}

// PipeResponsesSSE streams Bohrium's native /responses SSE directly to an OpenAI client,
// filtering `: heartbeat` comments and capturing the completed response object for storage & usage.
func PipeResponsesSSE(w http.ResponseWriter, body io.Reader, req *ResponsesRequest) (map[string]any, UsageMetrics, error) {
	return pipeResponsesSSE(context.Background(), w, body, req, defaultKeepaliveInterval, false)
}

func pipeResponsesSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, req *ResponsesRequest, keepaliveInterval time.Duration, headersCommitted bool) (map[string]any, UsageMetrics, error) {
	if !headersCommitted {
		setSSEHeaders(w)
		flushWriter(w)
	}
	sw := newSyncKeepaliveWriter(w)
	stopKA := startKeepalive(ctx, sw, keepaliveInterval, ": keepalive\n\n")
	defer stopKA()

	var terminalResp map[string]any
	var usage UsageMetrics

	var curEvent string
	var dataLines []string

	emitFrame := func() {
		if len(dataLines) == 0 {
			curEvent = ""
			return
		}
		payload := strings.Join(dataLines, "\n")
		var ev map[string]any
		if json.Unmarshal([]byte(payload), &ev) == nil {
			typ, _ := ev["type"].(string)
			if curEvent == "" {
				curEvent = typ
			}
			if curEvent == "error" || typ == "error" {
				c := classifyUpstreamError(http.StatusBadGateway, []byte(payload))
				if patched, err := json.Marshal(map[string]any{
					"type": "error",
					"error": map[string]any{
						"type":    c.Type,
						"code":    c.Code,
						"message": c.PublicMessage,
						"param":   nil,
					},
				}); err == nil {
					payload = string(patched)
				}
			} else if respObj, ok := ev["response"].(map[string]any); ok {
				// Patch client-facing previous_response_id & store flags onto response snapshots if present
				patchClientResponseMeta(respObj, req)
				if typ == "response.failed" && respObj["error"] != nil {
					sanitizedErr, _ := sanitizeStreamErrorDetailMap(respObj["error"])
					respObj["error"] = sanitizedErr
				}
				if typ == "response.completed" || typ == "response.incomplete" {
					terminalResp = respObj
					usage = usageFromResponseMap(respObj)
				}
				if patched, err := json.Marshal(ev); err == nil {
					payload = string(patched)
				}
			}
		}
		_ = sw.writeString(formatSSEEvent(curEvent, []byte(payload)))
		curEvent = ""
		dataLines = dataLines[:0]
	}

	sc := newSSEScanner(body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			emitFrame()
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Skip `: heartbeat`
			continue
		}
		if strings.HasPrefix(line, "event:") {
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	emitFrame()
	if err := sc.Err(); err != nil {
		return terminalResp, usage, err
	}
	return terminalResp, usage, nil
}

// StreamChatToResponsesSSE translates Bohrium's /chat/completions SSE stream in real time into
// the full official OpenAI Responses API SSE event sequence (response.created, response.in_progress,
// reasoning/message/function_call output items + deltas + done, response.completed/incomplete).
func StreamChatToResponsesSSE(w http.ResponseWriter, body io.Reader, req *ResponsesRequest, modelID, respID string) (*ResponseObject, UsageMetrics, error) {
	return streamChatToResponsesSSE(context.Background(), w, body, req, modelID, respID, defaultKeepaliveInterval, false)
}

func streamChatToResponsesSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, req *ResponsesRequest, modelID, respID string, keepaliveInterval time.Duration, headersCommitted bool) (*ResponseObject, UsageMetrics, error) {
	if !headersCommitted {
		setSSEHeaders(w)
		flushWriter(w)
	}
	sw := newSyncKeepaliveWriter(w)
	stopKA := startKeepalive(ctx, sw, keepaliveInterval, ": keepalive\n\n")
	defer stopKA()

	now := time.Now().UTC().Unix()
	seq := 0
	sendEv := func(evType string, body map[string]any) {
		body["type"] = evType
		body["sequence_number"] = seq
		seq++
		b, _ := json.Marshal(body)
		_ = sw.writeString(formatSSEEvent(evType, b))
	}

	// Emit response.created & response.in_progress
	initialResp := buildResponseEnvelope(respID, modelID, now, nil, "in_progress", nil, []map[string]any{}, nil, req)
	sendEv("response.created", map[string]any{"response": initialResp})
	sendEv("response.in_progress", map[string]any{"response": initialResp})

	nextOutputIdx := 0
	var completedOutputs []map[string]any

	// Reasoning state
	reasoningOpen := false
	reasoningOutputIdx := -1
	reasoningItemID := ""
	var reasoningBuf strings.Builder

	// Message text state
	msgOpen := false
	msgOutputIdx := -1
	msgItemID := ""
	var msgBuf strings.Builder

	// Tool call state (indexed by chat delta tool_calls[i].index)
	type tcState struct {
		outputIdx int
		itemID    string
		callID    string
		name      string
		argsBuf   strings.Builder
		closed    bool
	}
	toolCalls := make(map[int]*tcState)
	var toolOrder []int

	closeReasoning := func(itemStatus string) {
		if !reasoningOpen {
			return
		}
		reasoningOpen = false
		fullText := reasoningBuf.String()
		sendEv("response.reasoning_text.done", map[string]any{
			"item_id":       reasoningItemID,
			"output_index":  reasoningOutputIdx,
			"content_index": 0,
			"text":          fullText,
		})
		part := map[string]any{"type": "reasoning_text", "text": fullText}
		sendEv("response.content_part.done", map[string]any{
			"item_id":       reasoningItemID,
			"output_index":  reasoningOutputIdx,
			"content_index": 0,
			"part":          part,
		})
		item := map[string]any{
			"id":      reasoningItemID,
			"type":    "reasoning",
			"status":  itemStatus,
			"content": []map[string]any{part},
			"summary": []any{},
		}
		sendEv("response.output_item.done", map[string]any{
			"output_index": reasoningOutputIdx,
			"item":         item,
		})
		completedOutputs = append(completedOutputs, item)
	}

	closeMessage := func(itemStatus string) {
		if !msgOpen {
			return
		}
		msgOpen = false
		fullText := msgBuf.String()
		sendEv("response.output_text.done", map[string]any{
			"item_id":       msgItemID,
			"output_index":  msgOutputIdx,
			"content_index": 0,
			"text":          fullText,
			"logprobs":      []any{},
		})
		part := map[string]any{
			"type":        "output_text",
			"text":        fullText,
			"annotations": []any{},
			"logprobs":    []any{},
		}
		sendEv("response.content_part.done", map[string]any{
			"item_id":       msgItemID,
			"output_index":  msgOutputIdx,
			"content_index": 0,
			"part":          part,
		})
		item := map[string]any{
			"id":      msgItemID,
			"type":    "message",
			"status":  itemStatus,
			"role":    "assistant",
			"content": []map[string]any{part},
		}
		sendEv("response.output_item.done", map[string]any{
			"output_index": msgOutputIdx,
			"item":         item,
		})
		completedOutputs = append(completedOutputs, item)
	}

	closeToolCalls := func(itemStatus string) {
		sort.Ints(toolOrder)
		for _, idx := range toolOrder {
			st := toolCalls[idx]
			if st == nil || st.closed {
				continue
			}
			st.closed = true
			args := st.argsBuf.String()
			sendEv("response.function_call_arguments.done", map[string]any{
				"item_id":      st.itemID,
				"output_index": st.outputIdx,
				"name":         st.name,
				"arguments":    args,
			})
			item := map[string]any{
				"id":        st.itemID,
				"type":      "function_call",
				"status":    itemStatus,
				"call_id":   st.callID,
				"name":      st.name,
				"arguments": args,
			}
			sendEv("response.output_item.done", map[string]any{
				"output_index": st.outputIdx,
				"item":         item,
			})
			completedOutputs = append(completedOutputs, item)
		}
	}

	var usage UsageMetrics
	var respUsage *ResponsesUsage
	finishReason := "stop"

	sc := newSSEScanner(body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = usageFromChat(chunk.Usage)
			respUsage = responsesUsageFromChat(chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finishReason = *ch.FinishReason
		}

		// 1. Reasoning delta
		if ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "" {
			if !reasoningOpen {
				reasoningOpen = true
				reasoningOutputIdx = nextOutputIdx
				nextOutputIdx++
				reasoningItemID = RandomID("rs_")
				sendEv("response.output_item.added", map[string]any{
					"output_index": reasoningOutputIdx,
					"item": map[string]any{
						"id":      reasoningItemID,
						"type":    "reasoning",
						"status":  "in_progress",
						"content": []any{},
						"summary": []any{},
					},
				})
				sendEv("response.content_part.added", map[string]any{
					"item_id":       reasoningItemID,
					"output_index":  reasoningOutputIdx,
					"content_index": 0,
					"part":          map[string]any{"type": "reasoning_text", "text": ""},
				})
			}
			delta := *ch.Delta.ReasoningContent
			reasoningBuf.WriteString(delta)
			sendEv("response.reasoning_text.delta", map[string]any{
				"item_id":       reasoningItemID,
				"output_index":  reasoningOutputIdx,
				"content_index": 0,
				"delta":         delta,
			})
		}

		// 2. Text content delta
		if ch.Delta.Content != nil && *ch.Delta.Content != "" {
			if reasoningOpen {
				closeReasoning("completed")
			}
			if !msgOpen {
				msgOpen = true
				msgOutputIdx = nextOutputIdx
				nextOutputIdx++
				msgItemID = RandomID("msg_")
				sendEv("response.output_item.added", map[string]any{
					"output_index": msgOutputIdx,
					"item": map[string]any{
						"id":      msgItemID,
						"type":    "message",
						"status":  "in_progress",
						"role":    "assistant",
						"content": []any{},
					},
				})
				sendEv("response.content_part.added", map[string]any{
					"item_id":       msgItemID,
					"output_index":  msgOutputIdx,
					"content_index": 0,
					"part": map[string]any{
						"type":        "output_text",
						"text":        "",
						"annotations": []any{},
						"logprobs":    []any{},
					},
				})
			}
			delta := *ch.Delta.Content
			msgBuf.WriteString(delta)
			sendEv("response.output_text.delta", map[string]any{
				"item_id":       msgItemID,
				"output_index":  msgOutputIdx,
				"content_index": 0,
				"delta":         delta,
				"logprobs":      []any{},
			})
		}

		// 3. Tool call deltas
		if len(ch.Delta.ToolCalls) > 0 {
			if reasoningOpen {
				closeReasoning("completed")
			}
			if msgOpen {
				closeMessage("completed")
			}
			for i, tc := range ch.Delta.ToolCalls {
				idx := i
				if tc.Index != nil {
					idx = *tc.Index
				}
				st, exists := toolCalls[idx]
				if !exists {
					callID := tc.ID
					if callID == "" {
						callID = RandomID("call_")
					}
					st = &tcState{
						outputIdx: nextOutputIdx,
						itemID:    RandomID("fc_"),
						callID:    callID,
						name:      tc.Function.Name,
					}
					nextOutputIdx++
					toolCalls[idx] = st
					toolOrder = append(toolOrder, idx)
					sendEv("response.output_item.added", map[string]any{
						"output_index": st.outputIdx,
						"item": map[string]any{
							"id":        st.itemID,
							"type":      "function_call",
							"status":    "in_progress",
							"call_id":   st.callID,
							"name":      st.name,
							"arguments": "",
						},
					})
				} else {
					if tc.ID != "" && st.callID == "" {
						st.callID = tc.ID
					}
					if tc.Function.Name != "" && st.name == "" {
						st.name = tc.Function.Name
					}
				}
				if tc.Function.Arguments != "" {
					st.argsBuf.WriteString(tc.Function.Arguments)
					sendEv("response.function_call_arguments.delta", map[string]any{
						"item_id":      st.itemID,
						"output_index": st.outputIdx,
						"delta":        tc.Function.Arguments,
					})
				}
			}
		}
	}

	if err := sc.Err(); err != nil {
		failedAt := time.Now().UTC().Unix()
		failedResp := buildResponseEnvelope(respID, modelID, now, &failedAt, "failed", nil, completedOutputs, respUsage, req)
		failedResp.Error = &ErrorDetail{Message: publicMsgStreamInterrupted, Type: "server_error", Code: "stream_error"}
		sendEv("response.failed", map[string]any{"response": failedResp})
		return failedResp, usage, err
	}

	status := "completed"
	itemStatus := "completed"
	var incomplete *IncompleteDetails
	if finishReason == "length" {
		status = "incomplete"
		itemStatus = "incomplete"
		incomplete = &IncompleteDetails{Reason: "max_output_tokens"}
	}

	closeReasoning(itemStatus)
	closeMessage(itemStatus)
	closeToolCalls(itemStatus)

	completedAt := time.Now().UTC().Unix()
	finalResp := buildResponseEnvelope(respID, modelID, now, &completedAt, status, incomplete, completedOutputs, respUsage, req)
	terminalEv := "response.completed"
	if status == "incomplete" {
		terminalEv = "response.incomplete"
	}
	sendEv(terminalEv, map[string]any{"response": finalResp})
	return finalResp, usage, nil
}

// StreamResponsesToChatSSE translates a Bohrium native /responses SSE stream into
// standard OpenAI /v1/chat/completions chunks (`chat.completion.chunk` + `[DONE]`).
func StreamResponsesToChatSSE(w http.ResponseWriter, body io.Reader, modelID string, includeUsage bool) (UsageMetrics, error) {
	return streamResponsesToChatSSE(context.Background(), w, body, modelID, includeUsage, defaultKeepaliveInterval, false)
}

func streamResponsesToChatSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, modelID string, includeUsage bool, keepaliveInterval time.Duration, headersCommitted bool) (UsageMetrics, error) {
	if !headersCommitted {
		setSSEHeaders(w)
		flushWriter(w)
	}
	sw := newSyncKeepaliveWriter(w)
	stopKA := startKeepalive(ctx, sw, keepaliveInterval, ": keepalive\n\n")
	defer stopKA()

	chatID := RandomID("chatcmpl-")
	created := time.Now().UTC().Unix()
	sentRole := false
	hasToolCalls := false
	toolIdxByOut := make(map[int]int)
	nextToolIdx := 0
	var usage UsageMetrics
	var finalChatUsage *ChatUsage
	finishReason := "stop"

	emitChunk := func(delta ChatCompletionDelta, fr *string, u *ChatUsage) {
		chunk := ChatCompletionChunk{
			ID:      chatID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   modelID,
			Choices: []ChatCompletionChunkChoice{{
				Index:        0,
				Delta:        delta,
				Logprobs:     nil,
				FinishReason: fr,
			}},
			Usage: u,
		}
		b, _ := json.Marshal(chunk)
		_ = sw.writeString("data: " + string(b) + "\n\n")
	}

	ensureRole := func() {
		if !sentRole {
			sentRole = true
			empty := ""
			emitChunk(ChatCompletionDelta{Role: "assistant", Content: &empty}, nil, nil)
		}
	}

	var dataLines []string
	var streamErr error
	processFrame := func() {
		if len(dataLines) == 0 || streamErr != nil {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		var ev map[string]any
		if json.Unmarshal([]byte(payload), &ev) != nil {
			return
		}
		evType, _ := ev["type"].(string)
		switch evType {
		case "error":
			c := classifyUpstreamError(http.StatusBadGateway, []byte(payload))
			_ = sw.writeString("data: " + string(marshalErrorEnvelopeBytes(c.Type, c.Code, c.PublicMessage)) + "\n\ndata: [DONE]\n\n")
			streamErr = fmt.Errorf("upstream stream error: %s", c.RawDetail)
			return
		case "response.failed":
			errType, errCode, pubMsg, rawDetail := "upstream_error", "upstream_error", publicMsgStreamInterrupted, "upstream response.failed"
			if r, ok := ev["response"].(map[string]any); ok && r["error"] != nil {
				rawB, _ := json.Marshal(map[string]any{"error": r["error"]})
				c := classifyUpstreamError(http.StatusBadGateway, rawB)
				errType, errCode, pubMsg, rawDetail = c.Type, c.Code, c.PublicMessage, c.RawDetail
			}
			_ = sw.writeString("data: " + string(marshalErrorEnvelopeBytes(errType, errCode, pubMsg)) + "\n\ndata: [DONE]\n\n")
			streamErr = fmt.Errorf("upstream stream error: %s", rawDetail)
			return
		case "response.created":
			if r, ok := ev["response"].(map[string]any); ok {
				if id, _ := r["id"].(string); id != "" {
					chatID = id
				}
			}
			ensureRole()
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if d, _ := ev["delta"].(string); d != "" {
				ensureRole()
				emitChunk(ChatCompletionDelta{ReasoningContent: &d}, nil, nil)
			}
		case "response.output_text.delta":
			if d, _ := ev["delta"].(string); d != "" {
				ensureRole()
				emitChunk(ChatCompletionDelta{Content: &d}, nil, nil)
			}
		case "response.output_item.added":
			if item, ok := ev["item"].(map[string]any); ok && item["type"] == "function_call" {
				ensureRole()
				hasToolCalls = true
				outIdx := int(asFloat(ev["output_index"]))
				tIdx := nextToolIdx
				nextToolIdx++
				toolIdxByOut[outIdx] = tIdx
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				emitChunk(ChatCompletionDelta{
					ToolCalls: []ChatToolCall{{
						Index: &tIdx,
						ID:    callID,
						Type:  "function",
						Function: ChatToolCallFunction{
							Name:      name,
							Arguments: "",
						},
					}},
				}, nil, nil)
			}
		case "response.function_call_arguments.delta":
			if d, _ := ev["delta"].(string); d != "" {
				outIdx := int(asFloat(ev["output_index"]))
				tIdx := toolIdxByOut[outIdx]
				emitChunk(ChatCompletionDelta{
					ToolCalls: []ChatToolCall{{
						Index: &tIdx,
						Function: ChatToolCallFunction{
							Arguments: d,
						},
					}},
				}, nil, nil)
			}
		case "response.completed", "response.incomplete":
			if evType == "response.incomplete" {
				finishReason = "length"
			} else if hasToolCalls {
				finishReason = "tool_calls"
			}
			if r, ok := ev["response"].(map[string]any); ok {
				usage = usageFromResponseMap(r)
				finalChatUsage = chatUsageFromMetrics(usage)
			}
		}
	}

	sc := newSSEScanner(body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			processFrame()
			if streamErr != nil {
				return usage, streamErr
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	processFrame()
	if streamErr != nil {
		return usage, streamErr
	}
	if err := sc.Err(); err != nil {
		return usage, err
	}

	fr := finishReason
	var emitU *ChatUsage
	if includeUsage {
		emitU = finalChatUsage
	}
	emitChunk(ChatCompletionDelta{}, &fr, emitU)
	_ = sw.writeString("data: [DONE]\n\n")
	return usage, nil
}

// isSSEContentType reports whether an upstream HTTP response carries an SSE stream.
func isSSEContentType(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

// AggregateChatSSE reads a Bohrium /chat/completions SSE stream (requested with stream=true to match brain.mjs)
// and reassembles it into a complete non-streaming OpenAI ChatCompletionResponse.
func AggregateChatSSE(body io.Reader, fallbackModel string) (*ChatCompletionResponse, error) {
	chatID := ""
	created := int64(0)
	model := fallbackModel
	finishReason := ""

	var contentBuf strings.Builder
	var reasoningBuf strings.Builder

	type tcAccum struct {
		id      string
		typ     string
		name    string
		argsBuf strings.Builder
	}
	tcMap := make(map[int]*tcAccum)
	var tcOrder []int
	var finalUsage *ChatUsage

	sc := newSSEScanner(body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk ChatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.ID != "" && chatID == "" {
			chatID = chunk.ID
		}
		if chunk.Created > 0 && created == 0 {
			created = chunk.Created
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finishReason = *ch.FinishReason
		}
		if ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "" {
			reasoningBuf.WriteString(*ch.Delta.ReasoningContent)
		}
		if ch.Delta.Content != nil && *ch.Delta.Content != "" {
			contentBuf.WriteString(*ch.Delta.Content)
		}
		for i, tc := range ch.Delta.ToolCalls {
			idx := i
			if tc.Index != nil {
				idx = *tc.Index
			}
			acc, ok := tcMap[idx]
			if !ok {
				acc = &tcAccum{
					id:   tc.ID,
					typ:  tc.Type,
					name: tc.Function.Name,
				}
				if acc.typ == "" {
					acc.typ = "function"
				}
				tcMap[idx] = acc
				tcOrder = append(tcOrder, idx)
			} else {
				if tc.ID != "" && acc.id == "" {
					acc.id = tc.ID
				}
				if tc.Type != "" && acc.typ == "" {
					acc.typ = tc.Type
				}
				if tc.Function.Name != "" && acc.name == "" {
					acc.name = tc.Function.Name
				}
			}
			if tc.Function.Arguments != "" {
				acc.argsBuf.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if chatID == "" {
		chatID = RandomID("chatcmpl-")
	}
	if created == 0 {
		created = time.Now().UTC().Unix()
	}

	sort.Ints(tcOrder)
	var toolCalls []ChatToolCall
	for _, idx := range tcOrder {
		acc := tcMap[idx]
		callID := acc.id
		if callID == "" {
			callID = RandomID("call_")
		}
		toolCalls = append(toolCalls, ChatToolCall{
			ID:   callID,
			Type: acc.typ,
			Function: ChatToolCallFunction{
				Name:      acc.name,
				Arguments: acc.argsBuf.String(),
			},
		})
	}
	if finishReason == "" {
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}

	msg := ChatMessage{
		Role:      "assistant",
		ToolCalls: toolCalls,
	}
	if contentBuf.Len() > 0 || len(toolCalls) == 0 {
		msg.Content = contentBuf.String()
	}
	if reasoningBuf.Len() > 0 {
		rc := reasoningBuf.String()
		msg.ReasoningContent = &rc
	}

	return &ChatCompletionResponse{
		ID:      chatID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []ChatCompletionChoice{{
			Index:        0,
			Message:      msg,
			Logprobs:     nil,
			FinishReason: finishReason,
		}},
		Usage: finalUsage,
	}, nil
}

// AggregateResponsesSSE reads a Bohrium native /responses SSE stream (requested with stream=true to match brain.mjs)
// and returns the final OpenAI Responses object map.
func AggregateResponsesSSE(body io.Reader, req *ResponsesRequest, fallbackModel string) (map[string]any, error) {
	var terminalResp map[string]any
	var createdResp map[string]any
	var textBuf strings.Builder
	var reasoningBuf strings.Builder

	var dataLines []string
	processFrame := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		var ev map[string]any
		if json.Unmarshal([]byte(payload), &ev) != nil {
			return
		}
		evType, _ := ev["type"].(string)
		switch evType {
		case "response.created", "response.in_progress":
			if r, ok := ev["response"].(map[string]any); ok {
				createdResp = r
			}
		case "response.output_text.delta":
			if d, _ := ev["delta"].(string); d != "" {
				textBuf.WriteString(d)
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if d, _ := ev["delta"].(string); d != "" {
				reasoningBuf.WriteString(d)
			}
		case "response.completed", "response.incomplete", "response.failed":
			if r, ok := ev["response"].(map[string]any); ok {
				terminalResp = r
			}
		}
	}

	sc := newSSEScanner(body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			processFrame()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	processFrame()
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if terminalResp != nil {
		patchClientResponseMeta(terminalResp, req)
		if errObj, hasErr := terminalResp["error"]; hasErr && errObj != nil {
			sanitizedErr, _ := sanitizeStreamErrorDetailMap(errObj)
			terminalResp["error"] = sanitizedErr
		}
		return terminalResp, nil
	}
	// Fallback if upstream ended without a terminal snapshot event
	now := time.Now().UTC().Unix()
	respID := RandomID("resp_")
	if createdResp != nil {
		if id, _ := createdResp["id"].(string); id != "" {
			respID = id
		}
	}
	outputs := make([]map[string]any, 0, 2)
	if reasoningBuf.Len() > 0 {
		outputs = append(outputs, map[string]any{
			"id":      RandomID("rs_"),
			"type":    "reasoning",
			"status":  "completed",
			"content": []map[string]any{{"type": "reasoning_text", "text": reasoningBuf.String()}},
			"summary": []any{},
		})
	}
	outputs = append(outputs, map[string]any{
		"id":     RandomID("msg_"),
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []map[string]any{{
			"type":        "output_text",
			"text":        textBuf.String(),
			"annotations": []any{},
			"logprobs":    []any{},
		}},
	})
	env := buildResponseEnvelope(respID, fallbackModel, now, &now, "completed", nil, outputs, nil, req)
	return structToMap(env), nil
}

func patchClientResponseMeta(respObj map[string]any, req *ResponsesRequest) {
	if req == nil || respObj == nil {
		return
	}
	if req.PreviousResponseID != "" {
		respObj["previous_response_id"] = req.PreviousResponseID
	}
	if req.Store == nil || *req.Store {
		respObj["store"] = true
	}
	if req.Metadata != nil {
		respObj["metadata"] = req.Metadata
	}
}

func usageFromChat(u *ChatUsage) UsageMetrics {
	if u == nil {
		return UsageMetrics{}
	}
	m := UsageMetrics{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.PromptCacheHitTokens,
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		m.CachedTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		m.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return m
}

func responsesUsageFromChat(u *ChatUsage) *ResponsesUsage {
	if u == nil {
		return nil
	}
	m := usageFromChat(u)
	var cw int64
	if u.PromptTokensDetails != nil {
		cw = u.PromptTokensDetails.CacheWriteTokens
	}
	return &ResponsesUsage{
		InputTokens: m.PromptTokens,
		InputTokensDetails: ResponsesInputTokenDetails{
			CachedTokens:     m.CachedTokens,
			CacheWriteTokens: cw,
		},
		OutputTokens: m.CompletionTokens,
		OutputTokensDetails: ResponsesOutputTokenDetails{
			ReasoningTokens: m.ReasoningTokens,
		},
		TotalTokens: m.TotalTokens,
	}
}

func usageFromResponseMap(respObj map[string]any) UsageMetrics {
	uMap, ok := respObj["usage"].(map[string]any)
	if !ok {
		return UsageMetrics{}
	}
	m := UsageMetrics{
		PromptTokens:     int64(asFloat(uMap["input_tokens"])),
		CompletionTokens: int64(asFloat(uMap["output_tokens"])),
		TotalTokens:      int64(asFloat(uMap["total_tokens"])),
	}
	if inDet, ok := uMap["input_tokens_details"].(map[string]any); ok {
		m.CachedTokens = int64(asFloat(inDet["cached_tokens"]))
	}
	if outDet, ok := uMap["output_tokens_details"].(map[string]any); ok {
		m.ReasoningTokens = int64(asFloat(outDet["reasoning_tokens"]))
	}
	return m
}

func chatUsageFromMetrics(m UsageMetrics) *ChatUsage {
	if m.PromptTokens == 0 && m.CompletionTokens == 0 && m.TotalTokens == 0 {
		return nil
	}
	return &ChatUsage{
		PromptTokens:     m.PromptTokens,
		CompletionTokens: m.CompletionTokens,
		TotalTokens:      m.TotalTokens,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: m.CachedTokens,
		},
		CompletionTokensDetails: &CompletionTokensDetails{
			ReasoningTokens: m.ReasoningTokens,
		},
	}
}

func structToMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func extractOutputItems(respMap map[string]any) []map[string]any {
	arr, ok := respMap["output"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, el := range arr {
		if m, ok := el.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
