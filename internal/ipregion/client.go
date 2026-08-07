// Package ipregion 解析 IP 地域，对齐 Express utils/ipRegion.js：
//   - 本地 IP（127.0.0.1 / ::1 / ::ffff:127.0.0.1）直接返回 "本地"
//   - 否则外呼 https://ipapi.co/<ip>/json/（5s 超时），拼接 country_name · region · city
//   - 24h LRU 缓存，上限 1000 条，超出按插入序删最旧
package ipregion

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// cacheTTL 是地域缓存有效期。
const cacheTTL = 24 * time.Hour

// cacheMax 是地域缓存上限（对齐 IP_REGION_CACHE_MAX）。
const cacheMax = 1000

// localIPs 是视为本地的 IP（对齐 ipRegion.js）。
var localIPs = map[string]bool{
	"127.0.0.1":      true,
	"::1":            true,
	"::ffff:127.0.0.1": true,
}

// Client 解析 IP 地域；fetch 可注入（测试用）。
type Client struct {
	fetch func(ctx context.Context, ip string) string
	mu    sync.Mutex
	cache map[string]cacheEntry
	// order 记录插入序，用于超限淘汰最旧。
	order []string
}

type cacheEntry struct {
	region    string
	expiresAt time.Time
}

// NewClient 构造 IP 地域客户端。fetch 为空时使用默认 ipapi.co 外呼。
func NewClient(fetch func(ctx context.Context, ip string) string) *Client {
	c := &Client{cache: make(map[string]cacheEntry)}
	if fetch != nil {
		c.fetch = fetch
	} else {
		c.fetch = fetchFromIPAPI
	}
	return c
}

// GetRegion 返回 IP 地域，带 24h LRU 缓存。
func (c *Client) GetRegion(ctx context.Context, ip string) string {
	if localIPs[ip] {
		return "本地"
	}
	// 命中缓存
	c.mu.Lock()
	if e, ok := c.cache[ip]; ok {
		c.mu.Unlock()
		if time.Now().Before(e.expiresAt) {
			return e.region
		}
		// 过期：删除，继续外呼
		c.mu.Lock()
		delete(c.cache, ip)
		c.mu.Unlock()
		return c.fetchAndCache(ctx, ip)
	}
	c.mu.Unlock()
	return c.fetchAndCache(ctx, ip)
}

func (c *Client) fetchAndCache(ctx context.Context, ip string) string {
	region := c.fetch(ctx, ip)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[ip] = cacheEntry{region: region, expiresAt: time.Now().Add(cacheTTL)}
	c.order = append(c.order, ip)
	// 超限淘汰最旧
	for len(c.order) > cacheMax {
		oldest := c.order[0]
		c.order = c.order[1:]
		if _, exists := c.cache[oldest]; exists && len(c.cache) > cacheMax {
			delete(c.cache, oldest)
		}
	}
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
