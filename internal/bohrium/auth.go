package bohrium

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bohrium-club-auto/internal/store"
)

// Exact AES-128-CBC key/IV extracted from platform.bohrium.com login bundle.
const (
	platformAESKey = "pxQ$|2ydTBu<5@+P"
	platformAESExt = "eyJlbmNyeXB0X3R5cGUiOiJhZXMifQ==" // base64({"encrypt_type":"aes"})
)

// WeChatQRResponse is returned when requesting a remote WeChat Official Account QR code.
type WeChatQRResponse struct {
	SceneID   string `json:"sceneId"`
	QRCodeURL string `json:"qrCodeUrl"`
}

// WeChatPollResult represents a single poll of check_login_status.
type WeChatPollResult struct {
	Status     string           `json:"status"` // "waiting" | "scanned" | "confirmed" | "expired" | "logged_in"
	RegOrLogin string           `json:"regOrLogin,omitempty"`
	LoggedIn   bool             `json:"loggedIn"`
	Auth       *store.AuthState `json:"auth,omitempty"`
	RawMessage string           `json:"rawMessage,omitempty"`
}

// SessionStatus summarizes live Bohrium login and club binding state.
type SessionStatus struct {
	HasToken     bool             `json:"hasToken"`
	Valid        bool             `json:"valid"`
	ClubLoggedIn bool             `json:"clubLoggedIn"`
	ClubBound    bool             `json:"clubBound"`
	ExpiresInSec int64            `json:"expiresInSec"`
	Auth         store.AuthState  `json:"auth"`
	Error        string           `json:"error,omitempty"`
}

// StartWeChatQR fetches a fresh WeChat Official Account QR code for remote server-side login.
func (c *Client) StartWeChatQR(ctx context.Context) (*WeChatQRResponse, error) {
	u := c.endpoints.PlatformBase + "/api/account/wechat/login_qrcode?refer=%7B%7D"
	resp, err := c.http.DoJSON(ctx, http.MethodGet, u, nil, map[string]string{
		"Origin":  c.endpoints.PlatformBase,
		"Referer": c.endpoints.PlatformBase + "/login",
	})
	if err != nil {
		return nil, fmt.Errorf("request wechat qrcode: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wechat qrcode HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}
	var env struct {
		Code int `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			SceneID string `json:"sceneId"`
			QRCode  string `json:"qrCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("parse wechat qrcode response: %w", err)
	}
	if env.Code != 0 || env.Data.SceneID == "" || env.Data.QRCode == "" {
		return nil, fmt.Errorf("wechat qrcode error (code=%d): %s", env.Code, env.Msg)
	}
	return &WeChatQRResponse{
		SceneID:   env.Data.SceneID,
		QRCodeURL: env.Data.QRCode,
	}, nil
}

// PollWeChatQR polls check_login_status for a sceneId and completes login once a brmToken is issued.
func (c *Client) PollWeChatQR(ctx context.Context, sceneID string) (*WeChatPollResult, error) {
	sceneID = strings.TrimSpace(sceneID)
	if sceneID == "" {
		return nil, fmt.Errorf("sceneId is required")
	}

	// Build device payload identical to platform.bohrium.com login bundle (note intentional "with" spelling in upstream JS).
	devicePayload := map[string]any{
		"userAgent": c.http.UserAgent(),
		"screenInfo": map[string]int{
			"with":   1920,
			"height": 1080,
		},
		"memory": 8,
	}
	devJSON, _ := json.Marshal(devicePayload)
	devB64 := base64.StdEncoding.EncodeToString(devJSON)

	q := url.Values{}
	q.Set("sceneId", sceneID)
	q.Set("channel", "web")
	q.Set("device", devB64)
	u := c.endpoints.PlatformBase + "/api/account/wechat/qr_code/check_login_status?" + q.Encode()

	resp, err := c.http.DoJSON(ctx, http.MethodGet, u, nil, map[string]string{
		"Origin":  c.endpoints.PlatformBase,
		"Referer": c.endpoints.PlatformBase + "/login",
	})
	if err != nil {
		return nil, fmt.Errorf("poll wechat qr status: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll wechat qr HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}

	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("parse wechat poll response: %w", err)
	}
	data := asMap(parsed["data"])
	status := asString(data["status"])
	regOrLogin := asString(data["regOrLogin"])

	token := extractTokenFromResponse(resp.Headers, data)
	if token != "" {
		auth, err := c.CompleteLogin(ctx, token, "wechat_qr")
		if err != nil {
			return nil, err
		}
		return &WeChatPollResult{
			Status:     "logged_in",
			RegOrLogin: regOrLogin,
			LoggedIn:   true,
			Auth:       auth,
		}, nil
	}

	return &WeChatPollResult{
		Status:     status,
		RegOrLogin: regOrLogin,
		LoggedIn:   false,
		RawMessage: asString(parsed["msg"]),
	}, nil
}

// EncryptPlatformPassword reproduces platform.bohrium.com's double-base64 AES-128-CBC encryption.
func EncryptPlatformPassword(plaintext string) (string, error) {
	key := []byte(platformAESKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, key)
	mode.CryptBlocks(ciphertext, padded)
	first := base64.StdEncoding.EncodeToString(ciphertext)
	second := base64.StdEncoding.EncodeToString([]byte(first))
	return second, nil
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	padLen := blockSize - (len(src) % blockSize)
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(src, padding...)
}

// LoginWithPassword attempts direct password login ONLY if /api/captcha/check_user explicitly skips CAPTCHA.
// It never attempts to solve or bypass Tencent Captcha; if CAPTCHA is required, it instructs the user to use WeChat QR.
func (c *Client) LoginWithPassword(ctx context.Context, account, password string) (*store.AuthState, error) {
	account = strings.TrimSpace(account)
	if account == "" || password == "" {
		return nil, fmt.Errorf("account and password are required")
	}
	headers := map[string]string{
		"Origin":  c.endpoints.PlatformBase,
		"Referer": c.endpoints.PlatformBase + "/login",
	}
	checkResp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+"/api/captcha/check_user", map[string]any{
		"account": account,
	}, headers)
	if err != nil {
		return nil, fmt.Errorf("check captcha requirement: %w", err)
	}
	var checkBody map[string]any
	_ = json.Unmarshal(checkResp.Body, &checkBody)
	if !asBool(asMap(checkBody["data"])["skip"]) {
		return nil, fmt.Errorf("该账号登录触发了图形验证码，请使用「微信扫码登录」或「导入 brmToken」完成远程登录")
	}

	encPW, err := EncryptPlatformPassword(password)
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}
	loginResp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+"/api/account/v3/login", map[string]any{
		"account":  account,
		"password": encPW,
		"ext":      platformAESExt,
	}, headers)
	if err != nil {
		return nil, fmt.Errorf("password login request: %w", err)
	}
	var loginBody map[string]any
	if err := json.Unmarshal(loginResp.Body, &loginBody); err != nil {
		return nil, fmt.Errorf("parse login response: %w", err)
	}
	if int(asInt64(loginBody["code"])) != 0 {
		msg := asString(loginBody["msg"])
		if msg == "" {
			msg = asString(loginBody["error"])
		}
		return nil, fmt.Errorf("登录失败 (code=%d): %s", asInt64(loginBody["code"]), msg)
	}
	token := extractTokenFromResponse(loginResp.Headers, asMap(loginBody["data"]))
	if token == "" {
		return nil, fmt.Errorf("登录响应未返回 brmToken，请改用微信扫码登录")
	}
	return c.CompleteLogin(ctx, token, "password")
}

// ImportToken validates and saves a manually imported brmToken cookie/JWT.
func (c *Client) ImportToken(ctx context.Context, rawToken string) (*store.AuthState, error) {
	token := CleanToken(rawToken)
	if token == "" {
		return nil, fmt.Errorf("brmToken 不能为空")
	}
	return c.CompleteLogin(ctx, token, "manual_token")
}

// CompleteLogin validates a brmToken, fetches user info & club session (auto-enrolling if needed), and persists AuthState.
func (c *Client) CompleteLogin(ctx context.Context, rawToken, method string) (*store.AuthState, error) {
	token := CleanToken(rawToken)
	claims, err := ParseJWTClaims(token)
	if err != nil {
		return nil, fmt.Errorf("解析 brmToken 失败: %w", err)
	}

	now := time.Now().UTC()
	auth := store.AuthState{
		Token:         token,
		UserID:        claims.UserID,
		OrgID:         claims.OrgID,
		UserType:      claims.UserType,
		ExpiresAt:     claims.ExpiresAt,
		LastLoginAt:   now,
		LastRefreshAt: now,
		LoginMethod:   method,
	}

	// Fetch account profile from www.bohrium.com
	if infoResp, err := c.http.DoJSON(ctx, http.MethodGet, c.endpoints.WWWBase+"/bohrapi/v1/account/info", nil, c.wwwHeaders(token, "")); err == nil && infoResp.StatusCode == http.StatusOK {
		var body map[string]any
		if json.Unmarshal(infoResp.Body, &body) == nil && int(asInt64(body["code"])) == 0 {
			if data := asMap(body["data"]); data != nil {
				if uid := asInt64(data["user_id"]); uid > 0 && auth.UserID == 0 {
					auth.UserID = uid
				}
				if uid := asInt64(data["userId"]); uid > 0 && auth.UserID == 0 {
					auth.UserID = uid
				}
				if oid := asInt64(data["org_id"]); oid > 0 && auth.OrgID == 0 {
					auth.OrgID = oid
				}
				if oid := asInt64(data["orgId"]); oid > 0 && auth.OrgID == 0 {
					auth.OrgID = oid
				}
				if n := asString(data["name"]); n != "" {
					auth.Nickname = n
				} else if n := asString(data["nickname"]); n != "" {
					auth.Nickname = n
				}
				auth.Phone = asString(data["phone"])
				auth.Email = asString(data["email"])
				auth.Avatar = asString(data["avatar"])
			}
		}
	}

	// Check club.bohrium.com session & auto-enroll binding if logged in but not yet bound.
	clubLoggedIn, clubBound, clubNick, err := c.checkClubSession(ctx, token)
	if err == nil {
		if clubNick != "" && auth.Nickname == "" {
			auth.Nickname = clubNick
		}
		if clubLoggedIn && !clubBound {
			if enrollErr := c.enrollClub(ctx, token); enrollErr == nil {
				clubBound = true
			}
		}
		auth.ClubBound = clubBound
	}

	// Reject obviously invalid token if both club session and account info failed and token is already expired.
	if !clubLoggedIn && !auth.ExpiresAt.IsZero() && time.Now().After(auth.ExpiresAt) {
		return nil, fmt.Errorf("brmToken 已于 %s 过期，请重新登录", auth.ExpiresAt.Local().Format(time.RFC3339))
	}

	if err := c.store.SetAuth(auth); err != nil {
		return nil, fmt.Errorf("保存登录状态失败: %w", err)
	}
	_ = c.store.AppendLog(store.RunLog{
		Trigger: "manual",
		Action:  "login",
		Success: true,
		Message: fmt.Sprintf("登录成功（方式: %s，用户: %s / UID %d）", method, nonEmpty(auth.Nickname, "Bohrium用户"), auth.UserID),
	})
	return &auth, nil
}

// RefreshToken calls /platform-gateway/v1/account/refresh_token to extend the active JWT session.
func (c *Client) RefreshToken(ctx context.Context, trigger string) (*store.AuthState, error) {
	start := time.Now()
	auth := c.store.GetAuth()
	if auth.Token == "" {
		return nil, fmt.Errorf("尚未登录 Bohrium 账号")
	}

	u := c.endpoints.WWWBase + "/platform-gateway/v1/account/refresh_token"
	resp, err := c.http.DoJSON(ctx, http.MethodPost, u, map[string]any{
		"businessLine": "bohrium",
		"token":        auth.Token,
	}, c.wwwHeaders(auth.Token, ""))
	if err != nil {
		return nil, fmt.Errorf("refresh_token 请求失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh_token HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 200))
	}

	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("解析 refresh_token 响应失败: %w", err)
	}
	if code := int(asInt64(parsed["code"])); code != 0 {
		return nil, fmt.Errorf("refresh_token 返回错误 code=%d: %s", code, asString(parsed["msg"]))
	}

	newToken := extractTokenFromResponse(resp.Headers, asMap(parsed["data"]))
	if newToken == "" {
		// Some responses return token directly as string in data
		if s, ok := parsed["data"].(string); ok && strings.Count(s, ".") == 2 {
			newToken = CleanToken(s)
		}
	}
	if newToken == "" {
		return nil, fmt.Errorf("refresh_token 响应未包含新 token")
	}

	claims, err := ParseJWTClaims(newToken)
	if err != nil {
		return nil, fmt.Errorf("解析刷新后的 JWT 失败: %w", err)
	}
	auth.Token = newToken
	if claims.UserID > 0 {
		auth.UserID = claims.UserID
	}
	if claims.OrgID > 0 {
		auth.OrgID = claims.OrgID
	}
	if !claims.ExpiresAt.IsZero() {
		auth.ExpiresAt = claims.ExpiresAt
	}
	auth.LastRefreshAt = time.Now().UTC()

	if err := c.store.SetAuth(auth); err != nil {
		return nil, err
	}
	_ = c.store.AppendLog(store.RunLog{
		Trigger:    trigger,
		Action:     "refresh_token",
		Success:    true,
		DurationMs: time.Since(start).Milliseconds(),
		Message:    fmt.Sprintf("Token 自动续期成功，新有效期至 %s", auth.ExpiresAt.Local().Format("2006-01-02 15:04:05")),
	})
	return &auth, nil
}

// CheckSession checks whether the currently persisted token is valid on club.bohrium.com.
func (c *Client) CheckSession(ctx context.Context) SessionStatus {
	auth := c.store.GetAuth()
	st := SessionStatus{
		HasToken: auth.Token != "",
		Auth:     redactedAuth(auth),
	}
	if !auth.ExpiresAt.IsZero() {
		st.ExpiresInSec = int64(time.Until(auth.ExpiresAt).Seconds())
	}
	if auth.Token == "" {
		return st
	}
	loggedIn, bound, nick, err := c.checkClubSession(ctx, auth.Token)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.ClubLoggedIn = loggedIn
	st.ClubBound = bound
	st.Valid = loggedIn
	if loggedIn && !bound {
		if c.enrollClub(ctx, auth.Token) == nil {
			st.ClubBound = true
			auth.ClubBound = true
			_ = c.store.SetAuth(auth)
		}
	}
	if nick != "" && auth.Nickname != nick {
		auth.Nickname = nick
		_ = c.store.SetAuth(auth)
		st.Auth = redactedAuth(auth)
	}
	return st
}

// RedactedAuth returns AuthState without leaking the full JWT token over APIs.
func (c *Client) RedactedAuth() store.AuthState {
	return redactedAuth(c.store.GetAuth())
}

func redactedAuth(a store.AuthState) store.AuthState {
	out := a
	if len(out.Token) > 16 {
		out.Token = out.Token[:8] + "..." + out.Token[len(out.Token)-6:]
	} else if out.Token != "" {
		out.Token = "***"
	}
	return out
}

func (c *Client) checkClubSession(ctx context.Context, token string) (loggedIn bool, bound bool, nickname string, err error) {
	resp, err := c.http.DoJSON(ctx, http.MethodGet, c.endpoints.ClubBase+"/api/platform/session", nil, c.clubHeaders(token))
	if err != nil {
		return false, false, "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return false, false, "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, false, "", fmt.Errorf("club session HTTP %d", resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return false, false, "", err
	}
	loggedIn = asBool(parsed["logged_in"])
	if binding := asMap(parsed["binding"]); binding != nil && len(binding) > 0 {
		bound = true
		nickname = asString(binding["nickname"])
	}
	return loggedIn, bound, nickname, nil
}

func (c *Client) enrollClub(ctx context.Context, token string) error {
	resp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.ClubBase+"/api/enroll", map[string]any{}, c.clubHeaders(token))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("club enroll HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 120))
}

func extractTokenFromResponse(headers http.Header, data map[string]any) string {
	for _, key := range []string{"token", "brmToken", "accessToken", "access_token"} {
		if v := CleanToken(asString(data[key])); strings.Count(v, ".") == 2 {
			return v
		}
	}
	for _, sc := range headers.Values("Set-Cookie") {
		for _, part := range strings.Split(sc, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "brmToken=") {
				if tok := CleanToken(strings.TrimPrefix(part, "brmToken=")); strings.Count(tok, ".") == 2 {
					return tok
				}
			}
		}
	}
	return ""
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
