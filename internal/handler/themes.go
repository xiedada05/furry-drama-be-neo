package handler

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/upload"
)

// 本文件实现 /api/themes 主题管理子域。
//
// 主题是「壁纸 + 图标」的组合外观包（仅壁纸 / 仅图标 / 全套），不含按钮调色。
//
// 公开：GET /active（站点默认主题）、GET /list（可选系统主题）；
// 登录用户：GET /my、GET /my/selection、PUT /selection、POST /、
// PUT/DELETE /:id、POST /:id/submit（提交审核）、
// POST /upload-wallpaper、POST /upload-icon（主题资源上传）；
// 超管：GET /all、POST /:id/review（审核）、POST /:id/default（设默认）、
// PUT /:id/admin（系统/个人切换、启用状态）。

// Themes 是主题域（/api/themes）handler 容器。
type Themes struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewThemes 构造主题 handler 容器。
func NewThemes(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc) *Themes {
	return &Themes{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/themes 全部端点。
func (h *Themes) Register(g *gin.RouterGroup) {
	super := h.AuthMW.Protect(middleware.RoleSuperAdmin)
	g.GET("/active", h.Active)
	g.GET("/list", h.List)
	g.GET("/my", h.AuthMW.Protect(), h.MyList)
	g.GET("/my/selection", h.AuthMW.Protect(), h.MySelection)
	g.PUT("/selection", h.AuthMW.Protect(), h.SetSelection)
	g.POST("/", h.AuthMW.Protect(), h.Create)
	g.POST("/upload-wallpaper", h.AuthMW.Protect(), h.UploadWallpaper)
	g.POST("/upload-icon", h.AuthMW.Protect(), h.UploadIcon)
	g.PUT("/:id", h.AuthMW.Protect(), h.Update)
	g.DELETE("/:id", h.AuthMW.Protect(), h.Delete)
	g.POST("/:id/submit", h.AuthMW.Protect(), h.Submit)
	g.GET("/all", super, h.AdminAll)
	g.POST("/:id/review", super, h.Review)
	g.POST("/:id/default", super, h.SetDefault)
	g.PUT("/:id/admin", super, h.AdminUpdate)
}

// themeIconsOr 规范化 icons 为非 nil map。
func themeIconsOr(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// themeJSON 组装主题响应对象（对齐项目 DTO 组装风格）。
func themeJSON(t *model.Theme) gin.H {
	ownerID := ""
	if t.OwnerID != nil {
		ownerID = t.OwnerID.Hex()
	}
	return gin.H{
		"_id":            t.ID.Hex(),
		"name":           t.Name,
		"description":    t.Description,
		"wallpaperUrl":   t.WallpaperURL,
		"wallpaperThumb": t.WallpaperThumb,
		"icons":          themeIconsOr(t.Icons),
		"accentColor":    t.AccentColor,
		"themeType":      t.ThemeType(),
		"isSystem":       t.IsSystem,
		"ownerId":        ownerID,
		"status":         t.Status,
		"reviewNote":     t.ReviewNote,
		"isDefault":      t.IsDefault,
		"enabled":        t.Enabled,
		"createdAt":      t.CreatedAt,
		"updatedAt":      t.UpdatedAt,
		"__v":            t.VersionKey,
	}
}

// themePublicJSON 组装主题公开视图（GET /list / /active，隐藏审核备注与作者）。
func themePublicJSON(t *model.Theme) gin.H {
	return gin.H{
		"_id":            t.ID.Hex(),
		"name":           t.Name,
		"description":    t.Description,
		"wallpaperUrl":   t.WallpaperURL,
		"wallpaperThumb": t.WallpaperThumb,
		"icons":          themeIconsOr(t.Icons),
		"accentColor":    t.AccentColor,
		"themeType":      t.ThemeType(),
		"isSystem":       t.IsSystem,
		"isDefault":      t.IsDefault,
	}
}

// themeAssetURLAllowed 判断是否为合法主题资源地址：站内 /uploads/ 或 http(s) 外链。
func themeAssetURLAllowed(s string) bool {
	return strings.HasPrefix(s, "/uploads/") ||
		strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// sanitizeThemeAssetURL 校验壁纸地址（允许空串 = 清除壁纸）。
func sanitizeThemeAssetURL(v any) (string, string) {
	s, ok := v.(string)
	if !ok {
		return "", "壁纸地址必须为字符串"
	}
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return "", "壁纸地址过长"
	}
	if s != "" && !themeAssetURLAllowed(s) {
		return "", "壁纸地址不合法"
	}
	return s, ""
}

// sanitizeThemeAccentColor 校验主题强调色（空串 = 不设置；否则必须 #rrggbb）。
func sanitizeThemeAccentColor(v any) (string, string) {
	s, ok := v.(string)
	if !ok {
		return "", "主题色必须为字符串"
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	if !themeHexColorRe.MatchString(s) {
		return "", "主题色格式不合法（须为 #rrggbb）"
	}
	return s, ""
}

// themeHexColorRe 合法主题色正则（#rrggbb）。
var themeHexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// themeProtectedIconKeys 是不允许被主题覆盖的组件标识（站点身份类图标，
// 如站点 Logo：主题是用户外观包，不应篡改站点品牌资产）。
var themeProtectedIconKeys = map[string]bool{
	"misc.logo": true,
}

// sanitizeThemeIcons 校验并规范化主题图标映射：
//   - key 必须是合法组件标识（复用图标映射正则，如 nav.home），
//     且不在 themeProtectedIconKeys 保护名单内（站点 Logo 等不可覆盖）；
//   - value 必须是 /uploads/ 或 http(s) 资源地址；
//   - 最多 64 个映射。
func sanitizeThemeIcons(input map[string]any) (map[string]string, string) {
	if input == nil {
		return map[string]string{}, ""
	}
	if len(input) > 64 {
		return nil, "主题图标数量不能超过 64 个"
	}
	out := make(map[string]string, len(input))
	for k, v := range input {
		if !iconMappingKeyRe.MatchString(k) {
			return nil, "图标映射标识不合法: " + k
		}
		if themeProtectedIconKeys[k] {
			return nil, "该图标不允许通过主题覆盖: " + k
		}
		s, ok := v.(string)
		if !ok {
			return nil, "图标地址必须为字符串: " + k
		}
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 500 {
			return nil, "图标地址不合法: " + k
		}
		if !themeAssetURLAllowed(s) {
			return nil, "图标地址不合法: " + k
		}
		out[k] = s
	}
	return out, ""
}

// isSuperUser 判断当前登录用户是否超管。
func isSuperUser(c *gin.Context) bool {
	if u, ok := middleware.GetUser(c); ok {
		return u.Role == middleware.RoleSuperAdmin
	}
	return false
}

// Active GET /api/themes/active：获取站点默认主题（公开，全站加载时调用）。
func (h *Themes) Active(c *gin.Context) {
	t, err := h.Repos.Themes.FindDefault(c.Request.Context())
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(200, gin.H{"theme": nil})
			return
		}
		c.JSON(500, gin.H{"message": "获取默认主题失败"})
		return
	}
	c.JSON(200, gin.H{"theme": themePublicJSON(t)})
}

// List GET /api/themes/list：获取全部可选系统主题（公开）。
func (h *Themes) List(c *gin.Context) {
	list, err := h.Repos.Themes.FindSystemEnabled(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"message": "获取主题列表失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, themePublicJSON(&list[i]))
	}
	c.JSON(200, out)
}

// MyList GET /api/themes/my：获取我的全部个人主题。
func (h *Themes) MyList(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	list, err := h.Repos.Themes.FindByOwner(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "获取个人主题失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, themeJSON(&list[i]))
	}
	c.JSON(200, out)
}

// accessibleTheme 校验主题对当前用户是否可用（自己的个人主题 / 启用的系统主题），
// 不可用返回 nil。
func (h *Themes) accessibleTheme(c *gin.Context, t *model.Theme) bool {
	if t == nil {
		return false
	}
	if t.IsSystem {
		return t.Enabled || isSuperUser(c)
	}
	if u, ok := middleware.GetUser(c); ok {
		return t.OwnerID != nil && *t.OwnerID == u.ID
	}
	return false
}

// MySelection GET /api/themes/my/selection：获取当前用户生效主题（用户选择 > 站点默认）。
// 响应含 applyIcons / applyWallpaper（用户勾选的应用组合，用于前端图标引擎）；
// 未选择主题而回落站点默认主题时附带 isDefaultFallback=true（不视为用户的选择）。
func (h *Themes) MySelection(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	if !user.ThemeID.IsZero() {
		t, err := h.Repos.Themes.FindByID(c.Request.Context(), user.ThemeID)
		if err == nil && h.accessibleTheme(c, t) {
			applyIcons := user.ThemeApplyIcons == nil || *user.ThemeApplyIcons
			applyWallpaper := user.ThemeApplyWallpaper == nil || *user.ThemeApplyWallpaper
			c.JSON(200, gin.H{
				"theme":          themePublicJSON(t),
				"applyIcons":     applyIcons,
				"applyWallpaper": applyWallpaper,
				"accentColor":    t.AccentColor,
			})
			return
		}
		// 引用失效（主题被删/禁用）：静默回收引用。
		_ = h.Repos.Users.UpdateThemeID(c.Request.Context(), user.ID, primitive.NilObjectID)
	}
	// 未选择主题：回落站点默认主题（图标/壁纸对其生效，但不视为用户主动选择，
	// 前端据 isDefaultFallback 不高亮任何主题卡片）。
	if dt, derr := h.Repos.Themes.FindDefault(c.Request.Context()); derr == nil {
		c.JSON(200, gin.H{
			"theme":             themePublicJSON(dt),
			"applyIcons":        true,
			"applyWallpaper":    true,
			"isDefaultFallback": true,
		})
		return
	}
	c.JSON(200, gin.H{"theme": nil, "applyIcons": true, "applyWallpaper": true, "isDefaultFallback": false})
}

// bodyBool 提取可选布尔（缺省返回 def）。
func bodyBool(body map[string]any, key string, def bool) bool {
	v, ok := body[key]
	if !ok {
		return def
	}
	return truthy(v)
}

// SetSelection PUT /api/themes/selection：设置用户当前使用主题与应用组合
//（applyIcons / applyWallpaper 可自由勾选：仅壁纸、仅图标或全套，缺省全部应用）。
// 勾选壁纸且主题含壁纸时，同步写入用户背景偏好（多端生效）。
func (h *Themes) SetSelection(c *gin.Context) {
	body := cmsReadBody(c)
	user, _ := middleware.GetUser(c)
	rawID, _ := body["themeId"].(string)
	if strings.TrimSpace(rawID) == "" {
		if err := h.Repos.Users.UpdateThemeSelection(c.Request.Context(), user.ID,
			primitive.NilObjectID, nil, nil); err != nil {
			c.JSON(500, gin.H{"message": "设置主题失败"})
			return
		}
		// 取消主题：清掉此前主题写入的壁纸偏好，壁纸回落站点默认主题（或无背景）。
		var bgPrefs *model.BackgroundPrefs
		emptyImage, disabled := "", false
		if err := h.Repos.Users.UpdateBackgroundPrefs(c.Request.Context(), user.ID,
			repository.BackgroundPrefsPatch{Image: &emptyImage, Enabled: &disabled}); err == nil {
			if u, ferr := h.Repos.Users.FindByID(c.Request.Context(), user.ID); ferr == nil {
				bgPrefs = &u.BackgroundPrefs
			}
		}
		resp := gin.H{"theme": nil}
		if bgPrefs != nil {
			resp["backgroundPrefs"] = *bgPrefs
		}
		c.JSON(200, resp)
		return
	}
	oid, err := primitive.ObjectIDFromHex(rawID)
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return
	}
	t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "设置主题失败"})
		return
	}
	if !h.accessibleTheme(c, t) {
		c.JSON(403, gin.H{"message": "无权使用该主题"})
		return
	}
	applyIcons := bodyBool(body, "applyIcons", true)
	applyWallpaper := bodyBool(body, "applyWallpaper", true)
	if err := h.Repos.Users.UpdateThemeSelection(c.Request.Context(), user.ID, oid,
		&applyIcons, &applyWallpaper); err != nil {
		c.JSON(500, gin.H{"message": "设置主题失败"})
		return
	}
	// 勾选壁纸部分：把主题壁纸写入用户背景偏好（保留 opacity/blur）。
	var bgPrefs *model.BackgroundPrefs
	if applyWallpaper && t.WallpaperURL != "" {
		enabled := true
		if err := h.Repos.Users.UpdateBackgroundPrefs(c.Request.Context(), user.ID,
			repository.BackgroundPrefsPatch{Image: &t.WallpaperURL, Enabled: &enabled}); err == nil {
			if u, err := h.Repos.Users.FindByID(c.Request.Context(), user.ID); err == nil {
				bgPrefs = &u.BackgroundPrefs
			}
		}
	}
	resp := gin.H{"theme": themePublicJSON(t), "applyIcons": applyIcons, "applyWallpaper": applyWallpaper}
	if bgPrefs != nil {
		resp["backgroundPrefs"] = *bgPrefs
	}
	c.JSON(200, resp)
}

// Create POST /api/themes：创建主题（壁纸 + 图标组合包）。
// 普通用户创建个人主题（draft，仅自己可见）；超管可传 isSystem:true 直接创建
// 启用的系统主题。主题必须包含壁纸或图标至少其一。
func (h *Themes) Create(c *gin.Context) {
	body := cmsReadBody(c)
	name := strings.TrimSpace(asString(body["name"]))
	if name == "" || len([]rune(name)) > 30 {
		c.JSON(400, gin.H{"message": "主题名称不能为空且不超过 30 个字符"})
		return
	}
	description := strings.TrimSpace(asString(body["description"]))
	if len([]rune(description)) > 200 {
		c.JSON(400, gin.H{"message": "主题描述不能超过 200 个字符"})
		return
	}
	wallpaperURL, errMsg := sanitizeThemeAssetURL(body["wallpaperUrl"])
	if errMsg != "" {
		c.JSON(400, gin.H{"message": errMsg})
		return
	}
	wallpaperThumb, errMsg := sanitizeThemeAssetURL(body["wallpaperThumb"])
	if errMsg != "" {
		c.JSON(400, gin.H{"message": errMsg})
		return
	}
	iconsRaw, _ := body["icons"].(map[string]any)
	icons, errMsg := sanitizeThemeIcons(iconsRaw)
	if errMsg != "" {
		c.JSON(400, gin.H{"message": errMsg})
		return
	}
	accentColor, errMsg := sanitizeThemeAccentColor(body["accentColor"])
	if errMsg != "" {
		c.JSON(400, gin.H{"message": errMsg})
		return
	}
	if wallpaperURL == "" && len(icons) == 0 {
		c.JSON(400, gin.H{"message": "主题需包含壁纸或图标至少其一"})
		return
	}
	user, _ := middleware.GetUser(c)
	t := &model.Theme{
		Name:           name,
		Description:    description,
		WallpaperURL:   wallpaperURL,
		WallpaperThumb: wallpaperThumb,
		Icons:          icons,
		AccentColor:    accentColor,
		Enabled:        true,
	}
	if isSuperUser(c) && truthy(body["isSystem"]) {
		t.IsSystem = true
		t.Status = model.ThemeStatusApproved
	} else {
		t.IsSystem = false
		t.Status = model.ThemeStatusDraft
		t.OwnerID = &user.ID
	}
	if err := h.Repos.Themes.Create(c.Request.Context(), t); err != nil {
		c.JSON(500, gin.H{"message": "创建主题失败"})
		return
	}
	c.JSON(200, themeJSON(t))
}

// canEditTheme 判断用户能否编辑主题：超管全部可编辑；普通用户仅自己的个人主题。
func canEditTheme(c *gin.Context, t *model.Theme) bool {
	if isSuperUser(c) {
		return true
	}
	u, ok := middleware.GetUser(c)
	if !ok {
		return false
	}
	return !t.IsSystem && t.OwnerID != nil && *t.OwnerID == u.ID
}

// Update PUT /api/themes/:id：更新主题（名称/描述/壁纸/图标）。
// 系统主题仅超管可改；个人主题仅 owner 可改。
// 显式提交壁纸/图标字段时，最终内容必须仍包含壁纸或图标至少其一。
func (h *Themes) Update(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return
	}
	t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "更新主题失败"})
		return
	}
	if !canEditTheme(c, t) {
		c.JSON(403, gin.H{"message": "无权编辑该主题"})
		return
	}
	body := cmsReadBody(c)
	set := bson.M{"updatedAt": time.Now().UTC().Truncate(time.Millisecond)}
	contentTouched := false
	if v, ok := body["name"]; ok {
		name := strings.TrimSpace(asString(v))
		if name == "" || len([]rune(name)) > 30 {
			c.JSON(400, gin.H{"message": "主题名称不能为空且不超过 30 个字符"})
			return
		}
		set["name"] = name
	}
	if v, ok := body["description"]; ok {
		desc := strings.TrimSpace(asString(v))
		if len([]rune(desc)) > 200 {
			c.JSON(400, gin.H{"message": "主题描述不能超过 200 个字符"})
			return
		}
		set["description"] = desc
	}
	finalWallpaper := t.WallpaperURL
	if v, ok := body["wallpaperUrl"]; ok {
		wallpaperURL, errMsg := sanitizeThemeAssetURL(v)
		if errMsg != "" {
			c.JSON(400, gin.H{"message": errMsg})
			return
		}
		set["wallpaperUrl"] = wallpaperURL
		finalWallpaper = wallpaperURL
		contentTouched = true
	}
	if v, ok := body["wallpaperThumb"]; ok {
		wallpaperThumb, errMsg := sanitizeThemeAssetURL(v)
		if errMsg != "" {
			c.JSON(400, gin.H{"message": errMsg})
			return
		}
		set["wallpaperThumb"] = wallpaperThumb
	}
	finalIcons := t.Icons
	if v, ok := body["icons"]; ok {
		iconsRaw, _ := v.(map[string]any)
		icons, errMsg := sanitizeThemeIcons(iconsRaw)
		if errMsg != "" {
			c.JSON(400, gin.H{"message": errMsg})
			return
		}
		set["icons"] = icons
		finalIcons = icons
		contentTouched = true
	}
	if v, ok := body["accentColor"]; ok {
		accentColor, errMsg := sanitizeThemeAccentColor(v)
		if errMsg != "" {
			c.JSON(400, gin.H{"message": errMsg})
			return
		}
		set["accentColor"] = accentColor
	}
	if contentTouched && finalWallpaper == "" && len(finalIcons) == 0 {
		c.JSON(400, gin.H{"message": "主题需包含壁纸或图标至少其一"})
		return
	}
	updated, err := h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": set})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "更新主题失败"})
		return
	}
	c.JSON(200, themeJSON(updated))
}

// Delete DELETE /api/themes/:id：删除主题（owner 或超管）。
// 删除默认主题时同时清除引用该主题的用户的 themeId。
func (h *Themes) Delete(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return
	}
	t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "删除主题失败"})
		return
	}
	if !canEditTheme(c, t) {
		c.JSON(403, gin.H{"message": "无权删除该主题"})
		return
	}
	if _, err := h.Repos.Themes.FindOneAndDelete(c.Request.Context(), oid); err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "删除主题失败"})
		return
	}
	// 回收用户引用与默认标记。
	_ = h.Repos.Users.ClearThemeReferences(c.Request.Context(), oid)
	if t.IsDefault {
		_ = h.Repos.Themes.ClearDefaultExcept(c.Request.Context(), primitive.NilObjectID)
	}
	c.JSON(200, gin.H{"message": "已删除"})
}

// Submit POST /api/themes/:id/submit：owner 提交个人主题审核。
func (h *Themes) Submit(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return
	}
	t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "提交审核失败"})
		return
	}
	u, _ := middleware.GetUser(c)
	if t.IsSystem || t.OwnerID == nil || *t.OwnerID != u.ID {
		c.JSON(403, gin.H{"message": "只能提交自己的个人主题"})
		return
	}
	if t.Status == model.ThemeStatusPending {
		c.JSON(400, gin.H{"message": "该主题已在审核中"})
		return
	}
	if t.Status == model.ThemeStatusApproved {
		c.JSON(400, gin.H{"message": "该主题已是系统主题"})
		return
	}
	updated, err := h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": bson.M{
		"status":     model.ThemeStatusPending,
		"reviewNote": "",
		"updatedAt":  time.Now().UTC().Truncate(time.Millisecond),
	}})
	if err != nil {
		c.JSON(500, gin.H{"message": "提交审核失败"})
		return
	}
	c.JSON(200, themeJSON(updated))
}

// AdminAll GET /api/themes/all（superadmin）：全部主题（含个人/待审核）。
func (h *Themes) AdminAll(c *gin.Context) {
	list, err := h.Repos.Themes.FindAllAdmin(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"message": "获取主题列表失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, themeJSON(&list[i]))
	}
	c.JSON(200, out)
}

// Review POST /api/themes/:id/review（superadmin）：审核个人主题。
// approve → 变为系统主题（isSystem=true, status=approved, enabled=true）；
// reject  → 保持个人主题（status=rejected，可带驳回原因）。
func (h *Themes) Review(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return
	}
	body := cmsReadBody(c)
	action := asString(body["action"])
	note := strings.TrimSpace(asString(body["note"]))
	if len([]rune(note)) > 200 {
		c.JSON(400, gin.H{"message": "审核备注不能超过 200 个字符"})
		return
	}
	set := bson.M{"reviewNote": note, "updatedAt": time.Now().UTC().Truncate(time.Millisecond)}
	switch action {
	case "approve":
		set["status"] = model.ThemeStatusApproved
		set["isSystem"] = true
		set["enabled"] = true
	case "reject":
		if t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid); err == nil && t.IsSystem {
			c.JSON(400, gin.H{"message": "系统主题无需审核"})
			return
		}
		set["status"] = model.ThemeStatusRejected
		set["isSystem"] = false
	default:
		c.JSON(400, gin.H{"message": "action 必须为 approve 或 reject"})
		return
	}
	updated, err := h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": set})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "审核失败"})
		return
	}
	// 审核结果站内通知主题作者（fire-and-forget，与剧集审核通知一致）。
	h.notifyThemeReviewResult(updated, action, note)
	c.JSON(200, themeJSON(updated))
}

// notifyThemeReviewResult 主题审核结果站内通知（对齐创作者主页审核通知模式）。
func (h *Themes) notifyThemeReviewResult(t *model.Theme, action, note string) {
	if t == nil || t.OwnerID == nil {
		return
	}
	var message string
	if action == "approve" {
		message = "您的主题「" + t.Name + "」已通过审核并上架主题市场"
	} else {
		message = "您的主题「" + t.Name + "」未通过审核"
		if note != "" {
			message += "：" + note
		}
	}
	notif := &model.Notification{
		UserID:    *t.OwnerID,
		Type:      "theme_review",
		Message:   message,
		Link:      "/settings",
		Metadata:  primitive.M{"status": action, "note": note, "themeId": t.ID.Hex()},
		CreatedAt: time.Now(),
	}
	_ = h.Repos.Notifications.Create(context.Background(), notif)
}

// SetDefault POST /api/themes/:id/default（superadmin）：设置/取消站点默认主题（全站唯一）。
// body: {"default": true|false}，缺省视为 true（兼容旧「设为默认」调用）。
//   - true：设为默认（仅限启用的系统主题），并清除其它主题的默认标记；
//   - false：取消该主题的默认标记，取消后站点允许没有任何默认主题。
func (h *Themes) SetDefault(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return
	}
	t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "设置默认主题失败"})
		return
	}
	body := cmsReadBody(c)
	wantDefault := true
	if v, ok := body["default"]; ok {
		wantDefault = truthy(v)
	}
	if !wantDefault {
		// 取消默认：仅系统主题可操作（个人主题本就不可能是默认）。
		if !t.IsSystem {
			c.JSON(400, gin.H{"message": "只有系统主题可以取消默认"})
			return
		}
		updated, err := h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": bson.M{
			"isDefault": false,
			"updatedAt":  time.Now().UTC().Truncate(time.Millisecond),
		}})
		if err != nil {
			c.JSON(500, gin.H{"message": "取消默认主题失败"})
			return
		}
		c.JSON(200, themeJSON(updated))
		return
	}
	if !t.IsSystem || !t.Enabled {
		c.JSON(400, gin.H{"message": "只有启用的系统主题才能设为默认"})
		return
	}
	updated, err := h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": bson.M{
		"isDefault": true,
		"updatedAt":  time.Now().UTC().Truncate(time.Millisecond),
	}})
	if err != nil {
		c.JSON(500, gin.H{"message": "设置默认主题失败"})
		return
	}
	// 清除其它主题的默认标记。
	_ = h.Repos.Themes.ClearDefaultExcept(c.Request.Context(), oid)
	c.JSON(200, themeJSON(updated))
}

// AdminUpdate PUT /api/themes/:id/admin（superadmin）：管理主题类型与状态。
// 可将个人主题升级为系统主题（或反向降级）、启用/停用系统主题。
func (h *Themes) AdminUpdate(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return
	}
	t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "更新主题失败"})
		return
	}
	body := cmsReadBody(c)
	set := bson.M{"updatedAt": time.Now().UTC().Truncate(time.Millisecond)}
	if v, ok := body["isSystem"]; ok {
		wantSystem := truthy(v)
		if wantSystem && !t.IsSystem {
			// 个人 → 系统：视为审核通过。
			set["isSystem"] = true
			set["status"] = model.ThemeStatusApproved
		} else if !wantSystem && t.IsSystem {
			// 系统 → 个人：退回 owner（无 owner 时保持系统但停用）。
			if t.OwnerID == nil {
				c.JSON(400, gin.H{"message": "该主题无创建者，无法转为个人主题"})
				return
			}
			set["isSystem"] = false
			set["status"] = model.ThemeStatusApproved
			set["isDefault"] = false
		}
	}
	if v, ok := body["enabled"]; ok {
		set["enabled"] = truthy(v)
	}
	// 默认主题开关（与 SetDefault 等价，供管理端开关直调）：
	// 开启需为「更新后」启用的系统主题；关闭直接取消（站点允许无默认主题）。
	if v, ok := body["isDefault"]; ok {
		wantDefault := truthy(v)
		if wantDefault {
			finalSystem := t.IsSystem
			if sv, ok := body["isSystem"]; ok {
				finalSystem = truthy(sv)
			}
			finalEnabled := t.Enabled
			if ev, ok := body["enabled"]; ok {
				finalEnabled = truthy(ev)
			}
			if !finalSystem || !finalEnabled {
				c.JSON(400, gin.H{"message": "只有启用的系统主题才能设为默认"})
				return
			}
			set["isDefault"] = true
		} else {
			set["isDefault"] = false
		}
	}
	updated, err := h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": set})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "更新主题失败"})
		return
	}
	// 若被降级/停用的是默认主题，清除默认标记。
	if updated.IsDefault && (!updated.IsSystem || !updated.Enabled) {
		updated.IsDefault = false
		_, _ = h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": bson.M{"isDefault": false}})
		_ = h.Repos.Themes.ClearDefaultExcept(c.Request.Context(), primitive.NilObjectID)
	} else if updated.IsDefault {
		// 设为默认后清除其它主题的默认标记（保持全站唯一）。
		_ = h.Repos.Themes.ClearDefaultExcept(c.Request.Context(), oid)
	}
	c.JSON(200, themeJSON(updated))
}

// UploadWallpaper POST /api/themes/upload-wallpaper（登录用户 + ≤8MB 图片）。
// 上传主题用壁纸，返回 {"url": "/uploads/theme-wallpaper-xxx.png"}。
func (h *Themes) UploadWallpaper(c *gin.Context) {
	url, err := upload.SaveImage(c, "image", "theme-wallpaper", 8<<20)
	if err != nil {
		abortThemeUploadError(c, h.Config, err, "上传主题壁纸失败")
		return
	}
	c.JSON(200, gin.H{"url": url})
}

// UploadIcon POST /api/themes/upload-icon（登录用户 + ≤1MB SVG）。
// 上传主题用图标，返回 {"url": "/uploads/icons/icon-xxx.svg"}。
func (h *Themes) UploadIcon(c *gin.Context) {
	url, err := upload.SaveSVG(c, "file")
	if err != nil {
		if err == upload.ErrNoFile {
			c.JSON(400, gin.H{"message": "请选择要上传的 SVG 图标"})
			return
		}
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"url": url})
}

// abortThemeUploadError 渲染主题资源上传错误（复用壁纸上传错误契约）。
func abortThemeUploadError(c *gin.Context, cfg *config.Config, err error, fallback string) {
	if err == upload.ErrNoFile {
		c.JSON(400, gin.H{"message": "请选择要上传的图片"})
		return
	}
	abortImageUploadError(c, cfg, err, fallback)
}
