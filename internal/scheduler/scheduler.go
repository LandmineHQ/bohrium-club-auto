package scheduler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/store"
)

const (
	poolBalanceRefreshInterval = 15 * time.Minute
	bindingPurgeInterval       = 1 * time.Hour
	minInterAccountGapSec      = 20
	maxInterAccountGapSec      = 60
)

// AccountScheduleInfo exposes a single account's randomized daily check-in slot and status.
type AccountScheduleInfo struct {
	AccountID        string    `json:"accountId"`
	CheckinEnabled   bool      `json:"checkinEnabled"`
	Running          bool      `json:"running"`
	PlannedTodayAt   string    `json:"plannedTodayAt,omitempty"`
	NextRunAt        string    `json:"nextRunAt,omitempty"`
	DoneToday        bool      `json:"doneToday"`
	LastScheduledDay string    `json:"lastScheduledDay,omitempty"`
	LastRunAt        time.Time `json:"lastRunAt,omitempty"`
	LastRunSummary   string    `json:"lastRunSummary,omitempty"`
}

// AccountRunResult captures the outcome of running Web check-in tasks for one account.
type AccountRunResult struct {
	AccountID   string                `json:"accountId"`
	DisplayName string                `json:"displayName"`
	Success     bool                  `json:"success"`
	Error       string                `json:"error,omitempty"`
	Result      *bohrium.RunAllResult `json:"result,omitempty"`
}

// Status describes the live multi-account scheduler state for the Web Console.
type Status struct {
	Enabled          bool                           `json:"enabled"`
	Running          bool                           `json:"running"`
	CurrentJob       string                         `json:"currentJob,omitempty"`
	ActiveAccountID  string                         `json:"activeAccountId,omitempty"`
	TimeHHMM         string                         `json:"timeHHMM"`
	Timezone         string                         `json:"timezone"`
	JitterMinutes    int                            `json:"jitterMinutes"`
	AutoClaim        bool                           `json:"autoClaim"`
	AutoRefresh      bool                           `json:"autoRefresh"`
	NextRunAt        string                         `json:"nextRunAt,omitempty"`
	TotalAccounts    int                            `json:"totalAccounts"`
	EnabledAccounts  int                            `json:"enabledAccounts"`
	DoneTodayCount   int                            `json:"doneTodayCount"`
	LastScheduledDay string                         `json:"lastScheduledDay,omitempty"`
	LastRunAt        time.Time                      `json:"lastRunAt,omitempty"`
	LastRunSummary   string                         `json:"lastRunSummary,omitempty"`
	Accounts         map[string]AccountScheduleInfo `json:"accounts,omitempty"`
}

// Scheduler coordinates per-account jittered daily task execution, staggered anti-burst spacing,
// background JWT token renewal, pooled balance refreshes, and hourly 7-day binding purges.
type Scheduler struct {
	mgr   *bohrium.Manager
	cfg   *config.Manager
	store *store.Store

	checkinGate chan struct{} // serializes scheduled Web check-ins across accounts to avoid burst spikes

	accLockMu sync.Mutex
	accLocks  map[string]*sync.Mutex

	stateMu         sync.RWMutex
	running         bool
	currentJob      string
	activeAccountID string
	runningAccounts map[string]bool

	maintMu             sync.Mutex
	lastPurgeAt         time.Time
	lastBalanceSyncAt   time.Time
	nextAllowedCheckinAt time.Time

	stopCh chan struct{}
	doneCh chan struct{}
}

// New creates a multi-account Scheduler instance.
func New(mgr *bohrium.Manager, cfg *config.Manager, st *store.Store) *Scheduler {
	return &Scheduler{
		mgr:             mgr,
		cfg:             cfg,
		store:           st,
		checkinGate:     make(chan struct{}, 1),
		accLocks:        make(map[string]*sync.Mutex),
		runningAccounts: make(map[string]bool),
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
}

// Start launches the background schedule, token-refresh, balance-sync, and TTL-purge loop.
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
	nowUTC := time.Now().UTC()
	cfg := s.cfg.Get()

	// 1. Periodic purge of expired (>7d) SQLite OpenAI session bindings.
	s.maintMu.Lock()
	doPurge := s.lastPurgeAt.IsZero() || nowUTC.Sub(s.lastPurgeAt) >= bindingPurgeInterval
	if doPurge {
		s.lastPurgeAt = nowUTC
	}
	doBalSync := s.lastBalanceSyncAt.IsZero() || nowUTC.Sub(s.lastBalanceSyncAt) >= poolBalanceRefreshInterval
	if doBalSync {
		s.lastBalanceSyncAt = nowUTC
	}
	s.maintMu.Unlock()

	if doPurge {
		if purged, err := s.store.PurgeExpiredSessionBindings(nowUTC); err == nil && purged > 0 {
			log.Printf("[scheduler] purged %d expired OpenAI session binding(s) (>7d TTL)", purged)
		}
		retDays := cfg.Gateway.AnalyticsRetentionDays
		if retDays <= 0 {
			retDays = config.DefaultAnalyticsRetentionDays
		}
		if purged, err := s.store.PurgeExpiredGatewayUsage(nowUTC, retDays); err == nil && purged > 0 {
			log.Printf("[scheduler] purged %d expired OpenAI usage log(s) (>%dd retention)", purged, retDays)
		}
	}

	accounts, err := s.store.ListAccounts()
	if err != nil || len(accounts) == 0 {
		return
	}

	// 2. Background token auto-refresh across all accounts.
	if cfg.Schedule.AutoRefresh {
		for _, acc := range accounts {
			if strings.TrimSpace(acc.Token) == "" {
				continue
			}
			needsRefresh := false
			if !acc.ExpiresAt.IsZero() && time.Until(acc.ExpiresAt) < 48*time.Hour && time.Until(acc.ExpiresAt) > 0 {
				needsRefresh = true
			} else if !acc.LastRefreshAt.IsZero() && time.Since(acc.LastRefreshAt) > 24*time.Hour {
				needsRefresh = true
			}
			if needsRefresh && time.Since(acc.LastRefreshAt) > 30*time.Minute {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				if _, rErr := s.mgr.ForAccount(acc.ID).RefreshToken(ctx, "schedule"); rErr != nil {
					log.Printf("[scheduler] auto refresh_token failed for %s: %v", acc.DisplayName(), rErr)
					if !acc.ExpiresAt.IsZero() && !acc.ExpiresAt.After(nowUTC) {
						_ = s.store.UpdateAccountHealth(acc.ID, "expired", rErr.Error(), time.Time{})
					}
				}
				cancel()
			}
		}
	}

	// 3. Background balance refresh for pooled accounts (drives weighted-balance OpenAI routing).
	if doBalSync {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			s.mgr.RefreshStaleBalances(ctx, poolBalanceRefreshInterval)
		}()
	}

	// 4. Per-account randomized daily check-in scheduling (staggered serial execution).
	if !cfg.Schedule.Enabled {
		return
	}
	loc := loadLocation(cfg.Schedule.Timezone)
	nowLocal := time.Now().In(loc)
	today := nowLocal.Format("2006-01-02")

	// Respect inter-account anti-burst stagger gap.
	s.maintMu.Lock()
	waitUntil := s.nextAllowedCheckinAt
	s.maintMu.Unlock()
	if !waitUntil.IsZero() && nowUTC.Before(waitUntil) {
		return
	}

	// Find due accounts ordered by their randomized slot for today.
	type dueItem struct {
		acc     store.Account
		planned time.Time
	}
	var due []dueItem
	for _, acc := range accounts {
		if !acc.CheckinEnabled || strings.TrimSpace(acc.Token) == "" {
			continue
		}
		if acc.LastScheduledDay == today {
			continue
		}
		if !acc.ExpiresAt.IsZero() && !acc.ExpiresAt.After(nowUTC) {
			continue
		}
		slot := PlannedTimeForAccount(nowLocal, acc.ID, cfg.Schedule.TimeHHMM, cfg.Schedule.JitterMinutes)
		if !nowLocal.Before(slot) {
			due = append(due, dueItem{acc: acc, planned: slot})
		}
	}
	if len(due) == 0 {
		return
	}
	sort.Slice(due, func(i, j int) bool { return due[i].planned.Before(due[j].planned) })

	// Pick the earliest due account and launch it under the serial check-in gate.
	target := due[0].acc
	select {
	case s.checkinGate <- struct{}{}:
	default:
		return // another account is currently running scheduled check-in
	}

	go func(acc store.Account, day string) {
		defer func() {
			// Add randomized 20..60s cooldown spacing before the next account starts.
			gap := randomRangeSec(minInterAccountGapSec, maxInterAccountGapSec)
			s.maintMu.Lock()
			s.nextAllowedCheckinAt = time.Now().UTC().Add(time.Duration(gap) * time.Second)
			s.maintMu.Unlock()
			<-s.checkinGate
		}()

		mu := s.accountMutex(acc.ID)
		if !mu.TryLock() {
			return
		}
		defer mu.Unlock()

		// Re-check persisted state in case a manual run finished right before lock acquisition.
		latest, ok, err := s.store.GetAccount(acc.ID)
		if err != nil || !ok || latest.LastScheduledDay == day || !latest.CheckinEnabled {
			return
		}

		s.markRunning(acc.ID, true, "scheduled:"+acc.DisplayName())
		defer s.markRunning(acc.ID, false, "")

		ctx, cancel := s.jobContext()
		defer cancel()

		log.Printf("[scheduler] starting daily Bohrium Web tasks for %s (%s)", acc.DisplayName(), day)
		cli := s.mgr.ForAccount(acc.ID)
		res, runErr := cli.RunAllWebTasks(ctx, "schedule", cfg.Schedule.AutoClaim)
		if runErr != nil {
			log.Printf("[scheduler] daily run failed for %s (%s): %v", acc.DisplayName(), day, runErr)
			_ = s.store.ForAccount(acc.ID).AppendLog(store.RunLog{
				Trigger: "schedule",
				Action:  "run_all",
				Success: false,
				Message: fmt.Sprintf("定时任务执行失败: %v", runErr),
			})
			if strings.Contains(runErr.Error(), "401") || strings.Contains(runErr.Error(), "登录") {
				_ = s.store.UpdateAccountCheckin(acc.ID, day, "跳过: "+runErr.Error())
				_ = s.store.UpdateAccountHealth(acc.ID, "expired", runErr.Error(), time.Time{})
			}
			return
		}
		_ = s.store.UpdateAccountCheckin(acc.ID, day, res.Summary)
		// Immediately refresh photon balance so newly earned photons feed into OpenAI pool routing weights.
		balCtx, balCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, _ = s.mgr.RefreshAccountBalance(balCtx, acc.ID)
		balCancel()
		log.Printf("[scheduler] finished daily Bohrium Web tasks for %s (%s): %s", acc.DisplayName(), day, res.Summary)
	}(target, today)
}

// RunAccountManual triggers all 3 Web tasks manually for a specific account.
func (s *Scheduler) RunAccountManual(ctx context.Context, accountID string) (*bohrium.RunAllResult, error) {
	acc, ok, err := s.store.GetAccount(accountID)
	if err != nil || !ok {
		return nil, fmt.Errorf("账号不存在: %s", accountID)
	}
	mu := s.accountMutex(acc.ID)
	if !mu.TryLock() {
		return nil, fmt.Errorf("账号 %s 已有任务正在执行中，请稍候", acc.DisplayName())
	}
	defer mu.Unlock()

	s.markRunning(acc.ID, true, "manual:"+acc.DisplayName())
	defer s.markRunning(acc.ID, false, "")

	cfg := s.cfg.Get()
	cli := s.mgr.ForAccount(acc.ID)
	res, err := cli.RunAllWebTasks(ctx, "manual", cfg.Schedule.AutoClaim)
	if err == nil && res != nil && res.AllWebDone {
		loc := loadLocation(cfg.Schedule.Timezone)
		today := time.Now().In(loc).Format("2006-01-02")
		_ = s.store.UpdateAccountCheckin(acc.ID, today, "手动执行完成: "+res.Summary)
	}
	// Refresh balance snapshot after manual execution.
	balCtx, balCancel := context.WithTimeout(context.Background(), 20*time.Second)
	_, _ = s.mgr.RefreshAccountBalance(balCtx, acc.ID)
	balCancel()
	return res, err
}

// RunAccountSingleTaskManual triggers one specific Web task manually for a specific account.
func (s *Scheduler) RunAccountSingleTaskManual(ctx context.Context, accountID, taskKey string) (*bohrium.TaskExecutionResult, error) {
	acc, ok, err := s.store.GetAccount(accountID)
	if err != nil || !ok {
		return nil, fmt.Errorf("账号不存在: %s", accountID)
	}
	mu := s.accountMutex(acc.ID)
	if !mu.TryLock() {
		return nil, fmt.Errorf("账号 %s 已有任务正在执行中，请稍候", acc.DisplayName())
	}
	defer mu.Unlock()

	s.markRunning(acc.ID, true, fmt.Sprintf("manual_task:%s:%s", acc.DisplayName(), taskKey))
	defer s.markRunning(acc.ID, false, "")

	res, err := s.mgr.ForAccount(acc.ID).ExecuteSingleTask(ctx, taskKey, "manual")
	if err == nil && res != nil && res.Success {
		balCtx, balCancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, _ = s.mgr.RefreshAccountBalance(balCtx, acc.ID)
		balCancel()
	}
	return res, err
}

// RunAllAccountsManual executes daily Web tasks sequentially across all check-in enabled accounts.
func (s *Scheduler) RunAllAccountsManual(ctx context.Context) ([]AccountRunResult, error) {
	accounts, err := s.store.ListAccounts()
	if err != nil {
		return nil, err
	}
	results := make([]AccountRunResult, 0, len(accounts))
	for i, acc := range accounts {
		if ctx.Err() != nil {
			break
		}
		if !acc.CheckinEnabled || strings.TrimSpace(acc.Token) == "" {
			continue
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return results, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
		res, runErr := s.RunAccountManual(ctx, acc.ID)
		item := AccountRunResult{
			AccountID:   acc.ID,
			DisplayName: acc.DisplayName(),
			Result:      res,
		}
		if runErr != nil {
			item.Error = runErr.Error()
		} else if res != nil {
			item.Success = res.AllWebDone
		}
		results = append(results, item)
	}
	return results, nil
}

// RunAllManual runs all Web tasks for the primary/first account (backward compatibility).
func (s *Scheduler) RunAllManual(ctx context.Context) (*bohrium.RunAllResult, error) {
	accounts, err := s.store.ListAccounts()
	if err != nil || len(accounts) == 0 {
		return nil, fmt.Errorf("尚未登录任何 Bohrium 账号")
	}
	return s.RunAccountManual(ctx, accounts[0].ID)
}

// RunSingleManual runs one Web task for the primary/first account (backward compatibility).
func (s *Scheduler) RunSingleManual(ctx context.Context, taskKey string) (*bohrium.TaskExecutionResult, error) {
	accounts, err := s.store.ListAccounts()
	if err != nil || len(accounts) == 0 {
		return nil, fmt.Errorf("尚未登录任何 Bohrium 账号")
	}
	return s.RunAccountSingleTaskManual(ctx, accounts[0].ID, taskKey)
}

// Status returns global scheduler status plus each account's randomized daily check-in schedule.
func (s *Scheduler) Status() Status {
	cfg := s.cfg.Get()
	s.stateMu.RLock()
	running := s.running
	curJob := s.currentJob
	activeAcc := s.activeAccountID
	runningMap := make(map[string]bool, len(s.runningAccounts))
	for k, v := range s.runningAccounts {
		runningMap[k] = v
	}
	s.stateMu.RUnlock()

	loc := loadLocation(cfg.Schedule.Timezone)
	nowLocal := time.Now().In(loc)
	today := nowLocal.Format("2006-01-02")

	accounts, _ := s.store.ListAccounts()
	perAcc := make(map[string]AccountScheduleInfo, len(accounts))

	var (
		earliestNext time.Time
		enabledCnt   int
		doneTodayCnt int
		latestRunAt  time.Time
		latestDay    string
		latestSum    string
	)

	for _, acc := range accounts {
		doneToday := acc.LastScheduledDay == today
		if acc.CheckinEnabled {
			enabledCnt++
		}
		if doneToday {
			doneTodayCnt++
		}
		if acc.LastRunAt.After(latestRunAt) {
			latestRunAt = acc.LastRunAt
			latestDay = acc.LastScheduledDay
			latestSum = acc.LastRunSummary
		}

		info := AccountScheduleInfo{
			AccountID:        acc.ID,
			CheckinEnabled:   acc.CheckinEnabled,
			Running:          runningMap[acc.ID],
			DoneToday:        doneToday,
			LastScheduledDay: acc.LastScheduledDay,
			LastRunAt:        acc.LastRunAt,
			LastRunSummary:   acc.LastRunSummary,
		}
		if cfg.Schedule.Enabled && acc.CheckinEnabled {
			todaySlot := PlannedTimeForAccount(nowLocal, acc.ID, cfg.Schedule.TimeHHMM, cfg.Schedule.JitterMinutes)
			info.PlannedTodayAt = todaySlot.Format("15:04:05")
			nextSlot := todaySlot
			if doneToday {
				nextSlot = PlannedTimeForAccount(nowLocal.AddDate(0, 0, 1), acc.ID, cfg.Schedule.TimeHHMM, cfg.Schedule.JitterMinutes)
			}
			info.NextRunAt = nextSlot.Format("2006-01-02 15:04:05 MST")
			if earliestNext.IsZero() || nextSlot.Before(earliestNext) {
				earliestNext = nextSlot
			}
		}
		perAcc[acc.ID] = info
	}

	st := Status{
		Enabled:          cfg.Schedule.Enabled,
		Running:          running,
		CurrentJob:       curJob,
		ActiveAccountID:  activeAcc,
		TimeHHMM:         cfg.Schedule.TimeHHMM,
		Timezone:         cfg.Schedule.Timezone,
		JitterMinutes:    cfg.Schedule.JitterMinutes,
		AutoClaim:        cfg.Schedule.AutoClaim,
		AutoRefresh:      cfg.Schedule.AutoRefresh,
		TotalAccounts:    len(accounts),
		EnabledAccounts:  enabledCnt,
		DoneTodayCount:   doneTodayCnt,
		LastScheduledDay: latestDay,
		LastRunAt:        latestRunAt,
		LastRunSummary:   latestSum,
		Accounts:         perAcc,
	}
	if !earliestNext.IsZero() {
		st.NextRunAt = earliestNext.Format("2006-01-02 15:04:05 MST")
	} else if cfg.Schedule.Enabled && len(accounts) == 0 {
		st.NextRunAt = PlannedTimeForAccount(nowLocal, "default", cfg.Schedule.TimeHHMM, cfg.Schedule.JitterMinutes).Format("2006-01-02 15:04:05 MST")
	}
	return st
}

func (s *Scheduler) jobContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (s *Scheduler) accountMutex(accountID string) *sync.Mutex {
	s.accLockMu.Lock()
	defer s.accLockMu.Unlock()
	if mu, ok := s.accLocks[accountID]; ok {
		return mu
	}
	mu := &sync.Mutex{}
	s.accLocks[accountID] = mu
	return mu
}

func (s *Scheduler) markRunning(accountID string, active bool, job string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if active {
		s.runningAccounts[accountID] = true
		s.running = true
		s.currentJob = job
		s.activeAccountID = accountID
		return
	}
	delete(s.runningAccounts, accountID)
	if len(s.runningAccounts) == 0 {
		s.running = false
		s.currentJob = ""
		s.activeAccountID = ""
	}
}

func loadLocation(tz string) *time.Location {
	if loc, err := time.LoadLocation(strings.TrimSpace(tz)); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*3600)
}

// PlannedTimeForAccount computes HH:MM + deterministic per-day, per-account jitter (minutes + seconds)
// so each account executes at a distinct randomized time each day while staying stable across restarts.
func PlannedTimeForAccount(dayRef time.Time, accountID, hhmm string, jitterMinutes int) time.Time {
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
	sum := sha256.Sum256([]byte("bohrium-jitter:" + dayRef.Format("2006-01-02") + ":" + strings.TrimSpace(accountID)))
	if jitterMinutes > 0 {
		minOffset := int(binary.BigEndian.Uint32(sum[:4]) % uint32(jitterMinutes+1))
		base = base.Add(time.Duration(minOffset) * time.Minute)
	}
	secOffset := int(binary.BigEndian.Uint32(sum[4:8]) % 60)
	base = base.Add(time.Duration(secOffset) * time.Second)
	return base
}

func randomRangeSec(minSec, maxSec int) int {
	if maxSec <= minSec {
		return minSec
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxSec-minSec+1)))
	if err != nil {
		return minSec
	}
	return minSec + int(n.Int64())
}
