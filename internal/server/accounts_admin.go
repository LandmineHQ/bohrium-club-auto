package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/scheduler"
	"bohrium-club-auto/internal/store"
)

// AccountSummaryView is the enriched, token-redacted multi-account payload returned to the Web Console.
type AccountSummaryView struct {
	store.Account
	DisplayName      string                         `json:"displayName"`
	EffectiveBalance float64                        `json:"effectiveBalance"`
	WeightPercent    float64                        `json:"weightPercent"`
	HealthyForPool   bool                           `json:"healthyForPool"`
	ExpiresInSec     int64                          `json:"expiresInSec"`
	ActiveBindings   int                            `json:"activeBindings"`
	Schedule         *scheduler.AccountScheduleInfo `json:"schedule,omitempty"`
}

func (s *Server) buildAccountViews() ([]AccountSummaryView, map[string]any) {
	now := time.Now().UTC()
	accounts, _ := s.store.ListAccounts()
	schedSt := s.scheduler.Status()
	bindCounts, _ := s.store.AccountBindingCounts(now)
	poolViews, poolTotalEff, _ := s.gateway.Router().PoolSummary()

	weightByID := make(map[string]float64, len(poolViews))
	for _, pv := range poolViews {
		weightByID[pv.AccountID] = pv.WeightPercent
	}

	var (
		totalPhotons    float64
		totalOrg        float64
		onlineCount     int
		poolCount       int
		healthyPoolCnt  int
		checkinDoneCnt  int
		checkinEnabled  int
		totalBindings   int
	)

	views := make([]AccountSummaryView, 0, len(accounts))
	for _, acc := range accounts {
		red := acc.Redacted()
		expSec := int64(0)
		if !acc.ExpiresAt.IsZero() {
			expSec = int64(time.Until(acc.ExpiresAt).Seconds())
		}
		online := strings.TrimSpace(acc.Token) != "" && (acc.ExpiresAt.IsZero() || acc.ExpiresAt.After(now)) && acc.HealthStatus != "expired"
		healthy := acc.IsHealthyForPool(now)
		if online {
			onlineCount++
		}
		if acc.PoolEnabled {
			poolCount++
			if healthy {
				healthyPoolCnt++
			}
		}
		if acc.CheckinEnabled {
			checkinEnabled++
		}
		totalPhotons += acc.PhotonBalance
		totalOrg += acc.OrgBalance
		bc := bindCounts[acc.ID]
		totalBindings += bc

		var schedPtr *scheduler.AccountScheduleInfo
		if sc, ok := schedSt.Accounts[acc.ID]; ok {
			cp := sc
			schedPtr = &cp
			if sc.DoneToday {
				checkinDoneCnt++
			}
		}

		views = append(views, AccountSummaryView{
			Account:          red,
			DisplayName:      acc.DisplayName(),
			EffectiveBalance: acc.EffectiveBalance(),
			WeightPercent:    weightByID[acc.ID],
			HealthyForPool:   healthy,
			ExpiresInSec:     expSec,
			ActiveBindings:   bc,
			Schedule:         schedPtr,
		})
	}

	summary := map[string]any{
		"totalAccounts":        len(accounts),
		"onlineAccounts":       onlineCount,
		"poolAccounts":         poolCount,
		"healthyPoolAccounts":  healthyPoolCnt,
		"totalPhotons":         totalPhotons,
		"totalOrgBalance":      totalOrg,
		"poolEffectivePhotons": poolTotalEff,
		"checkinDoneToday":     checkinDoneCnt,
		"checkinEnabledCount":  checkinEnabled,
		"activeBindings":       totalBindings,
	}
	return views, summary
}

func (s *Server) resolveTargetAccountID(r *http.Request, bodyAccountID string) string {
	if id := strings.TrimSpace(r.PathValue("id")); id != "" {
		return id
	}
	if id := strings.TrimSpace(bodyAccountID); id != "" {
		return id
	}
	if id := strings.TrimSpace(r.URL.Query().Get("accountId")); id != "" {
		return id
	}
	accs, err := s.store.ListAccounts()
	if err == nil && len(accs) > 0 {
		return accs[0].ID
	}
	return ""
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	views, summary := s.buildAccountViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"accounts":  views,
		"summary":   summary,
		"scheduler": s.scheduler.Status(),
	})
}

func (s *Server) handleAccountDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	acc, ok, err := s.store.GetAccount(id)
	if err != nil || !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "账号不存在"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	cli := s.mgr.ForAccount(acc.ID)
	session := cli.CheckSession(ctx)
	var (
		tasksResp  *bohrium.DailyTasksResponse
		tasksErr   string
		assetsResp *bohrium.PointsAndCapacity
		assetsErr  string
	)
	if session.HasToken {
		if t, tErr := cli.ListDailyTasks(ctx); tErr != nil {
			tasksErr = tErr.Error()
		} else {
			tasksResp = t
		}
		if a, aErr := s.mgr.RefreshAccountBalance(ctx, acc.ID); aErr != nil {
			assetsErr = aErr.Error()
		} else {
			assetsResp = a
		}
	}
	freshAcc, _, _ := s.store.GetAccount(acc.ID)
	if freshAcc == nil {
		freshAcc = acc
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"account":     freshAcc.Redacted(),
		"session":     session,
		"tasks":       tasksResp,
		"tasksError":  tasksErr,
		"assets":      assetsResp,
		"assetsError": assetsErr,
		"logs":        s.store.GetLogsFiltered(acc.ID, 40),
	})
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Label          *string `json:"label"`
		PoolEnabled    *bool   `json:"poolEnabled"`
		PoolOrder      *int    `json:"poolOrder"`
		CheckinEnabled *bool   `json:"checkinEnabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	updated, err := s.store.UpdateAccountSettings(id, body.Label, body.PoolEnabled, body.PoolOrder, body.CheckinEnabled)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// If newly enabled in pool and balance hasn't been fetched yet, refresh in background.
	if body.PoolEnabled != nil && *body.PoolEnabled && updated.BalanceUpdatedAt.IsZero() {
		go func(accID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _ = s.mgr.RefreshAccountBalance(ctx, accID)
		}(updated.ID)
	}
	views, summary := s.buildAccountViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"account":  updated.Redacted(),
		"accounts": views,
		"summary":  summary,
	})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	acc, _, _ := s.store.GetAccount(id)
	if err := s.mgr.DeleteAccount(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	label := id
	if acc != nil {
		label = acc.DisplayName()
	}
	_ = s.store.AppendLog(store.RunLog{
		Trigger: "manual",
		Action:  "logout",
		Success: true,
		Message: "已删除账号并清理其路由绑定: " + label,
	})
	views, summary := s.buildAccountViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"accounts": views,
		"summary":  summary,
	})
}

func (s *Server) handleAccountRefreshToken(w http.ResponseWriter, r *http.Request) {
	id := s.resolveTargetAccountID(r, "")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "未指定账号"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cli := s.mgr.ForAccount(id)
	if _, err := cli.RefreshToken(ctx, "manual"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_, _, _ = cli.SyncProfile(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": cli.CheckSession(ctx),
	})
}

func (s *Server) handleAccountRefreshBalance(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	assets, err := s.mgr.RefreshAccountBalance(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	views, summary := s.buildAccountViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"assets":   assets,
		"accounts": views,
		"summary":  summary,
	})
}

func (s *Server) handleRefreshAllBalances(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	accounts, _ := s.store.ListAccounts()
	refreshed := 0
	var errs []string
	for _, acc := range accounts {
		if strings.TrimSpace(acc.Token) == "" {
			continue
		}
		if _, err := s.mgr.RefreshAccountBalance(ctx, acc.ID); err != nil {
			errs = append(errs, acc.DisplayName()+": "+err.Error())
		} else {
			refreshed++
		}
	}
	views, summary := s.buildAccountViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"refreshed": refreshed,
		"errors":    errs,
		"accounts":  views,
		"summary":   summary,
	})
}

func (s *Server) handleRunAllAccounts(w http.ResponseWriter, r *http.Request) {
	results, err := s.scheduler.RunAllAccountsManual(r.Context())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "results": results})
		return
	}
	views, summary := s.buildAccountViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"results":  results,
		"accounts": views,
		"summary":  summary,
	})
}

func (s *Server) handleAccountClaimAll(w http.ResponseWriter, r *http.Request) {
	id := s.resolveTargetAccountID(r, "")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "尚未登录任何账号"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := s.mgr.ForAccount(id).ClaimAllTasks(ctx, "manual")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleAccountClaimOne(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID string `json:"accountId"`
		TaskKey   string `json:"taskKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.TaskKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "taskKey 不能为空"})
		return
	}
	id := s.resolveTargetAccountID(r, body.AccountID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "尚未登录任何账号"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := s.mgr.ForAccount(id).ClaimTask(ctx, body.TaskKey, "manual")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleAccountRunOne(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID string `json:"accountId"`
		TaskKey   string `json:"taskKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.TaskKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "taskKey 不能为空"})
		return
	}
	id := s.resolveTargetAccountID(r, body.AccountID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "尚未登录任何账号"})
		return
	}
	ctx := r.Context()
	res, err := s.scheduler.RunAccountSingleTaskManual(ctx, id, body.TaskKey)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	after, _ := s.mgr.ForAccount(id).ListDailyTasks(ctx)
	respBody := map[string]any{
		"ok":         res.Success,
		"data":       res,
		"tasksAfter": after,
	}
	if !res.Success {
		respBody["error"] = res.Message
	}
	writeJSON(w, http.StatusOK, respBody)
}

func (s *Server) handleAccountRunAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID string `json:"accountId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := s.resolveTargetAccountID(r, body.AccountID)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "尚未登录任何账号"})
		return
	}
	res, err := s.scheduler.RunAccountManual(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) finalizeLoginResponse(ctx context.Context, w http.ResponseWriter, scope *store.LoginScope) {
	acc, isNew := scope.Result()
	if acc != nil {
		// Fetch initial photon balance quickly so newly added account displays balance right away.
		balCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, _ = s.mgr.RefreshAccountBalance(balCtx, acc.ID)
		cancel()
		if fresh, ok, _ := s.store.GetAccount(acc.ID); ok && fresh != nil {
			acc = fresh
		}
	}
	var session bohrium.SessionStatus
	var redAcc *store.Account
	if acc != nil {
		session = s.mgr.ForAccount(acc.ID).CheckSession(ctx)
		cp := acc.Redacted()
		redAcc = &cp
	}
	views, summary := s.buildAccountViews()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"isNew":    isNew,
		"account":  redAcc,
		"session":  session,
		"accounts": views,
		"summary":  summary,
	})
}
