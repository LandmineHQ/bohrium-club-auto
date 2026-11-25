package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/scheduler"
	"bohrium-club-auto/internal/store"
)

func TestAdminPasswordProtectionAndHealthz(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("BOHRIUM_DATA_DIR", tmp)
	t.Setenv("BOHRIUM_ADMIN_PASSWORD", "super-secret-admin")

	cfgMgr, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := store.Open(tmp)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	httpCli, _ := chromehttp.New(chromehttp.Options{})
	bCli := bohrium.NewClient(httpCli, cfgMgr, st)
	sched := scheduler.New(bCli, cfgMgr, st)
	srv := New("test-1.0", cfgMgr, st, httpCli, bCli, sched)

	// 1. /healthz is public and reports Chrome UA
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Chrome/") {
		t.Fatalf("unexpected /healthz: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Protected API rejects unauthenticated access
	req2 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/settings, got %d", rec2.Code)
	}

	// 3. Wrong admin password rejected
	badBody, _ := json.Marshal(map[string]string{"password": "wrong"})
	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(badBody))
	rec3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", rec3.Code)
	}

	// 4. Correct admin password issues HttpOnly cookie that grants access
	goodBody, _ := json.Marshal(map[string]string{"password": "super-secret-admin"})
	req4 := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(goodBody))
	rec4 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("expected 200 login, got %d: %s", rec4.Code, rec4.Body.String())
	}
	cookies := rec4.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != adminCookieName || !cookies[0].HttpOnly {
		t.Fatalf("expected HttpOnly session cookie, got %+v", cookies)
	}

	req5 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req5.AddCookie(cookies[0])
	rec5 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", rec5.Code)
	}

	// 5. Embedded dashboard HTML renders cleanly
	req6 := httptest.NewRequest(http.MethodGet, "/", nil)
	rec6 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec6, req6)
	if rec6.Code != http.StatusOK || !strings.Contains(rec6.Body.String(), "Bohrium Club Auto") {
		t.Fatalf("expected embedded dashboard HTML, got code=%d", rec6.Code)
	}
}
