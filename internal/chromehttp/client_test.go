package chromehttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestDoTransferBypassesShortRPCTimeout(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("uploaded-ok"))
	}))
	defer srv.Close()

	cli, err := New(Options{Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Standard DoRaw enforces short RPC Timeout (50ms) -> should fail with timeout
	if _, err := cli.DoRaw(context.Background(), http.MethodPost, srv.URL, []byte("payload"), nil); err == nil {
		t.Fatalf("expected DoRaw to time out with 50ms RPC timeout")
	}

	// DoTransfer does not enforce short RPC Timeout -> should succeed and keep Chrome UA
	resp, err := cli.DoTransfer(context.Background(), http.MethodPost, srv.URL, []byte("payload"), nil)
	if err != nil {
		t.Fatalf("DoTransfer failed unexpectedly: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(resp.Body) != "uploaded-ok" {
		t.Fatalf("unexpected DoTransfer response: status=%d body=%q", resp.StatusCode, string(resp.Body))
	}
	if !strings.Contains(gotUA, "Chrome/") {
		t.Fatalf("expected Chrome UA on DoTransfer, got %q", gotUA)
	}
}
