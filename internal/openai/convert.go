package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bohrium-club-auto/internal/bohrium"
)

// NormalizeChatRequestForSciX converts an incoming OpenAI /v1/chat/completions request
// into the exact payload shape expected by Bohrium desktop client's /api/v1/scix/llm/chat/completions.
func NormalizeChatRequestForSciX(req *ChatCompletionRequest, model bohrium.SciXModel) (map[string]any, string, error) {
	if len(req.Messages) == 0 {
		return nil, "", fmt.Errorf("messages array is required and cannot be empty")
	}
	if req.N != nil && *req.N != 1 {
		return nil, "", fmt.Errorf("only n=1 is supported")
	}

	outMsgs := make([]map[string]any, 0, len(req.Messages))
	question := ""
	hasImage := false

	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "developer":
			// Bohrium desktop client (brain.mjs) sets supportsDeveloperRole: false for chat_completions models.
			role = "system"
		case "function":
			role = "tool"
		}

		msgObj := map[string]any{"role": role}
		if m.Name != "" {
			msgObj["name"] = m.Name
		}

		if role == "tool" {
			tcID := m.ToolCallID
			if tcID == "" && m.Name != "" {
				tcID = "call_" + m.Name
			}
			msgObj["tool_call_id"] = tcID
			msgObj["content"] = flattenContentToText(m.Content)
			outMsgs = append(outMsgs, msgObj)
			continue
		}

		if role == "assistant" {
			if m.Content != nil {
				msgObj["content"] = m.Content
			} else if len(m.ToolCalls) == 0 {
				msgObj["content"] = ""
			}
			if m.ReasoningContent != nil && *m.ReasoningContent != "" {
				msgObj["reasoning_content"] = *m.ReasoningContent
			} else if m.Reasoning != nil && *m.Reasoning != "" {
				msgObj["reasoning_content"] = *m.Reasoning
			}
			if len(m.ToolCalls) > 0 {
				tcs := make([]map[string]any, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					typ := tc.Type
					if typ == "" {
						typ = "function"
					}
					tcs = append(tcs, map[string]any{
						"id":   tc.ID,
						"type": typ,
						"function": map[string]any{
							"name":      tc.Function.Name,
							"arguments": tc.Function.Arguments,
						},
					})
				}
				msgObj["tool_calls"] = tcs
			} else if len(m.FunctionCall) > 0 && string(m.FunctionCall) != "null" {
				var fc struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}
				if json.Unmarshal(m.FunctionCall, &fc) == nil && fc.Name != "" {
					msgObj["tool_calls"] = []map[string]any{{
						"id":   "call_" + fc.Name,
						"type": "function",
						"function": map[string]any{
							"name":      fc.Name,
							"arguments": fc.Arguments,
						},
					}}
				}
			}
			outMsgs = append(outMsgs, msgObj)
			continue
		}

		// system / user
		normContent, contentText, imgFound := normalizeChatContent(m.Content)
		if imgFound {
			hasImage = true
		}
		if role == "user" && contentText != "" {
			question = contentText
		}
		msgObj["content"] = normContent
		outMsgs = append(outMsgs, msgObj)
	}

	if hasImage && len(model.Input) > 0 && !model.SupportsImage() {
		return nil, "", fmt.Errorf("model %q does not support image inputs; use a vision-capable model such as kimi-k3, kimi-k3.1-pro, advanced, or vision", model.ID)
	}

	payload := map[string]any{
		"model":    model.ID,
		"messages": outMsgs,
		"stream":   req.Stream,
	}
	if req.Stream {
		includeUsage := true
		if req.StreamOptions != nil {
			includeUsage = req.StreamOptions.IncludeUsage
		}
		if includeUsage {
			payload["stream_options"] = map[string]any{"include_usage": true}
		}
	}

	// Bohrium desktop client specifies max_tokens (never both max_tokens & max_completion_tokens).
	maxTok := pickMaxTokens(req.MaxTokens, req.MaxCompletionTokens)
	if maxTok > 0 {
		payload["max_tokens"] = maxTok
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 && string(req.Stop) != "null" {
		payload["stop"] = req.Stop
	}
	if req.PresencePenalty != nil {
		payload["presence_penalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		payload["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.Seed != nil {
		payload["seed"] = *req.Seed
	}
	if req.Logprobs != nil {
		payload["logprobs"] = *req.Logprobs
	}
	if req.TopLogprobs != nil {
		payload["top_logprobs"] = *req.TopLogprobs
	}
	if len(req.LogitBias) > 0 {
		payload["logit_bias"] = req.LogitBias
	}
	if len(req.ResponseFormat) > 0 && string(req.ResponseFormat) != "null" {
		payload["response_format"] = req.ResponseFormat
	}
	if req.User != "" {
		payload["user"] = req.User
	}
	if req.PromptCacheKey != "" {
		payload["prompt_cache_key"] = req.PromptCacheKey
	}

	// Tools (plus legacy functions upgrade)
	tools := normalizeChatTools(req.Tools, req.Functions)
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	if tc := normalizeChatToolChoice(req.ToolChoice, req.FunctionCall); tc != nil {
		payload["tool_choice"] = tc
	}
	if req.ParallelToolCalls != nil && len(tools) > 0 {
		payload["parallel_tool_calls"] = *req.ParallelToolCalls
	}

	if effort := model.ClampReasoningEffort(req.ReasoningEffort); effort != "" {
		payload["reasoning_effort"] = effort
	}

	return payload, question, nil
}

// ParseResponsesInputItems normalizes a Responses API `input` (string or array of items)
// into a slice of canonical item maps, resolving any `item_reference` entries via store.
func ParseResponsesInputItems(raw json.RawMessage, rs *ResponseStore) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	// Case 1: shorthand string input
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("invalid input string: %w", err)
		}
		return []map[string]any{{
			"type": "message",
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": text,
			}},
		}}, nil
	}
	// Case 2: array of input items
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("input must be a string or an array of input items: %w", err)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if item == nil {
			continue
		}
		typ, _ := item["type"].(string)
		if typ == "item_reference" {
			refID, _ := item["id"].(string)
			if rs != nil && refID != "" {
				if resolved, ok := rs.FindItemByID(refID); ok {
					out = append(out, resolved)
				}
			}
			continue
		}
		if typ == "" {
			if _, hasRole := item["role"]; hasRole {
				cp := cloneMap(item)
				cp["type"] = "message"
				out = append(out, cp)
				continue
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// NormalizeResponsesRequestForSciX builds the exact stateless payload for Bohrium's native /api/v1/scix/llm/responses endpoint.
func NormalizeResponsesRequestForSciX(req *ResponsesRequest, model bohrium.SciXModel, allInputItems []map[string]any, effectiveInstructions *string) (map[string]any, string, error) {
	if req.Background != nil && *req.Background {
		return nil, "", fmt.Errorf("background mode is not supported by Bohrium SciX Responses")
	}

	normalizedInput := make([]map[string]any, 0, len(allInputItems))
	question := ""
	hasImage := false

	for _, item := range allInputItems {
		typ, _ := item["type"].(string)
		switch typ {
		case "message", "":
			role, _ := item["role"].(string)
			if role == "" {
				role = "user"
			}
			parts, textVal, imgFound := normalizeResponsesMessageContent(item["content"], role)
			if imgFound {
				hasImage = true
			}
			if role == "user" && textVal != "" {
				question = textVal
			}
			normalizedInput = append(normalizedInput, map[string]any{
				"type":    "message",
				"role":    role,
				"content": parts,
			})
		default:
			// function_call, function_call_output, reasoning, etc.
			cp := cloneMap(item)
			// Strip fields that stateless upstream does not need or may reject on replay
			if typ == "reasoning" {
				// Keep summary/content/encrypted_content/id as provided
			}
			normalizedInput = append(normalizedInput, cp)
		}
	}

	if hasImage && len(model.Input) > 0 && !model.SupportsImage() {
		return nil, "", fmt.Errorf("model %q does not support image inputs; use kimi-k3, kimi-k3.1-pro, advanced, or vision", model.ID)
	}

	payload := map[string]any{
		"model":  model.ID,
		"input":  normalizedInput,
		"stream": req.Stream,
		"store":  false, // Bohrium SciX Responses enforces stateless requests
	}
	if effectiveInstructions != nil && *effectiveInstructions != "" {
		payload["instructions"] = *effectiveInstructions
		if question == "" {
			question = *effectiveInstructions
		}
	}
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		payload["max_output_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if req.PresencePenalty != nil {
		payload["presence_penalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		payload["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.TopLogprobs != nil {
		payload["top_logprobs"] = *req.TopLogprobs
	}
	if req.ParallelToolCalls != nil {
		payload["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.MaxToolCalls != nil {
		payload["max_tool_calls"] = *req.MaxToolCalls
	}
	if len(req.Tools) > 0 {
		payload["tools"] = normalizeResponsesTools(req.Tools)
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		payload["tool_choice"] = req.ToolChoice
	}
	if req.Reasoning != nil {
		rObj := map[string]any{}
		if req.Reasoning.Effort != nil {
			if effort := model.ClampReasoningEffort(*req.Reasoning.Effort); effort != "" {
				rObj["effort"] = effort
			}
		}
		if req.Reasoning.Summary != nil && *req.Reasoning.Summary != "" {
			rObj["summary"] = *req.Reasoning.Summary
		}
		if len(rObj) > 0 {
			payload["reasoning"] = rObj
		}
	}
	if req.Text != nil {
		tObj := map[string]any{}
		if len(req.Text.Format) > 0 && string(req.Text.Format) != "null" {
			tObj["format"] = req.Text.Format
		}
		if req.Text.Verbosity != nil && *req.Text.Verbosity != "" {
			tObj["verbosity"] = *req.Text.Verbosity
		}
		if len(tObj) > 0 {
			payload["text"] = tObj
		}
	}
	if req.Truncation != "" {
		payload["truncation"] = req.Truncation
	}
	if req.PromptCacheKey != "" {
		payload["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.PromptCacheRet != "" {
		payload["prompt_cache_retention"] = req.PromptCacheRet
	}
	if req.User != "" {
		payload["user"] = req.User
	}
	if len(req.Include) > 0 {
		payload["include"] = req.Include
	}
	return payload, question, nil
}

// ResponsesToChatRequest bridges an OpenAI /v1/responses request to Bohrium's native /api/v1/scix/llm/chat/completions
// for chat_completions-native models (dspro-high, dsflash-high, kimi-k3, kimi-k3.1-pro, standard, advanced).
func ResponsesToChatRequest(req *ResponsesRequest, model bohrium.SciXModel, allInputItems []map[string]any, effectiveInstructions *string) (map[string]any, string, error) {
	if req.Background != nil && *req.Background {
		return nil, "", fmt.Errorf("background mode is not supported")
	}

	messages := make([]map[string]any, 0, len(allInputItems)+1)
	question := ""
	hasImage := false

	if effectiveInstructions != nil && *effectiveInstructions != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": *effectiveInstructions,
		})
	}

	var pendingReasoning string
	var pendingAssistant *map[string]any

	flushAssistant := func() {
		if pendingAssistant != nil {
			messages = append(messages, *pendingAssistant)
			pendingAssistant = nil
		}
	}

	for _, item := range allInputItems {
		typ, _ := item["type"].(string)
		if typ == "" {
			if _, ok := item["role"]; ok {
				typ = "message"
			}
		}

		switch typ {
		case "reasoning":
			txt := extractReasoningItemText(item)
			if txt != "" {
				if pendingReasoning != "" {
					pendingReasoning += "\n"
				}
				pendingReasoning += txt
			}

		case "message":
			role, _ := item["role"].(string)
			role = strings.ToLower(strings.TrimSpace(role))
			if role == "" {
				role = "user"
			}
			if role == "developer" {
				role = "system"
			}
			chatContent, plainText, imgFound := responsesContentToChatContent(item["content"])
			if imgFound {
				hasImage = true
			}
			if role == "assistant" {
				flushAssistant()
				m := map[string]any{
					"role":    "assistant",
					"content": chatContent,
				}
				if pendingReasoning != "" {
					m["reasoning_content"] = pendingReasoning
					pendingReasoning = ""
				}
				pendingAssistant = &m
			} else {
				flushAssistant()
				pendingReasoning = ""
				if role == "user" && plainText != "" {
					question = plainText
				}
				messages = append(messages, map[string]any{
					"role":    role,
					"content": chatContent,
				})
			}

		case "function_call":
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID, _ = item["id"].(string)
			}
			if callID == "" {
				callID = RandomID("call_")
			}
			name, _ := item["name"].(string)
			argsStr := ""
			switch a := item["arguments"].(type) {
			case string:
				argsStr = a
			default:
				if b, err := json.Marshal(a); err == nil {
					argsStr = string(b)
				}
			}
			tc := map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": argsStr,
				},
			}
			if pendingAssistant == nil {
				m := map[string]any{
					"role":       "assistant",
					"content":    nil,
					"tool_calls": []map[string]any{tc},
				}
				if pendingReasoning != "" {
					m["reasoning_content"] = pendingReasoning
					pendingReasoning = ""
				}
				pendingAssistant = &m
			} else {
				existing, _ := (*pendingAssistant)["tool_calls"].([]map[string]any)
				(*pendingAssistant)["tool_calls"] = append(existing, tc)
			}

		case "function_call_output":
			flushAssistant()
			pendingReasoning = ""
			callID, _ := item["call_id"].(string)
			outputText, toolImages := extractFunctionCallOutput(item["output"])
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": callID,
				"content":      outputText,
			})
			if len(toolImages) > 0 {
				hasImage = true
				parts := make([]map[string]any, 0, len(toolImages)+1)
				parts = append(parts, map[string]any{
					"type": "text",
					"text": "Attached image(s) from tool output:",
				})
				for _, u := range toolImages {
					parts = append(parts, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": u},
					})
				}
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": parts,
				})
			}
		}
	}
	flushAssistant()

	if len(messages) == 0 {
		return nil, "", fmt.Errorf("input or instructions is required")
	}
	if hasImage && len(model.Input) > 0 && !model.SupportsImage() {
		return nil, "", fmt.Errorf("model %q does not support image inputs; use kimi-k3, kimi-k3.1-pro, advanced, or vision", model.ID)
	}
	if question == "" && effectiveInstructions != nil {
		question = *effectiveInstructions
	}

	payload := map[string]any{
		"model":    model.ID,
		"messages": messages,
		"stream":   req.Stream,
	}
	if req.Stream {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		payload["max_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if req.PresencePenalty != nil {
		payload["presence_penalty"] = *req.PresencePenalty
	}
	if req.FrequencyPenalty != nil {
		payload["frequency_penalty"] = *req.FrequencyPenalty
	}
	if req.User != "" {
		payload["user"] = req.User
	}
	if req.PromptCacheKey != "" {
		payload["prompt_cache_key"] = req.PromptCacheKey
	}

	// Convert Responses tools -> Chat Completions tools
	if len(req.Tools) > 0 {
		chatTools := responsesToolsToChatTools(req.Tools)
		if len(chatTools) > 0 {
			payload["tools"] = chatTools
		}
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		if tc := responsesToolChoiceToChatToolChoice(req.ToolChoice); tc != nil {
			payload["tool_choice"] = tc
		}
	}
	if req.ParallelToolCalls != nil && len(req.Tools) > 0 {
		payload["parallel_tool_calls"] = *req.ParallelToolCalls
	}

	// Convert reasoning.effort -> reasoning_effort
	if req.Reasoning != nil && req.Reasoning.Effort != nil {
		if effort := model.ClampReasoningEffort(*req.Reasoning.Effort); effort != "" {
			payload["reasoning_effort"] = effort
		}
	}

	// Convert text.format -> response_format
	if req.Text != nil && len(req.Text.Format) > 0 && string(req.Text.Format) != "null" {
		if rf := responsesTextFormatToChatResponseFormat(req.Text.Format); rf != nil {
			payload["response_format"] = rf
		}
	}

	return payload, question, nil
}

// ChatToResponsesRequest bridges an OpenAI /v1/chat/completions request to Bohrium's native /api/v1/scix/llm/responses
// for responses-native models (vision, title, summary, etc.).
func ChatToResponsesRequest(req *ChatCompletionRequest, model bohrium.SciXModel) (map[string]any, string, error) {
	if len(req.Messages) == 0 {
		return nil, "", fmt.Errorf("messages array is required and cannot be empty")
	}
	if req.N != nil && *req.N != 1 {
		return nil, "", fmt.Errorf("only n=1 is supported")
	}

	var instructionsParts []string
	inputItems := make([]map[string]any, 0, len(req.Messages))
	question := ""
	hasImage := false

	for _, m := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		switch role {
		case "system", "developer":
			txt := flattenContentToText(m.Content)
			if txt != "" {
				if len(inputItems) == 0 {
					instructionsParts = append(instructionsParts, txt)
				} else {
					inputItems = append(inputItems, map[string]any{
						"type": "message",
						"role": "developer",
						"content": []map[string]any{{
							"type": "input_text",
							"text": txt,
						}},
					})
				}
			}
		case "user":
			parts, plainText, imgFound := chatContentToResponsesParts(m.Content, "user")
			if imgFound {
				hasImage = true
			}
			if plainText != "" {
				question = plainText
			}
			inputItems = append(inputItems, map[string]any{
				"type":    "message",
				"role":    "user",
				"content": parts,
			})
		case "assistant":
			txt := flattenContentToText(m.Content)
			if txt != "" {
				inputItems = append(inputItems, map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{{
						"type":        "output_text",
						"text":        txt,
						"annotations": []any{},
					}},
				})
			}
			for _, tc := range m.ToolCalls {
				callID := tc.ID
				if callID == "" {
					callID = RandomID("call_")
				}
				inputItems = append(inputItems, map[string]any{
					"type":      "function_call",
					"call_id":   callID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
		case "tool", "function":
			callID := m.ToolCallID
			if callID == "" && m.Name != "" {
				callID = "call_" + m.Name
			}
			inputItems = append(inputItems, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  flattenContentToText(m.Content),
			})
		}
	}

	if hasImage && len(model.Input) > 0 && !model.SupportsImage() {
		return nil, "", fmt.Errorf("model %q does not support image inputs", model.ID)
	}

	payload := map[string]any{
		"model":  model.ID,
		"input":  inputItems,
		"stream": req.Stream,
		"store":  false,
	}
	if len(instructionsParts) > 0 {
		instr := strings.Join(instructionsParts, "\n\n")
		payload["instructions"] = instr
		if question == "" {
			question = instr
		}
	}
	if maxTok := pickMaxTokens(req.MaxTokens, req.MaxCompletionTokens); maxTok > 0 {
		if maxTok < 16 {
			maxTok = 16 // OpenAI Responses minimum output token limit (matching brain.mjs OPENAI_RESPONSES_MIN_OUTPUT_TOKENS)
		}
		payload["max_output_tokens"] = maxTok
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if req.User != "" {
		payload["user"] = req.User
	}
	if req.PromptCacheKey != "" {
		payload["prompt_cache_key"] = req.PromptCacheKey
	}
	if effort := model.ClampReasoningEffort(req.ReasoningEffort); effort != "" {
		payload["reasoning"] = map[string]any{"effort": effort}
	}
	if tools := chatToolsToResponsesTools(req.Tools, req.Functions); len(tools) > 0 {
		payload["tools"] = tools
	}
	if tc := chatToolChoiceToResponsesToolChoice(req.ToolChoice, req.FunctionCall); tc != nil {
		payload["tool_choice"] = tc
	}
	if req.ParallelToolCalls != nil {
		payload["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if len(req.ResponseFormat) > 0 && string(req.ResponseFormat) != "null" {
		if fmtObj := chatResponseFormatToResponsesTextFormat(req.ResponseFormat); fmtObj != nil {
			payload["text"] = map[string]any{"format": fmtObj}
		}
	}

	return payload, question, nil
}

// ChatResponseToOpenAIResponse converts a non-streaming Bohrium /chat/completions JSON body
// into a complete OpenAI Responses API `response` object.
func ChatResponseToOpenAIResponse(chatResp *ChatCompletionResponse, req *ResponsesRequest, modelID, respID string) *ResponseObject {
	now := time.Now().UTC().Unix()
	created := chatResp.Created
	if created == 0 {
		created = now
	}
	completedAt := now

	output := make([]map[string]any, 0, 3)
	status := "completed"
	var incomplete *IncompleteDetails

	if len(chatResp.Choices) > 0 {
		ch := chatResp.Choices[0]
		if ch.FinishReason == "length" {
			status = "incomplete"
			incomplete = &IncompleteDetails{Reason: "max_output_tokens"}
		}
		itemStatus := "completed"
		if status == "incomplete" {
			itemStatus = "incomplete"
		}

		// 1. Reasoning item (if reasoning_content present)
		rc := ""
		if ch.Message.ReasoningContent != nil {
			rc = *ch.Message.ReasoningContent
		} else if ch.Message.Reasoning != nil {
			rc = *ch.Message.Reasoning
		}
		if rc != "" {
			output = append(output, map[string]any{
				"id":     RandomID("rs_"),
				"type":   "reasoning",
				"status": itemStatus,
				"content": []map[string]any{{
					"type": "reasoning_text",
					"text": rc,
				}},
				"summary": []any{},
			})
		}

		// 2. Assistant message item
		textContent := flattenContentToText(ch.Message.Content)
		if textContent != "" || len(ch.Message.ToolCalls) == 0 {
			output = append(output, map[string]any{
				"id":     RandomID("msg_"),
				"type":   "message",
				"status": itemStatus,
				"role":   "assistant",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        textContent,
					"annotations": []any{},
					"logprobs":    []any{},
				}},
			})
		}

		// 3. Function call items
		for _, tc := range ch.Message.ToolCalls {
			callID := tc.ID
			if callID == "" {
				callID = RandomID("call_")
			}
			output = append(output, map[string]any{
				"id":        RandomID("fc_"),
				"type":      "function_call",
				"status":    itemStatus,
				"call_id":   callID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			})
		}
	}

	var usage *ResponsesUsage
	if chatResp.Usage != nil {
		cached := chatResp.Usage.PromptCacheHitTokens
		var cacheWrite int64
		if chatResp.Usage.PromptTokensDetails != nil {
			if chatResp.Usage.PromptTokensDetails.CachedTokens > 0 {
				cached = chatResp.Usage.PromptTokensDetails.CachedTokens
			}
			cacheWrite = chatResp.Usage.PromptTokensDetails.CacheWriteTokens
		}
		var reasoningTok int64
		if chatResp.Usage.CompletionTokensDetails != nil {
			reasoningTok = chatResp.Usage.CompletionTokensDetails.ReasoningTokens
		}
		usage = &ResponsesUsage{
			InputTokens: chatResp.Usage.PromptTokens,
			InputTokensDetails: ResponsesInputTokenDetails{
				CachedTokens:     cached,
				CacheWriteTokens: cacheWrite,
			},
			OutputTokens: chatResp.Usage.CompletionTokens,
			OutputTokensDetails: ResponsesOutputTokenDetails{
				ReasoningTokens: reasoningTok,
			},
			TotalTokens: chatResp.Usage.TotalTokens,
		}
	}

	return buildResponseEnvelope(respID, modelID, created, &completedAt, status, incomplete, output, usage, req)
}

// OpenAIResponseToChatResponse converts a non-streaming Bohrium /responses JSON body
// into a standard OpenAI /v1/chat/completions response object.
func OpenAIResponseToChatResponse(respMap map[string]any, fallbackModel string) *ChatCompletionResponse {
	id, _ := respMap["id"].(string)
	if id == "" {
		id = RandomID("chatcmpl-")
	}
	model, _ := respMap["model"].(string)
	if model == "" {
		model = fallbackModel
	}
	created := int64(asFloat(respMap["created_at"]))
	if created == 0 {
		created = time.Now().UTC().Unix()
	}

	var textParts []string
	var reasoningParts []string
	var toolCalls []ChatToolCall

	if outArr, ok := respMap["output"].([]any); ok {
		for _, rawItem := range outArr {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			switch item["type"] {
			case "reasoning":
				if txt := extractReasoningItemText(item); txt != "" {
					reasoningParts = append(reasoningParts, txt)
				}
			case "message":
				if contentArr, ok := item["content"].([]any); ok {
					for _, rawPart := range contentArr {
						p, ok := rawPart.(map[string]any)
						if !ok {
							continue
						}
						if t, _ := p["text"].(string); t != "" {
							textParts = append(textParts, t)
						} else if r, _ := p["refusal"].(string); r != "" {
							textParts = append(textParts, r)
						}
					}
				}
			case "function_call":
				idx := len(toolCalls)
				callID, _ := item["call_id"].(string)
				if callID == "" {
					callID, _ = item["id"].(string)
				}
				name, _ := item["name"].(string)
				args, _ := item["arguments"].(string)
				toolCalls = append(toolCalls, ChatToolCall{
					Index: &idx,
					ID:    callID,
					Type:  "function",
					Function: ChatToolCallFunction{
						Name:      name,
						Arguments: args,
					},
				})
			}
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	} else if st, _ := respMap["status"].(string); st == "incomplete" {
		finishReason = "length"
	}

	msg := ChatMessage{
		Role:      "assistant",
		Content:   strings.Join(textParts, ""),
		ToolCalls: toolCalls,
	}
	if len(reasoningParts) > 0 {
		rc := strings.Join(reasoningParts, "\n")
		msg.ReasoningContent = &rc
	}

	var usage *ChatUsage
	if uMap, ok := respMap["usage"].(map[string]any); ok {
		inTok := int64(asFloat(uMap["input_tokens"]))
		outTok := int64(asFloat(uMap["output_tokens"]))
		totTok := int64(asFloat(uMap["total_tokens"]))
		if totTok == 0 {
			totTok = inTok + outTok
		}
		var cached, cacheWrite, reasoningTok int64
		if inDet, ok := uMap["input_tokens_details"].(map[string]any); ok {
			cached = int64(asFloat(inDet["cached_tokens"]))
			cacheWrite = int64(asFloat(inDet["cache_write_tokens"]))
		}
		if outDet, ok := uMap["output_tokens_details"].(map[string]any); ok {
			reasoningTok = int64(asFloat(outDet["reasoning_tokens"]))
		}
		usage = &ChatUsage{
			PromptTokens:     inTok,
			CompletionTokens: outTok,
			TotalTokens:      totTok,
			PromptTokensDetails: &PromptTokensDetails{
				CachedTokens:     cached,
				CacheWriteTokens: cacheWrite,
			},
			CompletionTokensDetails: &CompletionTokensDetails{
				ReasoningTokens: reasoningTok,
			},
		}
	}

	return &ChatCompletionResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []ChatCompletionChoice{{
			Index:        0,
			Message:      msg,
			Logprobs:     nil,
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
}

func buildResponseEnvelope(respID, modelID string, created int64, completedAt *int64, status string, incomplete *IncompleteDetails, output []map[string]any, usage *ResponsesUsage, req *ResponsesRequest) *ResponseObject {
	if output == nil {
		output = []map[string]any{}
	}
	parallel := true
	if req != nil && req.ParallelToolCalls != nil {
		parallel = *req.ParallelToolCalls
	}
	temp := 1.0
	if req != nil && req.Temperature != nil {
		temp = *req.Temperature
	}
	topP := 1.0
	if req != nil && req.TopP != nil {
		topP = *req.TopP
	}
	store := true
	if req != nil && req.Store != nil {
		store = *req.Store
	}
	truncation := "disabled"
	if req != nil && req.Truncation != "" {
		truncation = req.Truncation
	}
	serviceTier := "default"
	if req != nil && req.ServiceTier != "" {
		serviceTier = req.ServiceTier
	}

	reasoningMap := map[string]any{"effort": nil, "summary": nil}
	if req != nil && req.Reasoning != nil {
		if req.Reasoning.Effort != nil {
			reasoningMap["effort"] = *req.Reasoning.Effort
		}
		if req.Reasoning.Summary != nil {
			reasoningMap["summary"] = *req.Reasoning.Summary
		}
	}

	textMap := map[string]any{
		"format":    map[string]any{"type": "text"},
		"verbosity": nil,
	}
	if req != nil && req.Text != nil {
		if len(req.Text.Format) > 0 && string(req.Text.Format) != "null" {
			var parsed any
			if json.Unmarshal(req.Text.Format, &parsed) == nil {
				textMap["format"] = parsed
			}
		}
		if req.Text.Verbosity != nil {
			textMap["verbosity"] = *req.Text.Verbosity
		}
	}

	var toolChoice any = "auto"
	if req != nil && len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		var parsed any
		if json.Unmarshal(req.ToolChoice, &parsed) == nil {
			toolChoice = parsed
		}
	}

	toolsList := make([]any, 0)
	if req != nil && len(req.Tools) > 0 {
		for _, raw := range req.Tools {
			var parsed any
			if json.Unmarshal(raw, &parsed) == nil {
				toolsList = append(toolsList, parsed)
			}
		}
	}

	metadata := map[string]any{}
	if req != nil && req.Metadata != nil {
		metadata = req.Metadata
	}

	var prevID *string
	if req != nil && req.PreviousResponseID != "" {
		p := req.PreviousResponseID
		prevID = &p
	}
	var userPtr *string
	if req != nil && req.User != "" {
		u := req.User
		userPtr = &u
	}
	var cacheKeyPtr *string
	if req != nil && req.PromptCacheKey != "" {
		k := req.PromptCacheKey
		cacheKeyPtr = &k
	}
	var instrPtr *string
	var maxOutPtr *int
	var maxToolsPtr *int
	if req != nil {
		instrPtr = req.Instructions
		maxOutPtr = req.MaxOutputTokens
		maxToolsPtr = req.MaxToolCalls
	}

	return &ResponseObject{
		ID:                 respID,
		Object:             "response",
		CreatedAt:          created,
		CompletedAt:        completedAt,
		Status:             status,
		Error:              nil,
		IncompleteDetails:  incomplete,
		Instructions:       instrPtr,
		MaxOutputTokens:    maxOutPtr,
		MaxToolCalls:       maxToolsPtr,
		Model:              modelID,
		Output:             output,
		ParallelToolCalls:  parallel,
		PreviousResponseID: prevID,
		Reasoning:          reasoningMap,
		Store:              store,
		Background:         false,
		Temperature:        temp,
		TopP:               topP,
		TopLogprobs:        0,
		PresencePenalty:    0,
		FrequencyPenalty:   0,
		Text:               textMap,
		ToolChoice:         toolChoice,
		Tools:              toolsList,
		Truncation:         truncation,
		Usage:              usage,
		User:               userPtr,
		Metadata:           metadata,
		ServiceTier:        serviceTier,
		PromptCacheKey:     cacheKeyPtr,
	}
}

// --- Helper functions ---

func pickMaxTokens(a, b *int) int {
	if a != nil && *a > 0 {
		return *a
	}
	if b != nil && *b > 0 {
		return *b
	}
	return 0
}

func flattenContentToText(v any) string {
	switch c := v.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		var parts []string
		for _, el := range c {
			m, ok := el.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok && t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(c)
		return string(b)
	}
}

func normalizeChatContent(v any) (any, string, bool) {
	switch c := v.(type) {
	case nil:
		return "", "", false
	case string:
		return c, c, false
	case []any:
		out := make([]map[string]any, 0, len(c))
		var texts []string
		hasImg := false
		for _, el := range c {
			m, ok := el.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "text", "input_text", "output_text":
				t, _ := m["text"].(string)
				texts = append(texts, t)
				out = append(out, map[string]any{"type": "text", "text": t})
			case "image_url":
				hasImg = true
				out = append(out, m)
			case "input_image":
				hasImg = true
				imgURL := extractImageURL(m["image_url"])
				if imgURL != "" {
					iu := map[string]any{"url": imgURL}
					if d, ok := m["detail"].(string); ok && d != "" {
						iu["detail"] = d
					}
					out = append(out, map[string]any{"type": "image_url", "image_url": iu})
				}
			default:
				out = append(out, m)
			}
		}
		return out, strings.Join(texts, "\n"), hasImg
	default:
		s := flattenContentToText(v)
		return s, s, false
	}
}

func normalizeResponsesMessageContent(v any, role string) ([]map[string]any, string, bool) {
	textType := "input_text"
	if strings.EqualFold(role, "assistant") {
		textType = "output_text"
	}
	switch c := v.(type) {
	case nil:
		return []map[string]any{}, "", false
	case string:
		return []map[string]any{{"type": textType, "text": c}}, c, false
	case []any:
		out := make([]map[string]any, 0, len(c))
		var texts []string
		hasImg := false
		for _, el := range c {
			m, ok := el.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "text", "input_text", "output_text":
				t, _ := m["text"].(string)
				texts = append(texts, t)
				pt := typ
				if pt == "text" {
					pt = textType
				}
				part := map[string]any{"type": pt, "text": t}
				if pt == "output_text" {
					part["annotations"] = []any{}
				}
				out = append(out, part)
			case "input_image":
				hasImg = true
				imgURL := extractImageURL(m["image_url"])
				part := map[string]any{"type": "input_image", "image_url": imgURL}
				if d, ok := m["detail"].(string); ok && d != "" {
					part["detail"] = d
				}
				out = append(out, part)
			case "image_url":
				hasImg = true
				imgURL := extractImageURL(m["image_url"])
				part := map[string]any{"type": "input_image", "image_url": imgURL}
				if iu, ok := m["image_url"].(map[string]any); ok {
					if d, ok := iu["detail"].(string); ok && d != "" {
						part["detail"] = d
					}
				}
				out = append(out, part)
			default:
				out = append(out, m)
			}
		}
		return out, strings.Join(texts, "\n"), hasImg
	default:
		s := flattenContentToText(v)
		return []map[string]any{{"type": textType, "text": s}}, s, false
	}
}

func responsesContentToChatContent(v any) (any, string, bool) {
	switch c := v.(type) {
	case nil:
		return "", "", false
	case string:
		return c, c, false
	case []any:
		parts := make([]map[string]any, 0, len(c))
		var texts []string
		hasImg := false
		for _, el := range c {
			m, ok := el.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "input_text", "output_text", "text":
				t, _ := m["text"].(string)
				texts = append(texts, t)
				parts = append(parts, map[string]any{"type": "text", "text": t})
			case "refusal":
				r, _ := m["refusal"].(string)
				texts = append(texts, r)
				parts = append(parts, map[string]any{"type": "text", "text": r})
			case "input_image", "image_url":
				imgURL := extractImageURL(m["image_url"])
				if imgURL == "" {
					imgURL, _ = m["url"].(string)
				}
				if imgURL != "" {
					hasImg = true
					iu := map[string]any{"url": imgURL}
					if d, ok := m["detail"].(string); ok && d != "" {
						iu["detail"] = d
					}
					parts = append(parts, map[string]any{"type": "image_url", "image_url": iu})
				}
			case "input_file":
				fn, _ := m["filename"].(string)
				fd, _ := m["file_data"].(string)
				fu, _ := m["file_url"].(string)
				desc := "[Attached file"
				if fn != "" {
					desc += ": " + fn
				}
				if fu != "" {
					desc += " (" + fu + ")"
				}
				desc += "]"
				if fd != "" && len(fd) < 4096 && !strings.HasPrefix(fd, "data:") {
					desc += "\n" + fd
				}
				texts = append(texts, desc)
				parts = append(parts, map[string]any{"type": "text", "text": desc})
			}
		}
		plain := strings.Join(texts, "\n")
		if !hasImg {
			return plain, plain, false
		}
		return parts, plain, true
	default:
		s := flattenContentToText(v)
		return s, s, false
	}
}

func chatContentToResponsesParts(v any, role string) ([]map[string]any, string, bool) {
	return normalizeResponsesMessageContent(v, role)
}

func extractImageURL(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if u, ok := x["url"].(string); ok {
			return u
		}
	}
	return ""
}

func extractReasoningItemText(item map[string]any) string {
	var parts []string
	if arr, ok := item["content"].([]any); ok {
		for _, el := range arr {
			if m, ok := el.(map[string]any); ok {
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				}
			}
		}
	}
	if len(parts) == 0 {
		if arr, ok := item["summary"].([]any); ok {
			for _, el := range arr {
				if m, ok := el.(map[string]any); ok {
					if t, _ := m["text"].(string); t != "" {
						parts = append(parts, t)
					}
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func extractFunctionCallOutput(v any) (string, []string) {
	switch o := v.(type) {
	case nil:
		return "", nil
	case string:
		return o, nil
	case []any:
		var texts []string
		var imgs []string
		for _, el := range o {
			m, ok := el.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "input_text", "output_text", "text":
				if t, _ := m["text"].(string); t != "" {
					texts = append(texts, t)
				}
			case "input_image", "image_url":
				if u := extractImageURL(m["image_url"]); u != "" {
					imgs = append(imgs, u)
				}
			}
		}
		txt := strings.Join(texts, "\n")
		if txt == "" && len(imgs) > 0 {
			txt = "(see attached image)"
		}
		return txt, imgs
	default:
		b, _ := json.Marshal(o)
		return string(b), nil
	}
}

func normalizeChatTools(tools []json.RawMessage, legacyFuncs []json.RawMessage) []any {
	out := make([]any, 0, len(tools)+len(legacyFuncs))
	for _, raw := range tools {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil && m != nil {
			out = append(out, m)
		}
	}
	for _, raw := range legacyFuncs {
		var fn map[string]any
		if json.Unmarshal(raw, &fn) == nil && fn != nil {
			out = append(out, map[string]any{
				"type":     "function",
				"function": fn,
			})
		}
	}
	return out
}

func normalizeChatToolChoice(toolChoice, legacyFuncCall json.RawMessage) any {
	if len(toolChoice) > 0 && string(toolChoice) != "null" {
		var v any
		if json.Unmarshal(toolChoice, &v) == nil {
			return v
		}
	}
	if len(legacyFuncCall) > 0 && string(legacyFuncCall) != "null" {
		var s string
		if json.Unmarshal(legacyFuncCall, &s) == nil {
			return s
		}
		var obj map[string]any
		if json.Unmarshal(legacyFuncCall, &obj) == nil {
			if name, ok := obj["name"].(string); ok && name != "" {
				return map[string]any{
					"type":     "function",
					"function": map[string]any{"name": name},
				}
			}
		}
	}
	return nil
}

func normalizeResponsesTools(tools []json.RawMessage) []any {
	out := make([]any, 0, len(tools))
	for _, raw := range tools {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil || m == nil {
			continue
		}
		// Flatten chat-completions style {type:"function", function:{name,...}} if passed to /v1/responses
		if m["type"] == "function" {
			if fn, ok := m["function"].(map[string]any); ok {
				flat := map[string]any{"type": "function"}
				for k, v := range fn {
					flat[k] = v
				}
				out = append(out, flat)
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func responsesToolsToChatTools(tools []json.RawMessage) []any {
	out := make([]any, 0, len(tools))
	for _, raw := range tools {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil || m == nil {
			continue
		}
		typ, _ := m["type"].(string)
		if typ == "function" {
			if _, hasNested := m["function"].(map[string]any); hasNested {
				out = append(out, m)
				continue
			}
			fn := map[string]any{}
			if name, ok := m["name"]; ok {
				fn["name"] = name
			}
			if desc, ok := m["description"]; ok {
				fn["description"] = desc
			}
			if params, ok := m["parameters"]; ok {
				fn["parameters"] = params
			}
			if strict, ok := m["strict"]; ok {
				fn["strict"] = strict
			}
			out = append(out, map[string]any{
				"type":     "function",
				"function": fn,
			})
		} else {
			out = append(out, m)
		}
	}
	return out
}

func responsesToolChoiceToChatToolChoice(raw json.RawMessage) any {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil
	}
	if m["type"] == "function" {
		if _, hasNested := m["function"].(map[string]any); hasNested {
			return m
		}
		if name, ok := m["name"].(string); ok && name != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": name},
			}
		}
	}
	return m
}

func responsesTextFormatToChatResponseFormat(raw json.RawMessage) any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "text":
		return map[string]any{"type": "text"}
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		js := map[string]any{}
		for _, k := range []string{"name", "description", "schema", "strict"} {
			if v, ok := m[k]; ok {
				js[k] = v
			}
		}
		return map[string]any{
			"type":        "json_schema",
			"json_schema": js,
		}
	default:
		return nil
	}
}

func chatToolsToResponsesTools(tools, legacyFuncs []json.RawMessage) []any {
	chatTools := normalizeChatTools(tools, legacyFuncs)
	out := make([]any, 0, len(chatTools))
	for _, item := range chatTools {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "function" {
			if fn, ok := m["function"].(map[string]any); ok {
				flat := map[string]any{"type": "function"}
				for k, v := range fn {
					flat[k] = v
				}
				out = append(out, flat)
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func chatToolChoiceToResponsesToolChoice(toolChoice, legacyFuncCall json.RawMessage) any {
	tc := normalizeChatToolChoice(toolChoice, legacyFuncCall)
	if tc == nil {
		return nil
	}
	if s, ok := tc.(string); ok {
		return s
	}
	if m, ok := tc.(map[string]any); ok {
		if m["type"] == "function" {
			if fn, ok := m["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					return map[string]any{"type": "function", "name": name}
				}
			}
		}
		return m
	}
	return tc
}

func chatResponseFormatToResponsesTextFormat(raw json.RawMessage) any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "text", "json_object":
		return map[string]any{"type": typ}
	case "json_schema":
		out := map[string]any{"type": "json_schema"}
		if js, ok := m["json_schema"].(map[string]any); ok {
			for k, v := range js {
				out[k] = v
			}
		}
		return out
	default:
		return nil
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	default:
		return 0
	}
}
