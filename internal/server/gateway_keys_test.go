package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/store"
)

func TestGatewayKeyPolicyAdminLifecycle(t *testing.T) {
	srv, st, password := setupTestServer(t)
	doAdmin := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("X-Admin-Password", password)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	unauth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/gateway/keys", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected key list to require admin, got %d", unauth.Code)
	}

	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	createdRec := doAdmin(http.MethodPost, "/api/gateway/keys", map[string]any{
		"name": "Codex isolated", "enabled": true, "defaultModel": "scix/dsflash-high",
		"restrictModels": true, "allowedModels": []string{"scix/dsflash-high", "DSFLASH-HIGH", "vision"},
		"rateLimit": 60, "concurrencyLimit": 3, "expiresAt": expiresAt,
		"tokenQuota": 10000, "dailyTokenQuota": 500,
	})
	if createdRec.Code != http.StatusOK {
		t.Fatalf("create key: status=%d body=%s", createdRec.Code, createdRec.Body.String())
	}
	var created struct {
		OK  bool              `json:"ok"`
		Key gatewayAPIKeyView `json:"key"`
	}
	if err := json.Unmarshal(createdRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	key := created.Key
	if key.ID == "" || !strings.HasPrefix(key.Key, "sk-brm-") || key.Status != "active" || key.UpdatedAt == "" {
		t.Fatalf("unexpected created key: %+v", key)
	}
	if key.DefaultModel != "dsflash-high" || len(key.AllowedModels) != 2 || key.AllowedModels[0] != "dsflash-high" || key.AllowedModels[1] != "vision" {
		t.Fatalf("expected normalized deduplicated model policy, got %+v", key.APIKey)
	}
	if key.RateLimit != 60 || key.ConcurrencyLimit != 3 || key.TokenQuota != 10000 || key.DailyTokenQuota != 500 || key.ExpiresAt == "" {
		t.Fatalf("policy fields not persisted: %+v", key.APIKey)
	}

	now := time.Now().UTC()
	for _, rec := range []store.GatewayUsageRecord{
		{ID: "admin_key_today", CreatedAt: now, APIKeyID: key.ID, TotalTokens: 400, Status: 200},
		{ID: "admin_key_old", CreatedAt: now.AddDate(0, 0, -2), APIKeyID: key.ID, TotalTokens: 100, Status: 200},
	} {
		if err := st.RecordGatewayUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
	listRec := doAdmin(http.MethodGet, "/api/gateway/keys", nil)
	var list struct {
		APIKeys []gatewayAPIKeyView `json:"apiKeys"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var current *gatewayAPIKeyView
	for i := range list.APIKeys {
		if list.APIKeys[i].ID == key.ID {
			current = &list.APIKeys[i]
			break
		}
	}
	if current == nil || current.RequestCount != 2 || current.TotalTokens != 500 || current.TodayTokens != 400 || current.LastUsedAt == nil {
		t.Fatalf("unexpected key usage view: %+v", current)
	}
	if current.RemainingQuota == nil || *current.RemainingQuota != 9500 || current.RemainingDailyQuota == nil || *current.RemainingDailyQuota != 100 {
		t.Fatalf("unexpected remaining quota: %+v", current)
	}

	patchRec := doAdmin(http.MethodPatch, "/api/gateway/keys/"+key.ID, map[string]any{
		"enabled": false, "expiresAt": "", "rateLimit": 0, "tokenQuota": 0,
	})
	if patchRec.Code != http.StatusOK || !strings.Contains(patchRec.Body.String(), `"status":"disabled"`) || !strings.Contains(patchRec.Body.String(), `"rateLimit":0`) {
		t.Fatalf("patch key: status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	persisted := srv.cfg.Get().Gateway.APIKeys
	var stored config.APIKey
	for _, item := range persisted {
		if item.ID == key.ID {
			stored = item
		}
	}
	if stored.ExpiresAt != "" || stored.TokenQuota != 0 || stored.Enabled {
		t.Fatalf("explicit zero/empty patch did not clear fields: %+v", stored)
	}

	rotateRec := doAdmin(http.MethodPost, "/api/gateway/keys/"+key.ID+"/regenerate", nil)
	var rotated struct {
		Key gatewayAPIKeyView `json:"key"`
	}
	if err := json.Unmarshal(rotateRec.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotateRec.Code != http.StatusOK || rotated.Key.ID != key.ID || rotated.Key.Key == key.Key || rotated.Key.TokenQuota != 0 {
		t.Fatalf("unexpected rotation result: status=%d key=%+v", rotateRec.Code, rotated.Key)
	}

	badRec := doAdmin(http.MethodPatch, "/api/gateway/keys/"+key.ID, map[string]any{"dailyTokenQuota": -1})
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected negative quota rejection, got %d", badRec.Code)
	}
	emptyModelsRec := doAdmin(http.MethodPatch, "/api/gateway/keys/"+key.ID, map[string]any{"restrictModels": true, "allowedModels": []string{}})
	if emptyModelsRec.Code != http.StatusBadRequest {
		t.Fatalf("expected empty active model whitelist rejection, got %d", emptyModelsRec.Code)
	}
	missingRec := doAdmin(http.MethodPatch, "/api/gateway/keys/key_missing", map[string]any{"name": "missing"})
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected missing key 404, got %d", missingRec.Code)
	}
	deleteRec := doAdmin(http.MethodDelete, "/api/gateway/keys/"+key.ID, nil)
	if deleteRec.Code != http.StatusOK || !strings.Contains(deleteRec.Body.String(), `"deleted":true`) {
		t.Fatalf("delete key: status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	total, _, err := st.GatewayKeyUsageForAdmission(key.ID, now, "Asia/Shanghai")
	if err != nil || total != 500 {
		t.Fatalf("deleting key must preserve audit counters, total=%d err=%v", total, err)
	}
}
