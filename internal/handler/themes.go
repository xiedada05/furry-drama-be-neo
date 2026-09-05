package handler

import (
	"context"
	"errors"
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
	g.PUT("/background-prefs", h.AuthMW.Protect(), h.UpdateBackgroundPrefs)
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

// resolveThemeSlot 解析一个主题槽的生效主题。
// 优先取 slotID；为空时回落 fallbackID（旧单主题按 apply 标志拆槽）。
// 返回：主题（nil 表示无）、是否为用户主动设置、引用是否失效需回收。
func (h *Themes) resolveThemeSlot(ctx context.Context, c *gin.Context, user *model.User,
	slotID, fallbackID primitive.ObjectID) (*model.Theme, bool, bool) {
	id := slotID
	if id.IsZero() {
		id = fallbackID
	}
	if id.IsZero() {
		return nil, false, false
	}
	t, err := h.Repos.Themes.FindByID(ctx, id)
	if err != nil {
		return nil, !slotID.IsZero(), !slotID.IsZero() || !fallbackID.IsZero()
	}
	if !h.accessibleTheme(c, t) {
		return nil, !slotID.IsZero(), true
	}
	return t, !slotID.IsZero() || !fallbackID.IsZero(), false
}

// MySelection GET /api/themes/my/selection：获取当前用户生效主题（用户选择 > 站点默认）。
// 背景与图标为两个独立槽（支持主题A背景 + 主题B图标自由组合）：
//   - wallpaperTheme / iconsTheme：两槽当前主题（无则为 null，回落站点默认主题）；
//   - wallpaperIsDefault / iconsIsDefault：该槽是否为默认主题兜底（非用户主动选择）；
//   - 兼容旧单主题字段：按 applyIcons/applyWallpaper 拆分到对应槽。
func (h *Themes) MySelection(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	ctx := c.Request.Context()

	// 旧单主题拆槽：applyIcons/applyWallpaper 决定旧主题落到哪个槽。
	legacyWallpaper := user.ThemeID
	legacyIcons := user.ThemeID
	if !user.ThemeID.IsZero() {
		if user.ThemeApplyWallpaper != nil && !*user.ThemeApplyWallpaper {
			legacyWallpaper = primitive.NilObjectID
		}
		if user.ThemeApplyIcons != nil && !*user.ThemeApplyIcons {
			legacyIcons = primitive.NilObjectID
		}
	}

	wallpaperTheme, _, wpStale := h.resolveThemeSlot(ctx, c, user, user.ThemeWallpaperID, legacyWallpaper)
	iconsTheme, _, icStale := h.resolveThemeSlot(ctx, c, user, user.ThemeIconsID, legacyIcons)

	// 迁移到两槽模型：清失效引用，把旧单主题选择落到对应槽（保留有效选择）。
	if wpStale || icStale || !user.ThemeID.IsZero() {
		newWallpaper := user.ThemeWallpaperID
		if wpStale {
			newWallpaper = primitive.NilObjectID
		} else if newWallpaper.IsZero() {
			newWallpaper = legacyWallpaper
		}
		newIcons := user.ThemeIconsID
		if icStale {
			newIcons = primitive.NilObjectID
		} else if newIcons.IsZero() {
			newIcons = legacyIcons
		}
		_ = h.Repos.Users.UpdateThemeSlots(ctx, user.ID, newWallpaper, newIcons)
	}

	// 未设置的槽回落站点默认主题（生效但不视为用户主动选择，
	// 前端据 isDefault 标记不高亮对应主题卡片）。
	var dt *model.Theme
	if wallpaperTheme == nil || iconsTheme == nil {
		if d, derr := h.Repos.Themes.FindDefault(ctx); derr == nil {
			dt = d
		}
	}
	wpIsDefault := false
	if wallpaperTheme == nil && dt != nil && dt.WallpaperURL != "" {
		wallpaperTheme = dt
		wpIsDefault = true
	}
	icIsDefault := false
	if iconsTheme == nil && dt != nil && len(dt.Icons) > 0 {
		iconsTheme = dt
		icIsDefault = true
	}

	resp := gin.H{
		"wallpaperTheme":    nil,
		"iconsTheme":        nil,
		"wallpaperIsDefault": wpIsDefault,
		"iconsIsDefault":     icIsDefault,
	}
	if wallpaperTheme != nil {
		resp["wallpaperTheme"] = themePublicJSON(wallpaperTheme)
	}
	if iconsTheme != nil {
		resp["iconsTheme"] = themePublicJSON(iconsTheme)
	}
	c.JSON(200, resp)
}

// bodyBool 提取可选布尔（缺省返回 def）。
func bodyBool(body map[string]any, key string, def bool) bool {
	v, ok := body[key]
	if !ok {
		return def
	}
	return truthy(v)
}

// SetSelection PUT /api/themes/selection：设置用户背景/图标两槽主题（自由组合）。
// 新 body：{ wallpaperThemeId, iconsThemeId }——空串表示清空该槽，键不存在表示
// 该槽保持不变；兼容旧 body { themeId, applyIcons, applyWallpaper }（拆到对应槽）。
// 壁纸槽变更时同步写入/清空用户背景偏好（多端生效）；响应含两槽主题与背景偏好，
// 以及被设置主题的主题色（有则前端应用、无则保持）。
func (h *Themes) SetSelection(c *gin.Context) {
	body := cmsReadBody(c)
	user, _ := middleware.GetUser(c)
	ctx := c.Request.Context()

	// 计算目标槽值（零值 = 清空该槽）。
	var wallpaperID, iconsID primitive.ObjectID
	var setWallpaper, setIcons bool // 该槽是否在本次请求中被显式指定
	var appliedAccent string        // 被设置主题的主题色（供前端应用）

	if rawID, ok := body["themeId"].(string); ok && strings.TrimSpace(rawID) != "" {
		// 旧 body：单主题按应用组合拆槽。
		oid, err := primitive.ObjectIDFromHex(rawID)
		if err != nil {
			c.JSON(400, gin.H{"message": "主题 ID 不合法"})
			return
		}
		t, err := h.Repos.Themes.FindByID(ctx, oid)
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
		if bodyBool(body, "applyWallpaper", true) {
			wallpaperID, setWallpaper = oid, true
		}
		if bodyBool(body, "applyIcons", true) {
			iconsID, setIcons = oid, true
		}
		appliedAccent = t.AccentColor
	} else {
		// 新 body：两槽各自设置/清空（键存在即视为显式指定）。
		if raw, ok := body["wallpaperThemeId"].(string); ok {
			setWallpaper = true
			if strings.TrimSpace(raw) != "" {
				oid, t, err := h.findValidatedTheme(c, raw, true)
				if err != nil {
					return
				}
				wallpaperID = oid
				appliedAccent = t.AccentColor
			}
		}
		if raw, ok := body["iconsThemeId"].(string); ok {
			setIcons = true
			if strings.TrimSpace(raw) != "" {
				oid, t, err := h.findValidatedTheme(c, raw, false)
				if err != nil {
					return
				}
				iconsID = oid
				if appliedAccent == "" {
					appliedAccent = t.AccentColor
				}
			}
		}
	}
	if !setWallpaper && !setIcons {
		c.JSON(400, gin.H{"message": "请求需包含 wallpaperThemeId / iconsThemeId（或旧版 themeId）"})
		return
	}

	// 未指定的槽保持现值（两槽字段优先，旧单主题按应用组合拆槽兜底）。
	curWallpaper := user.ThemeWallpaperID
	curIcons := user.ThemeIconsID
	if curWallpaper.IsZero() && !user.ThemeID.IsZero() &&
		(user.ThemeApplyWallpaper == nil || *user.ThemeApplyWallpaper) {
		curWallpaper = user.ThemeID
	}
	if curIcons.IsZero() && !user.ThemeID.IsZero() &&
		(user.ThemeApplyIcons == nil || *user.ThemeApplyIcons) {
		curIcons = user.ThemeID
	}
	if !setWallpaper {
		wallpaperID = curWallpaper
	}
	if !setIcons {
		iconsID = curIcons
	}

	if err := h.Repos.Users.UpdateThemeSlots(ctx, user.ID, wallpaperID, iconsID); err != nil {
		c.JSON(500, gin.H{"message": "设置主题失败"})
		return
	}

	// 壁纸槽变更：写入/清空背景偏好（保留用户自调的 opacity/blur）。
	var bgPrefs *model.BackgroundPrefs
	if setWallpaper {
		var patch repository.BackgroundPrefsPatch
		if !wallpaperID.IsZero() {
			t, err := h.Repos.Themes.FindByID(ctx, wallpaperID)
			if err == nil {
				enabled := true
				patch = repository.BackgroundPrefsPatch{Image: &t.WallpaperURL, Enabled: &enabled}
			}
		} else {
			emptyImage, disabled := "", false
			patch = repository.BackgroundPrefsPatch{Image: &emptyImage, Enabled: &disabled}
		}
		if err := h.Repos.Users.UpdateBackgroundPrefs(ctx, user.ID, patch); err == nil {
			if u, ferr := h.Repos.Users.FindByID(ctx, user.ID); ferr == nil {
				bgPrefs = &u.BackgroundPrefs
			}
		}
	}

	resp := gin.H{"wallpaperTheme": nil, "iconsTheme": nil}
	if !wallpaperID.IsZero() {
		if t, err := h.Repos.Themes.FindByID(ctx, wallpaperID); err == nil {
			resp["wallpaperTheme"] = themePublicJSON(t)
		}
	}
	if !iconsID.IsZero() {
		if t, err := h.Repos.Themes.FindByID(ctx, iconsID); err == nil {
			resp["iconsTheme"] = themePublicJSON(t)
		}
	}
	if bgPrefs != nil {
		resp["backgroundPrefs"] = *bgPrefs
	}
	if appliedAccent != "" {
		resp["accentColor"] = appliedAccent
	}
	c.JSON(200, resp)
}

// findValidatedTheme 按 hex ID 查找主题并校验可用性与槽匹配内容
//（壁纸槽需含壁纸 / 图标槽需含图标）。失败时已写入响应，调用方直接 return。
func (h *Themes) findValidatedTheme(c *gin.Context, raw string, forWallpaper bool) (
	primitive.ObjectID, *model.Theme, error) {
	oid, err := primitive.ObjectIDFromHex(strings.TrimSpace(raw))
	if err != nil {
		c.JSON(400, gin.H{"message": "主题 ID 不合法"})
		return primitive.NilObjectID, nil, err
	}
	t, err := h.Repos.Themes.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "主题不存在"})
			return primitive.NilObjectID, nil, err
		}
		c.JSON(500, gin.H{"message": "设置主题失败"})
		return primitive.NilObjectID, nil, err
	}
	if !h.accessibleTheme(c, t) {
		c.JSON(403, gin.H{"message": "无权使用该主题"})
		return primitive.NilObjectID, nil, errors.New("forbidden")
	}
	if forWallpaper && t.WallpaperURL == "" {
		c.JSON(400, gin.H{"message": "该主题不含壁纸，无法应用到背景"})
		return primitive.NilObjectID, nil, errors.New("no wallpaper")
	}
	if !forWallpaper && len(t.Icons) == 0 {
		c.JSON(400, gin.H{"message": "该主题不含图标，无法应用到图标"})
		return primitive.NilObjectID, nil, errors.New("no icons")
	}
	return oid, t, nil
}

// UpdateBackgroundPrefs PUT /api/themes/background-prefs：用户调整当前背景的
// 透明度 / 模糊度 / 单独开关（不改 image——壁纸图片由主题槽驱动）。
func (h *Themes) UpdateBackgroundPrefs(c *gin.Context) {
	body := cmsReadBody(c)
	user, _ := middleware.GetUser(c)
	patch := repository.BackgroundPrefsPatch{}
	if v, ok := body["opacity"]; ok {
		n := toInt(v)
		if n < 0 || n > 100 {
			c.JSON(400, gin.H{"message": "透明度需在 0-100 之间"})
			return
		}
		patch.Opacity = &n
	}
	if v, ok := body["blur"]; ok {
		n := toInt(v)
		if n < 0 || n > 40 {
			c.JSON(400, gin.H{"message": "模糊度需在 0-40 之间"})
			return
		}
		patch.Blur = &n
	}
	if v, ok := body["enabled"]; ok {
		b := truthy(v)
		patch.Enabled = &b
	}
	if patch.Opacity == nil && patch.Blur == nil && patch.Enabled == nil {
		c.JSON(400, gin.H{"message": "请求需包含 opacity / blur / enabled 至少其一"})
		return
	}
	if err := h.Repos.Users.UpdateBackgroundPrefs(c.Request.Context(), user.ID, patch); err != nil {
		c.JSON(500, gin.H{"message": "保存背景设置失败"})
		return
	}
	u, err := h.Repos.Users.FindByID(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "保存背景设置失败"})
		return
	}
	c.JSON(200, gin.H{"backgroundPrefs": u.BackgroundPrefs})
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
	// 清理孤儿资源文件：主题删除后，其壁纸/图标文件若无任何其他引用
	//（其他主题 / 系统壁纸库 / 任一用户的个人壁纸库）则一并删除磁盘文件，
	// 避免删除/被拒主题的资源残留占用存储。
	h.removeUnreferencedThemeFiles(c.Request.Context(), t, oid)
	c.JSON(200, gin.H{"message": "已删除"})
}

// removeUnreferencedThemeFiles 删除主题资源中已无引用的磁盘文件（fire-and-forget，
// 失败仅记录日志不阻断删除）。仅处理本地 /uploads/ 前缀文件；外链（https://）不动。
func (h *Themes) removeUnreferencedThemeFiles(ctx context.Context, t *model.Theme, id primitive.ObjectID) {
	urls := make([]string, 0, 2+len(t.Icons))
	if u := strings.TrimSpace(t.WallpaperURL); u != "" {
		urls = append(urls, u)
	}
	if u := strings.TrimSpace(t.WallpaperThumb); u != "" {
		urls = append(urls, u)
	}
	for _, u := range t.Icons {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	seen := make(map[string]bool, len(urls))
	for _, url := range urls {
		if seen[url] || !strings.HasPrefix(url, "/uploads/") {
			continue
		}
		seen[url] = true
		// 仍被其他主题 / 系统壁纸库 / 个人壁纸库引用的文件保留。
		if n, err := h.Repos.Themes.CountURLReferences(ctx, url, id); err == nil && n > 0 {
			continue
		}
		if ok, err := h.Repos.Wallpapers.ExistsByURL(ctx, url); err == nil && ok {
			continue
		}
		if ok, err := h.Repos.Users.PersonalWallpaperInUse(ctx, url); err == nil && ok {
			continue
		}
		_ = upload.RemoveFile(strings.TrimPrefix(url, "/uploads/"))
	}
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
