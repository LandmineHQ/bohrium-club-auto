package recon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"bohrium-club-auto/internal/chromehttp"
)

const (
	DefaultClubURL     = "https://club.bohrium.com/"
	DefaultPlatformURL = "https://platform.bohrium.com/login"
	DefaultWWWURL      = "https://www.bohrium.com/"
	DefaultOutDir      = "recon/bohrium-web"
	DefaultMaxAssets   = 200
	DefaultMaxBytes    = 25 * 1024 * 1024
)

// Options configures Bohrium web source snapshot / diff runs (modeled on LandmineHQ/duckai-cpa).
type Options struct {
	TargetURLs      []string
	OutDir          string
	ManifestPath    string
	UserAgent       string
	ProxyURL        string
	MaxAssets       int
	MaxBytes        int64
	DelayMs         int
	TimeoutMs       int
	IncludeExternal bool
	Tolerant        bool
}

// AssetRecord describes one fetched upstream static asset.
type AssetRecord struct {
	URL          string `json:"url"`
	LocalPath    string `json:"localPath,omitempty"`
	Status       int    `json:"status"`
	ContentType  string `json:"contentType,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Size         int    `json:"size"`
	SHA256       string `json:"sha256,omitempty"`
	Error        string `json:"error,omitempty"`
	Skipped      string `json:"skipped,omitempty"`
}

// TargetSummary captures entry HTML fingerprint for one Bohrium origin.
type TargetSummary struct {
	EntryURL   string `json:"entryUrl"`
	FinalURL   string `json:"finalUrl"`
	Status     int    `json:"status"`
	HTMLSHA256 string `json:"htmlSha256"`
	Version    string `json:"version,omitempty"`
}

// ContractMarkers extracts critical Bohrium frontend API markers found in crawled bundles
// so drift checks immediately highlight breaking upstream changes.
type ContractMarkers struct {
	PlazaTasksEndpoint  bool     `json:"plazaTasksEndpoint"`
	PlazaClaimEndpoint  bool     `json:"plazaClaimEndpoint"`
	GatewayHeader       bool     `json:"gatewayHeader"`
	WeChatQREndpoint    bool     `json:"wechatQrEndpoint"`
	DailyTaskBundleURLs []string `json:"dailyTaskBundleUrls,omitempty"`
	SciSpaceTeamIDFound bool     `json:"sciSpaceTeamIdFound"`
	QueryEventsEndpoint bool     `json:"queryEventsEndpoint"`
}

// Manifest is written to recon/bohrium-web/manifest.json.
type Manifest struct {
	SchemaVersion   int             `json:"schemaVersion"`
	FetchedAt       string          `json:"fetchedAt"`
	Targets         []TargetSummary `json:"targets"`
	Assets          []AssetRecord   `json:"assets"`
	Markers         ContractMarkers `json:"markers"`
	SkippedExternal []string        `json:"skippedExternal,omitempty"`
	TotalBytes      int64           `json:"totalBytes"`
	Truncated       bool            `json:"truncated"`
	OutDir          string          `json:"outDir,omitempty"`
	ManifestPath    string          `json:"manifestPath,omitempty"`
}

// ChangedAsset pairs an updated live asset with its baseline record.
type ChangedAsset struct {
	AssetRecord
	Previous AssetRecord `json:"previous"`
}

// DiffResult summarizes differences between local baseline manifest and live upstream site.
type DiffResult struct {
	Same              bool            `json:"same"`
	BaselineExists    bool            `json:"baselineExists"`
	BaselineFetchedAt string          `json:"baselineFetchedAt,omitempty"`
	CurrentFetchedAt  string          `json:"currentFetchedAt,omitempty"`
	TargetsChanged    []string        `json:"targetsChanged,omitempty"`
	MarkerChanges     []string        `json:"markerChanges,omitempty"`
	Added             []AssetRecord   `json:"added"`
	Removed           []AssetRecord   `json:"removed"`
	Changed           []ChangedAsset  `json:"changed"`
	BaselineAssets    int             `json:"baselineAssets"`
	CurrentAssets     int             `json:"currentAssets"`
	Summary           string          `json:"summary"`
	CurrentMarkers    ContractMarkers `json:"currentMarkers"`
}

var (
	reHTMLTagRef   = regexp.MustCompile(`(?i)<(?:script|link|img|source)[^>]*?\s(?:src|href)=["']([^"']+)["']`)
	reHTMLAssetRef = regexp.MustCompile(`(?i)\s(?:src|href)=["']([^"']+\.(?:m?js|css|json)(?:[?#][^"']*)?)["']`)
	reInlineScript = regexp.MustCompile(`(?is)<script[^>]*>([\s\S]*?)</script>`)
	reScriptImport = regexp.MustCompile(`(?m)(?:import|export)\s+(?:[^'"` + "`" + `]+?\s+from\s+)?["']([^"']+)["']`)
	reDynImport    = regexp.MustCompile(`(?:import|require)\s*\(\s*["']([^"']+)["']\s*\)`)
	reCSSURL       = regexp.MustCompile(`(?i)url\(\s*["']?([^"')\s]+)["']?\s*\)`)
	reVersionAttr  = regexp.MustCompile(`(?i)(?:data-version=["']([^"']+)["']|daily-task-[a-z0-9-]+-(\d{8,})|buildId["']?\s*[:=]\s*["']([A-Za-z0-9_-]{6,})["'])`)
	reSafeSeg      = regexp.MustCompile(`[^A-Za-z0-9._-]`)
	reClientSignal = regexp.MustCompile(`clientSignal="[^"]*"`)
	reHTMLNonce    = regexp.MustCompile(`\snonce="[^"]*"`)
)

// NormalizeOptions applies sensible defaults to Options.
func NormalizeOptions(opts Options) Options {
	if len(opts.TargetURLs) == 0 {
		opts.TargetURLs = []string{DefaultClubURL, DefaultPlatformURL}
	}
	if strings.TrimSpace(opts.OutDir) == "" {
		opts.OutDir = DefaultOutDir
	}
	if strings.TrimSpace(opts.ManifestPath) == "" {
		opts.ManifestPath = filepath.Join(opts.OutDir, "manifest.json")
	}
	if opts.MaxAssets <= 0 {
		opts.MaxAssets = DefaultMaxAssets
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.DelayMs < 0 {
		opts.DelayMs = 0
	}
	if opts.TimeoutMs <= 0 {
		opts.TimeoutMs = 25000
	}
	return opts
}

// TakeSnapshot crawls the configured public Bohrium frontend entries and referenced static JS/CSS assets.
// If writeDisk is true, it replaces opts.OutDir and writes manifest.json (like duckai-cpa recon:pull).
func TakeSnapshot(ctx context.Context, opts Options, writeDisk bool) (*Manifest, error) {
	opts = NormalizeOptions(opts)
	client, err := chromehttp.New(chromehttp.Options{
		UserAgent: opts.UserAgent,
		ProxyURL:  opts.ProxyURL,
		Timeout:   time.Duration(opts.TimeoutMs) * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}

	allowedOrigins := make(map[string]bool)
	for _, raw := range opts.TargetURLs {
		if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
			allowedOrigins[strings.ToLower(u.Scheme+"://"+u.Host)] = true
		}
	}

	assets := make([]AssetRecord, 0, 32)
	bodies := make(map[string][]byte)
	seen := make(map[string]bool)
	queue := make([]string, 0, 64)
	skippedExtMap := make(map[string]bool)
	targets := make([]TargetSummary, 0, len(opts.TargetURLs))
	var totalBytes int64
	var markers ContractMarkers
	dailyBundleSet := make(map[string]bool)

	enqueue := func(raw, base string) {
		norm := normalizeURL(raw, base)
		if norm == "" {
			return
		}
		if !isRelevantAsset(norm) {
			return
		}
		parsed, err := url.Parse(norm)
		if err != nil {
			return
		}
		origin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
		isFirstPartyBohriumCDN := strings.EqualFold(parsed.Hostname(), "cdn.bohrium.com") && strings.HasPrefix(parsed.Path, "/platform/web/")
		if !opts.IncludeExternal && !allowedOrigins[origin] && !isFirstPartyBohriumCDN {
			skippedExtMap[norm] = true
			return
		}
		// Skip bare preconnect/dns-prefetch origins with empty or "/" path
		if (parsed.Path == "" || parsed.Path == "/") && !allowedOrigins[origin] {
			return
		}
		if seen[norm] {
			return
		}
		seen[norm] = true
		queue = append(queue, norm)
	}

	scanMarkers := func(assetURL, text string) {
		if strings.Contains(text, "/api/plaza/tasks") {
			markers.PlazaTasksEndpoint = true
		}
		if strings.Contains(text, "/api/plaza/tasks/claim") {
			markers.PlazaClaimEndpoint = true
		}
		if strings.Contains(text, "X-Plaza-Daily-Task-Gateway") {
			markers.GatewayHeader = true
		}
		if strings.Contains(text, "/api/account/wechat/login_qrcode") || strings.Contains(text, "wechat/login_qrcode") {
			markers.WeChatQREndpoint = true
		}
		if strings.Contains(text, "AgK4D5B8Fdju") {
			markers.SciSpaceTeamIDFound = true
		}
		if strings.Contains(text, "/api/v1/query-events") {
			markers.QueryEventsEndpoint = true
		}
		if strings.Contains(assetURL, "daily-task") {
			dailyBundleSet[assetURL] = true
		}
	}

	// Fetch entry HTML pages first.
	for _, entryURL := range opts.TargetURLs {
		normEntry := normalizeURL(entryURL, entryURL)
		if normEntry == "" || seen[normEntry] {
			continue
		}
		seen[normEntry] = true

		resp, err := client.DoRaw(ctx, http.MethodGet, normEntry, nil, map[string]string{
			"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		})
		if err != nil {
			if !opts.Tolerant {
				return nil, fmt.Errorf("fetch entry failed %s: %w", normEntry, err)
			}
			assets = append(assets, AssetRecord{URL: normEntry, Error: err.Error()})
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if !opts.Tolerant {
				return nil, fmt.Errorf("fetch entry failed: HTTP %d %s", resp.StatusCode, normEntry)
			}
		}

		htmlText := string(resp.Body)
		sum := sha256Hex(normalizeHTMLForHash(resp.Body))
		totalBytes += int64(len(resp.Body))
		rec := AssetRecord{
			URL:          resp.FinalURL,
			LocalPath:    safeRelativePath(resp.FinalURL),
			Status:       resp.StatusCode,
			ContentType:  resp.ContentType,
			ETag:         resp.Headers.Get("ETag"),
			LastModified: resp.Headers.Get("Last-Modified"),
			Size:         len(resp.Body),
			SHA256:       sum,
		}
		assets = append(assets, rec)
		bodies[rec.URL] = resp.Body

		scanMarkers(rec.URL, htmlText)
		ver := extractVersion(htmlText)
		targets = append(targets, TargetSummary{
			EntryURL:   normEntry,
			FinalURL:   resp.FinalURL,
			Status:     resp.StatusCode,
			HTMLSHA256: sum,
			Version:    ver,
		})

		for _, ref := range extractHTMLRefs(htmlText) {
			enqueue(ref, resp.FinalURL)
		}
		for _, m := range reInlineScript.FindAllStringSubmatch(htmlText, -1) {
			if !strings.Contains(strings.ToLower(m[0]), "src=") {
				for _, ref := range extractScriptRefs(m[1]) {
					enqueue(ref, resp.FinalURL)
				}
			}
		}
	}

	// Crawl discovered same-origin JS/CSS/JSON assets sequentially with polite delay.
	for len(queue) > 0 && len(assets) < opts.MaxAssets {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		assetURL := queue[0]
		queue = queue[1:]

		resp, err := client.DoRaw(ctx, http.MethodGet, assetURL, nil, map[string]string{
			"Accept": "*/*",
		})
		if err != nil {
			if !opts.Tolerant {
				return nil, fmt.Errorf("fetch asset failed %s: %w", assetURL, err)
			}
			assets = append(assets, AssetRecord{URL: assetURL, Error: err.Error()})
			continue
		}
		if (resp.StatusCode < 200 || resp.StatusCode >= 300) && !opts.Tolerant {
			return nil, fmt.Errorf("fetch asset failed: HTTP %d %s", resp.StatusCode, assetURL)
		}
		if int64(len(resp.Body)) > opts.MaxBytes {
			if !opts.Tolerant {
				return nil, fmt.Errorf("asset too large: %s (%d bytes)", assetURL, len(resp.Body))
			}
			assets = append(assets, AssetRecord{
				URL:         resp.FinalURL,
				Status:      resp.StatusCode,
				ContentType: resp.ContentType,
				Size:        len(resp.Body),
				Skipped:     "too_large",
			})
			continue
		}

		totalBytes += int64(len(resp.Body))
		rec := AssetRecord{
			URL:          resp.FinalURL,
			LocalPath:    safeRelativePath(resp.FinalURL),
			Status:       resp.StatusCode,
			ContentType:  resp.ContentType,
			ETag:         resp.Headers.Get("ETag"),
			LastModified: resp.Headers.Get("Last-Modified"),
			Size:         len(resp.Body),
			SHA256:       sha256Hex(resp.Body),
		}
		assets = append(assets, rec)
		bodies[rec.URL] = resp.Body

		kind := crawlableKind(resp.ContentType, resp.FinalURL)
		if kind != "" {
			text := string(resp.Body)
			scanMarkers(rec.URL, text)
			switch kind {
			case "html":
				for _, ref := range extractHTMLRefs(text) {
					enqueue(ref, resp.FinalURL)
				}
			case "css":
				for _, ref := range extractCSSRefs(text) {
					enqueue(ref, resp.FinalURL)
				}
			case "js":
				for _, ref := range extractScriptRefs(text) {
					enqueue(ref, resp.FinalURL)
				}
			}
		}

		if opts.DelayMs > 0 && len(queue) > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(opts.DelayMs) * time.Millisecond):
			}
		}
	}

	sort.Slice(assets, func(i, j int) bool { return assets[i].URL < assets[j].URL })
	skippedExternal := make([]string, 0, len(skippedExtMap))
	for u := range skippedExtMap {
		skippedExternal = append(skippedExternal, u)
	}
	sort.Strings(skippedExternal)
	for u := range dailyBundleSet {
		markers.DailyTaskBundleURLs = append(markers.DailyTaskBundleURLs, u)
	}
	sort.Strings(markers.DailyTaskBundleURLs)

	manifest := &Manifest{
		SchemaVersion:   1,
		FetchedAt:       time.Now().UTC().Format(time.RFC3339),
		Targets:         targets,
		Assets:          assets,
		Markers:         markers,
		SkippedExternal: skippedExternal,
		TotalBytes:      totalBytes,
		Truncated:       len(queue) > 0,
	}

	if writeDisk {
		outDirAbs, err := filepath.Abs(opts.OutDir)
		if err != nil {
			return nil, err
		}
		if err := os.RemoveAll(outDirAbs); err != nil {
			return nil, fmt.Errorf("clean recon dir: %w", err)
		}
		if err := os.MkdirAll(outDirAbs, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir recon dir: %w", err)
		}
		for _, a := range assets {
			if a.LocalPath == "" || a.SHA256 == "" {
				continue
			}
			body, ok := bodies[a.URL]
			if !ok {
				continue
			}
			dst := filepath.Join(outDirAbs, a.LocalPath)
			if !strings.HasPrefix(dst, outDirAbs+string(filepath.Separator)) {
				return nil, fmt.Errorf("unsafe local path: %s", a.LocalPath)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, body, 0o644); err != nil {
				return nil, err
			}
		}
		manifestPath := opts.ManifestPath
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
			return nil, err
		}
		manifest.OutDir = opts.OutDir
		manifest.ManifestPath = manifestPath
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return nil, err
		}
		raw = append(raw, '\n')
		if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
			return nil, err
		}
	}

	return manifest, nil
}

// LoadManifest reads a saved local baseline manifest.json.
func LoadManifest(manifestPath string) (*Manifest, error) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse baseline manifest %s: %w", manifestPath, err)
	}
	m.ManifestPath = manifestPath
	return &m, nil
}

// CompareManifests diffs a local baseline manifest against a freshly fetched live manifest.
func CompareManifests(baseline, current *Manifest) *DiffResult {
	baseMap := make(map[string]AssetRecord, len(baseline.Assets))
	for _, a := range baseline.Assets {
		baseMap[a.URL] = a
	}
	curMap := make(map[string]AssetRecord, len(current.Assets))
	for _, a := range current.Assets {
		curMap[a.URL] = a
	}

	added := make([]AssetRecord, 0)
	removed := make([]AssetRecord, 0)
	changed := make([]ChangedAsset, 0)

	for u, cur := range curMap {
		old, exists := baseMap[u]
		if !exists {
			added = append(added, cur)
		} else if old.SHA256 != cur.SHA256 || old.Status != cur.Status {
			changed = append(changed, ChangedAsset{AssetRecord: cur, Previous: old})
		}
	}
	for u, old := range baseMap {
		if _, exists := curMap[u]; !exists {
			removed = append(removed, old)
		}
	}

	sort.Slice(added, func(i, j int) bool { return added[i].URL < added[j].URL })
	sort.Slice(removed, func(i, j int) bool { return removed[i].URL < removed[j].URL })
	sort.Slice(changed, func(i, j int) bool { return changed[i].URL < changed[j].URL })

	targetsChanged := make([]string, 0)
	baseTargets := make(map[string]TargetSummary)
	for _, t := range baseline.Targets {
		baseTargets[t.EntryURL] = t
	}
	for _, t := range current.Targets {
		if old, ok := baseTargets[t.EntryURL]; !ok || old.HTMLSHA256 != t.HTMLSHA256 {
			targetsChanged = append(targetsChanged, t.EntryURL)
		}
	}

	markerChanges := compareMarkers(baseline.Markers, current.Markers)
	same := len(added) == 0 && len(removed) == 0 && len(changed) == 0 && len(targetsChanged) == 0 && len(markerChanges) == 0

	summary := "线上 Bohrium 前端源码与本地基线完全一致（无更新）"
	if !same {
		summary = fmt.Sprintf("检测到线上前端变化：新增 %d、变更 %d、移除 %d、契约标记变更 %d",
			len(added), len(changed), len(removed), len(markerChanges))
	}

	return &DiffResult{
		Same:              same,
		BaselineExists:    true,
		BaselineFetchedAt: baseline.FetchedAt,
		CurrentFetchedAt:  current.FetchedAt,
		TargetsChanged:    targetsChanged,
		MarkerChanges:     markerChanges,
		Added:             added,
		Removed:           removed,
		Changed:           changed,
		BaselineAssets:    len(baseline.Assets),
		CurrentAssets:     len(current.Assets),
		Summary:           summary,
		CurrentMarkers:    current.Markers,
	}
}

// CompareLive loads baseline manifest from disk and compares it against a live crawl (without modifying baseline).
func CompareLive(ctx context.Context, opts Options) (*DiffResult, error) {
	opts = NormalizeOptions(opts)
	baseline, err := LoadManifest(opts.ManifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &DiffResult{
				Same:           false,
				BaselineExists: false,
				Added:          []AssetRecord{},
				Removed:        []AssetRecord{},
				Changed:        []ChangedAsset{},
				Summary:        fmt.Sprintf("未找到本地基线 %s，请先执行 recon pull 拉取基线快照", opts.ManifestPath),
			}, nil
		}
		return nil, err
	}
	current, err := TakeSnapshot(ctx, opts, false)
	if err != nil {
		return nil, err
	}
	return CompareManifests(baseline, current), nil
}

func compareMarkers(a, b ContractMarkers) []string {
	var diffs []string
	checkBool := func(name string, x, y bool) {
		if x != y {
			diffs = append(diffs, fmt.Sprintf("%s: %v -> %v", name, x, y))
		}
	}
	checkBool("plazaTasksEndpoint", a.PlazaTasksEndpoint, b.PlazaTasksEndpoint)
	checkBool("plazaClaimEndpoint", a.PlazaClaimEndpoint, b.PlazaClaimEndpoint)
	checkBool("gatewayHeader", a.GatewayHeader, b.GatewayHeader)
	checkBool("wechatQrEndpoint", a.WeChatQREndpoint, b.WeChatQREndpoint)
	checkBool("sciSpaceTeamIdFound", a.SciSpaceTeamIDFound, b.SciSpaceTeamIDFound)
	checkBool("queryEventsEndpoint", a.QueryEventsEndpoint, b.QueryEventsEndpoint)
	if strings.Join(a.DailyTaskBundleURLs, ",") != strings.Join(b.DailyTaskBundleURLs, ",") {
		diffs = append(diffs, fmt.Sprintf("dailyTaskBundleUrls: %v -> %v", a.DailyTaskBundleURLs, b.DailyTaskBundleURLs))
	}
	return diffs
}

func normalizeURL(raw, baseStr string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "javascript:") || strings.HasPrefix(raw, "mailto:") || strings.HasPrefix(raw, "#") {
		return ""
	}
	base, err := url.Parse(baseStr)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

func isRelevantAsset(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	// Focus recon on HTML, JS, CSS, JSON/manifest bundles (skip bulky fonts/images/videos).
	for _, skipExt := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".ico", ".svg", ".woff", ".woff2", ".ttf", ".eot", ".mp4", ".webm", ".pdf"} {
		if strings.HasSuffix(p, skipExt) {
			return false
		}
	}
	return true
}

func safeRelativePath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "unknown.bin"
	}
	p := u.Path
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	parts := strings.Split(strings.TrimLeft(p, "/"), "/")
	clean := make([]string, 0, len(parts)+1)
	clean = append(clean, reSafeSeg.ReplaceAllString(u.Hostname(), "_"))
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		clean = append(clean, reSafeSeg.ReplaceAllString(seg, "_"))
	}
	rel := filepath.Join(clean...)
	if u.RawQuery != "" {
		h := sha256Hex([]byte(u.RawQuery))[:8]
		ext := filepath.Ext(rel)
		rel = strings.TrimSuffix(rel, ext) + "." + h + ext
	}
	return rel
}

func extractHTMLRefs(html string) []string {
	var out []string
	for _, m := range reHTMLTagRef.FindAllStringSubmatch(html, -1) {
		if len(m) >= 2 {
			out = append(out, m[1])
		}
	}
	for _, m := range reHTMLAssetRef.FindAllStringSubmatch(html, -1) {
		if len(m) >= 2 {
			out = append(out, m[1])
		}
	}
	return out
}

func extractScriptRefs(js string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{reScriptImport, reDynImport} {
		for _, m := range re.FindAllStringSubmatch(js, -1) {
			if len(m) >= 2 {
				ref := m[1]
				if strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
					out = append(out, ref)
				}
			}
		}
	}
	return out
}

func extractCSSRefs(css string) []string {
	var out []string
	for _, m := range reCSSURL.FindAllStringSubmatch(css, -1) {
		if len(m) >= 2 {
			out = append(out, m[1])
		}
	}
	return out
}

func crawlableKind(contentType, rawURL string) string {
	ct := strings.ToLower(contentType)
	lu := strings.ToLower(rawURL)
	if strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript") || strings.HasSuffix(lu, ".js") || strings.HasSuffix(lu, ".mjs") {
		return "js"
	}
	if strings.Contains(ct, "text/css") || strings.HasSuffix(lu, ".css") {
		return "css"
	}
	if strings.Contains(ct, "text/html") {
		return "html"
	}
	return ""
}

func extractVersion(html string) string {
	m := reVersionAttr.FindStringSubmatch(html)
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	return ""
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// normalizeHTMLForHash strips ephemeral per-request edge CDN attributes (such as Aliyun clientSignal
// and CSP nonces) so recon diff only alerts on genuine frontend code/bundle updates.
func normalizeHTMLForHash(b []byte) []byte {
	s := reClientSignal.ReplaceAllString(string(b), `clientSignal=""`)
	s = reHTMLNonce.ReplaceAllString(s, "")
	return []byte(s)
}
