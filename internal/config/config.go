package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"bohrium-club-auto/internal/chromehttp"
)

// Config holds persisted and environment-overridden application settings.
type Config struct {
	Host            string         `json:"host"`
	Port            int            `json:"port"`
	DataDir         string         `json:"dataDir"`
	AdminPassword   string         `json:"adminPassword"`
	SessionSecret   string         `json:"sessionSecret"`
	UserAgent       string         `json:"userAgent"`
	ProxyURL        string         `json:"proxyUrl"`
	Schedule        ScheduleConfig `json:"schedule"`
	PDFCacheDir     string         `json:"pdfCacheDir"`
	LocalPDFDir     string         `json:"localPdfDir"`
	ReconDir        string         `json:"reconDir"`
	TaskDelaySec    int            `json:"taskDelaySec"`
	CancelAfterMs   int            `json:"cancelAfterMs"`
	MaxPDFSizeBytes int64          `json:"maxPdfSizeBytes"`
}

// ScheduleConfig defines the daily automatic execution schedule (Asia/Shanghai).
type ScheduleConfig struct {
	Enabled       bool   `json:"enabled"`
	TimeHHMM      string `json:"timeHHMM"`      // e.g. "08:15" in Timezone
	Timezone      string `json:"timezone"`      // default "Asia/Shanghai"
	JitterMinutes int    `json:"jitterMinutes"` // 0..60 random delay minutes
	AutoClaim     bool   `json:"autoClaim"`
	AutoRefresh   bool   `json:"autoRefresh"`
}

// Manager provides thread-safe access and atomic persistence for Config.
type Manager struct {
	mu          sync.RWMutex
	cfg         Config
	path        string
	firstBootPW string
}

// Load reads config from <dataDir>/config.json (creating defaults on first boot)
// and applies environment variable overrides.
func Load() (*Manager, error) {
	dataDir := envOr("BOHRIUM_DATA_DIR", "data")
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		absDataDir = dataDir
	}
	if err := os.MkdirAll(absDataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", absDataDir, err)
	}

	cfgPath := filepath.Join(absDataDir, "config.json")
	cfg := defaultConfig(absDataDir)
	firstBoot := false

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read config %s: %w", cfgPath, err)
		}
		firstBoot = true
	} else {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", cfgPath, err)
		}
		fillMissingDefaults(&cfg, absDataDir)
	}

	// Environment overrides (take precedence for container/server deployment).
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_HOST")); v != "" {
		cfg.Host = v
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			cfg.Port = p
		}
	}
	cfg.DataDir = absDataDir

	if v := strings.TrimSpace(os.Getenv("BOHRIUM_ADMIN_PASSWORD")); v != "" {
		cfg.AdminPassword = v
	} else if v := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")); v != "" {
		cfg.AdminPassword = v
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_SESSION_SECRET")); v != "" {
		cfg.SessionSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_USER_AGENT")); v != "" {
		cfg.UserAgent = v
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_PROXY")); v != "" {
		cfg.ProxyURL = v
	} else if cfg.ProxyURL == "" {
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
			if p := strings.TrimSpace(os.Getenv(k)); p != "" {
				cfg.ProxyURL = p
				break
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_SCHEDULE_ENABLED")); v != "" {
		cfg.Schedule.Enabled = parseBool(v, cfg.Schedule.Enabled)
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_SCHEDULE_TIME")); v != "" {
		if ValidHHMM(v) {
			cfg.Schedule.TimeHHMM = v
		}
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_SCHEDULE_TZ")); v != "" {
		cfg.Schedule.Timezone = v
	}
	if v := strings.TrimSpace(os.Getenv("BOHRIUM_RECON_DIR")); v != "" {
		cfg.ReconDir = v
	}

	generatedPW := ""
	if strings.TrimSpace(cfg.AdminPassword) == "" {
		cfg.AdminPassword = randomHex(12)
		generatedPW = cfg.AdminPassword
	}
	if strings.TrimSpace(cfg.SessionSecret) == "" {
		cfg.SessionSecret = randomHex(32)
	}

	if err := os.MkdirAll(cfg.PDFCacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create pdf cache dir: %w", err)
	}
	if err := os.MkdirAll(cfg.LocalPDFDir, 0o755); err != nil {
		return nil, fmt.Errorf("create local pdf dir: %w", err)
	}

	m := &Manager{
		cfg:         cfg,
		path:        cfgPath,
		firstBootPW: generatedPW,
	}
	if firstBoot || generatedPW != "" {
		if err := m.saveLocked(); err != nil {
			return nil, fmt.Errorf("save initial config: %w", err)
		}
	}
	return m, nil
}

func defaultConfig(dataDir string) Config {
	return Config{
		Host:          "0.0.0.0",
		Port:          8788,
		DataDir:       dataDir,
		UserAgent:     chromehttp.DefaultChromeUA,
		PDFCacheDir:   filepath.Join(dataDir, "pdfs"),
		LocalPDFDir:   filepath.Join(dataDir, "custom_pdfs"),
		ReconDir:      filepath.Join("recon", "bohrium-web"),
		TaskDelaySec:  3,
		CancelAfterMs: 450,
		MaxPDFSizeBytes: 25 * 1024 * 1024,
		Schedule: ScheduleConfig{
			Enabled:       true,
			TimeHHMM:      "08:15",
			Timezone:      "Asia/Shanghai",
			JitterMinutes: 15,
			AutoClaim:     true,
			AutoRefresh:   true,
		},
	}
}

func fillMissingDefaults(cfg *Config, dataDir string) {
	def := defaultConfig(dataDir)
	if cfg.Host == "" {
		cfg.Host = def.Host
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = def.Port
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = def.UserAgent
	}
	if cfg.PDFCacheDir == "" {
		cfg.PDFCacheDir = def.PDFCacheDir
	}
	if cfg.LocalPDFDir == "" {
		cfg.LocalPDFDir = def.LocalPDFDir
	}
	if cfg.ReconDir == "" {
		cfg.ReconDir = def.ReconDir
	}
	if cfg.TaskDelaySec <= 0 {
		cfg.TaskDelaySec = def.TaskDelaySec
	}
	if cfg.CancelAfterMs <= 0 {
		cfg.CancelAfterMs = def.CancelAfterMs
	}
	if cfg.MaxPDFSizeBytes <= 0 {
		cfg.MaxPDFSizeBytes = def.MaxPDFSizeBytes
	}
	if !ValidHHMM(cfg.Schedule.TimeHHMM) {
		cfg.Schedule.TimeHHMM = def.Schedule.TimeHHMM
	}
	if cfg.Schedule.Timezone == "" {
		cfg.Schedule.Timezone = def.Schedule.Timezone
	}
}

// Get returns a snapshot copy of the current configuration.
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Path returns the config.json file path.
func (m *Manager) Path() string {
	return m.path
}

// GeneratedPassword returns the password generated on first boot (empty if pre-configured).
func (m *Manager) GeneratedPassword() string {
	return m.firstBootPW
}

// Update mutates the config via fn and atomically persists it to disk.
func (m *Manager) Update(fn func(*Config) error) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.cfg
	if err := fn(&next); err != nil {
		return m.cfg, err
	}
	m.cfg = next
	if err := m.saveLocked(); err != nil {
		return m.cfg, err
	}
	return m.cfg, nil
}

func (m *Manager) saveLocked() error {
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// ValidHHMM checks whether s matches "HH:MM" in 24-hour format.
func ValidHHMM(s string) bool {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && h >= 0 && h <= 23 && min >= 0 && min <= 59
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseBool(s string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
