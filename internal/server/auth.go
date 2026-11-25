package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const adminCookieName = "brm_auto_session"

type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{attempts: make(map[string][]time.Time)}
}

func (l *loginRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)
	recent := l.attempts[ip][:0]
	for _, t := range l.attempts[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	l.attempts[ip] = recent
	return len(recent) < 15
}

func (l *loginRateLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[ip] = append(l.attempts[ip], time.Now())
}

func (l *loginRateLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

func verifyPasswordConstantTime(candidate, expected string) bool {
	if expected == "" {
		return false
	}
	h1 := sha256.Sum256([]byte(candidate))
	h2 := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(h1[:], h2[:]) == 1
}

func signAdminSession(secret, pwFingerprint string, exp time.Time) string {
	payload := fmt.Sprintf("admin|%s|%d", pwFingerprint, exp.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	encPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return encPayload + "." + sig
}

func verifyAdminSession(cookieVal, secret, expectedPW string) bool {
	parts := strings.Split(cookieVal, ".")
	if len(parts) != 2 {
		return false
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(rawPayload)
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expectedSig)) != 1 {
		return false
	}
	fields := strings.Split(string(rawPayload), "|")
	if len(fields) != 3 || fields[0] != "admin" {
		return false
	}
	if fields[1] != passwordFingerprint(expectedPW) {
		return false
	}
	expUnix, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || time.Now().Unix() > expUnix {
		return false
	}
	return true
}

func passwordFingerprint(pw string) string {
	sum := sha256.Sum256([]byte("pwfp:" + pw))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
