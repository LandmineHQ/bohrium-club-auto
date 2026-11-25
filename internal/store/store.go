package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuthState persists the Bohrium login session and JWT metadata.
type AuthState struct {
	Token         string    `json:"token"`
	UserID        int64     `json:"userId"`
	OrgID         int64     `json:"orgId"`
	UserType      int       `json:"userType"`
	Nickname      string    `json:"nickname"`
	Phone         string    `json:"phone,omitempty"`
	Email         string    `json:"email,omitempty"`
	Avatar        string    `json:"avatar,omitempty"`
	ClubBound     bool      `json:"clubBound"`
	ExpiresAt     time.Time `json:"expiresAt"`
	LastLoginAt   time.Time `json:"lastLoginAt"`
	LastRefreshAt time.Time `json:"lastRefreshAt"`
	LoginMethod   string    `json:"loginMethod"` // "wechat_qr" | "manual_token" | "password"
}

// RunLog records a single automation action or task execution.
type RunLog struct {
	ID         string         `json:"id"`
	Timestamp  time.Time      `json:"timestamp"`
	Trigger    string         `json:"trigger"` // "manual" | "schedule" | "system"
	Action     string         `json:"action"`  // "claim_all" | "claim_one" | "task_run" | "run_all" | "refresh_token" | "login" | "recon"
	TaskKey    string         `json:"taskKey,omitempty"`
	TaskTitle  string         `json:"taskTitle,omitempty"`
	Success    bool           `json:"success"`
	Skipped    bool           `json:"skipped,omitempty"`
	Message    string         `json:"message"`
	DurationMs int64          `json:"durationMs"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// State is the persisted runtime state stored atomically in <dataDir>/state.json.
type State struct {
	Auth             AuthState `json:"auth"`
	LastScheduledDay string    `json:"lastScheduledDay"` // YYYY-MM-DD in Asia/Shanghai
	LastRunAt        time.Time `json:"lastRunAt,omitempty"`
	LastRunSummary   string    `json:"lastRunSummary,omitempty"`
	Logs             []RunLog  `json:"logs"`
}

const maxLogs = 200

// Store manages thread-safe atomic persistence of State.
type Store struct {
	mu    sync.RWMutex
	path  string
	state State
}

// Open loads or initializes <dataDir>/state.json.
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	p := filepath.Join(dataDir, "state.json")
	s := &Store{
		path: p,
		state: State{
			Logs: make([]RunLog, 0),
		},
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read state %s: %w", p, err)
	}
	if err := json.Unmarshal(raw, &s.state); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", p, err)
	}
	if s.state.Logs == nil {
		s.state.Logs = make([]RunLog, 0)
	}
	return s, nil
}

// GetAuth returns a copy of the stored AuthState.
func (s *Store) GetAuth() AuthState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Auth
}

// SetAuth updates and persists AuthState.
func (s *Store) SetAuth(auth AuthState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Auth = auth
	return s.saveLocked()
}

// ClearAuth removes stored Bohrium credentials.
func (s *Store) ClearAuth() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Auth = AuthState{}
	return s.saveLocked()
}

// GetLastScheduledDay returns the YYYY-MM-DD of the most recent scheduled daily run.
func (s *Store) GetLastScheduledDay() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.LastScheduledDay
}

// SetLastScheduledDay persists the last scheduled run day and summary.
func (s *Store) SetLastScheduledDay(day, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastScheduledDay = day
	s.state.LastRunAt = time.Now().UTC()
	s.state.LastRunSummary = summary
	return s.saveLocked()
}

// GetSummary returns last run metadata.
func (s *Store) GetSummary() (lastDay string, lastAt time.Time, summary string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.LastScheduledDay, s.state.LastRunAt, s.state.LastRunSummary
}

// AppendLog prepends a log entry (newest first) and trims to maxLogs.
func (s *Store) AppendLog(entry RunLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%d", entry.Timestamp.UnixNano())
	}
	s.state.Logs = append([]RunLog{entry}, s.state.Logs...)
	if len(s.state.Logs) > maxLogs {
		s.state.Logs = s.state.Logs[:maxLogs]
	}
	return s.saveLocked()
}

// GetLogs returns up to limit recent logs (newest first).
func (s *Store) GetLogs(limit int) []RunLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.state.Logs) {
		limit = len(s.state.Logs)
	}
	out := make([]RunLog, limit)
	copy(out, s.state.Logs[:limit])
	return out
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
