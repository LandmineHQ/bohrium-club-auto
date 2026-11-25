package bohrium

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bohrium-club-auto/internal/store"
)

// DailyTask represents one Bohrium Plaza daily task enriched with local execution metadata.
type DailyTask struct {
	ID                   int64  `json:"id"`
	TaskKey              string `json:"taskKey"`
	Publisher            string `json:"publisher"`
	TitleZh              string `json:"titleZh"`
	TitleEn              string `json:"titleEn"`
	DescriptionZh        string `json:"descriptionZh"`
	RewardAmount         int64  `json:"rewardAmount"`
	CreditedRewardAmount int64  `json:"creditedRewardAmount"`
	Status               int    `json:"status"` // -1 unclaimed, 0 claimed/pending, 1 completed
	StatusLabel          string `json:"statusLabel"`
	StartAt              string `json:"startAt,omitempty"`
	EndAt                string `json:"endAt,omitempty"`
	CompletedAt          string `json:"completedAt,omitempty"`

	// Enriched automation metadata
	ActionKey  string `json:"actionKey"`
	Executable bool   `json:"executable"`
	Skipped    bool   `json:"skipped"`
	SkipReason string `json:"skipReason,omitempty"`
	TargetURL  string `json:"targetUrl,omitempty"`
}

// DailyTasksResponse is returned by ListDailyTasks.
type DailyTasksResponse struct {
	TaskDate        string      `json:"taskDate"`
	Tasks           []DailyTask `json:"tasks"`
	TotalCount      int         `json:"totalCount"`
	UnclaimedCount  int         `json:"unclaimedCount"`
	PendingWebCount int         `json:"pendingWebCount"`
	CompletedCount  int         `json:"completedCount"`
	SkippedCount    int         `json:"skippedCount"`
	EarnedPhotons   int64       `json:"earnedPhotons"`
	PossiblePhotons int64       `json:"possiblePhotons"`
	FetchedAt       time.Time   `json:"fetchedAt"`
}

// ClaimResult summarizes a claim-all or claim-one API call.
type ClaimResult struct {
	Success    bool               `json:"success"`
	ClaimedKey string             `json:"claimedKey,omitempty"`
	Message    string             `json:"message"`
	Tasks      *DailyTasksResponse `json:"tasks,omitempty"`
}

// ListDailyTasks fetches the 4 daily tasks from club.bohrium.com (with platform-gateway fallback).
func (c *Client) ListDailyTasks(ctx context.Context) (*DailyTasksResponse, error) {
	auth := c.store.GetAuth()
	if auth.Token == "" {
		return nil, fmt.Errorf("请先登录 Bohrium 账号")
	}

	resp, err := c.http.DoJSON(ctx, http.MethodGet, c.endpoints.ClubBase+"/api/plaza/tasks", nil, c.clubHeaders(auth.Token))
	if err != nil || resp.StatusCode != http.StatusOK {
		// Fallback directly to www.bohrium.com platform-gateway daily-task/list
		fbResp, fbErr := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.WWWBase+"/platform-gateway/v1/daily-task/list", map[string]any{
			"publisher": PublisherCommunity,
		}, c.wwwHeaders(auth.Token, ""))
		if fbErr != nil {
			if err != nil {
				return nil, fmt.Errorf("获取每日任务失败: %w", err)
			}
			return nil, fmt.Errorf("获取每日任务 HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 200))
		}
		resp = fbResp
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("登录已失效（HTTP 401），请重新扫码或更新 brmToken")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取每日任务失败 HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}

	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			TaskDate string      `json:"taskDate"`
			List     []DailyTask `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("解析每日任务列表失败: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("每日任务接口返回错误 code=%d: %s", env.Code, env.Msg)
	}

	out := &DailyTasksResponse{
		TaskDate:  env.Data.TaskDate,
		Tasks:     make([]DailyTask, 0, len(env.Data.List)),
		FetchedAt: time.Now().UTC(),
	}
	for _, t := range env.Data.List {
		enriched := classifyTask(t)
		out.Tasks = append(out.Tasks, enriched)
		out.TotalCount++
		if enriched.Skipped {
			out.SkippedCount++
		} else {
			out.PossiblePhotons += enriched.RewardAmount
		}
		switch enriched.Status {
		case -1:
			out.UnclaimedCount++
		case 0:
			if !enriched.Skipped {
				out.PendingWebCount++
			}
		case 1:
			out.CompletedCount++
			if enriched.CreditedRewardAmount > 0 {
				out.EarnedPhotons += enriched.CreditedRewardAmount
			} else {
				out.EarnedPhotons += enriched.RewardAmount
			}
		}
	}
	return out, nil
}

// ClaimAllTasks invokes POST /api/plaza/tasks/claim to claim all daily tasks in one click.
func (c *Client) ClaimAllTasks(ctx context.Context, trigger string) (*ClaimResult, error) {
	return c.claimTasks(ctx, "", trigger)
}

// ClaimTask invokes POST /api/plaza/tasks/claim for a single taskKey.
func (c *Client) ClaimTask(ctx context.Context, taskKey, trigger string) (*ClaimResult, error) {
	return c.claimTasks(ctx, strings.TrimSpace(taskKey), trigger)
}

func (c *Client) claimTasks(ctx context.Context, taskKey, trigger string) (*ClaimResult, error) {
	start := time.Now()
	auth := c.store.GetAuth()
	if auth.Token == "" {
		return nil, fmt.Errorf("请先登录 Bohrium 账号")
	}

	payload := map[string]any{
		"publisher": PublisherCommunity,
	}
	action := "claim_all"
	if taskKey != "" {
		payload["taskKey"] = taskKey
		action = "claim_one"
	}

	resp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.ClubBase+"/api/plaza/tasks/claim", payload, c.clubHeaders(auth.Token))
	if err != nil {
		return nil, fmt.Errorf("领取任务请求失败: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("登录已失效（HTTP 401），请重新登录")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("领取任务失败 HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}

	var parsed map[string]any
	_ = json.Unmarshal(resp.Body, &parsed)
	if code := int(asInt64(parsed["code"])); code != 0 {
		msg := asString(parsed["msg"])
		if msg == "" {
			msg = asString(parsed["error"])
		}
		_ = c.store.AppendLog(store.RunLog{
			Trigger:    trigger,
			Action:     action,
			TaskKey:    taskKey,
			Success:    false,
			DurationMs: time.Since(start).Milliseconds(),
			Message:    fmt.Sprintf("领取任务失败 (code=%d): %s", code, msg),
		})
		return nil, fmt.Errorf("领取任务失败 (code=%d): %s", code, msg)
	}

	refreshed, _ := c.ListDailyTasks(ctx)
	msg := "已一键领取全部每日任务"
	if taskKey != "" {
		msg = fmt.Sprintf("已领取任务 %s", taskKey)
	}
	_ = c.store.AppendLog(store.RunLog{
		Trigger:    trigger,
		Action:     action,
		TaskKey:    taskKey,
		Success:    true,
		DurationMs: time.Since(start).Milliseconds(),
		Message:    msg,
	})
	return &ClaimResult{
		Success:    true,
		ClaimedKey: taskKey,
		Message:    msg,
		Tasks:      refreshed,
	}, nil
}

// classifyTask maps an upstream plaza daily task to its automation handler
// (including Task #2 Desktop Client via authentic Bohrium SciX /scix/llm protocol).
func classifyTask(t DailyTask) DailyTask {
	switch t.Status {
	case -1:
		t.StatusLabel = "未领取"
	case 0:
		t.StatusLabel = "待完成"
	case 1:
		t.StatusLabel = "已完成"
	default:
		t.StatusLabel = fmt.Sprintf("状态(%d)", t.Status)
	}

	keyLower := strings.ToLower(t.TaskKey)
	titleLower := strings.ToLower(t.TitleZh + " " + t.TitleEn)

	// Rule 1: Desktop Client task (Task #2: 体验玻尔科学空间「客户端」) — executed via native SciX desktop protocol.
	if strings.Contains(keyLower, "client") ||
		strings.Contains(keyLower, "desktop") ||
		strings.Contains(t.TitleZh, "客户端") ||
		strings.Contains(titleLower, "desktop") ||
		strings.Contains(titleLower, "client") {
		t.ActionKey = TaskKeyScienceSpaceClient
		t.Skipped = false
		t.Executable = true
		t.TargetURL = "scix://"
		return t
	}

	// Rule 2: Literature Reading Web task (Task #3: 体验玻尔「文献阅读」).
	if strings.Contains(keyLower, "literature") ||
		strings.Contains(keyLower, "reading") ||
		strings.Contains(keyLower, "paper") ||
		strings.Contains(t.TitleZh, "文献阅读") ||
		strings.Contains(titleLower, "literature") ||
		strings.Contains(titleLower, "reading") {
		t.ActionKey = TaskKeyLiteratureReading
		t.Executable = true
		t.TargetURL = "https://www.bohrium.com/agent/chat-with-paper"
		return t
	}

	// Rule 3: Academic Writing Web task (Task #4: 体验玻尔「学术写作」).
	if strings.Contains(keyLower, "writing") ||
		strings.Contains(t.TitleZh, "学术写作") ||
		strings.Contains(titleLower, "writing") {
		t.ActionKey = TaskKeyAcademicWriting
		t.Executable = true
		t.TargetURL = "https://www.bohrium.com/academic-writing"
		return t
	}

	// Rule 4: Science Space Web task (Task #1: 体验玻尔科学空间「Web端」).
	if strings.Contains(keyLower, "science_space") ||
		strings.Contains(keyLower, "web") ||
		strings.Contains(t.TitleZh, "Web") ||
		strings.Contains(t.TitleZh, "科学空间") ||
		strings.Contains(titleLower, "science space") {
		t.ActionKey = TaskKeyScienceSpaceWeb
		t.Executable = true
		t.TargetURL = "https://www.bohrium.com/"
		return t
	}

	// Unknown task fallback: mark non-executable so we never blindly trigger unintended tasks.
	t.ActionKey = t.TaskKey
	t.Executable = false
	t.SkipReason = "未知任务类型，请检查前端 Recon 变更"
	return t
}

// IsClientTask reports whether a taskKey or actionKey corresponds to Task #2 (Desktop Client).
func IsClientTask(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return k == TaskKeyScienceSpaceClient || strings.Contains(k, "client") || strings.Contains(k, "desktop")
}
