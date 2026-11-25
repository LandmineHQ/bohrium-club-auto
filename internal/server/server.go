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
	"bohrium-club-auto/internal/prompts"
	"bohrium-club-auto/internal/recon"
	"bohrium-club-auto/internal/scheduler"
	"bohrium-club-auto/internal/store"
	"bohrium-club-auto/web"
)

// Server exposes the admin-protected Web Dashboard and REST API.
type Server struct {
	version   string
	cfg       *config.Manager
	store     *store.Store
	httpCli   *chromehttp.Client
	client    *bohrium.Client
	scheduler *scheduler.Scheduler
	limiter   *loginRateLimiter
	mux       *http.ServeMux
	startedAt time.Time
}

// New builds the HTTP server and registers all routes.
func New(version string, cfg *config.Manager, st *store.Store, httpCli *chromehttp.Client, client *bohrium.Client, sched *scheduler.Scheduler) *Server {
	s := &Server{
		version:   version,
		cfg:       cfg,
		store:     st,
		httpCli:   httpCli,
		client:    client,
		scheduler: sched,
		limiter:   newLoginRateLimiter(),
		mux:       http.NewServeMux(),
		startedAt: time.Now().UTC(),
	}
	s.routes()
	return s
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

	// Protected admin operational endpoints
	s.mux.HandleFunc("GET /api/overview", s.requireAdmin(s.handleOverview))
	s.mux.HandleFunc("GET /api/bohrium/session", s.requireAdmin(s.handleBohriumSession))
	s.mux.HandleFunc("POST /api/bohrium/wechat/qr", s.requireAdmin(s.handleWeChatQR))
	s.mux.HandleFunc("GET /api/bohrium/wechat/poll", s.requireAdmin(s.handleWeChatPoll))
	s.mux.HandleFunc("POST /api/bohrium/login/token", s.requireAdmin(s.handleImportToken))
	s.mux.HandleFunc("POST /api/bohrium/login/password", s.requireAdmin(s.handlePasswordLogin))
	s.mux.HandleFunc("POST /api/bohrium/refresh-token", s.requireAdmin(s.handleRefreshToken))
	s.mux.HandleFunc("POST /api/bohrium/logout", s.requireAdmin(s.handleBohriumLogout))

	s.mux.HandleFunc("GET /api/tasks", s.requireAdmin(s.handleGetTasks))
	s.mux.HandleFunc("POST /api/tasks/claim-all", s.requireAdmin(s.handleClaimAll))
	s.mux.HandleFunc("POST /api/tasks/claim", s.requireAdmin(s.handleClaimOne))
	s.mux.HandleFunc("POST /api/tasks/run", s.requireAdmin(s.handleRunOne))
	s.mux.HandleFunc("POST /api/tasks/run-all", s.requireAdmin(s.handleRunAll))

	s.mux.HandleFunc("GET /api/assets", s.requireAdmin(s.handleGetAssets))
	s.mux.HandleFunc("GET /api/logs", s.requireAdmin(s.handleGetLogs))
	s.mux.HandleFunc("GET /api/settings", s.requireAdmin(s.handleGetSettings))
	s.mux.HandleFunc("PUT /api/settings", s.requireAdmin(s.handleUpdateSettings))
	s.mux.HandleFunc("GET /api/prompts/catalog", s.requireAdmin(s.handlePromptCatalog))

	s.mux.HandleFunc("GET /api/recon/status", s.requireAdmin(s.handleReconStatus))
	s.mux.HandleFunc("POST /api/recon/pull", s.requireAdmin(s.handleReconPull))
	s.mux.HandleFunc("POST /api/recon/diff", s.requireAdmin(s.handleReconDiff))

	// Embedded Web Dashboard SPA
	staticSub, err := fs.Sub(web.FS, "static")
	if err == nil {
		fileServer := http.FileServer(http.FS(staticSub))
		s.mux.Handle("GET /", fileServer)
	}
}

func (s *Server) isAdminAuthenticated(r *http.Request) bool {
	cfg := s.cfg.Get()
	// Allow X-Admin-Password header for automation/CI scripts.
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
	auth := s.store.GetAuth()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"service":        "bohrium-club-auto",
		"version":        s.version,
		"uptimeSec":      int64(time.Since(s.startedAt).Seconds()),
		"bohriumLoggedIn": auth.Token != "",
		"userAgent":      s.httpCli.UserAgent(),
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

	session := s.client.CheckSession(ctx)
	var (
		tasksResp  *bohrium.DailyTasksResponse
		tasksErr   string
		assetsResp *bohrium.PointsAndCapacity
		assetsErr  string
	)
	if session.HasToken {
		t, err := s.client.ListDailyTasks(ctx)
		if err != nil {
			tasksErr = err.Error()
		} else {
			tasksResp = t
		}
		a, err := s.client.GetPointsAndCapacity(ctx)
		if err != nil {
			assetsErr = err.Error()
		} else {
			assetsResp = a
		}
	}

	cachedPDFs, customPDFs := s.client.CountCachedPDFs()
	cfg := s.cfg.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"version":     s.version,
		"userAgent":   s.httpCli.UserAgent(),
		"session":     session,
		"tasks":       tasksResp,
		"tasksError":  tasksErr,
		"assets":      assetsResp,
		"assetsError": assetsErr,
		"scheduler":   s.scheduler.Status(),
		"promptStats": prompts.GetStats(),
		"pdfStats": map[string]int{
			"cached": cachedPDFs,
			"custom": customPDFs,
		},
		"reconDir": cfg.ReconDir,
		"logs":     s.store.GetLogs(30),
	})
}

func (s *Server) handleBohriumSession(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": s.client.CheckSession(ctx),
	})
}

func (s *Server) handleWeChatQR(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	qr, err := s.client.StartWeChatQR(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": qr})
}

func (s *Server) handleWeChatPoll(w http.ResponseWriter, r *http.Request) {
	sceneID := r.URL.Query().Get("sceneId")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := s.client.PollWeChatQR(ctx, sceneID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if res.Auth != nil {
		redacted := s.client.RedactedAuth()
		res.Auth = &redacted
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleImportToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := s.client.ImportToken(ctx, body.Token); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": s.client.CheckSession(ctx),
	})
}

func (s *Server) handlePasswordLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Account  string `json:"account"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效请求体"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := s.client.LoginWithPassword(ctx, body.Account, body.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": s.client.CheckSession(ctx),
	})
}

func (s *Server) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := s.client.RefreshToken(ctx, "manual"); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"session": s.client.CheckSession(ctx),
	})
}

func (s *Server) handleBohriumLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ClearAuth(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = s.store.AppendLog(store.RunLog{
		Trigger: "manual",
		Action:  "logout",
		Success: true,
		Message: "已清除本地保存的 Bohrium 登录凭据",
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetTasks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tasks, err := s.client.ListDailyTasks(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": tasks})
}

func (s *Server) handleClaimAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := s.client.ClaimAllTasks(ctx, "manual")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleClaimOne(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskKey string `json:"taskKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.TaskKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "taskKey 不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, err := s.client.ClaimTask(ctx, body.TaskKey, "manual")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleRunOne(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskKey string `json:"taskKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.TaskKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "taskKey 不能为空"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := s.scheduler.RunSingleManual(ctx, body.TaskKey)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	after, _ := s.client.ListDailyTasks(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         res.Success,
		"data":       res,
		"tasksAfter": after,
	})
}

func (s *Server) handleRunAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := s.scheduler.RunAllManual(ctx)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": res})
}

func (s *Server) handleGetAssets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	assets, err := s.client.GetPointsAndCapacity(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": assets})
}

func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"data": s.store.GetLogs(limit),
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
			if body.Schedule.JitterMinutes >= 0 && body.Schedule.JitterMinutes <= 120 {
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

	// Re-issue cookie if admin password was updated so current browser stays signed in.
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
		"message":   "配置已保存（修改代理或 UA 需重启容器生效）",
	})
}

func (s *Server) handlePromptCatalog(w http.ResponseWriter, r *http.Request) {
	papers, _ := prompts.Papers()
	cached, custom := s.client.CountCachedPDFs()
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
