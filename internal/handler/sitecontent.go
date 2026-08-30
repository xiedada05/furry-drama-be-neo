package handler

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/upload"
)

// 本文件实现 /api/site-content 子域（行为逐分支照抄 backend/routes/siteContent.js）。
//
// 端点角色：POST /upload / PUT /:key / GET / / POST /test-email 为
// superAdminProtect；GET /:key 公开但 key=email 时叠加 superAdminProtect
// （Express 内联中间件）；GET /pwa-manifest 公开。

// defaultSiteContentKey 是默认内容的 key 顺序（对齐 DEFAULT_CONTENT 的
// Object.entries 插入序）。
var defaultSiteContentKey = []string{"privacy", "terms", "about", "settings"}

// defaultSiteContent 对齐 siteContent.js DEFAULT_CONTENT（逐字照抄 content JSON）。
var defaultSiteContent = map[string]struct{ title, content string }{
	"privacy": {
		"隐私政策",
		"请在此编辑隐私政策内容。",
	},
	"terms": {
		"用户协议",
		"请在此编辑用户协议内容。",
	},
	"about": {
		"关于我们",
		`{"banner":"","logo":"","description":"","version":"1.0.0","updates":[],"changelog":[{"version":"1.0.0","date":"2026-05-02","items":["平台上线"]}],"icp":"","policeRecord":"","aiDisclaimer":"本网站部分内容由AI生成","copyright":"© 2026 09兽"}`,
	},
	"settings": {
		"站点设置",
		`{"siteName":"兽剧聚合平台","navLogo":"","welcomeTitle":"欢迎来到兽剧聚合平台","welcomeSubtitle":"发现和追踪你喜爱的兽剧内容","favicon":"","browserTitle":"兽剧聚合平台","pwaName":"兽剧聚合平台","pwaShortName":"兽剧","pwaDescription":"兽剧内容聚合平台 - 发现和追踪你喜爱的兽剧内容","pwaIcon192":"","pwaIcon512":"","pwaMaskableIcon":"","pwaBackgroundColor":"#0f172a","pwaThemeColor":"#6366f1"}`,
	},
}

// SiteContents 是 /api/site-content 域 handler 容器。
type SiteContents struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	Mail   *email.Client
}

// NewSiteContents 构造站点内容 handler 容器。mail 为邮件客户端（可为 nil，跳过 test-email）。
func NewSiteContents(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc, mail *email.Client) *SiteContents {
	return &SiteContents{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, Mail: mail}
}

// Register 挂载 /api/site-content 全部端点（不含 /api 前缀；路径对齐
// siteContent.js 子路径）。静态路由（upload/pwa-manifest/test-email）在
// 参数路由（/:key）之前注册，满足 gin 的 radix 树约束。
func (h *SiteContents) Register(g *gin.RouterGroup) {
	superadmin := []string{middleware.RoleSuperAdmin}
	g.POST("/upload", h.AuthMW.Protect(superadmin...), h.Upload)
	g.GET("/pwa-manifest", h.PWAManifest)
	g.GET("/:key", h.emailOnly, h.GetByKey)
	g.PUT("/:key", h.AuthMW.Protect(superadmin...), h.UpdateByKey)
	g.GET("", h.AuthMW.Protect(superadmin...), h.List)
	g.GET("/", h.AuthMW.Protect(superadmin...), h.List)
	g.POST("/test-email", h.AuthMW.Protect(superadmin...), h.TestEmail)
}

// emailOnly 对齐 siteContent.js GET /:key 的内联中间件：key=email 时先过
// superAdminProtect，其余 key 直接放行。
func (h *SiteContents) emailOnly(c *gin.Context) {
	if c.Param("key") != "email" {
		c.Next()
		return
	}
	h.AuthMW.Protect(middleware.RoleSuperAdmin)(c)
}

// Upload POST /api/site-content/upload（superAdminProtect + image ≤5MB）。
// @Summary 上传站点图片
// @Tags 站点内容
// @Security bearerAuth
// @Accept multipart/form-data
// @Param image formData file true "图片（≤5MB）"
// @Success 200 {object} map[string]string "url"
// @Router /site-content/upload [post]
func (h *SiteContents) Upload(c *gin.Context) {
	url, err := upload.SaveImage(c, "image", "site", 5<<20)
	if err != nil {
		if err == upload.ErrNoFile {
			c.JSON(400, gin.H{"message": "请选择要上传的图片"})
			return
		}
		if ue, ok := err.(*errors.UploadError); ok {
			switch ue.Code {
			case "LIMIT_FILE_SIZE":
				c.JSON(400, gin.H{"message": "文件大小不能超过5MB"})
			case "LIMIT_FILE_TYPE":
				c.JSON(400, gin.H{"message": "仅支持图片文件 (jpg, jpeg, png, gif, webp)"})
			case "BAD_MAGIC":
				c.JSON(400, gin.H{"message": "文件内容与类型不匹配，仅支持图片文件"})
			default:
				c.JSON(400, gin.H{"message": "文件上传错误"})
			}
			return
		}
		msg := err.Error()
		if msg == "" {
			msg = "文件上传失败"
		}
		c.JSON(400, gin.H{"message": msg})
		return
	}
	c.JSON(200, gin.H{"url": url})
}

// PWAManifest GET /api/site-content/pwa-manifest（公开）。
// 根据站点设置动态返回 PWA manifest，使浏览器安装提示展示自定义名称和图标。
// @Summary PWA manifest
// @Tags 站点内容
// @Success 200 {object} map[string]any "PWA manifest"
// @Router /site-content/pwa-manifest [get]
func (h *SiteContents) PWAManifest(c *gin.Context) {
	ctx := c.Request.Context()
	var settingsData map[string]any
	content, err := h.Repos.SiteContents.FindByKey(ctx, "settings")
	if repository.IsNotFound(err) {
		content = h.newDefaultContent("settings")
		if err := h.Repos.SiteContents.SiteContentsCreate(ctx, content); err != nil {
			serverError(c)
			return
		}
	} else if err != nil {
		serverError(c)
		return
	}
	if content != nil && content.Content != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(content.Content), &parsed); err == nil {
			settingsData = parsed
		}
	}

	s := settingsData
	if s == nil {
		s = map[string]any{}
	}
	pwaName := strOr(s["pwaName"], strOr(s["siteName"], "兽剧聚合平台"))
	pwaShortName := strOr(s["pwaShortName"], truncRunes(pwaName, 12))
	pwaDescription := strOr(s["pwaDescription"], "兽剧内容聚合平台 - 发现和追踪你喜爱的兽剧内容")
	pwaThemeColor := strOr(s["pwaThemeColor"], "#6366f1")
	pwaBackgroundColor := strOr(s["pwaBackgroundColor"], "#0f172a")
	pwaIcon192 := strOr(s["pwaIcon192"], "/icon-192x192.png")
	pwaIcon512 := strOr(s["pwaIcon512"], "/icon-512x512.png")
	pwaMaskableIcon := strOr(s["pwaMaskableIcon"], pwaIcon512)

	manifest := gin.H{
		"name":             pwaName,
		"short_name":       pwaShortName,
		"description":      pwaDescription,
		"start_url":        "/",
		"display":          "standalone",
		"orientation":      "any",
		"background_color": pwaBackgroundColor,
		"theme_color":      pwaThemeColor,
		"categories":       []string{"entertainment", "social"},
		"scope":            "/",
		"icons": []gin.H{
			{"src": pwaIcon192, "sizes": "192x192", "type": "image/png"},
			{"src": pwaIcon512, "sizes": "512x512", "type": "image/png"},
			{"src": pwaMaskableIcon, "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
		"shortcuts": []gin.H{
			{"name": "最新剧集", "short_name": "最新", "url": "/?tab=latest",
				"icons": []gin.H{{"src": pwaIcon192, "sizes": "192x192"}}},
			{"name": "搜索", "short_name": "搜索", "url": "/?action=search",
				"icons": []gin.H{{"src": pwaIcon192, "sizes": "192x192"}}},
		},
	}
	c.Header("Content-Type", "application/manifest+json")
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(200, manifest)
}

// GetByKey GET /api/site-content/:key（公开，key=email 需 superAdmin）。
// 不存在且存在默认内容时自动创建；email 键返回 ini/env 的当前配置（pass 脱敏）。
// @Summary 获取站点内容
// @Tags 站点内容
// @Param key path string true "内容键（privacy/terms/about/settings/email）"
// @Success 200 {object} map[string]any "站点内容文档"
// @Failure 404 {object} map[string]string "Content not found"
// @Router /site-content/{key} [get]
func (h *SiteContents) GetByKey(c *gin.Context) {
	key := c.Param("key")
	ctx := c.Request.Context()
	if key == "email" {
		c.JSON(200, h.emailConfigJSON())
		return
	}
	content, err := h.Repos.SiteContents.FindByKey(ctx, key)
	if repository.IsNotFound(err) {
		if _, ok := defaultSiteContent[key]; ok {
			content = h.newDefaultContent(key)
			if err := h.Repos.SiteContents.SiteContentsCreate(ctx, content); err != nil {
				serverError(c)
				return
			}
		} else {
			c.JSON(404, gin.H{"message": "Content not found"})
			return
		}
	} else if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, siteContentJSON(content))
}

// emailConfigJSON 返回当前邮件服务配置（来自 ini/env，pass 脱敏，仅供查询）。
func (h *SiteContents) emailConfigJSON() gin.H {
	e := h.Config.Email
	enabled := e.Host != "" && e.User != "" && e.Pass != ""
	content, _ := json.Marshal(map[string]any{
		"host":     e.Host,
		"port":     strconv.Itoa(e.Port),
		"user":     e.User,
		"pass":     "", // 脱敏：真实密码仅存于服务器配置，不回传
		"fromName": e.FromName,
		"enabled":  enabled,
	})
	return gin.H{
		"key":     "email",
		"title":   "邮件服务",
		"content": string(content),
	}
}

// UpdateByKey PUT /api/site-content/:key（superAdminProtect）。
// 更新标题/内容；email 键已禁用（配置走环境变量/配置文件，仅可 GET 查询）。
// @Summary 更新站点内容
// @Tags 站点内容
// @Security bearerAuth
// @Accept json
// @Param key path string true "内容键"
// @Param body body object true "title/content"
// @Success 200 {object} map[string]any "站点内容文档"
// @Router /site-content/{key} [put]
func (h *SiteContents) UpdateByKey(c *gin.Context) {
	key := c.Param("key")
	if key == "email" {
		c.JSON(400, gin.H{"message": "邮件服务配置已改为通过环境变量或配置文件设置，请勿在此修改"})
		return
	}
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	contentVal, hasContent := body["content"]
	processed := asString(contentVal)
	update := bson.M{"updatedAt": time.Now().UTC().Truncate(time.Millisecond)}
	if titleVal, ok := body["title"]; ok {
		update["title"] = asString(titleVal)
	}
	if hasContent {
		update["content"] = processed
	}
	updated, err := h.Repos.SiteContents.SiteContentsFindOneAndUpdate(c.Request.Context(), key, bson.M{"$set": update})
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, siteContentJSON(updated))
}

// List GET /api/site-content（superAdminProtect）。
// 返回全部站点内容；缺失的默认键自动创建并追加（对齐 DEFAULT_CONTENT 遍历）。
// @Summary 站点内容列表
// @Tags 站点内容
// @Security bearerAuth
// @Success 200 {array} map[string]any "站点内容文档数组"
// @Router /site-content [get]
func (h *SiteContents) List(c *gin.Context) {
	ctx := c.Request.Context()
	contents, err := h.Repos.SiteContents.SiteContentsFindAll(ctx)
	if err != nil {
		serverError(c)
		return
	}
	for _, key := range defaultSiteContentKey {
		found := false
		for i := range contents {
			if contents[i].Key == key {
				found = true
				break
			}
		}
		if !found {
			created := h.newDefaultContent(key)
			if err := h.Repos.SiteContents.SiteContentsCreate(ctx, created); err != nil {
				serverError(c)
				return
			}
			contents = append(contents, *created)
		}
	}
	out := make([]gin.H, 0, len(contents))
	for i := range contents {
		out = append(out, siteContentJSON(&contents[i]))
	}
	c.JSON(200, out)
}

// TestEmail POST /api/site-content/test-email（superAdminProtect）。
// 用请求体中的 SMTP 配置发送测试邮件。
// @Summary 发送测试邮件
// @Tags 站点内容
// @Security bearerAuth
// @Accept json
// @Param body body object true "host/port/user/pass/fromName/to"
// @Success 200 {object} map[string]string "测试邮件发送成功，请检查收件箱"
// @Failure 400 {object} map[string]string "配置缺失或发送失败"
// @Router /site-content/test-email [post]
func (h *SiteContents) TestEmail(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	host := asString(body["host"])
	user := asString(body["user"])
	pass := asString(body["pass"])
	to := asString(body["to"])
	if host == "" || user == "" || pass == "" || to == "" {
		c.JSON(400, gin.H{"message": "请填写完整的邮件服务配置和收件地址"})
		return
	}
	port := 465
	if p, err := strconv.Atoi(orDefaultString(body["port"], "465")); err == nil {
		port = p
	}
	fromName := orDefaultString(body["fromName"], "")
	if h.Mail == nil {
		c.JSON(400, gin.H{"message": "邮件发送失败，请检查邮件服务配置"})
		return
	}
	if err := h.Mail.SendSiteTestEmail(c.Request.Context(), host, port, user, pass, fromName, to); err != nil {
		slog.Error("[Email] test-email send failed", "host", host, "port", port, "to", to, "err", err)
		c.JSON(400, gin.H{"message": "邮件发送失败，请检查邮件服务配置"})
		return
	}
	c.JSON(200, gin.H{"message": "测试邮件发送成功，请检查收件箱"})
}

// ---- helpers ----

// newDefaultContent 构造默认站点内容文档（对齐 DEFAULT_CONTENT[key]）。
func (h *SiteContents) newDefaultContent(key string) *model.SiteContent {
	def, ok := defaultSiteContent[key]
	if !ok {
		return nil
	}
	return &model.SiteContent{Key: key, Title: def.title, Content: def.content}
}

// siteContentJSON 组装站点内容文档（对齐 mongoose toObject）。
func siteContentJSON(sc *model.SiteContent) gin.H {
	return gin.H{
		"_id":       sc.ID.Hex(),
		"key":       sc.Key,
		"title":     sc.Title,
		"content":   sc.Content,
		"updatedAt": sc.UpdatedAt,
		"__v":       sc.VersionKey,
	}
}

// strOr 取字符串值，非字符串/空串回退默认（对齐 `s.x || fallback` 真值语义）。
func strOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

// truncRunes 截取前 n 个字符（对齐 String.prototype.slice(0, n)）。
func truncRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n])
	}
	return s
}
