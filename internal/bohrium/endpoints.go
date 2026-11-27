package bohrium

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/store"
)

const (
	DefaultClubBaseURL     = "https://club.bohrium.com"
	DefaultPlatformBaseURL = "https://platform.bohrium.com"
	DefaultWWWBaseURL      = "https://www.bohrium.com"
	DefaultSBCloudBaseURL  = "https://sb-cloud.dp.tech"
	DefaultAgentBaseURL    = "https://agent.bohrium.com"
	DefaultSigmaLLMBaseURL = "https://www.bohrium.com/bohrapi/v1/sigma-search/api/v1/scix/llm"

	SciXModelCatalogTag   = "shrimp-brain-v2"
	SciXExecutionScenario = "local_agent"
	SciXExecutionHeader   = "x-scix-execution"
	SciXRuntimeHeader     = "x-scix-runtime"
	SciXRuntimeValue      = "scix_local"

	PublisherCommunity        = "bohrium-user-community"
	PlazaGatewayHeader        = "X-Plaza-Daily-Task-Gateway"
	PlazaGatewayProd          = "production"
	HistoryCapabilitiesValue  = "sessionForkHistoryV1"
	DefaultSciSpaceTeamID     = "AgK4D5B8Fdju"
	ShrimpClientSurfaceHeader = "x-shrimp-client-surface"
	ShrimpClientSurfaceValue  = "shrimp-web"

	TaskKeyScienceSpaceWeb    = "bohrium_science_space_web"
	TaskKeyScienceSpaceClient = "bohrium_science_space_client"
	TaskKeyLiteratureReading  = "bohrium_literature_reading"
	TaskKeyAcademicWriting    = "bohrium_academic_writing"
	TaskKeyClientSkipped      = TaskKeyScienceSpaceClient // Deprecated alias retained for backward compatibility
)

// Endpoints allows overriding upstream base URLs (e.g., for httptest unit tests).
type Endpoints struct {
	ClubBase     string
	PlatformBase string
	WWWBase      string
	SBCloudBase  string
	AgentBase    string
	SigmaLLMBase string
}

func defaultEndpoints() Endpoints {
	return Endpoints{
		ClubBase:     DefaultClubBaseURL,
		PlatformBase: DefaultPlatformBaseURL,
		WWWBase:      DefaultWWWBaseURL,
		SBCloudBase:  DefaultSBCloudBaseURL,
		AgentBase:    DefaultAgentBaseURL,
		SigmaLLMBase: DefaultSigmaLLMBaseURL,
	}
}

// AccountStore abstracts credential, StoreID, and log persistence for a single Bohrium account.
// Both *store.Store (legacy/default) and per-account *store.AccountScope / *store.LoginScope satisfy this interface.
type AccountStore interface {
	GetAuth() store.AuthState
	SetAuth(auth store.AuthState) error
	ClearAuth() error
	AppendLog(entry store.RunLog) error
	StoreID() string
}

// Client interacts with club.bohrium.com, platform.bohrium.com, www.bohrium.com, and sb-cloud.dp.tech
// scoped to a single Bohrium account's credentials and StoreID.
// Note: The deprecated legacy 200-photon check-in endpoint (/bohrapi/v1/account/user/integral_receive)
// is intentionally never implemented or called.
type Client struct {
	http      *chromehttp.Client
	cfg       *config.Manager
	store     AccountStore
	endpoints Endpoints

	catalogMu        sync.RWMutex
	catalogCache     *SciXModelCatalog
	catalogFetchedAt time.Time
	catalogUserID    int64
	sharedCatalog    *sharedCatalogCache

	// Optional override for reader document status polling interval (tests only; defaults to 2s/3s).
	readerPollInterval time.Duration
}

// NewClient creates a Bohrium API client bound to the given AccountStore.
func NewClient(httpClient *chromehttp.Client, cfg *config.Manager, st AccountStore) *Client {
	return &Client{
		http:      httpClient,
		cfg:       cfg,
		store:     st,
		endpoints: defaultEndpoints(),
	}
}

// SetEndpoints overrides upstream base URLs for testing.
func (c *Client) SetEndpoints(ep Endpoints) {
	if ep.ClubBase != "" {
		c.endpoints.ClubBase = strings.TrimRight(ep.ClubBase, "/")
	}
	if ep.PlatformBase != "" {
		c.endpoints.PlatformBase = strings.TrimRight(ep.PlatformBase, "/")
	}
	if ep.WWWBase != "" {
		c.endpoints.WWWBase = strings.TrimRight(ep.WWWBase, "/")
	}
	if ep.SBCloudBase != "" {
		c.endpoints.SBCloudBase = strings.TrimRight(ep.SBCloudBase, "/")
	}
	if ep.AgentBase != "" {
		c.endpoints.AgentBase = strings.TrimRight(ep.AgentBase, "/")
	}
	if ep.SigmaLLMBase != "" {
		c.endpoints.SigmaLLMBase = strings.TrimRight(ep.SigmaLLMBase, "/")
	}
}

// JWTClaims captures relevant fields decoded from brmToken's payload segment.
type JWTClaims struct {
	UserID    int64
	OrgID     int64
	UserType  int
	ExpiresAt time.Time
	Raw       map[string]any
}

// ParseJWTClaims decodes the payload of a Bohrium JWT (brmToken) without verifying the remote signing key.
func ParseJWTClaims(token string) (*JWTClaims, error) {
	token = CleanToken(token)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format: expected 3 segments, got %d", len(parts))
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Fallback with standard padding if needed
		padded := parts[1]
		if m := len(padded) % 4; m != 0 {
			padded += strings.Repeat("=", 4-m)
		}
		payloadBytes, err = base64.URLEncoding.DecodeString(padded)
		if err != nil {
			return nil, fmt.Errorf("decode JWT payload: %w", err)
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(payloadBytes, &raw); err != nil {
		return nil, fmt.Errorf("parse JWT payload JSON: %w", err)
	}
	claims := &JWTClaims{Raw: raw}
	if exp := asInt64(raw["exp"]); exp > 0 {
		claims.ExpiresAt = time.Unix(exp, 0).UTC()
	}
	if idObj, ok := raw["identity"].(map[string]any); ok {
		claims.UserID = asInt64(idObj["userId"])
		claims.OrgID = asInt64(idObj["orgId"])
		claims.UserType = int(asInt64(idObj["userType"]))
	}
	if claims.UserID == 0 {
		claims.UserID = asInt64(raw["userId"])
		if claims.UserID == 0 {
			claims.UserID = asInt64(raw["uid"])
		}
	}
	if claims.OrgID == 0 {
		claims.OrgID = asInt64(raw["orgId"])
	}
	return claims, nil
}

// CleanToken normalizes user-pasted cookies/tokens (e.g., strips "Bearer " or "brmToken=" prefix).
func CleanToken(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "Bearer ")
	s = strings.TrimPrefix(s, "bearer ")
	if strings.Contains(s, "brmToken=") {
		for _, part := range strings.Split(s, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "brmToken=") {
				s = strings.TrimPrefix(part, "brmToken=")
				break
			}
		}
	}
	return strings.Trim(strings.TrimSpace(s), `"'`)
}

func (c *Client) clubHeaders(token string) map[string]string {
	h := map[string]string{
		"Origin":           c.endpoints.ClubBase,
		"Referer":          c.endpoints.ClubBase + "/",
		PlazaGatewayHeader: PlazaGatewayProd,
	}
	if token != "" {
		h["Cookie"] = "brmToken=" + token
	}
	return h
}

func (c *Client) wwwHeaders(token, refererPath string) map[string]string {
	ref := c.endpoints.WWWBase + "/"
	if refererPath != "" {
		ref = c.endpoints.WWWBase + "/" + strings.TrimLeft(refererPath, "/")
	}
	h := map[string]string{
		"Origin":  c.endpoints.WWWBase,
		"Referer": ref,
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
		h["Cookie"] = "brmToken=" + token
	}
	return h
}

func (c *Client) sbCloudHeaders(token, refererPath string) map[string]string {
	ref := c.endpoints.WWWBase + "/"
	if refererPath != "" {
		ref = c.endpoints.WWWBase + "/" + strings.TrimLeft(refererPath, "/")
	}
	// Note: official frontend explicitly strips x-shrimp-client-surface & Idempotency-Key for brain-cloud (sb-cloud.dp.tech).
	h := map[string]string{
		"Origin":  c.endpoints.WWWBase,
		"Referer": ref,
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return h
}

func newUUIDv4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		var n int64
		_, _ = fmt.Sscanf(strings.TrimSpace(x), "%d", &n)
		return n
	default:
		return 0
	}
}

func asFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		var f float64
		_, _ = fmt.Sscanf(strings.TrimSpace(x), "%f", &f)
		return f
	default:
		return 0
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func truncateText(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "..."
}
