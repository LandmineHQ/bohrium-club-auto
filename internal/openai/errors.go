package openai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Client-facing OpenAI-compatible error messages.
// All upstream URLs, IPs, ports, service/brand names (e.g. Bohrium/SciX/Sigma/LiteLLM),
// and raw upstream payloads are stripped before reaching /v1/* callers and retained only
// in internal server logs and gateway_usage_logs.
const (
	publicMsgCatalogUnavailable   = "Model catalog is temporarily unavailable; please retry shortly."
	publicMsgAccountExpired       = "Upstream account credentials have expired; please retry shortly."
	publicMsgInsufficientBalance  = "Insufficient account balance to complete this request."
	publicMsgBillingUnavailable   = "Upstream billing check is temporarily unavailable; please retry shortly."
	publicMsgUpstreamTransport    = "Upstream model service connection failed; please retry shortly."
	publicMsgStreamInterrupted    = "Upstream response stream was interrupted; please retry shortly."
	publicMsgInvalidUpstreamResp  = "Upstream model service returned an invalid response; please retry shortly."
	publicMsgMarshalError         = "Failed to prepare request payload."
	publicMsgStoreError           = "Internal storage error."
	publicMsgRoutingError         = "Service is temporarily unable to route the request."
	publicMsgEmptyPool            = "No active accounts are configured in the routing pool."
	publicMsgNoHealthyPool        = "No healthy accounts are currently available in the routing pool."
	publicMsgAccountUnavailable   = "No authenticated upstream account is currently available."
)

type sanitizedUpstreamError struct {
	Status              int
	Type                string
	Code                string
	PublicMessage       string
	RawDetail           string
	InsufficientBalance bool
}

func classifyUpstreamError(status int, body []byte) sanitizedUpstreamError {
	rawDetail := extractUpstreamErrorText(body, status)
	trimmed := strings.TrimSpace(string(body))
	lower := strings.ToLower(trimmed)

	var parsed ErrorEnvelope
	_ = json.Unmarshal(body, &parsed)
	parsedMsgLower := strings.ToLower(strings.TrimSpace(parsed.Error.Message))
	parsedCodeLower := ""
	if parsed.Error.Code != nil {
		parsedCodeLower = strings.ToLower(strings.TrimSpace(fmt.Sprint(parsed.Error.Code)))
	}
	combined := lower + " " + parsedMsgLower + " " + parsedCodeLower

	if status == http.StatusPaymentRequired ||
		strings.Contains(combined, "insufficient_balance") ||
		strings.Contains(trimmed, "BILLING_PHOTON_NOT_ENOUGH") ||
		strings.Contains(parsed.Error.Message, "BILLING_PHOTON_NOT_ENOUGH") {
		return sanitizedUpstreamError{
			Status:              http.StatusPaymentRequired,
			Type:                "insufficient_quota",
			Code:                "insufficient_balance",
			PublicMessage:       publicMsgInsufficientBalance,
			RawDetail:           rawDetail,
			InsufficientBalance: true,
		}
	}

	if (status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity) &&
		containsAny(combined, "context_length_exceeded", "maximum context length", "too many tokens", "prompt is too long", "token limit") {
		return sanitizedUpstreamError{
			Status:        http.StatusBadRequest,
			Type:          "invalid_request_error",
			Code:          "context_length_exceeded",
			PublicMessage: "Request exceeds the model's maximum context length.",
			RawDetail:     rawDetail,
		}
	}

	if (status == http.StatusBadRequest || status == http.StatusForbidden) &&
		containsAny(combined, "content_filter", "content_policy", "content management policy", "content safety") {
		return sanitizedUpstreamError{
			Status:        http.StatusBadRequest,
			Type:          "invalid_request_error",
			Code:          "content_policy_violation",
			PublicMessage: "Request was rejected by content safety policy.",
			RawDetail:     rawDetail,
		}
	}

	if status == http.StatusTooManyRequests || strings.Contains(combined, "rate_limit") {
		return sanitizedUpstreamError{
			Status:        http.StatusTooManyRequests,
			Type:          "rate_limit_error",
			Code:          "rate_limit_exceeded",
			PublicMessage: "Upstream rate limit exceeded; please retry shortly.",
			RawDetail:     rawDetail,
		}
	}

	switch status {
	case http.StatusUnauthorized:
		return sanitizedUpstreamError{
			Status:        http.StatusUnauthorized,
			Type:          "authentication_error",
			Code:          "upstream_auth_error",
			PublicMessage: "Upstream authentication failed.",
			RawDetail:     rawDetail,
		}
	case http.StatusForbidden:
		return sanitizedUpstreamError{
			Status:        http.StatusForbidden,
			Type:          "permission_error",
			Code:          "upstream_forbidden",
			PublicMessage: "Request was rejected by upstream permission policy.",
			RawDetail:     rawDetail,
		}
	case http.StatusNotFound:
		return sanitizedUpstreamError{
			Status:        http.StatusNotFound,
			Type:          "invalid_request_error",
			Code:          "not_found",
			PublicMessage: "Requested model or resource was not found.",
			RawDetail:     rawDetail,
		}
	case http.StatusRequestEntityTooLarge:
		return sanitizedUpstreamError{
			Status:        http.StatusRequestEntityTooLarge,
			Type:          "invalid_request_error",
			Code:          "payload_too_large",
			PublicMessage: "Request payload is too large.",
			RawDetail:     rawDetail,
		}
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return sanitizedUpstreamError{
			Status:        status,
			Type:          "invalid_request_error",
			Code:          "upstream_invalid_request",
			PublicMessage: "The request was rejected as invalid by the model backend.",
			RawDetail:     rawDetail,
		}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return sanitizedUpstreamError{
			Status:        http.StatusGatewayTimeout,
			Type:          "upstream_error",
			Code:          "upstream_timeout",
			PublicMessage: "Upstream model service timed out; please retry shortly.",
			RawDetail:     rawDetail,
		}
	case http.StatusServiceUnavailable:
		return sanitizedUpstreamError{
			Status:        http.StatusServiceUnavailable,
			Type:          "upstream_error",
			Code:          "upstream_unavailable",
			PublicMessage: "Upstream model service is temporarily unavailable; please retry shortly.",
			RawDetail:     rawDetail,
		}
	default:
		return sanitizedUpstreamError{
			Status:        http.StatusBadGateway,
			Type:          "upstream_error",
			Code:          "upstream_error",
			PublicMessage: "Upstream model service encountered an error; please retry shortly.",
			RawDetail:     rawDetail,
		}
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func extractCommittedErrorParts(status int, body []byte) (errType, code, publicMsg, rawDetail string) {
	c := classifyUpstreamError(status, body)
	return c.Type, c.Code, c.PublicMessage, c.RawDetail
}

func forwardUpstreamError(w http.ResponseWriter, status int, body []byte) string {
	c := classifyUpstreamError(status, body)
	WriteError(w, c.Status, c.Type, c.Code, c.PublicMessage)
	return c.RawDetail
}

func sanitizeStreamErrorDetailMap(rawErr any) (map[string]any, string) {
	b, _ := json.Marshal(ErrorEnvelope{
		Error: ErrorDetail{Message: ""},
	})
	if rawErr != nil {
		b, _ = json.Marshal(map[string]any{"error": rawErr})
	}
	c := classifyUpstreamError(http.StatusBadGateway, b)
	return map[string]any{
		"type":    c.Type,
		"code":    c.Code,
		"message": c.PublicMessage,
		"param":   nil,
	}, c.RawDetail
}

func marshalErrorEnvelopeBytes(errType, code, message string) []byte {
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
	return b
}

func parseInlineChatError(data string) (sanitizedUpstreamError, bool) {
	var probe struct {
		Choices []json.RawMessage `json:"choices"`
		Error   json.RawMessage   `json:"error"`
	}
	if json.Unmarshal([]byte(data), &probe) != nil {
		return sanitizedUpstreamError{}, false
	}
	errRaw := strings.TrimSpace(string(probe.Error))
	if errRaw == "" || errRaw == "null" || len(probe.Choices) > 0 {
		return sanitizedUpstreamError{}, false
	}
	return classifyUpstreamError(http.StatusBadGateway, []byte(data)), true
}
