package bohrium

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bohrium-club-auto/internal/store"
)

// ClientLoginBenefitStatus describes eligibility for the one-time Bohrium desktop client
// first-login reward (10,000 photons, valid for 90 days).
type ClientLoginBenefitStatus struct {
	Eligible            bool      `json:"eligible"`
	PersonalAccount     bool      `json:"personalAccount"`
	WithinPeriod        bool      `json:"withinPeriod"`
	FirstLogin          bool      `json:"firstLogin"`
	HasOldActivityLogin bool      `json:"hasOldActivityLogin"`
	Claimed             bool      `json:"claimed"`
	RewardPhotons       int       `json:"rewardPhotons"`
	ValidDays           int       `json:"validDays"`
	Reason              string    `json:"reason,omitempty"`
	CheckedAt           time.Time `json:"checkedAt"`
}

// ClientLoginBenefitClaimResult describes the result of checking and claiming the one-time
// desktop client first-login photon benefit.
type ClientLoginBenefitClaimResult struct {
	Success        bool                      `json:"success"`
	Claimed        bool                      `json:"claimed"`
	AlreadyClaimed bool                      `json:"alreadyClaimed,omitempty"`
	Skipped        bool                      `json:"skipped,omitempty"`
	RewardPhotons  int                       `json:"rewardPhotons"`
	ValidDays      int                       `json:"validDays"`
	Message        string                    `json:"message"`
	Status         *ClientLoginBenefitStatus `json:"status,omitempty"`
}

// GetClientLoginBenefitStatus queries Bohrium desktop client's client-activity endpoint
// before any claim attempt.
func (c *Client) GetClientLoginBenefitStatus(ctx context.Context) (*ClientLoginBenefitStatus, error) {
	auth := c.store.GetAuth()
	if strings.TrimSpace(auth.Token) == "" {
		return nil, fmt.Errorf("请先登录 Bohrium 账号")
	}

	claims, err := ParseJWTClaims(auth.Token)
	personalAccount := false
	if err == nil {
		personalAccount = claims.IsPersonalAccountToken()
	}

	q := url.Values{}
	q.Set("channel", DesktopLoginChannel)
	u := c.endpoints.WWWBase + "/bohrapi/v1/sigma-search/api/v1/scix/membership/client-activity?" + q.Encode()

	headers := map[string]string{
		"Authorization": "Bearer " + auth.Token,
		"Accept":        "application/json",
		"Origin":        c.endpoints.WWWBase,
		"Referer":       c.endpoints.WWWBase + "/",
	}
	resp, err := c.http.DoJSON(ctx, http.MethodGet, u, nil, headers)
	if err != nil {
		return nil, fmt.Errorf("查询客户端首登活动状态失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("查询客户端首登活动状态 HTTP %d: %s", resp.StatusCode, truncateText(string(resp.Body), 180))
	}

	var parsed struct {
		Code    int            `json:"code"`
		Data    map[string]any `json:"data"`
		Message string         `json:"message"`
		Msg     string         `json:"msg"`
	}
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("解析客户端首登活动状态失败: %w", err)
	}
	if parsed.Code != 0 {
		msg := strings.TrimSpace(parsed.Message)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Msg)
		}
		return nil, fmt.Errorf("查询客户端首登活动状态错误 (code=%d): %s", parsed.Code, msg)
	}

	withinPeriod := asBool(parsed.Data["newUserBenefitWithinPeriod"])
	firstLogin := asBool(parsed.Data["newUserBenefitFirstLogin"])
	hasOldActivityLogin := asBool(parsed.Data["hasOldActivityLogin"])
	eligible := personalAccount && withinPeriod && firstLogin && !hasOldActivityLogin
	claimed := hasOldActivityLogin || (withinPeriod && !firstLogin)

	reason := ""
	switch {
	case !personalAccount:
		reason = "当前为机构或团队账号，仅个人版账号可领取首次登录客户端 10,000 光子礼包"
	case hasOldActivityLogin || (withinPeriod && !firstLogin):
		reason = "该账号已登录过客户端或已领取过首次登录客户端礼包"
	case !firstLogin:
		reason = "当前账号不符合客户端首次登录领取条件"
	case !withinPeriod:
		reason = "当前不在客户端首登礼包活动有效期内"
	}

	return &ClientLoginBenefitStatus{
		Eligible:            eligible,
		PersonalAccount:     personalAccount,
		WithinPeriod:        withinPeriod,
		FirstLogin:          firstLogin,
		HasOldActivityLogin: hasOldActivityLogin,
		Claimed:             claimed,
		RewardPhotons:       ClientLoginBenefitRewardPhotons,
		ValidDays:           ClientLoginBenefitValidDays,
		Reason:              reason,
		CheckedAt:           time.Now().UTC(),
	}, nil
}

// ClaimClientLoginPhotons verifies eligibility via GetClientLoginBenefitStatus first and only
// posts {"eventSubject":"client_register"} when all eligibility predicates hold.
func (c *Client) ClaimClientLoginPhotons(ctx context.Context, trigger string) (*ClientLoginBenefitClaimResult, error) {
	status, err := c.GetClientLoginBenefitStatus(ctx)
	if err != nil {
		return nil, err
	}
	if !status.Eligible {
		return &ClientLoginBenefitClaimResult{
			Success:        false,
			Claimed:        false,
			AlreadyClaimed: status.Claimed,
			Skipped:        true,
			RewardPhotons:  ClientLoginBenefitRewardPhotons,
			ValidDays:      ClientLoginBenefitValidDays,
			Message:        status.Reason,
			Status:         status,
		}, nil
	}

	auth := c.store.GetAuth()
	u := c.endpoints.PlatformGatewayBase + "/platform-gateway/v1/membership/receive_photon"
	payload := map[string]any{
		"eventSubject": ClientLoginBenefitEventSubject,
	}
	headers := map[string]string{
		"Authorization":      "Bearer " + auth.Token,
		"Accept":             "application/json",
		"X-Bohrium-Platform": "web",
		"Origin":             c.endpoints.WWWBase,
		"Referer":            c.endpoints.WWWBase + "/",
	}
	resp, err := c.http.DoJSON(ctx, http.MethodPost, u, payload, headers)
	if err != nil {
		c.appendBenefitLog(trigger, false, err.Error())
		return nil, fmt.Errorf("领取客户端首登光子请求失败: %w", err)
	}

	var parsed struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		c.appendBenefitLog(trigger, false, truncateText(string(resp.Body), 140))
		return nil, fmt.Errorf("解析客户端首登光子领取响应失败 (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || parsed.Code != 0 {
		msg := strings.TrimSpace(parsed.Message)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Msg)
		}
		if msg == "" {
			msg = strings.TrimSpace(parsed.Error)
		}
		if msg == "" {
			msg = truncateText(string(resp.Body), 160)
		}
		c.appendBenefitLog(trigger, false, fmt.Sprintf("HTTP %d code=%d: %s", resp.StatusCode, parsed.Code, msg))
		return nil, fmt.Errorf("领取客户端首登光子失败 (HTTP %d, code=%d): %s", resp.StatusCode, parsed.Code, msg)
	}

	status.Eligible = false
	status.Claimed = true
	status.FirstLogin = false
	status.Reason = "该账号已登录过客户端或已领取过首次登录客户端礼包"
	c.appendBenefitLog(trigger, true, fmt.Sprintf("已成功领取首次登录客户端 %d 光子（有效期 %d 天）", ClientLoginBenefitRewardPhotons, ClientLoginBenefitValidDays))

	return &ClientLoginBenefitClaimResult{
		Success:       true,
		Claimed:       true,
		RewardPhotons: ClientLoginBenefitRewardPhotons,
		ValidDays:     ClientLoginBenefitValidDays,
		Message:       fmt.Sprintf("已成功领取首次登录客户端礼包：%d 光子（%d 天有效）", ClientLoginBenefitRewardPhotons, ClientLoginBenefitValidDays),
		Status:        status,
	}, nil
}

func (c *Client) appendBenefitLog(trigger string, success bool, msg string) {
	if c.store == nil {
		return
	}
	_ = c.store.AppendLog(store.RunLog{
		Trigger: nonEmpty(strings.TrimSpace(trigger), "manual"),
		Action:  "client_login_benefit",
		TaskKey: TaskKeyClientFirstLogin,
		Success: success,
		Message: msg,
	})
}
