package bohrium

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
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

	// DefaultTencentCaptchaAppID matches platform.bohrium.com's production web Tencent Cloud Captcha AppID.
	DefaultTencentCaptchaAppID int64 = 194611140
)

// CaptchaPrepareResult provides Tencent Cloud Captcha widget initialization metadata
// fetched from platform.bohrium.com so the user can manually complete verification in the UI.
type CaptchaPrepareResult struct {
	Skip         bool   `json:"skip"`
	CaptchaAppID string `json:"captchaAppId"`
	AidEncrypted string `json:"aidEncrypted,omitempty"`
}

// CaptchaProof carries the user-completed Tencent Cloud Captcha verification ticket and randStr.
type CaptchaProof struct {
	Ticket       string `json:"ticket,omitempty"`
	RandStr      string `json:"randStr,omitempty"`
	CaptchaAppID int64  `json:"captchaAppId,omitempty"`
}

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

	devB64 := c.deviceInfoB64()

	q := url.Values{}
	q.Set("sceneId", sceneID)
	q.Set("channel", DesktopLoginChannel)
	q.Set("device", devB64)
	u := c.endpoints.PlatformBase + "/api/account/wechat/qr_code/check_login_status?" + q.Encode()

	resp, err := c.http.DoJSON(ctx, http.MethodGet, u, nil, map[string]string{
		"Origin":  c.endpoints.PlatformBase,
		"Referer": c.endpoints.PlatformBase + "/login?business=Bohrium&channel=" + DesktopLoginChannel,
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

// OAuthURLResponse describes an upstream OAuth authorize URL for Google/Apple/CARSI/Feishu/PKU.
type OAuthURLResponse struct {
	Provider    string `json:"provider"`
	TypeCode    int    `json:"typeCode"`
	AuthURL     string `json:"authUrl"`
	RedirectURI string `json:"redirectUri,omitempty"`
}

func resolveOAuthProvider(provider string) (name string, typeCode int, isOversea bool, redirectURL string, err error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "google":
		return "google", 4, true, "", nil
	case "apple":
		return "apple", 3, true, DefaultPlatformBaseURL + "/auth-code", nil
	case "carsi":
		return "carsi", 6, false, "", nil
	case "feishu", "lark":
		return "feishu", 40001, false, "", nil
	case "pku":
		return "pku", 11, false, "", nil
	default:
		return "", 0, false, "", fmt.Errorf("不支持的第三方登录方式: %s（支持 google / apple / carsi / feishu / pku）", provider)
	}
}

func (c *Client) deviceInfoB64() string {
	devicePayload := map[string]any{
		"userAgent": c.http.UserAgent(),
		"screenInfo": map[string]int{
			"with":   1920,
			"height": 1080,
		},
		"memory": 8,
	}
	devJSON, _ := json.Marshal(devicePayload)
	return base64.StdEncoding.EncodeToString(devJSON)
}

// GetOAuthURL fetches the official third-party OAuth authorization URL from platform.bohrium.com.
func (c *Client) GetOAuthURL(ctx context.Context, provider string) (*OAuthURLResponse, error) {
	name, typeCode, _, redirectURL, err := resolveOAuthProvider(provider)
	if err != nil {
		return nil, err
	}
	if name == "apple" {
		q := url.Values{}
		q.Set("response_type", "code")
		q.Set("response_mode", "query")
		q.Set("client_id", "com.bohrium.login")
		q.Set("redirect_uri", c.endpoints.PlatformBase+"/auth-code")
		q.Set("locale", "zh_CN")
		return &OAuthURLResponse{
			Provider:    name,
			TypeCode:    typeCode,
			AuthURL:     "https://appleid.apple.com/auth/authorize?" + q.Encode(),
			RedirectURI: c.endpoints.PlatformBase + "/auth-code",
		}, nil
	}

	u := fmt.Sprintf("%s/api/account/oauth_url?type=%d", c.endpoints.PlatformBase, typeCode)
	resp, err := c.http.DoJSON(ctx, http.MethodGet, u, nil, map[string]string{
		"Origin":  c.endpoints.PlatformBase,
		"Referer": c.endpoints.PlatformBase + "/login",
	})
	if err != nil {
		return nil, fmt.Errorf("获取 %s 登录授权链接失败: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取 %s 登录链接 HTTP %d: %s", name, resp.StatusCode, truncateText(string(resp.Body), 180))
	}
	var env struct {
		Code int `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("解析 oauth_url 响应失败: %w", err)
	}
	if env.Code != 0 || env.Data.URL == "" {
		return nil, fmt.Errorf("oauth_url 错误 (code=%d): %s", env.Code, env.Msg)
	}
	return &OAuthURLResponse{
		Provider:    name,
		TypeCode:    typeCode,
		AuthURL:     env.Data.URL,
		RedirectURI: redirectURL,
	}, nil
}

// VerifyOAuthCallback exchanges an OAuth callback URL (or raw authorization code) via POST /api/oauth/verify.
func (c *Client) VerifyOAuthCallback(ctx context.Context, provider, callbackInput string) (*store.AuthState, error) {
	name, typeCode, isOversea, redirectURL, err := resolveOAuthProvider(provider)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(callbackInput)
	if raw == "" {
		return nil, fmt.Errorf("请粘贴授权回调完整 URL 或授权码 code")
	}

	code := raw
	state := ""
	// Parse callback URL if user pasted the redirected browser address bar URL.
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") || strings.Contains(raw, "code=") || strings.Contains(raw, "token=") {
		parseTarget := raw
		if !strings.HasPrefix(parseTarget, "http://") && !strings.HasPrefix(parseTarget, "https://") {
			parseTarget = "https://placeholder.local/?" + strings.TrimLeft(parseTarget, "?")
		}
		if u, err := url.Parse(parseTarget); err == nil {
			q := u.Query()
			// Some OAuth flows (e.g. CARSI/PKU) redirect directly with ?token=<jwt>
			if directTok := CleanToken(q.Get("token")); strings.Count(directTok, ".") == 2 {
				return c.CompleteLogin(ctx, directTok, "oauth_"+name)
			}
			if cVal := strings.TrimSpace(q.Get("code")); cVal != "" {
				code = cVal
			} else if tVal := strings.TrimSpace(q.Get("ticket")); tVal != "" {
				code = tVal
			}
			state = strings.TrimSpace(q.Get("state"))
		}
	}

	payload := map[string]any{
		"type":         typeCode,
		"method":       "oauth-code",
		"code":         code,
		"businessLine": "bohrium",
		"isOversea":    isOversea,
		"channel":      DesktopLoginChannel,
		"refer":        map[string]any{},
	}
	if state != "" {
		payload["state"] = state
	}
	if redirectURL != "" {
		payload["redirectUrl"] = redirectURL
	}

	resp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+"/api/oauth/verify", payload, map[string]string{
		"Origin":  c.endpoints.PlatformBase,
		"Referer": c.endpoints.PlatformBase + "/login?business=Bohrium&channel=" + DesktopLoginChannel,
	})
	if err != nil {
		return nil, fmt.Errorf("OAuth 验证请求失败: %w", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("解析 OAuth 验证响应失败 (HTTP %d): %s", resp.StatusCode, truncateText(string(resp.Body), 180))
	}
	if codeNum := int(asInt64(parsed["code"])); codeNum != 0 {
		msg := asString(parsed["msg"])
		if msg == "" {
			msg = asString(asMap(parsed["error"])["msg"])
		}
		if msg == "" {
			msg = asString(parsed["message"])
		}
		return nil, fmt.Errorf("%s OAuth 登录失败 (code=%d): %s", name, codeNum, nonEmpty(msg, truncateText(string(resp.Body), 160)))
	}
	tok := extractTokenFromResponse(resp.Headers, asMap(parsed["data"]))
	if tok == "" {
		return nil, fmt.Errorf("%s 授权成功但未返回登录态（可能需在官网先完成手机号绑定），请改用微信扫码或导入 brmToken", name)
	}
	return c.CompleteLogin(ctx, tok, "oauth_"+name)
}

func (c *Client) platformHeaders(isOversea bool) map[string]string {
	lang := "zh-cn"
	if isOversea {
		lang = "en-us"
	}
	return map[string]string{
		"Origin":           c.endpoints.PlatformBase,
		"Referer":          c.endpoints.PlatformBase + "/login?business=Bohrium&channel=" + DesktopLoginChannel,
		"Content-Language": lang,
	}
}

func maskLoginTarget(target string) string {
	t := strings.TrimSpace(target)
	if idx := strings.IndexByte(t, '@'); idx > 0 {
		local := t[:idx]
		dom := t[idx:]
		if len(local) <= 2 {
			return local[:1] + "***" + dom
		}
		return local[:2] + "***" + dom
	}
	if len(t) > 6 {
		return t[:3] + "****" + t[len(t)-3:]
	}
	return "***"
}

// PrepareCaptcha fetches Tencent Cloud Captcha widget parameters from platform.bohrium.com
// so the user can complete interactive human verification in the Web Console.
// For scene="password", it first queries /api/captcha/check_user and sets Skip=true when exempt.
func (c *Client) PrepareCaptcha(ctx context.Context, scene, account string) (*CaptchaPrepareResult, error) {
	headers := c.platformHeaders(false)
	appIDStr := fmt.Sprintf("%d", DefaultTencentCaptchaAppID)

	if strings.EqualFold(strings.TrimSpace(scene), "password") && strings.TrimSpace(account) != "" {
		checkResp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+"/api/captcha/check_user", map[string]any{
			"account": strings.TrimSpace(account),
		}, headers)
		if err == nil && checkResp.StatusCode == http.StatusOK {
			var checkBody map[string]any
			if json.Unmarshal(checkResp.Body, &checkBody) == nil && asBool(asMap(checkBody["data"])["skip"]) {
				return &CaptchaPrepareResult{
					Skip:         true,
					CaptchaAppID: appIDStr,
				}, nil
			}
		}
	}

	resp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+"/api/captcha/encrypt_appid", map[string]any{
		"captchaAppId": DefaultTencentCaptchaAppID,
	}, headers)
	if err != nil {
		return nil, fmt.Errorf("获取图形验证码初始化参数失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("encrypt_appid HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 180))
	}
	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		if isAliyunWAFResponse(resp.Body) {
			return nil, wafBlockedErr("初始化图形验证码")
		}
		return nil, fmt.Errorf("解析 encrypt_appid 响应失败: %w", err)
	}
	if codeNum := int(asInt64(parsed["code"])); codeNum != 0 {
		msg := extractPlatformErrMsg(parsed, resp.Body)
		return nil, fmt.Errorf("初始化图形验证码失败 (code=%d): %s", codeNum, msg)
	}
	signAppID := asString(asMap(parsed["data"])["signAppIdStr"])
	if signAppID == "" {
		return nil, fmt.Errorf("encrypt_appid 响应未包含 signAppIdStr")
	}
	return &CaptchaPrepareResult{
		Skip:         false,
		CaptchaAppID: appIDStr,
		AidEncrypted: signAppID,
	}, nil
}

func isAliyunWAFResponse(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	low := strings.ToLower(string(body))
	return strings.Contains(low, "aliyun_waf") ||
		strings.Contains(low, `id="renderdata"`) ||
		strings.Contains(low, "acw_sc__v2") ||
		strings.Contains(low, "interfaceacting.js")
}

func wafBlockedErr(action string) error {
	return fmt.Errorf("%s被上游阿里云 WAF 拦截，后端直连无法通过官网浏览器环境校验；请点击「打开官方登录窗口」在浏览器完成登录后，复制 Cookie 中的 brmToken 粘贴到「导入 Token」页签，或改用微信扫码 / 第三方 OAuth 登录", action)
}

func extractPlatformErrMsg(parsed map[string]any, raw []byte) string {
	for _, cand := range []string{
		asString(parsed["message"]),
		asString(parsed["msg"]),
		asString(asMap(parsed["error"])["msg"]),
		asString(asMap(parsed["error"])["message"]),
		asString(parsed["error"]),
	} {
		if strings.TrimSpace(cand) != "" {
			return strings.TrimSpace(cand)
		}
	}
	return truncateText(string(raw), 160)
}

func applyCaptchaProof(payload map[string]any, proof *CaptchaProof) bool {
	if proof == nil {
		return false
	}
	ticket := strings.TrimSpace(proof.Ticket)
	randStr := strings.TrimSpace(proof.RandStr)
	if ticket == "" || randStr == "" {
		return false
	}
	appID := proof.CaptchaAppID
	if appID <= 0 {
		appID = DefaultTencentCaptchaAppID
	}
	payload["ticket"] = ticket
	payload["randStr"] = randStr
	payload["captchaAppId"] = appID
	return true
}

// SendVerifyCode sends a phone SMS or email OTP verification code via platform.bohrium.com.
// When the user completes Tencent Cloud Captcha in the UI, proof carries (ticket, randStr, captchaAppId).
func (c *Client) SendVerifyCode(ctx context.Context, kind, target, countryCode string, isOversea bool, captcha *CaptchaProof) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("请输入手机号或邮箱")
	}
	headers := c.platformHeaders(isOversea)

	var (
		endpoint string
		payload  map[string]any
	)
	switch kind {
	case "phone":
		phone := normalizePhone(target, countryCode)
		endpoint = "/api/account/code/send_by_phone_v3"
		payload = map[string]any{
			"phone":          phone,
			"channel":        DesktopLoginChannel,
			"sceneType":      2,
			"tokenKey":       "",
			"Version":        "v2",
			"captchaSceneId": "1cg9xwt4",
			"businessLine":   "bohrium",
		}
	case "email":
		endpoint = "/api/account/code/send_by_email_v3"
		payload = map[string]any{
			"email":        target,
			"channel":      DesktopLoginChannel,
			"sceneType":    2,
			"tokenKey":     "",
			"businessLine": "bohrium",
			"isOversea":    isOversea,
		}
	default:
		return fmt.Errorf("不支持的验证码类型: %s（支持 phone / email）", kind)
	}
	hasProof := applyCaptchaProof(payload, captcha)

	resp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+endpoint, payload, headers)
	if err != nil {
		log.Printf("[auth] send_code kind=%s target=%s oversea=%v err=%v", kind, maskLoginTarget(target), isOversea, err)
		return fmt.Errorf("发送验证码请求失败: %w", err)
	}
	bodyPreview := truncateText(string(resp.Body), 240)
	var parsed map[string]any
	if uErr := json.Unmarshal(resp.Body, &parsed); uErr != nil {
		log.Printf("[auth] send_code kind=%s target=%s oversea=%v http=%d non-json body=%s", kind, maskLoginTarget(target), isOversea, resp.StatusCode, bodyPreview)
		if isAliyunWAFResponse(resp.Body) {
			c.appendAuthLog("send_code", false, fmt.Sprintf("发送%s验证码被上游阿里云 WAF 拦截 (%s, HTTP %d)", kind, maskLoginTarget(target), resp.StatusCode))
			return wafBlockedErr("发送验证码")
		}
		c.appendAuthLog("send_code", false, fmt.Sprintf("发送%s验证码异常 (%s, HTTP %d): 非 JSON 响应 %s", kind, maskLoginTarget(target), resp.StatusCode, truncateText(string(resp.Body), 120)))
		return fmt.Errorf("发送验证码响应异常 (HTTP %d): %s", resp.StatusCode, truncateText(string(resp.Body), 140))
	}
	rawCode, hasCode := parsed["code"]
	codeNum := int(asInt64(rawCode))
	log.Printf("[auth] send_code kind=%s target=%s oversea=%v hasProof=%v http=%d hasCode=%v code=%d body=%s",
		kind, maskLoginTarget(target), isOversea, hasProof, resp.StatusCode, hasCode, codeNum, bodyPreview)

	if !hasCode || codeNum != 0 || resp.StatusCode != http.StatusOK {
		msg := extractPlatformErrMsg(parsed, resp.Body)
		c.appendAuthLog("send_code", false, fmt.Sprintf("发送%s验证码失败 (%s, HTTP %d, code=%d): %s", kind, maskLoginTarget(target), resp.StatusCode, codeNum, msg))
		if !hasProof && (strings.Contains(strings.ToLower(msg+string(resp.Body)), "captcha") || strings.Contains(msg, "验证")) {
			return fmt.Errorf("发送验证码需要先完成人机图形验证，请在界面中点击「获取验证码」并完成腾讯验证弹窗")
		}
		if codeNum == 180041 {
			return fmt.Errorf("发送验证码频次过高 (code=180041)，请稍后再试")
		}
		return fmt.Errorf("发送验证码失败 (HTTP %d, code=%d): %s", resp.StatusCode, codeNum, msg)
	}
	c.appendAuthLog("send_code", true, fmt.Sprintf("已请求发送%s验证码 (%s, oversea=%v, HTTP %d, resp=%s)", kind, maskLoginTarget(target), isOversea, resp.StatusCode, truncateText(string(resp.Body), 140)))
	return nil
}

func (c *Client) appendAuthLog(action string, success bool, msg string) {
	if c.store == nil {
		return
	}
	_ = c.store.AppendLog(store.RunLog{
		Trigger: "manual",
		Action:  action,
		Success: success,
		Message: msg,
	})
}

// LoginWithCode logs in using a phone SMS code or email verification code.
func (c *Client) LoginWithCode(ctx context.Context, kind, target, countryCode, code string, isOversea bool) (*store.AuthState, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	target = strings.TrimSpace(target)
	code = strings.TrimSpace(code)
	if target == "" || code == "" {
		return nil, fmt.Errorf("账号与验证码均不能为空")
	}
	headers := c.platformHeaders(isOversea)

	var (
		endpoint string
		payload  map[string]any
		method   string
	)
	switch kind {
	case "phone":
		endpoint = "/api/account/v2/login/phone_code"
		method = "phone_code"
		payload = map[string]any{
			"phone":        normalizePhone(target, countryCode),
			"phoneCode":    code,
			"businessLine": "bohrium",
			"refer":        map[string]any{},
			"autoRegist":   true,
			"channel":      DesktopLoginChannel,
			"device":       c.deviceInfoB64(),
		}
	case "email":
		endpoint = "/api/account/v2/login/email_code"
		method = "email_code"
		payload = map[string]any{
			"email":        target,
			"code":         code,
			"ext":          platformAESExt,
			"businessLine": "bohrium",
			"refer":        map[string]any{},
			"channel":      DesktopLoginChannel,
			"device":       c.deviceInfoB64(),
		}
	default:
		return nil, fmt.Errorf("不支持的验证码登录类型: %s", kind)
	}

	resp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+endpoint, payload, headers)
	if err != nil {
		return nil, fmt.Errorf("验证码登录请求失败: %w", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		if isAliyunWAFResponse(resp.Body) {
			c.appendAuthLog("login_code", false, fmt.Sprintf("验证码登录被上游阿里云 WAF 拦截 (%s, HTTP %d)", maskLoginTarget(target), resp.StatusCode))
			return nil, wafBlockedErr("验证码登录")
		}
		return nil, fmt.Errorf("解析验证码登录响应失败 (HTTP %d): %s", resp.StatusCode, truncateText(string(resp.Body), 140))
	}
	rawCode, hasCode := parsed["code"]
	codeNum := int(asInt64(rawCode))
	if !hasCode || codeNum != 0 || resp.StatusCode != http.StatusOK {
		msg := extractPlatformErrMsg(parsed, resp.Body)
		return nil, fmt.Errorf("验证码登录失败 (code=%d): %s", codeNum, msg)
	}
	tok := extractTokenFromResponse(resp.Headers, asMap(parsed["data"]))
	if tok == "" {
		return nil, fmt.Errorf("登录响应未返回 brmToken")
	}
	return c.CompleteLogin(ctx, tok, method)
}

func normalizePhone(phone, countryCode string) string {
	p := strings.TrimSpace(phone)
	if strings.HasPrefix(p, "+") {
		return p
	}
	cc := strings.TrimLeft(strings.TrimSpace(countryCode), "+")
	if cc == "" {
		cc = "86"
	}
	return "+" + cc + p
}

// LoginWithPassword logs in via /api/account/v3/login.
// When captcha proof (ticket + randStr) is provided by the user completing Tencent Cloud Captcha in the UI,
// it is forwarded directly; otherwise /api/captcha/check_user is consulted and non-exempt accounts are asked
// to complete interactive verification.
func (c *Client) LoginWithPassword(ctx context.Context, account, password string, captcha *CaptchaProof) (*store.AuthState, error) {
	account = strings.TrimSpace(account)
	if account == "" || password == "" {
		return nil, fmt.Errorf("account and password are required")
	}
	headers := c.platformHeaders(false)
	hasProof := captcha != nil && strings.TrimSpace(captcha.Ticket) != "" && strings.TrimSpace(captcha.RandStr) != ""
	if !hasProof {
		checkResp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+"/api/captcha/check_user", map[string]any{
			"account": account,
		}, headers)
		if err != nil {
			return nil, fmt.Errorf("check captcha requirement: %w", err)
		}
		var checkBody map[string]any
		_ = json.Unmarshal(checkResp.Body, &checkBody)
		if !asBool(asMap(checkBody["data"])["skip"]) {
			return nil, fmt.Errorf("该账号登录需要先完成人机图形验证，请在界面中完成腾讯验证弹窗后重试")
		}
	}

	encPW, err := EncryptPlatformPassword(password)
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}
	payload := map[string]any{
		"account":      account,
		"password":     encPW,
		"ext":          platformAESExt,
		"businessLine": "bohrium",
		"refer":        map[string]any{},
		"channel":      DesktopLoginChannel,
		"device":       c.deviceInfoB64(),
	}
	applyCaptchaProof(payload, captcha)

	loginResp, err := c.http.DoJSON(ctx, http.MethodPost, c.endpoints.PlatformBase+"/api/account/v3/login", payload, headers)
	if err != nil {
		return nil, fmt.Errorf("password login request: %w", err)
	}
	var loginBody map[string]any
	if err := json.Unmarshal(loginResp.Body, &loginBody); err != nil {
		if isAliyunWAFResponse(loginResp.Body) {
			c.appendAuthLog("login_password", false, fmt.Sprintf("密码登录被上游阿里云 WAF 拦截 (%s, HTTP %d)", maskLoginTarget(account), loginResp.StatusCode))
			return nil, wafBlockedErr("密码登录")
		}
		return nil, fmt.Errorf("parse login response: %w", err)
	}
	if int(asInt64(loginBody["code"])) != 0 {
		msg := asString(loginBody["msg"])
		if msg == "" {
			msg = asString(asMap(loginBody["error"])["msg"])
		}
		if msg == "" {
			msg = asString(loginBody["error"])
		}
		return nil, fmt.Errorf("登录失败 (code=%d): %s", asInt64(loginBody["code"]), nonEmpty(msg, truncateText(string(loginResp.Body), 140)))
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

	// Fetch account profile from www.bohrium.com (/bohrapi/v1/account/info)
	_, explicitNick, _ := c.fetchAccountProfile(ctx, token, &auth)

	// Check club.bohrium.com session & auto-enroll binding if logged in but not yet bound.
	clubInfo, err := c.checkClubSession(ctx, token)
	clubLoggedIn := false
	if err == nil {
		clubLoggedIn = clubInfo.LoggedIn
		applyClubSessionProfile(&auth, clubInfo, explicitNick)
		clubBound := clubInfo.Bound
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
	// Pre-checked best-effort claim for the one-time desktop client first-login benefit (10,000 photons).
	// ClaimClientLoginPhotons never calls receive_photon unless client-activity confirms eligibility.
	if ctx.Err() == nil {
		bCtx, bCancel := context.WithTimeout(ctx, 5*time.Second)
		_, _ = c.ClaimClientLoginPhotons(bCtx, "login")
		bCancel()
	}
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
	if auth.Nickname == "" {
		_, _, _ = c.fetchAccountProfile(ctx, newToken, &auth)
	}

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

// SyncProfile refreshes the persisted account profile (nickname, avatar, email, phone) from Bohrium upstream APIs.
func (c *Client) SyncProfile(ctx context.Context) (*store.AuthState, bool, error) {
	auth := c.store.GetAuth()
	if auth.Token == "" {
		return nil, false, fmt.Errorf("尚未登录 Bohrium 账号")
	}
	ok, explicitNick, changed := c.fetchAccountProfile(ctx, auth.Token, &auth)
	if !ok || auth.Nickname == "" || !explicitNick {
		if clubInfo, err := c.checkClubSession(ctx, auth.Token); err == nil && clubInfo.LoggedIn {
			if applyClubSessionProfile(&auth, clubInfo, explicitNick) {
				changed = true
			}
			if clubInfo.Bound && !auth.ClubBound {
				auth.ClubBound = true
				changed = true
			}
		}
	}
	if changed {
		if err := c.store.SetAuth(auth); err != nil {
			return &auth, false, err
		}
	}
	return &auth, changed, nil
}

// CheckSession checks whether the currently persisted token is valid on club.bohrium.com
// and automatically backfills missing profile fields (such as nickname) from Bohrium APIs.
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
	clubInfo, err := c.checkClubSession(ctx, auth.Token)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.ClubLoggedIn = clubInfo.LoggedIn
	st.ClubBound = clubInfo.Bound
	st.Valid = clubInfo.LoggedIn

	dirty := false
	if clubInfo.LoggedIn && !clubInfo.Bound {
		if c.enrollClub(ctx, auth.Token) == nil {
			st.ClubBound = true
			auth.ClubBound = true
			dirty = true
		}
	} else if clubInfo.Bound && !auth.ClubBound {
		auth.ClubBound = true
		dirty = true
	}

	explicitNick := false
	if clubInfo.LoggedIn && (strings.TrimSpace(auth.Nickname) == "" || (auth.Email == "" && auth.Phone == "")) {
		if _, exp, changed := c.fetchAccountProfile(ctx, auth.Token, &auth); changed {
			dirty = true
			explicitNick = exp
		} else {
			explicitNick = exp
		}
	}
	if clubInfo.LoggedIn && applyClubSessionProfile(&auth, clubInfo, explicitNick) {
		dirty = true
	}
	if dirty {
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

type clubSessionInfo struct {
	LoggedIn     bool
	Bound        bool
	ExplicitNick string // binding.nickname (custom Club nickname, nullable upstream)
	FallbackName string // binding.name / binding.platform_username / profile.name / profile.username
	Email        string
	Phone        string
}

func (ci clubSessionInfo) bestNickname() string {
	if s := strings.TrimSpace(ci.ExplicitNick); s != "" {
		return s
	}
	return strings.TrimSpace(ci.FallbackName)
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// applyAccountInfo maps /bohrapi/v1/account/info's real Bohrium response schema onto auth.
// Bohrium returns nested extend.nickName (custom display name), userName (primary username, e.g. "bohr68859c"),
// weChatNickname, userNameEn, userExtId, and avatarUrl (not top-level name/nickname/avatar).
func applyAccountInfo(auth *store.AuthState, data map[string]any) (explicitNick bool, changed bool) {
	if auth == nil || data == nil {
		return false, false
	}
	if uid := asInt64(data["user_id"]); uid > 0 && auth.UserID == 0 {
		auth.UserID = uid
		changed = true
	}
	if uid := asInt64(data["userId"]); uid > 0 && auth.UserID == 0 {
		auth.UserID = uid
		changed = true
	}
	if oid := asInt64(data["org_id"]); oid > 0 && auth.OrgID == 0 {
		auth.OrgID = oid
		changed = true
	}
	if oid := asInt64(data["orgId"]); oid > 0 && auth.OrgID == 0 {
		auth.OrgID = oid
		changed = true
	}

	ext := asMap(data["extend"])
	nick := ""
	if ext != nil {
		nick = firstNonEmptyString(asString(ext["nickName"]), asString(ext["nickname"]))
	}
	if nick == "" {
		nick = firstNonEmptyString(asString(data["nickName"]), asString(data["nickname"]))
	}
	if nick != "" {
		explicitNick = true
	} else {
		nick = firstNonEmptyString(
			asString(data["userName"]),
			asString(data["name"]),
			asString(data["weChatNickname"]),
			asString(data["userNameEn"]),
			asString(data["userExtId"]),
		)
	}
	if nick != "" && auth.Nickname != nick {
		auth.Nickname = nick
		changed = true
	}
	if phone := strings.TrimSpace(asString(data["phone"])); phone != "" && auth.Phone != phone {
		auth.Phone = phone
		changed = true
	}
	if email := strings.TrimSpace(asString(data["email"])); email != "" && auth.Email != email {
		auth.Email = email
		changed = true
	}
	if avatar := firstNonEmptyString(asString(data["avatarUrl"]), asString(data["avatar"])); avatar != "" && auth.Avatar != avatar {
		auth.Avatar = avatar
		changed = true
	}
	return explicitNick, changed
}

func applyClubSessionProfile(auth *store.AuthState, clubInfo clubSessionInfo, hasExplicitBohriumNick bool) bool {
	if auth == nil {
		return false
	}
	changed := false
	if !hasExplicitBohriumNick && strings.TrimSpace(clubInfo.ExplicitNick) != "" && auth.Nickname != strings.TrimSpace(clubInfo.ExplicitNick) {
		auth.Nickname = strings.TrimSpace(clubInfo.ExplicitNick)
		changed = true
	} else if strings.TrimSpace(auth.Nickname) == "" {
		if n := clubInfo.bestNickname(); n != "" {
			auth.Nickname = n
			changed = true
		}
	}
	if auth.Email == "" && strings.TrimSpace(clubInfo.Email) != "" {
		auth.Email = strings.TrimSpace(clubInfo.Email)
		changed = true
	}
	if auth.Phone == "" && strings.TrimSpace(clubInfo.Phone) != "" {
		auth.Phone = strings.TrimSpace(clubInfo.Phone)
		changed = true
	}
	return changed
}

func (c *Client) fetchAccountProfile(ctx context.Context, token string, auth *store.AuthState) (ok bool, explicitNick bool, changed bool) {
	infoResp, err := c.http.DoJSON(ctx, http.MethodGet, c.endpoints.WWWBase+"/bohrapi/v1/account/info", nil, c.wwwHeaders(token, ""))
	if err != nil || infoResp.StatusCode != http.StatusOK {
		return false, false, false
	}
	var body map[string]any
	if json.Unmarshal(infoResp.Body, &body) != nil || int(asInt64(body["code"])) != 0 {
		return false, false, false
	}
	data := asMap(body["data"])
	if data == nil {
		return false, false, false
	}
	exp, ch := applyAccountInfo(auth, data)
	return true, exp, ch
}

func (c *Client) checkClubSession(ctx context.Context, token string) (clubSessionInfo, error) {
	var info clubSessionInfo
	resp, err := c.http.DoJSON(ctx, http.MethodGet, c.endpoints.ClubBase+"/api/platform/session", nil, c.clubHeaders(token))
	if err != nil {
		return info, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return info, nil
	}
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("club session HTTP %d", resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return info, err
	}
	info.LoggedIn = asBool(parsed["logged_in"])
	binding := asMap(parsed["binding"])
	profile := asMap(parsed["profile"])
	if binding != nil && len(binding) > 0 {
		info.Bound = true
		info.ExplicitNick = strings.TrimSpace(asString(binding["nickname"]))
		info.FallbackName = firstNonEmptyString(
			asString(binding["name"]),
			asString(binding["platform_username"]),
		)
		info.Phone = strings.TrimSpace(asString(binding["bohr_phone"]))
	}
	if profile != nil {
		if info.ExplicitNick == "" {
			info.ExplicitNick = strings.TrimSpace(asString(profile["nickname"]))
		}
		if info.FallbackName == "" {
			info.FallbackName = firstNonEmptyString(
				asString(profile["name"]),
				asString(profile["username"]),
			)
		}
		if info.Email == "" {
			info.Email = strings.TrimSpace(asString(profile["email"]))
		}
		if info.Phone == "" {
			info.Phone = strings.TrimSpace(asString(profile["phone"]))
		}
	}
	return info, nil
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
