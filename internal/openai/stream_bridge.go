package openai

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const maxSSELineBytes = 16 * 1024 * 1024

func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func flushWriter(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func newSSEScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxSSELineBytes)
	return sc
}

// PipeChatSSE streams Bohrium's native /chat/completions SSE directly to an OpenAI client,
// filtering `: heartbeat` keepalive comments, guaranteeing a trailing `data: [DONE]`, and capturing usage.
func PipeChatSSE(w http.ResponseWriter, body io.Reader) (UsageMetrics, error) {
	setSSEHeaders(w)
	flushWriter(w)

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
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			flushWriter(w)
			break
		}
		// Extract token usage if present on chunk
		var chunk ChatCompletionChunk
		if json.Unmarshal([]byte(data), &chunk) == nil && chunk.Usage != nil {
			usage = usageFromChat(chunk.Usage)
		}
		_, _ = io.WriteString(w, "data: "+data+"\n\n")
		flushWriter(w)
	}
	if err := sc.Err(); err != nil {
		return usage, err
	}
	if !sawDone {
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flushWriter(w)
	}
	return usage, nil
}

// PipeResponsesSSE streams Bohrium's native /responses SSE directly to an OpenAI client,
// filtering `: heartbeat` comments and capturing the completed response object for storage & usage.
func PipeResponsesSSE(w http.ResponseWriter, body io.Reader, req *ResponsesRequest) (map[string]any, UsageMetrics, error) {
	setSSEHeaders(w)
	flushWriter(w)

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
			// Patch client-facing previous_response_id & store flags onto response snapshots if present
			if respObj, ok := ev["response"].(map[string]any); ok {
				patchClientResponseMeta(respObj, req)
				if typ == "response.completed" || typ == "response.incomplete" {
					terminalResp = respObj
					usage = usageFromResponseMap(respObj)
				}
				if patched, err := json.Marshal(ev); err == nil {
					payload = string(patched)
				}
			}
		}
		_, _ = io.WriteString(w, formatSSEEvent(curEvent, []byte(payload)))
		flushWriter(w)
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
	setSSEHeaders(w)
	flushWriter(w)

	now := time.Now().UTC().Unix()
	seq := 0
	sendEv := func(evType string, body map[string]any) {
		body["type"] = evType
		body["sequence_number"] = seq
		seq++
		b, _ := json.Marshal(body)
		_, _ = io.WriteString(w, formatSSEEvent(evType, b))
		flushWriter(w)
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
		failedResp.Error = &ErrorDetail{Message: err.Error(), Type: "server_error", Code: "stream_error"}
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
	setSSEHeaders(w)
	flushWriter(w)

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
		_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		flushWriter(w)
	}

	ensureRole := func() {
		if !sentRole {
			sentRole = true
			empty := ""
			emitChunk(ChatCompletionDelta{Role: "assistant", Content: &empty}, nil, nil)
		}
	}

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
		return usage, err
	}

	fr := finishReason
	var emitU *ChatUsage
	if includeUsage {
		emitU = finalChatUsage
	}
	emitChunk(ChatCompletionDelta{}, &fr, emitU)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flushWriter(w)
	return usage, nil
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

func forwardUpstreamError(w http.ResponseWriter, status int, body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	lower := strings.ToLower(trimmed)
	isInsufficientBalance := status == http.StatusPaymentRequired ||
		strings.Contains(lower, "insufficient_balance") ||
		strings.Contains(trimmed, "BILLING_PHOTON_NOT_ENOUGH")

	var parsed ErrorEnvelope
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		if isInsufficientBalance {
			outStatus := status
			if outStatus == http.StatusBadRequest || outStatus == http.StatusForbidden {
				outStatus = http.StatusPaymentRequired
			}
			WriteError(w, outStatus, "insufficient_quota", "insufficient_balance", parsed.Error.Message)
			return parsed.Error.Message
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return parsed.Error.Message
	}
	if isInsufficientBalance {
		msg := "[insufficient_balance] Insufficient balance"
		if trimmed != "" {
			if len(trimmed) > 300 {
				trimmed = trimmed[:300] + "..."
			}
			msg = trimmed
		}
		WriteError(w, http.StatusPaymentRequired, "insufficient_quota", "insufficient_balance", msg)
		return msg
	}
	msg := fmt.Sprintf("Bohrium upstream returned HTTP %d", status)
	if trimmed != "" {
		if len(trimmed) > 300 {
			trimmed = trimmed[:300] + "..."
		}
		msg += ": " + trimmed
	}
	errType := "upstream_error"
	switch status {
	case http.StatusBadRequest:
		errType = "invalid_request_error"
	case http.StatusUnauthorized, http.StatusForbidden:
		errType = "authentication_error"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
	}
	WriteError(w, status, errType, fmt.Sprintf("http_%d", status), msg)
	return msg
}
