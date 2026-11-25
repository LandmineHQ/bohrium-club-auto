package bohrium

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ExpiringLot represents a batch of expiring bonus photons.
type ExpiringLot struct {
	ID          any     `json:"id"`
	Remaining   float64 `json:"remaining"`
	CreatedTime string  `json:"createdTime"`
	ExpireTime  string  `json:"expireTime"`
	SourceType  any     `json:"sourceType"`
	Description string  `json:"description,omitempty"`
}

// IntegralRecord represents one recent photon ledger entry.
type IntegralRecord struct {
	ID          any     `json:"id"`
	Amount      float64 `json:"amount"`
	Balance     float64 `json:"balance,omitempty"`
	Type        any     `json:"type,omitempty"`
	Description string  `json:"description"`
	CreatedAt   string  `json:"createdAt"`
}

// QuotaCounter holds remaining/total counts for a Bohrium feature quota.
type QuotaCounter struct {
	Remaining *int64 `json:"remaining"`
	Total     *int64 `json:"total"`
}

// StorageCapacity summarizes Bohrium knowledge-base/literature storage.
type StorageCapacity struct {
	RemainingBytes int64   `json:"remainingBytes"`
	UsedBytes      int64   `json:"usedBytes"`
	TotalBytes     int64   `json:"totalBytes"`
	UsedPercent    float64 `json:"usedPercent"`
	RemainingHuman string  `json:"remainingHuman"`
	UsedHuman      string  `json:"usedHuman"`
	TotalHuman     string  `json:"totalHuman"`
}

// PointsAndCapacity aggregates all three Bohrium asset categories for the Dashboard.
type PointsAndCapacity struct {
	FetchedAt time.Time `json:"fetchedAt"`

	// Category 1: Photons & Wallet
	PhotonBalance         float64          `json:"photonBalance"`
	OrgBalance            float64          `json:"orgBalance"`
	OrgBalanceLimit       float64          `json:"orgBalanceLimit"`
	WalletBalance         float64          `json:"walletBalance"`
	Expiring7DayPhotons   float64          `json:"expiring7DayPhotons"`
	ExpiringLots          []ExpiringLot    `json:"expiringLots"`
	RecentIntegralHistory []IntegralRecord `json:"recentIntegralHistory"`

	// Category 2: Membership & Feature Quotas
	MembershipLevel   any          `json:"membershipLevel"`
	MembershipExpire  string       `json:"membershipExpire"`
	MembershipPhotons float64      `json:"membershipPhotons"`
	NextBenefitDate   string       `json:"nextBenefitDate"`
	PaperQuota        QuotaCounter `json:"paperQuota"`
	KnowledgeChat     QuotaCounter `json:"knowledgeChat"`
	DeepResearch      QuotaCounter `json:"deepResearch"`

	// Category 3: Knowledge Base Storage Capacity
	Storage StorageCapacity `json:"storage"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetPointsAndCapacity concurrently queries Bohrium's points, quota, membership, and storage APIs.
func (c *Client) GetPointsAndCapacity(ctx context.Context) (*PointsAndCapacity, error) {
	auth := c.store.GetAuth()
	if auth.Token == "" {
		return nil, fmt.Errorf("请先登录 Bohrium 账号")
	}

	res := &PointsAndCapacity{
		FetchedAt:             time.Now().UTC(),
		ExpiringLots:          make([]ExpiringLot, 0),
		RecentIntegralHistory: make([]IntegralRecord, 0),
		Warnings:              make([]string, 0),
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	addWarn := func(msg string) {
		mu.Lock()
		res.Warnings = append(res.Warnings, msg)
		mu.Unlock()
	}

	headers := c.wwwHeaders(auth.Token, "assets")

	// 1. Photons balance (/bohrapi/v1/account/user/integral)
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/bohrapi/v1/account/user/integral", nil, headers)
		if err != nil {
			addWarn("光子余额查询失败: " + err.Error())
			return
		}
		mu.Lock()
		res.PhotonBalance = asFloat64(data["balance"])
		res.OrgBalance = asFloat64(data["orgBalance"])
		res.OrgBalanceLimit = asFloat64(data["orgBalanceLimit"])
		mu.Unlock()
	}()

	// 2. Wallet balance (/brm/v1/wallet/info)
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/brm/v1/wallet/info", nil, headers)
		if err != nil {
			addWarn("钱包余额查询失败: " + err.Error())
			return
		}
		mu.Lock()
		res.WalletBalance = asFloat64(data["balance"])
		mu.Unlock()
	}()

	// 3. 7-day expiring photons (/platform-gateway/v1/benefits/quota/info)
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/platform-gateway/v1/benefits/quota/info", nil, headers)
		if err != nil {
			addWarn("7日过期光子查询失败: " + err.Error())
			return
		}
		mu.Lock()
		res.Expiring7DayPhotons = asFloat64(data["extraExpire7DayAmount"])
		mu.Unlock()
	}()

	// 4. Expiring quota lots (/platform-gateway/v1/benefits/quota/expire_page)
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/platform-gateway/v1/benefits/quota/expire_page?page=1&pageSize=20", nil, headers)
		if err != nil {
			addWarn("光子过期批次查询失败: " + err.Error())
			return
		}
		items, _ := data["items"].([]any)
		if len(items) == 0 {
			items, _ = data["list"].([]any)
		}
		lots := make([]ExpiringLot, 0, len(items))
		for _, it := range items {
			m := asMap(it)
			if m == nil {
				continue
			}
			lots = append(lots, ExpiringLot{
				ID:          m["id"],
				Remaining:   asFloat64(m["remaining"]),
				CreatedTime: firstNonEmptyStr(m, "createdTime", "createTime", "created_at"),
				ExpireTime:  firstNonEmptyStr(m, "expireTime", "expireDate", "expired_at"),
				SourceType:  m["sourceType"],
				Description: firstNonEmptyStr(m, "description", "sourceName", "remark"),
			})
		}
		mu.Lock()
		res.ExpiringLots = lots
		mu.Unlock()
	}()

	// 5. Recent photon ledger (/bohrapi/v1/account/user/integral_page)
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := c.fetchBohriumData(ctx, http.MethodPost, c.endpoints.WWWBase+"/bohrapi/v1/account/user/integral_page", map[string]any{
			"page":        1,
			"pageSize":    15,
			"productLine": "Bohrium",
		}, headers)
		if err != nil {
			addWarn("光子流水查询失败: " + err.Error())
			return
		}
		items, _ := data["items"].([]any)
		if len(items) == 0 {
			items, _ = data["list"].([]any)
		}
		records := make([]IntegralRecord, 0, len(items))
		for _, it := range items {
			m := asMap(it)
			if m == nil {
				continue
			}
			amt := asFloat64(m["amount"])
			if amt == 0 {
				amt = asFloat64(m["changeAmount"])
			}
			if amt == 0 {
				amt = asFloat64(m["integral"])
			}
			records = append(records, IntegralRecord{
				ID:          m["id"],
				Amount:      amt,
				Balance:     asFloat64(m["balance"]),
				Type:        m["type"],
				Description: firstNonEmptyStr(m, "remark", "description", "reason", "title", "scene"),
				CreatedAt:   firstNonEmptyStr(m, "createTime", "createdTime", "createdAt", "created_at"),
			})
		}
		mu.Lock()
		res.RecentIntegralHistory = records
		mu.Unlock()
	}()

	// 6. Storage capacity (/bohrapi/v1/literature-sage/api/v1/file/capacity)
	wg.Add(1)
	go func() {
		defer wg.Done()
		data, err := c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/bohrapi/v1/literature-sage/api/v1/file/capacity", nil, headers)
		if err != nil {
			addWarn("知识库存储容量查询失败: " + err.Error())
			return
		}
		rem := asInt64(data["remainingCapacity"])
		used := asInt64(data["usedCapacity"])
		total := asInt64(data["totalCapacity"])
		if total == 0 && (rem > 0 || used > 0) {
			total = rem + used
		}
		pct := 0.0
		if total > 0 {
			pct = float64(used) * 100.0 / float64(total)
		}
		mu.Lock()
		res.Storage = StorageCapacity{
			RemainingBytes: rem,
			UsedBytes:      used,
			TotalBytes:     total,
			UsedPercent:    pct,
			RemainingHuman: formatBytes(rem),
			UsedHuman:      formatBytes(used),
			TotalHuman:     formatBytes(total),
		}
		mu.Unlock()
	}()

	// 7. Membership & feature quotas (/platform-gateway/v1/membership/info + seat/info)
	wg.Add(1)
	go func() {
		defer wg.Done()
		memData, memErr := c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/platform-gateway/v1/membership/info", nil, headers)
		if memErr != nil {
			addWarn("会员信息查询失败: " + memErr.Error())
		} else {
			mu.Lock()
			res.MembershipLevel = memData["level"]
			res.MembershipExpire = firstNonEmptyStr(memData, "expireDate", "expireTime")
			res.MembershipPhotons = asFloat64(memData["photonNumber"])
			applyApplicationQuotas(res, asMap(asMap(memData["benefitsInfo"])["application"]))
			mu.Unlock()
		}

		// Try seat/info first, then package/info as fallback
		seatData, seatErr := c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/platform-gateway/v1/benefits/seat/info", nil, headers)
		if seatErr != nil {
			seatData, seatErr = c.fetchBohriumData(ctx, http.MethodGet, c.endpoints.WWWBase+"/platform-gateway/v1/benefits/package/info", nil, headers)
		}
		if seatErr == nil && seatData != nil {
			mu.Lock()
			if d := firstNonEmptyStr(seatData, "nextBenefitDate"); d != "" {
				res.NextBenefitDate = d
			}
			if res.MembershipLevel == nil && seatData["level"] != nil {
				res.MembershipLevel = seatData["level"]
			}
			if p := asFloat64(asMap(seatData["photon"])["number"]); p > 0 && res.MembershipPhotons == 0 {
				res.MembershipPhotons = p
			}
			applyApplicationQuotas(res, asMap(seatData["application"]))
			mu.Unlock()
		}
	}()

	wg.Wait()
	return res, nil
}

func applyApplicationQuotas(res *PointsAndCapacity, app map[string]any) {
	if app == nil {
		return
	}
	if sn := asMap(app["sn"]); sn != nil {
		if q := parseQuotaCounter(sn["paper"]); q.Remaining != nil || q.Total != nil {
			res.PaperQuota = q
		}
		if q := parseQuotaCounter(sn["deep_research"]); q.Remaining != nil || q.Total != nil {
			res.DeepResearch = q
		}
	}
	if kb := asMap(app["knowledge_base"]); kb != nil {
		if q := parseQuotaCounter(kb["chat"]); q.Remaining != nil || q.Total != nil {
			res.KnowledgeChat = q
		}
	}
}

func parseQuotaCounter(v any) QuotaCounter {
	if m := asMap(v); m != nil {
		var q QuotaCounter
		if _, ok := m["remaining"]; ok {
			val := asInt64(m["remaining"])
			q.Remaining = &val
		}
		if _, ok := m["total"]; ok {
			val := asInt64(m["total"])
			q.Total = &val
		}
		return q
	}
	if v != nil {
		val := asInt64(v)
		return QuotaCounter{Remaining: &val}
	}
	return QuotaCounter{}
}

func (c *Client) fetchBohriumData(ctx context.Context, method, rawURL string, payload any, headers map[string]string) (map[string]any, error) {
	resp, err := c.http.DoJSON(ctx, method, rawURL, payload, headers)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("HTTP 401 未登录或登录已过期")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("invalid JSON")
	}
	if code := int(asInt64(parsed["code"])); code != 0 {
		return nil, fmt.Errorf("code=%d %s", code, asString(parsed["msg"]))
	}
	data := asMap(parsed["data"])
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

func firstNonEmptyStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch x := v.(type) {
			case string:
				if x != "" {
					return x
				}
			case float64:
				if x > 1e12 {
					return time.UnixMilli(int64(x)).Local().Format("2006-01-02 15:04:05")
				} else if x > 1e9 {
					return time.Unix(int64(x), 0).Local().Format("2006-01-02 15:04:05")
				}
			}
		}
	}
	return ""
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024.0
	f := float64(b)
	switch {
	case f >= unit*unit*unit:
		return fmt.Sprintf("%.2f GB", f/(unit*unit*unit))
	case f >= unit*unit:
		return fmt.Sprintf("%.2f MB", f/(unit*unit))
	case f >= unit:
		return fmt.Sprintf("%.2f KB", f/unit)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
