package openai_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/openai"
	"bohrium-club-auto/internal/store"
)

func decodeOpenAIError(t *testing.T, resp *http.Response) openai.ErrorEnvelope {
	t.Helper()
	defer resp.Body.Close()
	var envelope openai.ErrorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return envelope
}

func TestPerKeyModelAllowlistAndDefaultModel(t *testing.T) {
	_, gwSrv, _, _, apiKey, capUp := setupTestGatewayEnvWithPolicy(t, true, func(cfg *config.Config) {
		for i := range cfg.Gateway.APIKeys {
			cfg.Gateway.APIKeys[i].RestrictModels = true
			cfg.Gateway.APIKeys[i].AllowedModels = []string{"dsflash-high"}
			cfg.Gateway.APIKeys[i].DefaultModel = "dsflash-high"
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"scoped","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{"content":"scoped"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})

	req, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list openai.OpenAIModelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(list.Data) != 1 || list.Data[0].ID != "dsflash-high" {
		t.Fatalf("expected only dsflash-high, got status=%d list=%+v", resp.StatusCode, list)
	}

	reqDenied, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/models/dspro-high", nil)
	reqDenied.Header.Set("Authorization", "Bearer "+apiKey)
	respDenied, err := http.DefaultClient.Do(reqDenied)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeOpenAIError(t, respDenied)
	if respDenied.StatusCode != http.StatusNotFound || envelope.Error.Code != "model_not_allowed" {
		t.Fatalf("expected scoped model 404, got status=%d envelope=%+v", respDenied.StatusCode, envelope)
	}

	chatDenied, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"dspro-high","messages":[{"role":"user","content":"denied"}]}`))
	chatDenied.Header.Set("Authorization", "Bearer "+apiKey)
	chatRespDenied, err := http.DefaultClient.Do(chatDenied)
	if err != nil {
		t.Fatal(err)
	}
	chatEnvelope := decodeOpenAIError(t, chatRespDenied)
	if chatRespDenied.StatusCode != http.StatusForbidden || chatEnvelope.Error.Code != "model_not_allowed" {
		t.Fatalf("expected chat model 403, got status=%d envelope=%+v", chatRespDenied.StatusCode, chatEnvelope)
	}

	chatDefault, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{"stream":false,"messages":[{"role":"user","content":"use key default"}]}`))
	chatDefault.Header.Set("Authorization", "Bearer "+apiKey)
	chatDefaultResp, err := http.DefaultClient.Do(chatDefault)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(chatDefaultResp.Body)
	chatDefaultResp.Body.Close()
	if chatDefaultResp.StatusCode != http.StatusOK {
		t.Fatalf("expected per-key default model call to succeed, got %d", chatDefaultResp.StatusCode)
	}
	capUp.mu.Lock()
	defer capUp.mu.Unlock()
	if capUp.body["model"] != "dsflash-high" {
		t.Fatalf("expected upstream dsflash-high, got %v", capUp.body["model"])
	}
}

func TestPerKeyExpiration(t *testing.T) {
	_, gwSrv, _, _, apiKey, _ := setupTestGatewayEnvWithPolicy(t, true, func(cfg *config.Config) {
		for i := range cfg.Gateway.APIKeys {
			cfg.Gateway.APIKeys[i].ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
		}
	}, nil)
	req, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeOpenAIError(t, resp)
	if resp.StatusCode != http.StatusUnauthorized || envelope.Error.Code != "api_key_expired" {
		t.Fatalf("expected expired key 401, got status=%d envelope=%+v", resp.StatusCode, envelope)
	}
}

func TestPerKeySlidingRateLimit(t *testing.T) {
	_, gwSrv, _, _, apiKey, _ := setupTestGatewayEnvWithPolicy(t, true, func(cfg *config.Config) {
		for i := range cfg.Gateway.APIKeys {
			cfg.Gateway.APIKeys[i].RateLimit = 1
		}
	}, nil)
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("first request status=%d", resp.StatusCode)
			}
			continue
		}
		envelope := decodeOpenAIError(t, resp)
		if resp.StatusCode != http.StatusTooManyRequests || envelope.Error.Code != "rate_limit_exceeded" || resp.Header.Get("Retry-After") == "" {
			t.Fatalf("expected rate-limited second request, got status=%d envelope=%+v retry=%q", resp.StatusCode, envelope, resp.Header.Get("Retry-After"))
		}
	}
}

func TestPerKeyConcurrencyLimit(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	_, gwSrv, _, _, apiKey, _ := setupTestGatewayEnvWithPolicy(t, true, func(cfg *config.Config) {
		for i := range cfg.Gateway.APIKeys {
			cfg.Gateway.APIKeys[i].ConcurrencyLimit = 1
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"id":"concurrency","object":"chat.completion.chunk","created":1700000000,"model":"dsflash-high","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})

	firstDone := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"dsflash-high","stream":true,"messages":[{"role":"user","content":"hold"}]}`))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			firstDone <- nil
			return
		}
		firstDone <- resp
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first request")
	}

	second, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/models", nil)
	second.Header.Set("Authorization", "Bearer "+apiKey)
	secondResp, err := http.DefaultClient.Do(second)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeOpenAIError(t, secondResp)
	if secondResp.StatusCode != http.StatusTooManyRequests || envelope.Error.Code != "concurrency_limit_exceeded" {
		t.Fatalf("expected concurrency rejection, got status=%d envelope=%+v", secondResp.StatusCode, envelope)
	}
	close(release)
	select {
	case first := <-firstDone:
		if first == nil {
			t.Fatal("first request failed")
		}
		_, _ = io.ReadAll(first.Body)
		first.Body.Close()
		if first.StatusCode != http.StatusOK {
			t.Fatalf("first request status=%d", first.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out releasing first request")
	}
}

func TestPerKeyTokenQuotas(t *testing.T) {
	for _, tc := range []struct {
		name       string
		total      int64
		daily      int64
		used       int64
		wantCode   string
		wantStatus int
	}{
		{name: "total", total: 100, used: 100, wantCode: "token_quota_exceeded", wantStatus: http.StatusTooManyRequests},
		{name: "daily", daily: 50, used: 50, wantCode: "daily_token_quota_exceeded", wantStatus: http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, gwSrv, st, _, apiKey, _ := setupTestGatewayEnvWithPolicy(t, true, func(cfg *config.Config) {
				for i := range cfg.Gateway.APIKeys {
					cfg.Gateway.APIKeys[i].TokenQuota = tc.total
					cfg.Gateway.APIKeys[i].DailyTokenQuota = tc.daily
				}
			}, nil)
			if err := st.RecordGatewayUsage(store.GatewayUsageRecord{ID: "quota_" + tc.name, CreatedAt: time.Now().UTC(), APIKeyID: "env-1", TotalTokens: tc.used, Status: 200}); err != nil {
				t.Fatal(err)
			}
			req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"dsflash-high","messages":[{"role":"user","content":"over quota"}]}`))
			req.Header.Set("Authorization", "Bearer "+apiKey)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			envelope := decodeOpenAIError(t, resp)
			if resp.StatusCode != tc.wantStatus || envelope.Error.Code != tc.wantCode {
				t.Fatalf("expected %s, got status=%d envelope=%+v", tc.wantCode, resp.StatusCode, envelope)
			}

			balanceReq, _ := http.NewRequest(http.MethodGet, gwSrv.URL+"/v1/user/balance", nil)
			balanceReq.Header.Set("Authorization", "Bearer "+apiKey)
			balanceResp, err := http.DefaultClient.Do(balanceReq)
			if err != nil {
				t.Fatal(err)
			}
			var balance openai.UserBalanceResponse
			_ = json.NewDecoder(balanceResp.Body).Decode(&balance)
			balanceResp.Body.Close()
			if balanceResp.StatusCode != http.StatusOK || balance.Balance != 0 || balance.IsAvailable {
				t.Fatalf("expected readable exhausted balance, got status=%d balance=%+v", balanceResp.StatusCode, balance)
			}
		})
	}
}
