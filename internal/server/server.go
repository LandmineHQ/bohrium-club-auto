package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/openai"
	"bohrium-club-auto/internal/prompts"
	"bohrium-club-auto/internal/recon"
	"bohrium-club-auto/internal/scheduler"
	"bohrium-club-auto/internal/store"
	"bohrium-club-auto/web"
)

// Server exposes the admin-protected Multi-Account Web Console, REST API, and OpenAI /v1/* gateway.
type Server struct {
	version   string
	cfg       *config.Manager
	store     *store.Store
	httpCli   *chromehttp.Client
	mgr       *bohrium.Manager
	scheduler *scheduler.Scheduler
	gateway   *openai.Gateway
	limiter   *loginRateLimiter
	mux       *http.ServeMux
	startedAt time.Time
}

// New builds the HTTP server and registers all routes.
func New(version string, cfg *config.Manager, st *store.Store, httpCli *chromehttp.Client, mgr *bohrium.Manager, sched *scheduler.Scheduler) *Server {
	s := &Server{
		version:   version,
		cfg:       cfg,
		store:     st,
		httpCli:   httpCli,
		mgr:       mgr,
		scheduler: sched,
		gateway:   openai.NewGateway(cfg, mgr),
		limiter:   newLoginRateLimiter(),
		mux:       http.NewServeMux(),
		startedAt: time.Now().UTC(),
	}
	s.routes()
	return s
}

// Gateway returns the embedded OpenAI-compatible gateway handler.
func (s *Server) Gateway() *openai.Gateway {
	return s.gateway
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	// Public health check & admin auth endpoints
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)

	// Overview & multi-account management endpoints
	s.mux.HandleFunc("GET /api/overview", s.requireAdmin(s.handleOverview))
	s.mux.HandleFunc("GET /api/accounts", s.requireAdmin(s.handleListAccounts))
	s.mux.HandleFunc("POST /api/accounts/refresh-balances", s.requireAdmin(s.handleRefreshAllBalances))
	s.mux.HandleFunc("POST /api/accounts/run-all", s.requireAdmin(s.handleRunAllAccounts))
	s.mux.HandleFunc("GET /api/accounts/{id}/detail", s.requireAdmin(s.handleAccountDetail))
	s.mux.HandleFunc("PATCH /api/accounts/{id}", s.requireAdmin(s.handleUpdateAccount))
	s.mux.HandleFunc("DELETE /api/accounts/{id}", s.requireAdmin(s.handleDeleteAccount))
	s.mux.HandleFunc("POST /api/accounts/{id}/refresh-token", s.requireAdmin(s.handleAccountRefreshToken))
	s.mux.HandleFunc("POST /api/accounts/{id}/refresh-balance", s.requireAdmin(s.handleAccountRefreshBalance))
	s.mux.HandleFunc("POST /api/accounts/{id}/tasks/claim-all", s.requireAdmin(s.handleAccountClaimAll))
	s.mux.HandleFunc("POST /api/accounts/{id}/tasks/claim", s.requireAdmin(s.handleAccountClaimOne))
	s.mux.HandleFunc("POST /api/accounts/{id}/tasks/run", s.requireAdmin(s.handleAccountRunOne))
	s.mux.HandleFunc("POST /api/accounts/{id}/tasks/run-all", s.requireAdmin(s.handleAccountRunAll))

	// Bohrium login endpoints (add new account or refresh existing account by UID; new accounts default to PoolEnabled=false)
	s.mux.HandleFunc("GET /api/bohrium/session", s.requireAdmin(s.handleBohriumSession))
	s.mux.HandleFunc("POST /api/bohrium/wechat/qr", s.requireAdmin(s.handleWeChatQR))
	s.mux.HandleFunc("GET /api/bohrium/wechat/poll", s.requireAdmin(s.handleWeChatPoll))
	s.mux.HandleFunc("POST /api/bohrium/oauth/url", s.requireAdmin(s.handleOAuthURL))
	s.mux.HandleFunc("POST /api/bohrium/oauth/verify", s.requireAdmin(s.handleOAuthVerify))
	s.mux.HandleFunc("POST /api/bohrium/captcha/prepare", s.requireAdmin(s.handlePrepareCaptcha))
	s.mux.HandleFunc("POST /api/bohrium/code/send", s.requireAdmin(s.handleSendCode))
	s.mux.HandleFunc("POST /api/bohrium/login/code", s.requireAdmin(s.handleCodeLogin))
	s.mux.HandleFunc("POST /api/bohrium/login/token", s.requireAdmin(s.handleImportToken))
	s.mux.HandleFunc("POST /api/bohrium/login/password", s.requireAdmin(s.handlePasswordLogin))
	s.mux.HandleFunc("POST /api/bohrium/refresh-token", s.requireAdmin(s.handleAccountRefreshToken))
	s.mux.HandleFunc("POST /api/bohrium/logout", s.requireAdmin(s.handleBohriumLogout))

	// Backward-compatible task/asset endpoints (accept ?accountId=... or body.accountId, defaulting to first account)
	s.mux.HandleFunc("GET /api/tasks", s.requireAdmin(s.handleGetTasks))
	s.mux.HandleFunc("GET /api/tasks/jobs", s.requireAdmin(s.handleListTaskJobs))
	s.mux.HandleFunc("GET /api/tasks/jobs/{id}", s.requireAdmin(s.handleGetTaskJob))
	s.mux.HandleFunc("POST /api/tasks/claim-all", s.requireAdmin(s.handleAccountClaimAll))
	s.mux.HandleFunc("POST /api/tasks/claim", s.requireAdmin(s.handleAccountClaimOne))
	s.mux.HandleFunc("POST /api/tasks/run", s.requireAdmin(s.handleAccountRunOne))
	s.mux.HandleFunc("POST /api/tasks/run-all", s.requireAdmin(s.handleAccountRunAll))

	s.mux.HandleFunc("GET /api/assets", s.requireAdmin(s.handleGetAssets))
	s.mux.HandleFunc("GET /api/logs", s.requireAdmin(s.handleGetLogs))
	s.mux.HandleFunc("GET /api/settings", s.requireAdmin(s.handleGetSettings))
	s.mux.HandleFunc("PUT /api/settings", s.requireAdmin(s.handleUpdateSettings))
	s.mux.HandleFunc("GET /api/prompts/catalog", s.requireAdmin(s.handlePromptCatalog))

	s.mux.HandleFunc("GET /api/recon/status", s.requireAdmin(s.handleReconStatus))
	s.mux.HandleFunc("POST /api/recon/pull", s.requireAdmin(s.handleReconPull))
	s.mux.HandleFunc("POST /api/recon/diff", s.requireAdmin(s.handleReconDiff))

	// Admin OpenAI Gateway, Routing Pool, Analytics & 7-Day Session Binding endpoints
	s.mux.HandleFunc("GET /api/gateway/status", s.requireAdmin(s.handleGatewayStatus))
	s.mux.HandleFunc("GET /api/gateway/analytics", s.requireAdmin(s.handleGatewayAnalytics))
	s.mux.HandleFunc("GET /api/gateway/analytics/logs", s.requireAdmin(s.handleGatewayAnalyticsLogs))
	s.mux.HandleFunc("POST /api/gateway/analytics/purge", s.requireAdmin(s.handleGatewayPurgeAnalytics))
	s.mux.HandleFunc("PUT /api/gateway/pool", s.requireAdmin(s.handleGatewaySetPool))
	s.mux.HandleFunc("GET /api/gateway/bindings", s.requireAdmin(s.handleGatewayListBindings))
	s.mux.HandleFunc("DELETE /api/gateway/bindings/{hash}", s.requireAdmin(s.handleGatewayDeleteBinding))
	s.mux.HandleFunc("DELETE /api/gateway/bindings", s.requireAdmin(s.handleGatewayClearBindings))
	s.mux.HandleFunc("POST /api/gateway/models/refresh", s.requireAdmin(s.handleGatewayRefreshModels))
	s.mux.HandleFunc("PATCH /api/gateway/models/{id...}", s.requireAdmin(s.handleGatewayUpdateModel))
	s.mux.HandleFunc("POST /api/gateway/keys", s.requireAdmin(s.handleGatewayCreateKey))
	s.mux.HandleFunc("PATCH /api/gateway/keys/{id}", s.requireAdmin(s.handleGatewayUpdateKey))
	s.mux.HandleFunc("DELETE /api/gateway/keys/{id}", s.requireAdmin(s.handleGatewayDeleteKey))
	s.mux.HandleFunc("PUT /api/gateway/settings", s.requireAdmin(s.handleGatewayUpdateSettings))
	s.mux.HandleFunc("POST /api/gateway/preflight", s.requireAdmin(s.handleGatewayPreflight))
	s.mux.HandleFunc("POST /api/gateway/playground", s.requireAdmin(s.handleGatewayPlayground))

	// OpenAI-compatible /v1/* endpoints (authenticated via Gateway API key)
	s.gateway.RegisterRoutes(s.mux)

	// Embedded Vite/React Web Dashboard SPA (web/dist)
	s.mux.Handle("GET /", spaHandler(web.FS))
}

func spaHandler(embedded fs.FS) http.Handler {
	distFS, err := fs.Sub(embedded, "dist")
	if err != nil {
		return unbuiltSPAHandler()
	}
	indexBytes, err := fs.ReadFile(distFS, "index.html")
	if err != nil || len(indexBytes) == 0 {
		return unbuiltSPAHandler()
	}
	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := strings.TrimPrefix(r.URL.Path, "/")
		if reqPath != "" {
			if f, err := distFS.Open(reqPath); err == nil {
				stat, statErr := f.Stat()
				_ = f.Close()
				if statErr == nil && !stat.IsDir() {
					if strings.HasPrefix(reqPath, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	})
}

func unbuiltSPAHandler() http.Handler {
	const fallbackHTML = `<!doctype html><html lang="zh-CN"><head><meta charset="UTF-8" /><title>Bohrium Club Auto</title></head><body style="font-family:system-ui,sans-serif;padding:32px;max-width:640px;margin:0 auto;"><h1>Bohrium Club Auto</h1><p>前端尚未构建，请先在项目根目录执行 <code>npm ci --prefix web &amp;&amp; npm run build --prefix web</code> 后重新编译 Go 二进制。</p></body></html>`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fallbackHTML))
	})
}

func (s *Server) isAdminAuthenticated(r *http.Request) bool {
	cfg := s.cfg.Get()
	if hdr := strings.TrimSpace(r.Header.Get("X-Admin-Password")); hdr != "" {
		return verifyPasswordConstantTime(hdr, cfg.AdminPassword)
	}
	c, err := r.Cookie(adminCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return verifyAdminSession(c.Value, cfg.SessionSecret, cfg.AdminPassword)
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAdminAuthenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "需要管理员密码登录",
			})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	allAccs, _ := s.store.ListAccounts()
	poolAccs, _ := s.store.ListPoolAccounts()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"service":          "bohrium-club-auto",
		"version":          s.version,
		"uptimeSec":        int64(time.Since(s.startedAt).Seconds()),
		"bohriumLoggedIn":  len(allAccs) > 0,
		"accountCount":     len(allAccs),
		"poolAccountCount": len(poolAccs),
		"userAgent":        s.httpCli.UserAgent(),
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"authenticated": s.isAdminAuthenticated(r),
		"version":       s.version,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"ok":    false,
			"error": "尝试次数过多，请 5 分钟后再试",
		})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	cfg := s.cfg.Get()
	if !verifyPasswordConstantTime(body.Password, cfg.AdminPassword) {
		s.limiter.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":    false,
			"error": "管理员密码错误",
		})
		return
	}
	s.limiter.reset(ip)
	exp := time.Now().Add(7 * 24 * time.Hour)
	token := signAdminSession(cfg.SessionSecret, passwordFingerprint(cfg.AdminPassword), exp)
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
		MaxAge:   7 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "authenticated": true})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	views, summary := s.buildAccountViews()
	selectedID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if selectedID == "" && len(views) > 0 {
		selectedID = views[0].ID
	}

	var (
		session    bohrium.SessionStatus
		tasksResp  *bohrium.DailyTasksResponse
		tasksErr   string
		assetsResp *bohrium.PointsAndCapacity
		assetsErr  string
	)
	if selectedID != "" {
		cli := s.mgr.ForAccount(selectedID)
		session = cli.CheckSession(ctx)
		if session.HasToken {
			if t, err := cli.ListDailyTasks(ctx); err != nil {
				tasksErr = err.Error()
			} else {
				tasksResp = t
			}
			if a, err := s.mgr.RefreshAccountBalance(ctx, selectedID); err != nil {
				assetsErr = err.Error()
			} else {
				assetsResp = a
			}
		}
		// Always recompute account views so any nickname/profile/balance sync from CheckSession or RefreshAccountBalance is reflected immediately.
		views, summary = s.buildAccountViews()
	}

	cachedPDFs, customPDFs := s.mgr.Ephemeral().CountCachedPDFs()
	cfg := s.cfg.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                true,
		"version":           s.version,
		"userAgent":         s.httpCli.UserAgent(),
		"selectedAccountId": selectedID,
		"accounts":          views,
		"accountSummary":    summary,
		"session":           session,
		"tasks":             tasksResp,
		"tasksError":        tasksErr,
		"assets":            assetsResp,
		"assetsError":       assetsErr,
		"scheduler":         s.scheduler.Status(),
		"activeJobs":        s.scheduler.ActiveTaskJobs(),
		"promptStats":       prompts.GetStats(),
		"pdfStats": map[string]int{
			"cached": cachedPDFs,
			"custom": customPDFs,
		},
		"reconDir": cfg.ReconDir,
		"logs":     s.store.GetLogs(40),
	})
}

func (s *Server) handleBohriumSession(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id := s.resolveTargetAccountID(r, "")
	var session bohrium.SessionStatus
	if id != "" {
		session = s.mgr.ForAccount(id).CheckSession(ctx)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"accountId": id,
		"session":   session,
	})
}

func (s *Server) handleWeChatQR(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	qr, err := s.mgr.Ephemeral().StartWeChatQR(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": qr})
}

func (s *Server) handleWeChatPoll(w http.ResponseWriter, r *http.Request) {
	sceneID := r.URL.Query().Get("sceneId")
	label := r.URL.Query().Get("label")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cli, scope := s.mgr.ForLogin(label)
	res, err := cli.PollWeChatQR(ctx, sceneID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	var addedAcc *store.Account
	var isNew bool
	if res.LoggedIn || res.Status == "confirmed" {
		if acc, created := scope.Result(); acc != nil {
			balCtx, bCancel := context.WithTimeout(ctx, 8*time.Second)
			_, _ = s.mgr.RefreshAccountBalance(balCtx, acc.ID)
			bCancel()
			if fresh, ok, _ := s.store.GetAccount(acc.ID); ok && fresh != nil {
				cp := fresh.Redacted()
				addedAcc = &cp
			} else {
				cp := acc.Redacted()
				addedAcc = &cp
			}
			isNew = created
		}
		if res.Auth != nil {
			redacted := cli.RedactedAuth()
			res.Auth = &redacted
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"data":    res,
		"account": addedAcc,
		"isNew":   isNew,
	})
}

func (s *Server) handleOAuthURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := s.mgr.Ephemeral().GetOAuthURL(ctx, body.Provider)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleOAuthVerify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider      string `json:"provider"`
		CallbackInput string `json:"callbackInput"`
		Label         string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cli, scope := s.mgr.ForLogin(body.Label)
	if _, err := cli.VerifyOAuthCallback(ctx, body.Provider, body.CallbackInput); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.finalizeLoginResponse(ctx, w, scope)
}

func (s *Server) handlePrepareCaptcha(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scene   string `json:"scene"`   // "otp" | "password"
		Account string `json:"account"` // optional, used by password scene for /api/captcha/check_user
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := s.mgr.Ephemeral().PrepareCaptcha(ctx, body.Scene, body.Account)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleSendCode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind         string `json:"kind"`
		Target       string `json:"target"`
		CountryCode  string `json:"countryCode"`
		IsOversea    bool   `json:"isOversea"`
		Ticket       string `json:"ticket"`
		RandStr      string `json:"randStr"`
		CaptchaAppID int64  `json:"captchaAppId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var proof *bohrium.CaptchaProof
	if strings.TrimSpace(body.Ticket) != "" && strings.TrimSpace(body.RandStr) != "" {
		proof = &bohrium.CaptchaProof{
			Ticket:       body.Ticket,
			RandStr:      body.RandStr,
			CaptchaAppID: body.CaptchaAppID,
		}
	}
	if err := s.mgr.Ephemeral().SendVerifyCode(ctx, body.Kind, body.Target, body.CountryCode, body.IsOversea, proof); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "验证码已发送"})
}

func (s *Server) handleCodeLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind        string `json:"kind"`
		Target      string `json:"target"`
		CountryCode string `json:"countryCode"`
		IsOversea   bool   `json:"isOversea"`
		Code        string `json:"code"`
		Label       string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cli, scope := s.mgr.ForLogin(body.Label)
	if _, err := cli.LoginWithCode(ctx, body.Kind, body.Target, body.CountryCode, body.Code, body.IsOversea); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.finalizeLoginResponse(ctx, w, scope)
}

func (s *Server) handleImportToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cli, scope := s.mgr.ForLogin(body.Label)
	if _, err := cli.ImportToken(ctx, body.Token); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.finalizeLoginResponse(ctx, w, scope)
}

func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Account      string `json:"account"`
		Password     string `json:"password"`
		Label        string `json:"label"`
		Ticket       string `json:"ticket"`
		RandStr      string `json:"randStr"`
		CaptchaAppID int64  `json:"captchaAppId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var proof *bohrium.CaptchaProof
	if strings.TrimSpace(body.Ticket) != "" && strings.TrimSpace(body.RandStr) != "" {
		proof = &bohrium.CaptchaProof{
			Ticket:       body.Ticket,
			RandStr:      body.RandStr,
			CaptchaAppID: body.CaptchaAppID,
		}
	}
	cli, scope := s.mgr.ForLogin(body.Label)
	if _, err := cli.LoginWithPassword(ctx, body.Account, body.Password, proof); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.finalizeLoginResponse(ctx, w, scope)
}

func (s *Server) handleBohriumLogout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AccountID string `json:"accountId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id := s.resolveTargetAccountID(r, body.AccountID)
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := s.mgr.DeleteAccount(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = s.store.AppendLog(store.RunLog{
		Trigger: "manual",
		Action:  "logout",
		Success: true,
		Message: "已清除账号登录凭据: " + id,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetTasks(w http.ResponseWriter, r *http.Request) {
	id := s.resolveTargetAccountID(r, "")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "尚未登录任何账号"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tasks, err := s.mgr.ForAccount(id).ListDailyTasks(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accountId": id, "data": tasks})
}

func (s *Server) handleGetAssets(w http.ResponseWriter, r *http.Request) {
	id := s.resolveTargetAccountID(r, "")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "尚未登录任何账号"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	assets, err := s.mgr.RefreshAccountBalance(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accountId": id, "data": assets})
}

func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	limit := 80
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	accID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"data": s.store.GetLogsFiltered(accID, limit),
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"data": map[string]any{
			"userAgent":     cfg.UserAgent,
			"proxyUrl":      cfg.ProxyURL,
			"schedule":      cfg.Schedule,
			"taskDelaySec":  cfg.TaskDelaySec,
			"cancelAfterMs": cfg.CancelAfterMs,
			"reconDir":      cfg.ReconDir,
		},
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserAgent        *string                `json:"userAgent"`
		ProxyURL         *string                `json:"proxyUrl"`
		TaskDelaySec     *int                   `json:"taskDelaySec"`
		CancelAfterMs    *int                   `json:"cancelAfterMs"`
		Schedule         *config.ScheduleConfig `json:"schedule"`
		NewAdminPassword string                 `json:"newAdminPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}

	pwChanged := false
	updated, err := s.cfg.Update(func(c *config.Config) error {
		if body.UserAgent != nil && strings.TrimSpace(*body.UserAgent) != "" {
			c.UserAgent = strings.TrimSpace(*body.UserAgent)
		}
		if body.ProxyURL != nil {
			c.ProxyURL = strings.TrimSpace(*body.ProxyURL)
		}
		if body.TaskDelaySec != nil && *body.TaskDelaySec >= 0 && *body.TaskDelaySec <= 60 {
			c.TaskDelaySec = *body.TaskDelaySec
		}
		if body.CancelAfterMs != nil && *body.CancelAfterMs >= 100 && *body.CancelAfterMs <= 10000 {
			c.CancelAfterMs = *body.CancelAfterMs
		}
		if body.Schedule != nil {
			if body.Schedule.TimeHHMM != "" && !config.ValidHHMM(body.Schedule.TimeHHMM) {
				return &badInputError{"定时时间格式需为 HH:MM（例如 08:15）"}
			}
			c.Schedule.Enabled = body.Schedule.Enabled
			if body.Schedule.TimeHHMM != "" {
				c.Schedule.TimeHHMM = body.Schedule.TimeHHMM
			}
			if body.Schedule.Timezone != "" {
				c.Schedule.Timezone = body.Schedule.Timezone
			}
			if body.Schedule.JitterMinutes >= 0 && body.Schedule.JitterMinutes <= 720 {
				c.Schedule.JitterMinutes = body.Schedule.JitterMinutes
			}
			c.Schedule.AutoClaim = body.Schedule.AutoClaim
			c.Schedule.AutoRefresh = body.Schedule.AutoRefresh
		}
		if strings.TrimSpace(body.NewAdminPassword) != "" {
			if len(body.NewAdminPassword) < 6 {
				return &badInputError{"新管理员密码至少需要 6 个字符"}
			}
			c.AdminPassword = strings.TrimSpace(body.NewAdminPassword)
			pwChanged = true
		}
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	if pwChanged {
		exp := time.Now().Add(7 * 24 * time.Hour)
		tok := signAdminSession(updated.SessionSecret, passwordFingerprint(updated.AdminPassword), exp)
		http.SetCookie(w, &http.Cookie{
			Name:     adminCookieName,
			Value:    tok,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Expires:  exp,
			MaxAge:   7 * 24 * 3600,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"scheduler": s.scheduler.Status(),
		"message":   "配置已保存（修改代理或 UA 需重启服务生效）",
	})
}

func (s *Server) handlePromptCatalog(w http.ResponseWriter, r *http.Request) {
	papers, _ := prompts.Papers()
	cached, custom := s.mgr.Ephemeral().CountCachedPDFs()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"stats":  prompts.GetStats(),
		"cached": cached,
		"custom": custom,
		"papers": papers,
	})
}

func (s *Server) handleReconStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	opts := recon.NormalizeOptions(recon.Options{OutDir: cfg.ReconDir})
	m, err := recon.LoadManifest(opts.ManifestPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"baselineExists": false,
			"manifestPath":   opts.ManifestPath,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"baselineExists": true,
		"manifestPath":   opts.ManifestPath,
		"fetchedAt":      m.FetchedAt,
		"assetCount":     len(m.Assets),
		"totalBytes":     m.TotalBytes,
		"targets":        m.Targets,
		"markers":        m.Markers,
	})
}

func (s *Server) handleReconPull(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	manifest, err := recon.TakeSnapshot(ctx, recon.Options{
		OutDir:    cfg.ReconDir,
		UserAgent: cfg.UserAgent,
		ProxyURL:  cfg.ProxyURL,
		MaxAssets: 120,
		DelayMs:   60,
		Tolerant:  true,
	}, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = s.store.AppendLog(store.RunLog{
		Trigger:    "manual",
		Action:     "recon",
		Success:    true,
		DurationMs: time.Since(start).Milliseconds(),
		Message:    "已拉取 Bohrium 前端基线快照（资源数: " + strconv.Itoa(len(manifest.Assets)) + "）",
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": manifest})
}

func (s *Server) handleReconDiff(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	diff, err := recon.CompareLive(ctx, recon.Options{
		OutDir:    cfg.ReconDir,
		UserAgent: cfg.UserAgent,
		ProxyURL:  cfg.ProxyURL,
		MaxAssets: 120,
		DelayMs:   60,
		Tolerant:  true,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = s.store.AppendLog(store.RunLog{
		Trigger:    "manual",
		Action:     "recon",
		Success:    true,
		DurationMs: time.Since(start).Milliseconds(),
		Message:    diff.Summary,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": diff})
}

type badInputError struct{ msg string }

func (e *badInputError) Error() string { return e.msg }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
