package chromehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultChromeUA mimics a current stable Desktop Chrome browser on Windows 10/11.
const (
	DefaultChromeUA           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
	DefaultSecCHUA            = `"Chromium";v="136", "Google Chrome";v="136", "Not.A/Brand";v="99"`
	DefaultSecCHUAMobile      = "?0"
	DefaultSecCHUAPlatform    = `"Windows"`
	DefaultAcceptLanguage     = "zh-CN,zh;q=0.9,en;q=0.8"
)

// Options configures a Chrome-emulating HTTP client.
type Options struct {
	UserAgent string
	ProxyURL  string
	Timeout   time.Duration
}

// Client wraps net/http.Client and automatically injects realistic Chrome browser headers
// on web requests, while also providing native (non-browser) HTTP transports for desktop
// protocol endpoints (such as Bohrium SciX /scix/llm/*) that should not send Chrome headers.
type Client struct {
	httpClient         *http.Client
	streamClient       *http.Client
	nativeClient       *http.Client
	nativeStreamClient *http.Client
	userAgent          string
	secCHUA            string
	proxyURL           string
}

// nativeTransport forwards requests without browser Sec-CH-UA / Accept-Language / Chrome UA headers,
// and suppresses Go's default "Go-http-client/1.1" User-Agent (matching Rust reqwest in scix.exe).
type nativeTransport struct {
	base http.RoundTripper
}

func (t *nativeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if _, exists := clone.Header["User-Agent"]; !exists {
		// Empty slice tells Go's net/http transport to omit the User-Agent header completely.
		clone.Header["User-Agent"] = []string{}
	}
	return t.base.RoundTrip(clone)
}

// chromeTransport wraps an http.RoundTripper to enforce Chrome headers.
type chromeTransport struct {
	base      http.RoundTripper
	userAgent string
	secCHUA   string
}

func (t *chromeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	// Always force Chrome User-Agent unless explicitly overridden with a non-Go UA.
	ua := clone.Header.Get("User-Agent")
	if ua == "" || strings.HasPrefix(ua, "Go-http-client") {
		clone.Header.Set("User-Agent", t.userAgent)
	}
	if clone.Header.Get("Accept-Language") == "" {
		clone.Header.Set("Accept-Language", DefaultAcceptLanguage)
	}
	if clone.Header.Get("Sec-CH-UA") == "" {
		clone.Header.Set("Sec-CH-UA", t.secCHUA)
	}
	if clone.Header.Get("Sec-CH-UA-Mobile") == "" {
		clone.Header.Set("Sec-CH-UA-Mobile", DefaultSecCHUAMobile)
	}
	if clone.Header.Get("Sec-CH-UA-Platform") == "" {
		clone.Header.Set("Sec-CH-UA-Platform", DefaultSecCHUAPlatform)
	}
	return t.base.RoundTrip(clone)
}

// New creates a new Chrome-emulating HTTP client.
func New(opts Options) (*Client, error) {
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = DefaultChromeUA
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTransport.MaxIdleConns = 64
	baseTransport.IdleConnTimeout = 90 * time.Second
	baseTransport.TLSHandshakeTimeout = 15 * time.Second
	if p := strings.TrimSpace(opts.ProxyURL); p != "" {
		parsed, err := url.Parse(p)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", p, err)
		}
		baseTransport.Proxy = http.ProxyURL(parsed)
	}

	secCH := deriveSecCHUA(ua)
	tr := &chromeTransport{
		base:      baseTransport,
		userAgent: ua,
		secCHUA:   secCH,
	}

	streamTransport := baseTransport.Clone()
	streamTransport.ResponseHeaderTimeout = 3 * time.Minute
	streamTr := &chromeTransport{
		base:      streamTransport,
		userAgent: ua,
		secCHUA:   secCH,
	}

	nativeTr := &nativeTransport{
		base: baseTransport,
	}
	nativeStreamTr := &nativeTransport{
		base: streamTransport,
	}

	return &Client{
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   timeout,
		},
		streamClient: &http.Client{
			Transport: streamTr,
			Timeout:   0, // Streaming requests rely on request context and ResponseHeaderTimeout
		},
		nativeClient: &http.Client{
			Transport: nativeTr,
			Timeout:   timeout,
		},
		nativeStreamClient: &http.Client{
			Transport: nativeStreamTr,
			Timeout:   0,
		},
		userAgent: ua,
		secCHUA:   secCH,
		proxyURL:  strings.TrimSpace(opts.ProxyURL),
	}, nil
}

func deriveSecCHUA(ua string) string {
	// Extract major Chrome version if present in UA string, e.g. Chrome/136.0.0.0
	idx := strings.Index(ua, "Chrome/")
	if idx < 0 {
		return DefaultSecCHUA
	}
	rest := ua[idx+len("Chrome/"):]
	major := ""
	for i := 0; i < len(rest); i++ {
		if rest[i] >= '0' && rest[i] <= '9' {
			major += string(rest[i])
		} else {
			break
		}
	}
	if major == "" {
		return DefaultSecCHUA
	}
	return fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", "Not.A/Brand";v="99"`, major, major)
}

// UserAgent returns the active Chrome User-Agent string.
func (c *Client) UserAgent() string {
	return c.userAgent
}

// ProxyURL returns the configured proxy URL, if any.
func (c *Client) ProxyURL() string {
	return c.proxyURL
}

// HTTPClient exposes the underlying *http.Client (still using chromeTransport).
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

// Do executes an HTTP request using the Chrome transport.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}

// DoStream executes an HTTP request without a total body timeout (suitable for long-lived SSE streams).
// Callers are responsible for closing resp.Body and cancelling req.Context().
func (c *Client) DoStream(req *http.Request) (*http.Response, error) {
	return c.streamClient.Do(req)
}

// DoNative executes an HTTP request without Chrome browser headers (and without Go's default User-Agent),
// matching Bohrium desktop client's Rust reqwest transport for /scix/llm/* endpoints.
func (c *Client) DoNative(req *http.Request) (*http.Response, error) {
	return c.nativeClient.Do(req)
}

// DoNativeStream executes a streaming HTTP request without Chrome browser headers or total body timeout.
func (c *Client) DoNativeStream(req *http.Request) (*http.Response, error) {
	return c.nativeStreamClient.Do(req)
}

// Response holds a fully read HTTP response.
type Response struct {
	StatusCode  int
	Headers     http.Header
	Body        []byte
	FinalURL    string
	ContentType string
}

// DoJSON sends an optional JSON payload and reads the response bytes.
func (c *Client) DoJSON(ctx context.Context, method, rawURL string, payload any, headers map[string]string) (*Response, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request payload: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.execAndRead(req, 16*1024*1024)
}

// DoRaw sends a raw byte body (e.g., binary PDF upload) and reads the response.
func (c *Client) DoRaw(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) (*Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.execAndRead(req, 64*1024*1024)
}

func (c *Client) execAndRead(req *http.Request, maxBytes int64) (*Response, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds limit (%d bytes) for %s", maxBytes, req.URL.String())
	}

	finalURL := req.URL.String()
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return &Response{
		StatusCode:  resp.StatusCode,
		Headers:     resp.Header.Clone(),
		Body:        data,
		FinalURL:    finalURL,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}
