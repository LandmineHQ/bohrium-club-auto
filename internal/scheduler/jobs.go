package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"bohrium-club-auto/internal/bohrium"
)

const maxRetainedTaskJobs = 100

// TaskJob tracks an asynchronous manual daily-task execution running in the Go backend,
// completely decoupled from HTTP request lifetimes (so CDN/reverse-proxy timeouts never cancel it).
type TaskJob struct {
	ID          string                       `json:"id"`
	Kind        string                       `json:"kind"` // "single_task" | "account_all" | "all_accounts"
	AccountID   string                       `json:"accountId,omitempty"`
	AccountName string                       `json:"accountName,omitempty"`
	TaskKey     string                       `json:"taskKey,omitempty"`
	Status      string                       `json:"status"` // "running" | "completed" | "failed"
	Message     string                       `json:"message,omitempty"`
	Error       string                       `json:"error,omitempty"`
	StartedAt   time.Time                    `json:"startedAt"`
	CompletedAt *time.Time                   `json:"completedAt,omitempty"`
	DurationMs  int64                        `json:"durationMs"`
	SingleRes   *bohrium.TaskExecutionResult `json:"singleResult,omitempty"`
	AccountRes  *bohrium.RunAllResult        `json:"accountResult,omitempty"`
	AllRes      []AccountRunResult           `json:"allResults,omitempty"`
}

func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("job_%d_%s", time.Now().UTC().UnixNano(), hex.EncodeToString(b[:]))
}

func cloneTaskJob(j *TaskJob) *TaskJob {
	if j == nil {
		return nil
	}
	cp := *j
	if j.CompletedAt != nil {
		t := *j.CompletedAt
		cp.CompletedAt = &t
	}
	if j.SingleRes != nil {
		sr := *j.SingleRes
		if sr.Meta != nil {
			m := make(map[string]any, len(sr.Meta))
			for k, v := range sr.Meta {
				m[k] = v
			}
			sr.Meta = m
		}
		cp.SingleRes = &sr
	}
	if j.AccountRes != nil {
		ar := *j.AccountRes
		if ar.Results != nil {
			ar.Results = append([]bohrium.TaskExecutionResult(nil), ar.Results...)
		}
		cp.AccountRes = &ar
	}
	if j.AllRes != nil {
		cp.AllRes = append([]AccountRunResult(nil), j.AllRes...)
	}
	return &cp
}

func (s *Scheduler) registerJob(job *TaskJob) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.jobs == nil {
		s.jobs = make(map[string]*TaskJob)
	}
	s.jobs[job.ID] = job
	s.jobOrder = append(s.jobOrder, job.ID)

	for len(s.jobOrder) > maxRetainedTaskJobs {
		evictIdx := -1
		for i, id := range s.jobOrder {
			if j, ok := s.jobs[id]; ok && j.Status != "running" {
				evictIdx = i
				break
			}
		}
		if evictIdx < 0 {
			break
		}
		evictID := s.jobOrder[evictIdx]
		delete(s.jobs, evictID)
		s.jobOrder = append(s.jobOrder[:evictIdx], s.jobOrder[evictIdx+1:]...)
	}
}

// StartAccountSingleTaskManual launches a single manual task asynchronously in the Go backend.
func (s *Scheduler) StartAccountSingleTaskManual(accountID, taskKey string) (*TaskJob, error) {
	acc, ok, err := s.store.GetAccount(accountID)
	if err != nil || !ok {
		return nil, fmt.Errorf("账号不存在: %s", accountID)
	}
	mu := s.accountMutex(acc.ID)
	if !mu.TryLock() {
		return nil, fmt.Errorf("账号 %s 已有任务正在执行中，请稍候", acc.DisplayName())
	}

	job := &TaskJob{
		ID:          newJobID(),
		Kind:        "single_task",
		AccountID:   acc.ID,
		AccountName: acc.DisplayName(),
		TaskKey:     taskKey,
		Status:      "running",
		Message:     "正在后台执行任务...",
		StartedAt:   time.Now().UTC(),
	}
	s.registerJob(job)
	s.markRunning(acc.ID, true, fmt.Sprintf("manual_task:%s:%s", acc.DisplayName(), taskKey))

	go func() {
		defer mu.Unlock()
		defer s.markRunning(acc.ID, false, "")
		ctx, cancel := s.jobContext()
		defer cancel()

		res, runErr := s.mgr.ForAccount(acc.ID).ExecuteSingleTask(ctx, taskKey, "manual")
		if runErr == nil && res != nil && res.Success {
			balCtx, balCancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, _ = s.mgr.RefreshAccountBalance(balCtx, acc.ID)
			balCancel()
		}

		now := time.Now().UTC()
		s.jobsMu.Lock()
		job.CompletedAt = &now
		job.DurationMs = now.Sub(job.StartedAt).Milliseconds()
		job.SingleRes = res
		if runErr != nil {
			job.Status = "failed"
			job.Error = runErr.Error()
			job.Message = runErr.Error()
		} else if res == nil {
			job.Status = "failed"
			job.Error = "未返回任务结果"
			job.Message = "未返回任务结果"
		} else if !res.Success {
			job.Status = "failed"
			job.Error = res.Message
			job.Message = res.Message
		} else {
			job.Status = "completed"
			job.Message = res.Message
		}
		s.jobsMu.Unlock()
	}()

	return cloneTaskJob(job), nil
}

// StartAccountManual launches all Web daily tasks for an account asynchronously in the Go backend.
func (s *Scheduler) StartAccountManual(accountID string) (*TaskJob, error) {
	acc, ok, err := s.store.GetAccount(accountID)
	if err != nil || !ok {
		return nil, fmt.Errorf("账号不存在: %s", accountID)
	}
	mu := s.accountMutex(acc.ID)
	if !mu.TryLock() {
		return nil, fmt.Errorf("账号 %s 已有任务正在执行中，请稍候", acc.DisplayName())
	}

	job := &TaskJob{
		ID:          newJobID(),
		Kind:        "account_all",
		AccountID:   acc.ID,
		AccountName: acc.DisplayName(),
		Status:      "running",
		Message:     "正在后台执行该账号全部每日任务...",
		StartedAt:   time.Now().UTC(),
	}
	s.registerJob(job)
	s.markRunning(acc.ID, true, "manual:"+acc.DisplayName())

	go func() {
		defer mu.Unlock()
		defer s.markRunning(acc.ID, false, "")
		ctx, cancel := s.jobContext()
		defer cancel()

		cfg := s.cfg.Get()
		cli := s.mgr.ForAccount(acc.ID)
		res, runErr := cli.RunAllWebTasks(ctx, "manual", cfg.Schedule.AutoClaim)
		if runErr == nil && res != nil && res.AllWebDone {
			loc := loadLocation(cfg.Schedule.Timezone)
			today := time.Now().In(loc).Format("2006-01-02")
			_ = s.store.UpdateAccountCheckin(acc.ID, today, "手动执行完成: "+res.Summary)
		}
		balCtx, balCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, _ = s.mgr.RefreshAccountBalance(balCtx, acc.ID)
		balCancel()

		now := time.Now().UTC()
		s.jobsMu.Lock()
		job.CompletedAt = &now
		job.DurationMs = now.Sub(job.StartedAt).Milliseconds()
		job.AccountRes = res
		if runErr != nil {
			job.Status = "failed"
			job.Error = runErr.Error()
			job.Message = runErr.Error()
		} else if res == nil {
			job.Status = "failed"
			job.Error = "未返回任务结果"
			job.Message = "未返回任务结果"
		} else {
			job.Status = "completed"
			job.Message = res.Summary
		}
		s.jobsMu.Unlock()
	}()

	return cloneTaskJob(job), nil
}

// StartAllAccountsManual launches sequential check-in across all check-in enabled accounts asynchronously.
func (s *Scheduler) StartAllAccountsManual() (*TaskJob, error) {
	if !s.allManualMu.TryLock() {
		return nil, fmt.Errorf("已有批量多账号签到任务正在执行中，请稍候")
	}

	job := &TaskJob{
		ID:        newJobID(),
		Kind:      "all_accounts",
		Status:    "running",
		Message:   "正在后台顺序执行全部启用签到的账号...",
		StartedAt: time.Now().UTC(),
	}
	s.registerJob(job)

	go func() {
		defer s.allManualMu.Unlock()
		ctx, cancel := s.jobContext()
		defer cancel()

		results, runErr := s.RunAllAccountsManual(ctx)
		okCnt := 0
		for _, r := range results {
			if r.Success {
				okCnt++
			}
		}

		now := time.Now().UTC()
		s.jobsMu.Lock()
		job.CompletedAt = &now
		job.DurationMs = now.Sub(job.StartedAt).Milliseconds()
		job.AllRes = results
		if runErr != nil {
			job.Status = "failed"
			job.Error = runErr.Error()
			job.Message = runErr.Error()
		} else {
			job.Status = "completed"
			job.Message = fmt.Sprintf("多账号批量签到完成：%d/%d 成功", okCnt, len(results))
		}
		s.jobsMu.Unlock()
	}()

	return cloneTaskJob(job), nil
}

// GetTaskJob returns a deep snapshot of an async task job by ID.
func (s *Scheduler) GetTaskJob(id string) (*TaskJob, bool) {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	return cloneTaskJob(j), true
}

// ListTaskJobs returns recent async task jobs in reverse chronological order.
func (s *Scheduler) ListTaskJobs(limit int) []*TaskJob {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	if limit <= 0 || limit > len(s.jobOrder) {
		limit = len(s.jobOrder)
	}
	out := make([]*TaskJob, 0, limit)
	for i := len(s.jobOrder) - 1; i >= 0 && len(out) < limit; i-- {
		if j, ok := s.jobs[s.jobOrder[i]]; ok {
			out = append(out, cloneTaskJob(j))
		}
	}
	return out
}

// ActiveTaskJobs returns currently running async task jobs.
func (s *Scheduler) ActiveTaskJobs() []*TaskJob {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	out := make([]*TaskJob, 0)
	for i := len(s.jobOrder) - 1; i >= 0; i-- {
		if j, ok := s.jobs[s.jobOrder[i]]; ok && j.Status == "running" {
			out = append(out, cloneTaskJob(j))
		}
	}
	return out
}
