package openai

import (
	"context"
	"crypto/subtle"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"bohrium-club-auto/internal/config"
)

const ctxKeyAPIKeyPolicy ctxKey = "bohrium_api_key_policy"

func withCallerKeyPolicy(ctx context.Context, key config.APIKey) context.Context {
	key.AllowedModels = append([]string(nil), key.AllowedModels...)
	return context.WithValue(ctx, ctxKeyAPIKeyPolicy, key)
}

func callerKeyPolicy(ctx context.Context) (config.APIKey, bool) {
	key, ok := ctx.Value(ctxKeyAPIKeyPolicy).(config.APIKey)
	return key, ok
}

type apiKeyAuthError struct {
	code    string
	message string
}

func (e *apiKeyAuthError) Error() string { return e.message }

// authenticateAPIKey deliberately compares the candidate against every stored
// secret. Disabled and expired keys are recognized so they cannot silently fall
// back to public access when allowNoAuth is enabled.
func authenticateAPIKey(candidate string, keys []config.APIKey, now time.Time) (config.APIKey, *apiKeyAuthError) {
	var matched config.APIKey
	found := false
	for _, key := range keys {
		if key.Key == "" {
			continue
		}
		if subtleCompare(candidate, key.Key) {
			matched = key
			found = true
		}
	}
	if !found {
		return config.APIKey{}, nil
	}
	if !matched.Enabled {
		return matched, &apiKeyAuthError{code: "api_key_disabled", message: "This API key is disabled"}
	}
	if matched.Expired(now) {
		return matched, &apiKeyAuthError{code: "api_key_expired", message: "This API key has expired"}
	}
	return matched, nil
}

// Isolated to keep all secret comparisons in this file's authentication path.
func subtleCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type keyRateState struct {
	requests []time.Time
	active   int
}

type keyRateLimiter struct {
	mu     sync.Mutex
	states map[string]*keyRateState
}

func newKeyRateLimiter() *keyRateLimiter {
	return &keyRateLimiter{states: make(map[string]*keyRateState)}
}

type keyRateDecision struct {
	allowed    bool
	code       string
	message    string
	retryAfter int
	release    func()
}

// acquire applies independent per-key sliding-minute and in-flight limits.
// A successful acquisition must be released exactly once.
func (l *keyRateLimiter) acquire(keyID string, requestsPerMinute, concurrency int, now time.Time) keyRateDecision {
	if l == nil || keyID == "" || (requestsPerMinute <= 0 && concurrency <= 0) {
		return keyRateDecision{allowed: true, release: func() {}}
	}
	if now.IsZero() {
		now = time.Now()
	}
	l.mu.Lock()
	state := l.states[keyID]
	if state == nil {
		state = &keyRateState{}
		l.states[keyID] = state
	}
	cutoff := now.Add(-time.Minute)
	kept := state.requests[:0]
	for _, at := range state.requests {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	state.requests = kept
	if concurrency > 0 && state.active >= concurrency {
		l.mu.Unlock()
		return keyRateDecision{
			code:       "concurrency_limit_exceeded",
			message:    fmt.Sprintf("API key concurrency limit reached (%d)", concurrency),
			retryAfter: 1,
		}
	}
	if requestsPerMinute > 0 && len(state.requests) >= requestsPerMinute {
		retry := 1
		if len(state.requests) > 0 {
			seconds := int(math.Ceil(state.requests[0].Add(time.Minute).Sub(now).Seconds()))
			if seconds > retry {
				retry = seconds
			}
		}
		l.mu.Unlock()
		return keyRateDecision{
			code:       "rate_limit_exceeded",
			message:    fmt.Sprintf("API key request rate limit reached (%d requests/minute)", requestsPerMinute),
			retryAfter: retry,
		}
	}
	if requestsPerMinute > 0 {
		state.requests = append(state.requests, now)
	}
	state.active++
	l.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			current := l.states[keyID]
			if current == nil {
				return
			}
			if current.active > 0 {
				current.active--
			}
			if current.active == 0 && len(current.requests) == 0 {
				delete(l.states, keyID)
			}
		})
	}
	return keyRateDecision{allowed: true, release: release}
}

func keyAllowsModel(ctx context.Context, modelID string) bool {
	key, ok := callerKeyPolicy(ctx)
	return !ok || key.AllowsModel(modelID)
}

func keyDefaultModel(ctx context.Context) string {
	key, ok := callerKeyPolicy(ctx)
	if !ok {
		return ""
	}
	return strings.TrimSpace(key.DefaultModel)
}

type modelNotAllowedError struct {
	modelID string
	msg     string
}

func (e *modelNotAllowedError) Error() string { return e.msg }

func rejectModelNotAllowed(modelID string) error {
	return &modelNotAllowedError{
		modelID: modelID,
		msg:     fmt.Sprintf("model %q is not allowed for this API key", modelID),
	}
}

func isTokenGeneratingEndpoint(path string) bool {
	return path == "/v1/chat/completions" || path == "/v1/responses"
}

// enforceKeyQuota checks already persisted usage immediately before request
// execution. Token usage is settled by RecordRequest after the upstream call.
func (g *Gateway) enforceKeyQuota(w http.ResponseWriter, r *http.Request, key config.APIKey) bool {
	if key.TokenQuota <= 0 && key.DailyTokenQuota <= 0 {
		return true
	}
	if !isTokenGeneratingEndpoint(r.URL.Path) {
		return true
	}
	if g.mgr == nil || g.mgr.Store() == nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "store_error", publicMsgStoreError)
		return false
	}
	timezone := g.cfg.Get().Schedule.Timezone
	total, today, err := g.mgr.Store().GatewayKeyUsageForAdmission(key.ID, time.Now().UTC(), timezone)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "server_error", "store_error", publicMsgStoreError)
		return false
	}
	if key.TokenQuota > 0 && total >= key.TokenQuota {
		WriteError(w, http.StatusTooManyRequests, "insufficient_quota", "token_quota_exceeded",
			fmt.Sprintf("API key total token quota exhausted (%d tokens)", key.TokenQuota))
		return false
	}
	if key.DailyTokenQuota > 0 && today >= key.DailyTokenQuota {
		WriteError(w, http.StatusTooManyRequests, "insufficient_quota", "daily_token_quota_exceeded",
			fmt.Sprintf("API key daily token quota exhausted (%d tokens)", key.DailyTokenQuota))
		return false
	}
	return true
}
