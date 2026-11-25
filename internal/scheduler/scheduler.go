package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/store"
)

// Status describes the live scheduler state for the Dashboard.
type Status struct {
	Enabled          bool      `json:"enabled"`
	Running          bool      `json:"running"`
	CurrentJob       string    `json:"currentJob,omitempty"`
	TimeHHMM         string    `json:"timeHHMM"`
	Timezone         string    `json:"timezone"`
	JitterMinutes    int       `json:"jitterMinutes"`
	AutoClaim        bool      `json:"autoClaim"`
	AutoRefresh      bool      `json:"autoRefresh"`
	NextRunAt        string    `json:"nextRunAt,omitempty"`
	LastScheduledDay string    `json:"lastScheduledDay,omitempty"`
	LastRunAt        time.Time `json:"lastRunAt,omitempty"`
	LastRunSummary   string    `json:"lastRunSummary,omitempty"`
}

// Scheduler coordinates daily automatic task execution, periodic JWT refresh,
// and mutual exclusion with manual dashboard triggers.
type Scheduler struct {
	client *bohrium.Client
	cfg    *config.Manager
	store  *store.Store

	execMu     sync.Mutex
	stateMu    sync.RWMutex
	running    bool
	currentJob string

	stopCh chan struct{}
	doneCh chan struct{}
}

// New creates a Scheduler instance.
func New(client *bohrium.Client, cfg *config.Manager, st *store.Store) *Scheduler {
	return &Scheduler{
		client: client,
		cfg:    cfg,
		store:  st,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start launches the background schedule and token-refresh loop.
func (s *Scheduler) Start() {
	go s.loop()
}

// Stop signals the scheduler loop to exit cleanly.
func (s *Scheduler) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	<-s.doneCh
}

func (s *Scheduler) loop() {
	defer close(s.doneCh)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Check token refresh shortly after boot (non-blocking)
	s.tick()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Scheduler) tick() {
	cfg := s.cfg.Get()
	auth := s.store.GetAuth()
	if auth.Token == "" {
		return
	}

	// 1. Background auto-refresh of brmToken before expiration (keeps server login alive).
	if cfg.Schedule.AutoRefresh {
		needsRefresh := false
		if !auth.ExpiresAt.IsZero() && time.Until(auth.ExpiresAt) < 48*time.Hour && time.Until(auth.ExpiresAt) > 0 {
			needsRefresh = true
		} else if !auth.LastRefreshAt.IsZero() && time.Since(auth.LastRefreshAt) > 24*time.Hour {
			needsRefresh = true
		}
		if needsRefresh && time.Since(auth.LastRefreshAt) > 30*time.Minute {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if _, err := s.client.RefreshToken(ctx, "schedule"); err != nil {
				log.Printf("[scheduler] auto refresh_token skipped/failed: %v", err)
			}
			cancel()
		}
	}

	// 2. Daily scheduled task run in configured timezone (default Asia/Shanghai).
	if !cfg.Schedule.Enabled {
		return
	}
	loc := loadLocation(cfg.Schedule.Timezone)
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	if s.store.GetLastScheduledDay() == today {
		return
	}

	targetTime := plannedTimeForDay(now, cfg.Schedule.TimeHHMM, cfg.Schedule.JitterMinutes)
	if now.Before(targetTime) {
		return
	}

	// Avoid overlapping with any manual execution.
	if !s.execMu.TryLock() {
		return
	}
	defer s.execMu.Unlock()

	if s.store.GetLastScheduledDay() == today {
		return
	}

	s.setRunning(true, "scheduled_daily_tasks")
	defer s.setRunning(false, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Printf("[scheduler] starting daily Bohrium Web tasks for %s", today)
	res, err := s.client.RunAllWebTasks(ctx, "schedule", cfg.Schedule.AutoClaim)
	if err != nil {
		log.Printf("[scheduler] daily run failed for %s: %v", today, err)
		_ = s.store.AppendLog(store.RunLog{
			Trigger: "schedule",
			Action:  "run_all",
			Success: false,
			Message: fmt.Sprintf("定时任务执行失败: %v", err),
		})
		// Mark today only if auth isn't missing, or wait next tick? Keep retryable on transient error by not marking day,
		// unless error is 401/unauthenticated (avoid hammering every 30s).
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "登录") {
			_ = s.store.SetLastScheduledDay(today, "跳过: "+err.Error())
		}
		return
	}
	_ = s.store.SetLastScheduledDay(today, res.Summary)
	log.Printf("[scheduler] finished daily Bohrium Web tasks for %s: %s", today, res.Summary)
}

// RunAllManual triggers all 3 Web tasks manually under the shared execution lock.
func (s *Scheduler) RunAllManual(ctx context.Context) (*bohrium.RunAllResult, error) {
	if !s.execMu.TryLock() {
		return nil, fmt.Errorf("已有任务正在执行中，请稍候")
	}
	defer s.execMu.Unlock()
	s.setRunning(true, "manual_run_all")
	defer s.setRunning(false, "")

	cfg := s.cfg.Get()
	res, err := s.client.RunAllWebTasks(ctx, "manual", cfg.Schedule.AutoClaim)
	if err == nil && res != nil && res.AllWebDone {
		loc := loadLocation(cfg.Schedule.Timezone)
		today := time.Now().In(loc).Format("2006-01-02")
		_ = s.store.SetLastScheduledDay(today, "手动执行完成: "+res.Summary)
	}
	return res, err
}

// RunSingleManual triggers one specific Web task manually under the shared execution lock.
func (s *Scheduler) RunSingleManual(ctx context.Context, taskKey string) (*bohrium.TaskExecutionResult, error) {
	if !s.execMu.TryLock() {
		return nil, fmt.Errorf("已有任务正在执行中，请稍候")
	}
	defer s.execMu.Unlock()
	s.setRunning(true, "manual_task:"+taskKey)
	defer s.setRunning(false, "")

	return s.client.ExecuteSingleTask(ctx, taskKey, "manual")
}

// Status returns the current scheduler configuration, execution status, and next planned run time.
func (s *Scheduler) Status() Status {
	cfg := s.cfg.Get()
	s.stateMu.RLock()
	running := s.running
	curJob := s.currentJob
	s.stateMu.RUnlock()

	lastDay, lastAt, summary := s.store.GetSummary()
	st := Status{
		Enabled:          cfg.Schedule.Enabled,
		Running:          running,
		CurrentJob:       curJob,
		TimeHHMM:         cfg.Schedule.TimeHHMM,
		Timezone:         cfg.Schedule.Timezone,
		JitterMinutes:    cfg.Schedule.JitterMinutes,
		AutoClaim:        cfg.Schedule.AutoClaim,
		AutoRefresh:      cfg.Schedule.AutoRefresh,
		LastScheduledDay: lastDay,
		LastRunAt:        lastAt,
		LastRunSummary:   summary,
	}

	if cfg.Schedule.Enabled {
		loc := loadLocation(cfg.Schedule.Timezone)
		now := time.Now().In(loc)
		today := now.Format("2006-01-02")
		planned := plannedTimeForDay(now, cfg.Schedule.TimeHHMM, cfg.Schedule.JitterMinutes)
		if lastDay == today || !now.Before(planned) && lastDay == today {
			tomorrow := now.AddDate(0, 0, 1)
			planned = plannedTimeForDay(tomorrow, cfg.Schedule.TimeHHMM, cfg.Schedule.JitterMinutes)
		}
		st.NextRunAt = planned.Format("2006-01-02 15:04:05 MST")
	}
	return st
}

func (s *Scheduler) setRunning(v bool, job string) {
	s.stateMu.Lock()
	s.running = v
	s.currentJob = job
	s.stateMu.Unlock()
}

func loadLocation(tz string) *time.Location {
	if loc, err := time.LoadLocation(strings.TrimSpace(tz)); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}

// plannedTimeForDay computes HH:MM + deterministic per-day jitter so restarts within a day keep the same target slot.
func plannedTimeForDay(dayRef time.Time, hhmm string, jitterMinutes int) time.Time {
	h, m := 8, 15
	parts := strings.Split(strings.TrimSpace(hhmm), ":")
	if len(parts) == 2 {
		if hh, err := strconv.Atoi(parts[0]); err == nil && hh >= 0 && hh <= 23 {
			h = hh
		}
		if mm, err := strconv.Atoi(parts[1]); err == nil && mm >= 0 && mm <= 59 {
			m = mm
		}
	}
	base := time.Date(dayRef.Year(), dayRef.Month(), dayRef.Day(), h, m, 0, 0, dayRef.Location())
	if jitterMinutes > 0 {
		sum := sha256.Sum256([]byte("bohrium-jitter:" + dayRef.Format("2006-01-02")))
		offset := int(binary.BigEndian.Uint32(sum[:4]) % uint32(jitterMinutes+1))
		base = base.Add(time.Duration(offset) * time.Minute)
	}
	return base
}
