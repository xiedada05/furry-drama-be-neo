package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// rssPubDateLayout 对齐 JS Date.prototype.toUTCString() 输出
// （如 "Sun, 10 Aug 2026 12:34:56 GMT"）。
const rssPubDateLayout = "Mon, 02 Jan 2006 15:04:05 GMT"

// rssSevenDays 是 RSS 最近更新窗口（7 天，对齐 routes/rss.js sevenDaysAgo）。
const rssSevenDays = 7 * 24 * time.Hour

// RSS 是 RSS 订阅域（/api/rss）handler 容器，行为逐端点对齐 backend/routes/rss.js。
type RSS struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewRSS 构造 RSS handler 容器。
func NewRSS(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc) *RSS {
	return &RSS{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 RSS 路由（路径照抄 Express 子路径，不含 /api 前缀）。
// GET / 同时注册 "" 与 "/" 以覆盖 /api/rss 与 /api/rss/（Express 默认 strict routing=false
// 两者均 200；gin 默认 RedirectTrailingSlash 会 301，故显式双注册）。
func (h *RSS) Register(g *gin.RouterGroup) {
	g.GET("", h.Feed)
	g.GET("/", h.Feed)
	// superAdminProtect（仅 superadmin，对齐 rss.js 第 63 行）。
	g.GET("/api-usage", h.AuthMW.Protect(middleware.RoleSuperAdmin), h.ApiUsage)
}

// Feed GET /api/rss（公开 RSS 订阅输出）。
// @Summary RSS 订阅（近 7 天更新剧集/单集）
// @Tags RSS
// @Success 200 {string} string "application/xml RSS 2.0 feed"
// @Router /rss [get]
func (h *RSS) Feed(c *gin.Context) {
	siteURL := h.Config.Server.SiteURL
	if siteURL == "" {
		siteURL = "http://localhost:3000"
	}
	siteName, siteDesc := h.loadSiteMeta(c.Request.Context())
	sevenDaysAgo := time.Now().Add(-rssSevenDays)

	eps, err := h.Repos.Episodes.RSSFindRecent(c.Request.Context(), sevenDaysAgo)
	if err != nil {
		serverError(c)
		return
	}
	singles, err := h.Repos.SingleEpisodes.RSSFindRecent(c.Request.Context(), sevenDaysAgo)
	if err != nil {
		serverError(c)
		return
	}

	var b strings.Builder
	for i := range eps {
		ep := &eps[i]
		status := "即将上映"
		if ep.Status == "ongoing" {
			status = "连载中"
		} else if ep.Status == "completed" {
			status = "已完结"
		}
		total := "null"
		if ep.TotalEpisodes != nil {
			total = strconv.Itoa(*ep.TotalEpisodes)
		}
		b.WriteString("<item><title>")
		b.WriteString(escapeXML(ep.Title))
		b.WriteString(" - 更新至第")
		b.WriteString(strconv.Itoa(ep.CurrentEpisodes))
		b.WriteString("集</title><link>")
		b.WriteString(siteURL)
		b.WriteString("/episode/")
		b.WriteString(ep.ID.Hex())
		b.WriteString("</link><description>状态：")
		b.WriteString(status)
		b.WriteString("，共")
		b.WriteString(total)
		b.WriteString("集</description><pubDate>")
		b.WriteString(ep.UpdatedAt.UTC().Format(rssPubDateLayout))
		b.WriteString("</pubDate></item>")
	}

	if len(singles) > 0 {
		ids := make([]primitive.ObjectID, 0, len(singles))
		for i := range singles {
			ids = append(ids, singles[i].EpisodeID)
		}
		titles, err := h.Repos.Episodes.RSSFindEpisodeTitles(c.Request.Context(), ids)
		if err != nil {
			serverError(c)
			return
		}
		for i := range singles {
			se := &singles[i]
			title, ok := titles[se.EpisodeID.Hex()]
			if !ok {
				// 被引用剧集已删除 → populate('episodeId') 得 null → 跳过（对齐 rss.js:52）。
				continue
			}
			b.WriteString("<item><title>")
			b.WriteString(escapeXML(title))
			b.WriteString(" 第")
			b.WriteString(strconv.Itoa(se.EpisodeNumber))
			b.WriteString("集更新</title><link>")
			b.WriteString(siteURL)
			b.WriteString("/episode/")
			b.WriteString(se.EpisodeID.Hex())
			b.WriteString("</link><description>")
			b.WriteString(escapeXML(se.Title))
			b.WriteString("</description><pubDate>")
			b.WriteString(se.CreatedAt.UTC().Format(rssPubDateLayout))
			b.WriteString("</pubDate></item>")
		}
	}

	xml := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>` +
		escapeXML(siteName) + ` - 更新订阅</title><link>` + siteURL +
		`</link><description>` + escapeXML(siteDesc) + `</description><language>zh-CN</language>` +
		b.String() + `</channel></rss>`
	c.Header("Content-Type", "application/xml; charset=utf-8")
	c.String(http.StatusOK, xml)
}

// ApiUsage GET /api/rss/api-usage（superAdminProtect）。
// @Summary 接口调用统计
// @Tags RSS
// @Security bearerAuth
// @Param days query int false "统计天数（默认 7）"
// @Success 200 {object} map[string]any "dailyTotals/topEndpoints/raw"
// @Router /rss/api-usage [get]
func (h *RSS) ApiUsage(c *gin.Context) {
	days := 7.0
	if ds := c.Query("days"); ds != "" {
		f, err := strconv.ParseFloat(ds, 64)
		if err != nil {
			// days 非法 → new Date(NaN).toISOString() 抛错 → catch → 500。
			serverError(c)
			return
		}
		days = f
	}
	since := time.Now().Add(-time.Duration(days * 24 * float64(time.Hour))).UTC().Format("2006-01-02")
	usage, err := h.Repos.ApiUsage.RSSFindSince(c.Request.Context(), since)
	if err != nil {
		serverError(c)
		return
	}

	dailyTotals := map[string]int64{}
	topEndpoints := map[string]int64{}
	for _, u := range usage {
		date, _ := u["date"].(string)
		dailyTotals[date] += rssDocCount(u)
		ep, _ := u["endpoint"].(string)
		topEndpoints[ep] += rssDocCount(u)
	}

	type epCount struct {
		name  string
		count int64
	}
	sorted := make([]epCount, 0, len(topEndpoints))
	for k, v := range topEndpoints {
		sorted = append(sorted, epCount{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
	if len(sorted) > 20 {
		sorted = sorted[:20]
	}
	// 对齐 Object.entries(topEndpoints).sort(...).slice(0,20)：输出 [endpoint, count] 对数组。
	top := make([][2]any, 0, len(sorted))
	for _, e := range sorted {
		top = append(top, [2]any{e.name, e.count})
	}

	raw := make([]any, 0, len(usage))
	for i, u := range usage {
		if i >= 100 {
			break
		}
		raw = append(raw, bsonJSONValue(u))
	}
	c.JSON(200, gin.H{"dailyTotals": dailyTotals, "topEndpoints": top, "raw": raw})
}

// loadSiteMeta 读取站点名称与描述（SiteContent 'settings'/'about'），
// 对齐 rss.js:24-35：单个 try/catch 包裹——settings 存在但 JSON.parse 抛错或 DB 错误时
// 跳过 about 读取；settings 缺失/无 siteName 时正常继续读 about。
func (h *RSS) loadSiteMeta(ctx context.Context) (siteName, siteDesc string) {
	siteName = "兽剧聚合平台"
	siteDesc = "兽剧内容聚合平台"
	continueAbout := true
	if doc, err := h.Repos.SiteContents.FindByKey(ctx, "settings"); err == nil {
		var s struct {
			SiteName string `json:"siteName"`
		}
		if err := json.Unmarshal([]byte(doc.Content), &s); err != nil {
			continueAbout = false // JSON.parse 抛错 → catch → 跳过 about
		} else if s.SiteName != "" {
			siteName = s.SiteName
		}
	} else if !repository.IsNotFound(err) {
		continueAbout = false // DB 错误 → catch → 跳过 about
	}
	if continueAbout {
		if doc, err := h.Repos.SiteContents.FindByKey(ctx, "about"); err == nil {
			var a struct {
				Description string `json:"description"`
			}
			if json.Unmarshal([]byte(doc.Content), &a) == nil && a.Description != "" {
				siteDesc = a.Description
			}
		}
	}
	return siteName, siteDesc
}

// escapeXML 对齐 rss.js escapeXml：转义 & < > " '；空串原样返回。
func escapeXML(s string) string {
	if s == "" {
		return ""
	}
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(s)
}

// rssDocCount 从 api-usage 原始文档提取 count（int64）。
func rssDocCount(m bson.M) int64 {
	switch v := m["count"].(type) {
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	}
	return 0
}
