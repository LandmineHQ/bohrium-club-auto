package chromehttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChromeHeadersInjected(t *testing.T) {
	var gotUA, gotSecCH, gotMobile, gotPlatform, gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotSecCH = r.Header.Get("Sec-CH-UA")
		gotMobile = r.Header.Get("Sec-CH-UA-Mobile")
		gotPlatform = r.Header.Get("Sec-CH-UA-Platform")
		gotLang = r.Header.Get("Accept-Language")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cli, err := New(Options{})
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	if _, err := cli.DoJSON(context.Background(), http.MethodGet, srv.URL, nil, nil); err != nil {
		t.Fatalf("DoJSON: %v", err)
	}

	if strings.Contains(gotUA, "Go-http-client") {
		t.Fatalf("leaked Go default User-Agent: %q", gotUA)
	}
	if !strings.Contains(gotUA, "Chrome/") || !strings.Contains(gotUA, "Safari/537.36") {
		t.Fatalf("expected Chrome UA, got %q", gotUA)
	}
	if !strings.Contains(gotSecCH, "Google Chrome") {
		t.Fatalf("expected Sec-CH-UA with Google Chrome, got %q", gotSecCH)
	}
	if gotMobile != "?0" || gotPlatform != `"Windows"` || gotLang == "" {
		t.Fatalf("unexpected CH/lang headers: mobile=%q platform=%q lang=%q", gotMobile, gotPlatform, gotLang)
	}
}

func TestCustomChromeUADerivesSecCH(t *testing.T) {
	cli, err := New(Options{
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(cli.secCHUA, `v="142"`) {
		t.Fatalf("expected Sec-CH-UA major version 142, got %q", cli.secCHUA)
	}
}
