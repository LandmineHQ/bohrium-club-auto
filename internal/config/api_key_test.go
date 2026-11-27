package config

import (
	"testing"
	"time"
)

func TestAPIKeyPolicyNormalizationAndExpiration(t *testing.T) {
	for input, want := range map[string]string{
		" dsflash-high ":    "dsflash-high",
		"SCIX/DsFlash-High": "dsflash-high",
		"scix/kimi-k3":      "kimi-k3",
	} {
		if got := NormalizeModelID(input); got != want {
			t.Fatalf("NormalizeModelID(%q)=%q, want %q", input, got, want)
		}
	}
	key := APIKey{RestrictModels: true, AllowedModels: []string{"SCIX/DsFlash-High"}}
	if !key.AllowsModel("dsflash-high") || key.AllowsModel("dspro-high") {
		t.Fatalf("unexpected model allowlist behavior")
	}
	now := time.Now().UTC()
	if (APIKey{}).Expired(now) {
		t.Fatal("empty expiration must never expire")
	}
	if (APIKey{ExpiresAt: "not-a-time"}).Expired(now) != true {
		t.Fatal("invalid persisted expiration must fail closed")
	}
	if !(APIKey{ExpiresAt: now.Add(-time.Second).Format(time.RFC3339)}).Expired(now) {
		t.Fatal("past expiration must be expired")
	}
	if (APIKey{ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}).Expired(now) {
		t.Fatal("future expiration must remain active")
	}
}
