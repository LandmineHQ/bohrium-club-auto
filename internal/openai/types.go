package openai

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

// OpenAIModelLimit carries models.dev / OpenCode style token limits.
type OpenAIModelLimit struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// OpenAIModelArchitecture carries OpenRouter modality metadata.
type OpenAIModelArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

// OpenAIModelTopProvider carries OpenRouter provider capacity metadata.
type OpenAIModelTopProvider struct {
	ContextLength       int  `json:"context_length,omitempty"`
	MaxCompletionTokens int  `json:"max_completion_tokens,omitempty"`
	IsModerated         bool `json:"is_moderated"`
}

// OpenAIModelThinking carries normalized reasoning/thinking capability metadata.
type OpenAIModelThinking struct {
	Supported bool     `json:"supported"`
	Levels    []string `json:"levels,omitempty"`
	Default   string   `json:"default,omitempty"`
}

// OpenAIModel is the OpenAI GET /v1/models/{id} and /v1/models item object,
// enriched with OpenRouter and multi-client compatibility extension fields.
type OpenAIModel struct {
	ID                     string                   `json:"id"`
	Object                 string                   `json:"object"`
	Created                int64                    `json:"created"`
	OwnedBy                string                   `json:"owned_by"`
	Name                   string                   `json:"name,omitempty"`
	DisplayName            string                   `json:"display_name,omitempty"`
	Description            string                   `json:"description,omitempty"`
	ContextWindow          int                      `json:"context_window,omitempty"`
	ContextLength          int                      `json:"context_length,omitempty"`
	MaxModelLen            int                      `json:"max_model_len,omitempty"`
	MaxInputTokens         int                      `json:"max_input_tokens,omitempty"`
	MaxOutputTokens        int                      `json:"max_output_tokens,omitempty"`
	MaxTokens              int                      `json:"max_tokens,omitempty"`
	Limit                  *OpenAIModelLimit        `json:"limit,omitempty"`
	TopProvider            *OpenAIModelTopProvider  `json:"top_provider,omitempty"`
	Input                  []string                 `json:"input,omitempty"`
	Architecture           *OpenAIModelArchitecture `json:"architecture,omitempty"`
	SupportedParameters    []string                 `json:"supported_parameters,omitempty"`
	SupportedEndpointTypes []string                 `json:"supported_endpoint_types,omitempty"`
	ReasoningEffort        string                   `json:"reasoning_effort,omitempty"`
	ReasoningEfforts       []string                 `json:"reasoning_efforts,omitempty"`
	Thinking               *OpenAIModelThinking     `json:"thinking,omitempty"`
}

// OpenAIModelList is the standard OpenAI GET /v1/models response envelope.
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// ErrorEnvelope matches the standard OpenAI API error JSON structure.
type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes a single OpenAI API error.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    any    `json:"code"`
}

// WriteError writes an OpenAI-formatted JSON error response.
func WriteError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
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
}

// WriteJSON writes a JSON object with no-store cache control.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// RandomID creates a prefixed random identifier such as "resp_...", "msg_...", "fc_...", "rs_...".
func RandomID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// UsageMetrics is an internal normalized token accounting summary used across endpoints.
type UsageMetrics struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// ChatCompletionRequest represents an OpenAI /v1/chat/completions request body.
type ChatCompletionRequest struct {
	Model               string              `json:"model"`
	Messages            []ChatMessage       `json:"messages"`
	Stream              bool                `json:"stream,omitempty"`
	StreamOptions       *StreamOptions      `json:"stream_options,omitempty"`
	Tools               []json.RawMessage   `json:"tools,omitempty"`
	ToolChoice          json.RawMessage     `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool               `json:"parallel_tool_calls,omitempty"`
	Functions           []json.RawMessage   `json:"functions,omitempty"`
	FunctionCall        json.RawMessage     `json:"function_call,omitempty"`
	ReasoningEffort     string              `json:"reasoning_effort,omitempty"`
	ResponseFormat      json.RawMessage     `json:"response_format,omitempty"`
	MaxTokens           *int                `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                `json:"max_completion_tokens,omitempty"`
	Temperature         *float64            `json:"temperature,omitempty"`
	TopP                *float64            `json:"top_p,omitempty"`
	Stop                json.RawMessage     `json:"stop,omitempty"`
	PresencePenalty     *float64            `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64            `json:"frequency_penalty,omitempty"`
	Seed                *int64              `json:"seed,omitempty"`
	Logprobs            *bool               `json:"logprobs,omitempty"`
	TopLogprobs         *int                `json:"top_logprobs,omitempty"`
	N                   *int                `json:"n,omitempty"`
	User                string              `json:"user,omitempty"`
	PromptCacheKey      string              `json:"prompt_cache_key,omitempty"`
	ServiceTier         string              `json:"service_tier,omitempty"`
	Store               *bool               `json:"store,omitempty"`
	Metadata            map[string]any      `json:"metadata,omitempty"`
	LogitBias           map[string]float64  `json:"logit_bias,omitempty"`
}

// StreamOptions controls streaming behavior such as usage emission.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatMessage represents a single message in OpenAI Chat Completions.
type ChatMessage struct {
	Role             string          `json:"role"`
	Content          any             `json:"content,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolCalls        []ChatToolCall  `json:"tool_calls,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	Reasoning        *string         `json:"reasoning,omitempty"`
	Refusal          *string         `json:"refusal,omitempty"`
	FunctionCall     json.RawMessage `json:"function_call,omitempty"`
}

// ChatToolCall represents a tool call on an assistant message or streaming delta.
type ChatToolCall struct {
	Index    *int                 `json:"index,omitempty"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function ChatToolCallFunction `json:"function"`
}

// ChatToolCallFunction carries function name and JSON arguments string.
type ChatToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

// ChatCompletionResponse represents a non-streaming /v1/chat/completions response.
type ChatCompletionResponse struct {
	ID                string                 `json:"id"`
	Object            string                 `json:"object"`
	Created           int64                  `json:"created"`
	Model             string                 `json:"model"`
	SystemFingerprint string                 `json:"system_fingerprint,omitempty"`
	ServiceTier       string                 `json:"service_tier,omitempty"`
	Choices           []ChatCompletionChoice `json:"choices"`
	Usage             *ChatUsage             `json:"usage,omitempty"`
}

// ChatCompletionChoice is one choice in a non-streaming Chat Completion.
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	Logprobs     any         `json:"logprobs"`
	FinishReason string      `json:"finish_reason"`
}

// ChatUsage mirrors OpenAI Chat Completions token accounting.
type ChatUsage struct {
	PromptTokens            int64                    `json:"prompt_tokens"`
	CompletionTokens        int64                    `json:"completion_tokens"`
	TotalTokens             int64                    `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	PromptCacheHitTokens    int64                    `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens   int64                    `json:"prompt_cache_miss_tokens,omitempty"`
}

// PromptTokensDetails provides cached/audio breakdown of prompt tokens.
type PromptTokensDetails struct {
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// CompletionTokensDetails provides reasoning breakdown of completion tokens.
type CompletionTokensDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// ChatCompletionChunk represents one SSE chunk from /v1/chat/completions.
type ChatCompletionChunk struct {
	ID                string                      `json:"id"`
	Object            string                      `json:"object"`
	Created           int64                       `json:"created"`
	Model             string                      `json:"model"`
	SystemFingerprint string                      `json:"system_fingerprint,omitempty"`
	Choices           []ChatCompletionChunkChoice `json:"choices"`
	Usage             *ChatUsage                  `json:"usage"`
}

// ChatCompletionChunkChoice is one choice delta in an SSE chunk.
type ChatCompletionChunkChoice struct {
	Index        int                 `json:"index"`
	Delta        ChatCompletionDelta `json:"delta"`
	Logprobs     any                 `json:"logprobs"`
	FinishReason *string             `json:"finish_reason"`
}

// ChatCompletionDelta holds incremental text, reasoning, or tool calls.
type ChatCompletionDelta struct {
	Role             string         `json:"role,omitempty"`
	Content          *string        `json:"content,omitempty"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
}

// ResponsesRequest represents an OpenAI /v1/responses request body.
type ResponsesRequest struct {
	Model               string            `json:"model"`
	Input               json.RawMessage   `json:"input,omitempty"`
	Instructions        *string           `json:"instructions,omitempty"`
	PreviousResponseID  string            `json:"previous_response_id,omitempty"`
	Store               *bool             `json:"store,omitempty"`
	Stream              bool              `json:"stream,omitempty"`
	Background          *bool             `json:"background,omitempty"`
	Conversation        json.RawMessage   `json:"conversation,omitempty"`
	Tools               []json.RawMessage `json:"tools,omitempty"`
	ToolChoice          json.RawMessage   `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls,omitempty"`
	MaxToolCalls        *int              `json:"max_tool_calls,omitempty"`
	Reasoning           *ReasoningConfig  `json:"reasoning,omitempty"`
	Text                *TextConfig       `json:"text,omitempty"`
	MaxOutputTokens     *int              `json:"max_output_tokens,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
	TopLogprobs         *int              `json:"top_logprobs,omitempty"`
	Truncation          string            `json:"truncation,omitempty"`
	Metadata            map[string]any    `json:"metadata,omitempty"`
	User                string            `json:"user,omitempty"`
	PromptCacheKey      string            `json:"prompt_cache_key,omitempty"`
	PromptCacheRet      string            `json:"prompt_cache_retention,omitempty"`
	ServiceTier         string            `json:"service_tier,omitempty"`
	SafetyIdentifier    string            `json:"safety_identifier,omitempty"`
	Include             []string          `json:"include,omitempty"`
}

// ReasoningConfig corresponds to Responses API reasoning object.
type ReasoningConfig struct {
	Effort  *string `json:"effort,omitempty"`
	Summary *string `json:"summary,omitempty"`
}

// TextConfig corresponds to Responses API text object (format + verbosity).
type TextConfig struct {
	Format    json.RawMessage `json:"format,omitempty"`
	Verbosity *string         `json:"verbosity,omitempty"`
}

// ResponsesUsage mirrors OpenAI Responses API usage object.
type ResponsesUsage struct {
	InputTokens         int64                       `json:"input_tokens"`
	InputTokensDetails  ResponsesInputTokenDetails  `json:"input_tokens_details"`
	OutputTokens        int64                       `json:"output_tokens"`
	OutputTokensDetails ResponsesOutputTokenDetails `json:"output_tokens_details"`
	TotalTokens         int64                       `json:"total_tokens"`
}

// ResponsesInputTokenDetails carries cached token metrics.
type ResponsesInputTokenDetails struct {
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

// ResponsesOutputTokenDetails carries reasoning token metrics.
type ResponsesOutputTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// IncompleteDetails describes why a response ended in status "incomplete".
type IncompleteDetails struct {
	Reason string `json:"reason"`
}

// ResponseObject represents a complete OpenAI Responses API object.
type ResponseObject struct {
	ID                 string             `json:"id"`
	Object             string             `json:"object"`
	CreatedAt          int64              `json:"created_at"`
	CompletedAt        *int64             `json:"completed_at"`
	Status             string             `json:"status"` // "in_progress" | "completed" | "incomplete" | "failed"
	Error              *ErrorDetail       `json:"error"`
	IncompleteDetails  *IncompleteDetails `json:"incomplete_details"`
	Instructions       *string            `json:"instructions"`
	MaxOutputTokens    *int               `json:"max_output_tokens"`
	MaxToolCalls       *int               `json:"max_tool_calls"`
	Model              string             `json:"model"`
	Output             []map[string]any   `json:"output"`
	ParallelToolCalls  bool               `json:"parallel_tool_calls"`
	PreviousResponseID *string            `json:"previous_response_id"`
	Reasoning          map[string]any     `json:"reasoning"`
	Store              bool               `json:"store"`
	Background         bool               `json:"background"`
	Temperature        float64            `json:"temperature"`
	TopP               float64            `json:"top_p"`
	TopLogprobs        int                `json:"top_logprobs"`
	PresencePenalty    float64            `json:"presence_penalty"`
	FrequencyPenalty   float64            `json:"frequency_penalty"`
	Text               map[string]any     `json:"text"`
	ToolChoice         any                `json:"tool_choice"`
	Tools              []any              `json:"tools"`
	Truncation         string             `json:"truncation"`
	Usage              *ResponsesUsage    `json:"usage"`
	User               *string            `json:"user"`
	Metadata           map[string]any     `json:"metadata"`
	ServiceTier        string             `json:"service_tier"`
	PromptCacheKey     *string            `json:"prompt_cache_key"`
}

func formatSSEEvent(eventName string, data []byte) string {
	if eventName != "" {
		return fmt.Sprintf("event: %s\ndata: %s\n\n", eventName, string(data))
	}
	return fmt.Sprintf("data: %s\n\n", string(data))
}
