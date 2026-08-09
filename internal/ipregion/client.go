// Package ipregion 解析 IP 地域，对齐 Express utils/ipRegion.js：
//   - 本地 IP（127.0.0.1 / ::1 / ::ffff:127.0.0.1）直接返回 "本地"
//   - 否则外呼 https://ipapi.co/<ip>/json/（5s 超时），拼接 country_name · region · city
//   - 缓存用 hashicorp/golang-lru（上限 1000 条）+ 24h TTL（过期条目在命中时淘汰）
package ipregion

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// cacheTTL 是地域缓存有效期。
const cacheTTL = 24 * time.Hour

// cacheMax 是地域缓存上限（对齐 IP_REGION_CACHE_MAX）。
const cacheMax = 1000

// localIPs 是视为本地的 IP（对齐 ipRegion.js）。
var localIPs = map[string]bool{
	"127.0.0.1":        true,
	"::1":              true,
	"::ffff:127.0.0.1": true,
}

// cacheEntry 是缓存的单个地域条目（带过期时间）。
type cacheEntry struct {
	region    string
	expiresAt time.Time
}

// Client 解析 IP 地域；fetch 可注入（测试用）。
type Client struct {
	fetch func(ctx context.Context, ip string) string
	cache *lru.Cache[string, cacheEntry]
}

// NewClient 构造 IP 地域客户端。fetch 为空时使用默认 ipapi.co 外呼。
func NewClient(fetch func(ctx context.Context, ip string) string) *Client {
	c, err := lru.New[string, cacheEntry](cacheMax)
	if err != nil {
		// 构造参数固定合法，正常不会失败。
		c, _ = lru.New[string, cacheEntry](cacheMax)
	}
	cl := &Client{cache: c}
	if fetch != nil {
		cl.fetch = fetch
	} else {
		cl.fetch = fetchFromIPAPI
	}
	return cl
}

// GetRegion 返回 IP 地域，带 LRU + 24h TTL 缓存。
func (c *Client) GetRegion(ctx context.Context, ip string) string {
	if localIPs[ip] {
		return "本地"
	}
	if v, ok := c.cache.Get(ip); ok {
		if time.Now().Before(v.expiresAt) {
			return v.region
		}
		c.cache.Remove(ip) // 过期：淘汰并重新外呼
	}
	region := c.fetch(ctx, ip)
	c.cache.Add(ip, cacheEntry{region: region, expiresAt: time.Now().Add(cacheTTL)})
	return region
}

// fetchFromIPAPI 外呼 ipapi.co（5s 超时）。返回 "country_name · region · city" 或 "未知"。
func fetchFromIPAPI(ctx context.Context, ip string) string {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := "https://ipapi.co/" + ip + "/json/"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "未知"
	}
	req.Header.Set("User-Agent", "furry-drama-be-neo")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "未知"
	}
	defer resp.Body.Close()
	var data struct {
		CountryName string `json:"country_name"`
		Region      string `json:"region"`
		City        string `json:"city"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "未知"
	}
	parts := []string{}
	for _, p := range []string{data.CountryName, data.Region, data.City} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "未知"
	}
	return strings.Join(parts, " · ")
}
