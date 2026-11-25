package bohrium

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bohrium-club-auto/internal/prompts"
	"bohrium-club-auto/internal/store"
)

// TaskExecutionResult captures detailed telemetry for one Web task execution.
type TaskExecutionResult struct {
	TaskKey       string         `json:"taskKey"`
	ActionKey     string         `json:"actionKey"`
	TaskTitle     string         `json:"taskTitle,omitempty"`
	Success       bool           `json:"success"`
	Skipped       bool           `json:"skipped"`
	Message       string         `json:"message"`
	PromptPreview string         `json:"promptPreview,omitempty"`
	PaperID       string         `json:"paperId,omitempty"`
	PaperTitle    string         `json:"paperTitle,omitempty"`
	PaperSource   string         `json:"paperSource,omitempty"`
	QueryID       string         `json:"queryId,omitempty"`
	WorkflowID    string         `json:"workflowId,omitempty"`
	SessionID     string         `json:"sessionId,omitempty"`
	QueryReported bool           `json:"queryReported"`
	Cancelled     bool           `json:"cancelled"`
	DurationMs    int64          `json:"durationMs"`
	Meta          map[string]any `json:"meta,omitempty"`
}

// RunAllResult summarizes executing claim + all 3 Web tasks (while skipping Task #2 desktop client).
type RunAllResult struct {
	StartedAt     time.Time             `json:"startedAt"`
	CompletedAt   time.Time             `json:"completedAt"`
	DurationMs    int64                 `json:"durationMs"`
	Claimed       bool                  `json:"claimed"`
	Results       []TaskExecutionResult `json:"results"`
	TasksAfter    *DailyTasksResponse   `json:"tasksAfter,omitempty"`
	AllWebDone    bool                  `json:"allWebDone"`
	Summary       string                `json:"summary"`
}

// RunAllWebTasks claims unclaimed tasks (if autoClaim is true), skips Task #2 (Desktop Client),
// and runs all incomplete Web tasks sequentially with a short delay between them.
func (c *Client) RunAllWebTasks(ctx context.Context, trigger string, autoClaim bool) (*RunAllResult, error) {
	start := time.Now()
	auth := c.store.GetAuth()
	if auth.Token == "" {
		return nil, fmt.Errorf("请先登录 Bohrium 账号")
	}

	out := &RunAllResult{
		StartedAt: start.UTC(),
		Results:   make([]TaskExecutionResult, 0, 4),
	}

	tasksResp, err := c.ListDailyTasks(ctx)
	if err != nil {
		return nil, err
	}

	// Step 1: Claim all unclaimed tasks first.
	if autoClaim && tasksResp.UnclaimedCount > 0 {
		if _, err := c.ClaimAllTasks(ctx, trigger); err != nil {
			return nil, fmt.Errorf("自动领取每日任务失败: %w", err)
		}
		out.Claimed = true
		if refreshed, err := c.ListDailyTasks(ctx); err == nil {
			tasksResp = refreshed
		}
	}

	delaySec := c.cfg.Get().TaskDelaySec
	if delaySec < 0 {
		delaySec = 0
	}

	executedAny := false
	for _, task := range tasksResp.Tasks {
		if task.Skipped {
			out.Results = append(out.Results, TaskExecutionResult{
				TaskKey:   task.TaskKey,
				ActionKey: task.ActionKey,
				TaskTitle: task.TitleZh,
				Success:   true,
				Skipped:   true,
				Message:   task.SkipReason,
			})
			continue
		}
		if task.Status == 1 {
			out.Results = append(out.Results, TaskExecutionResult{
				TaskKey:   task.TaskKey,
				ActionKey: task.ActionKey,
				TaskTitle: task.TitleZh,
				Success:   true,
				Skipped:   true,
				Message:   "今日已完成，无需重复执行",
			})
			continue
		}
		if !task.Executable {
			out.Results = append(out.Results, TaskExecutionResult{
				TaskKey:   task.TaskKey,
				ActionKey: task.ActionKey,
				TaskTitle: task.TitleZh,
				Success:   false,
				Skipped:   true,
				Message:   task.SkipReason,
			})
			continue
		}

		if executedAny && delaySec > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(delaySec) * time.Second):
			}
		}

		res := c.executeClassifiedTask(ctx, task, trigger)
		out.Results = append(out.Results, res)
		executedAny = true
	}

	// Give Bohrium backend a brief moment to settle query-events before re-querying task statuses.
	if executedAny {
		select {
		case <-ctx.Done():
		case <-time.After(1200 * time.Millisecond):
		}
	}

	after, _ := c.ListDailyTasks(ctx)
	out.TasksAfter = after
	out.CompletedAt = time.Now().UTC()
	out.DurationMs = time.Since(start).Milliseconds()

	okCount, failCount, skipCount := 0, 0, 0
	for _, r := range out.Results {
		if r.Skipped {
			skipCount++
		} else if r.Success {
			okCount++
		} else {
			failCount++
		}
	}
	if after != nil {
		out.AllWebDone = after.PendingWebCount == 0 && after.UnclaimedCount == 0
	} else {
		out.AllWebDone = failCount == 0
	}
	out.Summary = fmt.Sprintf("执行完成：成功 %d 项，失败 %d 项，跳过 %d 项", okCount, failCount, skipCount)

	_ = c.store.AppendLog(store.RunLog{
		Trigger:    trigger,
		Action:     "run_all",
		Success:    failCount == 0,
		DurationMs: out.DurationMs,
		Message:    out.Summary,
	})
	return out, nil
}

// ExecuteSingleTask executes a single Web task by its taskKey or actionKey.
func (c *Client) ExecuteSingleTask(ctx context.Context, taskKey, trigger string) (*TaskExecutionResult, error) {
	auth := c.store.GetAuth()
	if auth.Token == "" {
		return nil, fmt.Errorf("请先登录 Bohrium 账号")
	}
	if IsClientSkippedTask(taskKey) {
		return &TaskExecutionResult{
			TaskKey:   taskKey,
			ActionKey: TaskKeyClientSkipped,
			Success:   true,
			Skipped:   true,
			Message:   "客户端任务已按规则跳过，不会执行",
		}, nil
	}

	// Fetch current tasks so we know if it needs claiming first and have its exact title/actionKey.
	var target DailyTask
	found := false
	if list, err := c.ListDailyTasks(ctx); err == nil {
		for _, t := range list.Tasks {
			if t.TaskKey == taskKey || t.ActionKey == taskKey {
				target = t
				found = true
				break
			}
		}
	}
	if !found {
		target = classifyTask(DailyTask{TaskKey: taskKey, Status: 0})
	}
	if target.Skipped {
		return &TaskExecutionResult{
			TaskKey:   target.TaskKey,
			ActionKey: target.ActionKey,
			TaskTitle: target.TitleZh,
			Success:   true,
			Skipped:   true,
			Message:   target.SkipReason,
		}, nil
	}
	if target.Status == -1 {
		// Automatically claim before executing so manual button works in one click.
		if _, err := c.ClaimTask(ctx, target.TaskKey, trigger); err != nil {
			// Fallback to claim-all if single claim fails
			_, _ = c.ClaimAllTasks(ctx, trigger)
		}
	}

	res := c.executeClassifiedTask(ctx, target, trigger)
	return &res, nil
}

func (c *Client) executeClassifiedTask(ctx context.Context, task DailyTask, trigger string) TaskExecutionResult {
	start := time.Now()
	var res TaskExecutionResult
	switch task.ActionKey {
	case TaskKeyScienceSpaceWeb:
		res = c.runScienceSpaceWebTask(ctx, task)
	case TaskKeyLiteratureReading:
		res = c.runLiteratureReadingTask(ctx, task)
	case TaskKeyAcademicWriting:
		res = c.runAcademicWritingTask(ctx, task)
	default:
		res = TaskExecutionResult{
			TaskKey:   task.TaskKey,
			ActionKey: task.ActionKey,
			TaskTitle: task.TitleZh,
			Success:   false,
			Skipped:   true,
			Message:   "不支持的任务类型: " + task.ActionKey,
		}
	}
	res.DurationMs = time.Since(start).Milliseconds()

	_ = c.store.AppendLog(store.RunLog{
		Trigger:    trigger,
		Action:     "task_run",
		TaskKey:    task.TaskKey,
		TaskTitle:  nonEmpty(task.TitleZh, task.ActionKey),
		Success:    res.Success,
		Skipped:    res.Skipped,
		DurationMs: res.DurationMs,
		Message:    res.Message,
		Meta: map[string]any{
			"actionKey":     res.ActionKey,
			"workflowId":    res.WorkflowID,
			"queryId":       res.QueryID,
			"paperId":       res.PaperID,
			"paperSource":   res.PaperSource,
			"queryReported": res.QueryReported,
			"cancelled":     res.Cancelled,
			"promptPreview": res.PromptPreview,
		},
	})
	return res
}

// Task 1: 体验玻尔科学空间「Web端」
func (c *Client) runScienceSpaceWebTask(ctx context.Context, task DailyTask) TaskExecutionResult {
	res := TaskExecutionResult{
		TaskKey:   task.TaskKey,
		ActionKey: TaskKeyScienceSpaceWeb,
		TaskTitle: nonEmpty(task.TitleZh, "体验玻尔科学空间「Web端」"),
	}
	promptText, err := prompts.RandomScienceSpacePrompt()
	if err != nil {
		res.Message = "加载科学空间提问语料失败: " + err.Error()
		return res
	}
	res.PromptPreview = truncateText(promptText, 72)

	queryID := newUUIDv4()
	payload := map[string]any{
		"text": promptText,
		"target": map[string]any{
			"kind":   "team",
			"teamId": DefaultSciSpaceTeamID,
		},
		"queryId":             queryID,
		"invocationRequestId": newUUIDv4(),
		"clientTimeZone":      "Asia/Shanghai",
	}

	chatOut, err := c.submitChatAndCancel(ctx, payload, "", TaskKeyScienceSpaceWeb, queryID)
	if err != nil {
		res.Message = "科学空间 Web 提问失败: " + err.Error()
		return res
	}
	applyChatOutcome(&res, chatOut)
	res.Success = true
	res.Message = fmt.Sprintf("科学空间 Web 提问已提交并立即终止（workflow=%s, reported=%v, cancelled=%v）",
		nonEmpty(res.WorkflowID, "n/a"), res.QueryReported, res.Cancelled)
	return res
}

// Task 4: 体验玻尔「学术写作」
func (c *Client) runAcademicWritingTask(ctx context.Context, task DailyTask) TaskExecutionResult {
	res := TaskExecutionResult{
		TaskKey:   task.TaskKey,
		ActionKey: TaskKeyAcademicWriting,
		TaskTitle: nonEmpty(task.TitleZh, "体验玻尔「学术写作」"),
	}
	promptText, err := prompts.RandomAcademicWritingPrompt()
	if err != nil {
		res.Message = "加载学术写作语料失败: " + err.Error()
		return res
	}
	res.PromptPreview = truncateText(promptText, 72)

	queryID := newUUIDv4()
	payload := map[string]any{
		"text": promptText,
		"target": map[string]any{
			"kind":   "team",
			"teamId": DefaultSciSpaceTeamID,
		},
		"queryId":             queryID,
		"invocationRequestId": newUUIDv4(),
		"clientTimeZone":      "Asia/Shanghai",
	}

	chatOut, err := c.submitChatAndCancel(ctx, payload, "academic-writing", TaskKeyAcademicWriting, queryID)
	if err != nil {
		res.Message = "学术写作提问失败: " + err.Error()
		return res
	}
	applyChatOutcome(&res, chatOut)
	res.Success = true
	res.Message = fmt.Sprintf("学术写作提问已提交并立即终止（workflow=%s, reported=%v, cancelled=%v）",
		nonEmpty(res.WorkflowID, "n/a"), res.QueryReported, res.Cancelled)
	return res
}

// Task 3: 体验玻尔「文献阅读」 — pick random PDF from 100-paper catalog, upload to Reader, ask, and immediately cancel.
func (c *Client) runLiteratureReadingTask(ctx context.Context, task DailyTask) TaskExecutionResult {
	res := TaskExecutionResult{
		TaskKey:   task.TaskKey,
		ActionKey: TaskKeyLiteratureReading,
		TaskTitle: nonEmpty(task.TitleZh, "体验玻尔「文献阅读」"),
	}
	pdf, err := c.AcquireRandomPaperPDF(ctx)
	if err != nil {
		res.Message = "获取随机文献 PDF 失败: " + err.Error()
		return res
	}
	res.PaperID = pdf.PaperID
	res.PaperTitle = truncateText(pdf.Title, 80)
	res.PaperSource = pdf.Source

	promptText, err := prompts.RandomLiteratureReadingPrompt()
	if err != nil {
		res.Message = "加载文献阅读提问语料失败: " + err.Error()
		return res
	}
	res.PromptPreview = truncateText(promptText, 72)

	// Step 1: Upload PDF into Literature Sage Reader folder -> userResourceId.
	userResourceID, err := c.uploadReaderPDF(ctx, pdf.Filename, pdf.Data)
	if err != nil {
		res.Message = fmt.Sprintf("上传文献 PDF (%s) 失败: %v", pdf.Filename, err)
		return res
	}

	// Step 2: Register knowledge_base resource in SciX Reader -> readerEntryId.
	entryID, sourceRev, err := c.registerReaderEntry(ctx, userResourceID)
	if err != nil {
		res.Message = fmt.Sprintf("登记文献阅读条目失败 (resource=%s): %v", userResourceID, err)
		return res
	}

	// Step 3: Trigger document preparation/upgrade (POST .../document) and wait until kind=="ready".
	if err := c.waitForReaderDocumentReady(ctx, entryID, sourceRev); err != nil {
		res.Message = fmt.Sprintf("等待文献解析就绪失败 (entry=%s): %v", entryID, err)
		return res
	}

	// Step 4: Submit PDF reading question & immediately cancel workflow + report query-events.
	queryID := newUUIDv4()
	payload := map[string]any{
		"text":                promptText,
		"readerEntryId":       entryID,
		"queryId":             queryID,
		"invocationRequestId": newUUIDv4(),
		"clientTimeZone":      "Asia/Shanghai",
	}
	chatOut, err := c.submitChatAndCancel(ctx, payload, "agent/chat-with-paper", TaskKeyLiteratureReading, queryID)
	if err != nil {
		res.Message = fmt.Sprintf("文献阅读提问失败 (entry=%s): %v", entryID, err)
		return res
	}
	applyChatOutcome(&res, chatOut)
	res.Success = true
	res.Message = fmt.Sprintf("文献《%s》已上传并提问后立即终止（entry=%s, workflow=%s, reported=%v, cancelled=%v）",
		truncateText(pdf.Title, 32), entryID, nonEmpty(res.WorkflowID, "n/a"), res.QueryReported, res.Cancelled)
	return res
}

func (c *Client) uploadReaderPDF(ctx context.Context, fileName string, data []byte) (string, error) {
	auth := c.store.GetAuth()
	headers := c.wwwHeaders(auth.Token, "agent/chat-with-paper")

	// 1) Ensure "文献阅读" folder (non-fatal fallback to parentId=0 if it fails).
	var folderID int64
	if folderData, err := c.fetchBohriumData(ctx, http.MethodPost,
		c.endpoints.WWWBase+"/bohrapi/v1/literature-sage/api/v1/folder/create",
		map[string]any{
			"scene":      "reader",
			"parentId":   0,
			"folderName": "文献阅读",
		}, headers); err == nil {
		folderID = asInt64(folderData["folderId"])
		if folderID == 0 {
			folderID = asInt64(folderData["id"])
		}
	}

	// 2) Precheck / multipart init.
	sum := md5.Sum(data)
	md5Hex := hex.EncodeToString(sum[:])
	size := len(data)

	q := url.Values{}
	q.Set("scene", "reader")
	q.Set("parentId", strconv.FormatInt(folderID, 10))
	q.Set("fileName", fileName)
	q.Set("md5", md5Hex)
	q.Set("size", strconv.Itoa(size))

	initData, err := c.fetchBohriumData(ctx, http.MethodGet,
		c.endpoints.WWWBase+"/bohrapi/v1/literature-sage/api/v1/file/multipart?"+q.Encode(),
		nil, headers)
	if err != nil {
		return "", fmt.Errorf("file/multipart 预检失败: %w", err)
	}

	// Branch A: File already exists in folder -> reuse sourceUserResourceId directly.
	if asBool(initData["alreadyInFolder"]) {
		if existingID := firstStringField(initData, "userResourceId", "resourceId"); existingID != "" {
			var srcID any = existingID
			if n, err := strconv.ParseInt(existingID, 10, 64); err == nil {
				srcID = n
			}
			submitData, err := c.fetchBohriumData(ctx, http.MethodPost,
				c.endpoints.WWWBase+"/bohrapi/v1/literature-sage/api/v1/file/submit",
				map[string]any{
					"scene":                "reader",
					"parentId":             folderID,
					"sourceUserResourceId": srcID,
				}, headers)
			if err == nil {
				if id := firstStringField(submitData, "userResourceId", "resourceId"); id != "" {
					return id, nil
				}
			}
			return existingID, nil
		}
	}

	// Branch B: Upload raw binary to storage host if !fileExist (exact uploadLibraryFileApi contract).
	fileExist := asBool(initData["fileExist"])
	storagePath := asString(initData["path"])
	if !fileExist {
		host := strings.TrimRight(asString(initData["host"]), "/")
		uploadToken := asString(initData["token"])
		if host == "" || uploadToken == "" || storagePath == "" {
			return "", fmt.Errorf("multipart 预检未返回有效的上传凭证 (host/token/path)")
		}
		paramJSON, _ := json.Marshal(map[string]any{
			"path": storagePath,
			"option": map[string]string{
				"contentDisposition": "inline; filename=" + fileName,
				"contentType":        "application/pdf",
			},
		})
		storageParam := base64.StdEncoding.EncodeToString(paramJSON)
		upResp, err := c.http.DoRaw(ctx, http.MethodPost, host+"/api/upload/binary", data, map[string]string{
			"Content-Type":    "application/json",
			"X-Storage-Param": storageParam,
			"Authorization":   "Bearer " + uploadToken,
			"Origin":          c.endpoints.WWWBase,
			"Referer":         c.endpoints.WWWBase + "/agent/chat-with-paper",
		})
		if err != nil {
			return "", fmt.Errorf("上传二进制 PDF 失败: %w", err)
		}
		if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
			return "", fmt.Errorf("上传二进制 PDF HTTP %d: %s", upResp.StatusCode, truncateText(string(upResp.Body), 180))
		}
	}

	// Step 3: Commit uploaded file via file/submit.
	submitData, err := c.fetchBohriumData(ctx, http.MethodPost,
		c.endpoints.WWWBase+"/bohrapi/v1/literature-sage/api/v1/file/submit",
		map[string]any{
			"scene":    "reader",
			"parentId": folderID,
			"fileName": fileName,
			"md5":      md5Hex,
			"size":     size,
			"mimeType": "pdf",
			"url":      storagePath,
		}, headers)
	if err != nil {
		return "", fmt.Errorf("file/submit 提交失败: %w", err)
	}
	userResourceID := firstStringField(submitData, "userResourceId", "resourceId")
	if userResourceID == "" {
		return "", fmt.Errorf("file/submit 未返回 userResourceId")
	}
	return userResourceID, nil
}

func (c *Client) registerReaderEntry(ctx context.Context, userResourceID string) (string, string, error) {
	auth := c.store.GetAuth()
	u := c.endpoints.SBCloudBase + "/api/v1/scix/reader/entries"
	payload := map[string]any{
		"entries": []map[string]any{
			{
				"source": map[string]any{
					"kind":           "knowledge_base",
					"userResourceId": userResourceID,
				},
			},
		},
	}

	type readerEntryResult struct {
		Code      int    `json:"code"`
		Error     string `json:"error"`
		Retryable *bool  `json:"retryable"`
		Entry     struct {
			ID             string `json:"id"`
			SourceRevision string `json:"sourceRevision"`
		} `json:"entry"`
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		resp, err := c.http.DoJSON(ctx, http.MethodPost, u, payload, c.sbCloudHeaders(auth.Token, "agent/chat-with-paper"))
		if err != nil {
			lastErr = err
		} else if resp.StatusCode == http.StatusConflict || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("reader/entries HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 180))
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", "", fmt.Errorf("reader/entries HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 180))
		} else {
			var body struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
				Data struct {
					Results []readerEntryResult `json:"results"`
				} `json:"data"`
				Results []readerEntryResult `json:"results"`
			}
			if err := json.Unmarshal(resp.Body, &body); err != nil {
				return "", "", fmt.Errorf("解析 reader/entries 响应失败: %w", err)
			}
			if body.Code != 0 {
				return "", "", fmt.Errorf("reader/entries 返回错误 code=%d: %s", body.Code, body.Msg)
			}
			results := body.Data.Results
			if len(results) == 0 {
				results = body.Results
			}
			if len(results) > 0 && results[0].Entry.ID != "" && results[0].Code == 0 {
				return results[0].Entry.ID, results[0].Entry.SourceRevision, nil
			}
			errMsg := truncateText(string(resp.Body), 180)
			retryable := true
			if len(results) > 0 {
				if results[0].Error != "" {
					errMsg = results[0].Error
				}
				if results[0].Retryable != nil && !*results[0].Retryable {
					retryable = false
				}
			}
			lastErr = fmt.Errorf("reader/entries 登记失败: %s", errMsg)
			if !retryable {
				return "", "", lastErr
			}
		}

		if attempt < 3 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(1500 * time.Millisecond):
			}
		}
	}
	return "", "", lastErr
}

// waitForReaderDocumentReady reproduces the official reader-web prepareOnce state machine:
// 1. Call POST /api/v1/scix/reader/entries/<id>/document to kick off parsing/upgrade.
// 2. While kind != "ready": sleep briefly, re-POST if kind is "upgrade_required"/"not_started",
//    otherwise poll GET .../document?status=1 until ready (or failed/timeout).
func (c *Client) waitForReaderDocumentReady(ctx context.Context, entryID, sourceRev string) error {
	auth := c.store.GetAuth()
	headers := c.sbCloudHeaders(auth.Token, "agent/chat-with-paper")
	docURL := fmt.Sprintf("%s/api/v1/scix/reader/entries/%s/document", c.endpoints.SBCloudBase, url.PathEscape(entryID))

	prepareBody := map[string]any{}
	if sourceRev != "" {
		prepareBody["sourceRevision"] = sourceRev
	}

	parseKind := func(raw []byte) string {
		var parsed map[string]any
		if json.Unmarshal(raw, &parsed) != nil {
			return ""
		}
		obj := parsed
		if data := asMap(parsed["data"]); data != nil {
			obj = data
		}
		if k := asString(obj["kind"]); k != "" {
			return k
		}
		return asString(obj["status"])
	}

	// Step 1: Always invoke POST .../document first (triggers parsing / upgrade_required).
	resp, err := c.http.DoJSON(ctx, http.MethodPost, docURL, prepareBody, headers)
	kind := ""
	if err == nil {
		kind = parseKind(resp.Body)
	}

	deadline := time.Now().Add(90 * time.Second)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		switch kind {
		case "ready":
			return nil
		case "failed":
			return fmt.Errorf("文献解析失败 (kind=failed)")
		}

		wait := 2500 * time.Millisecond
		if attempt >= 4 {
			wait = 3500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		if kind == "" || kind == "upgrade_required" || kind == "not_started" {
			resp, err = c.http.DoJSON(ctx, http.MethodPost, docURL, prepareBody, headers)
		} else {
			resp, err = c.http.DoJSON(ctx, http.MethodGet, docURL+"?status=1", nil, headers)
		}
		if err != nil {
			return err
		}
		kind = parseKind(resp.Body)
	}
	return fmt.Errorf("等待文献解析超时（最后状态: %s）", nonEmpty(kind, "unknown"))
}

type chatOutcome struct {
	QueryID       string
	WorkflowID    string
	SessionID     string
	QueryReported bool
	Cancelled     bool
}

func applyChatOutcome(dst *TaskExecutionResult, out *chatOutcome) {
	dst.QueryID = out.QueryID
	dst.WorkflowID = out.WorkflowID
	dst.SessionID = out.SessionID
	dst.QueryReported = out.QueryReported
	dst.Cancelled = out.Cancelled
}

// submitChatAndCancel sends a real chat turn to sb-cloud.dp.tech (with agent.bohrium.com fallback),
// reports the query event for the matching daily task_key, and immediately cancels the running workflow.
func (c *Client) submitChatAndCancel(ctx context.Context, payload map[string]any, refererPath, taskKey, reqQueryID string) (*chatOutcome, error) {
	auth := c.store.GetAuth()
	chatPath := "/api/v1/chat?historyCapabilities=" + url.QueryEscape(HistoryCapabilitiesValue)

	chatBase := c.endpoints.SBCloudBase
	headers := c.sbCloudHeaders(auth.Token, refererPath)
	resp, err := c.http.DoJSON(ctx, http.MethodPost, chatBase+chatPath, payload, headers)

	// Fallback to agent.bohrium.com if sb-cloud is unavailable (5xx/network).
	if err != nil || resp.StatusCode == http.StatusNotFound || resp.StatusCode >= 500 {
		shrimpHeaders := c.sbCloudHeaders(auth.Token, refererPath)
		shrimpHeaders[ShrimpClientSurfaceHeader] = ShrimpClientSurfaceValue
		shrimpHeaders["Idempotency-Key"] = newUUIDv4()
		fbResp, fbErr := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.AgentBase+chatPath, payload, shrimpHeaders)
		if fbErr == nil && fbResp.StatusCode < 500 {
			chatBase = c.endpoints.AgentBase
			headers = shrimpHeaders
			resp = fbResp
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("HTTP 401 登录失效")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chat API HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 220))
	}

	var parsed map[string]any
	_ = json.Unmarshal(resp.Body, &parsed)
	workflowID := firstStringField(parsed, "workflowId", "workflow_id")
	sessionID := firstStringField(parsed, "sessionId", "session_id")
	queryID := firstStringField(parsed, "queryId", "query_id")
	if queryID == "" {
		if workflowID != "" {
			queryID = workflowID
		} else {
			queryID = reqQueryID
		}
	}

	// Report official query-events telemetry (identical to frontend noteWidgetChatResponse -> reportUserQuery).
	reported := c.reportQueryEvent(ctx, auth, queryID, taskKey)

	// Wait brief configured interval (default 450ms) and immediately cancel the workflow to save tokens/photons!
	cancelDelay := c.cfg.Get().CancelAfterMs
	if cancelDelay > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(cancelDelay) * time.Millisecond):
		}
	}

	cancelled := false
	if workflowID != "" {
		cancelURL := fmt.Sprintf("%s/api/v1/chat/%s/cancel", chatBase, url.PathEscape(workflowID))
		cancelResp, cancelErr := c.http.DoJSON(ctx, http.MethodPost, cancelURL, map[string]any{
			"source": "api_request",
		}, headers)
		if cancelErr == nil && cancelResp.StatusCode >= 200 && cancelResp.StatusCode < 300 {
			cancelled = true
		}
	}

	return &chatOutcome{
		QueryID:       queryID,
		WorkflowID:    workflowID,
		SessionID:     sessionID,
		QueryReported: reported,
		Cancelled:     cancelled,
	}, nil
}

func (c *Client) reportQueryEvent(ctx context.Context, auth store.AuthState, queryID, taskKey string) bool {
	if queryID == "" || taskKey == "" {
		return false
	}
	userID := auth.UserID
	orgID := auth.OrgID
	if userID == 0 {
		if claims, err := ParseJWTClaims(auth.Token); err == nil {
			userID = claims.UserID
			orgID = claims.OrgID
		}
	}
	eventKey := fmt.Sprintf("%d:%d:%s", userID, orgID, queryID)
	payload := map[string]any{
		"userId":    userID,
		"orgId":     orgID,
		"queryId":   queryID,
		"queryTime": time.Now().Unix(),
		"eventKey":  eventKey,
		"task_key":  taskKey,
	}
	resp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.SBCloudBase+"/api/v1/query-events", payload, c.sbCloudHeaders(auth.Token, ""))
	return err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch x := v.(type) {
			case string:
				if strings.TrimSpace(x) != "" {
					return strings.TrimSpace(x)
				}
			case float64:
				if x != 0 {
					return strconv.FormatInt(int64(x), 10)
				}
			}
		}
	}
	return ""
}
