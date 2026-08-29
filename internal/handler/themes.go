package handler

import (
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
)

// 本文件实现 /api/themes 主题管理子域。
//
// 公开：GET /active（站点默认主题）、GET /list（可选系统主题）；
// 登录用户：GET /my、GET /my/selection、PUT /selection、POST /、
// PUT/DELETE /:id、POST /:id/submit（提交审核）；
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
	g.PUT("/:id", h.AuthMW.Protect(), h.Update)
	g.DELETE("/:id", h.AuthMW.Protect(), h.Delete)
	g.POST("/:id/submit", h.AuthMW.Protect(), h.Submit)
	g.GET("/all", super, h.AdminAll)
	g.POST("/:id/review", super, h.Review)
	g.POST("/:id/default", super, h.SetDefault)
	g.PUT("/:id/admin", super, h.AdminUpdate)
}

// themeJSON 组装主题响应对象（对齐项目 DTO 组装风格）。
func themeJSON(t *model.Theme) gin.H {
	ownerID := ""
	if t.OwnerID != nil {
		ownerID = t.OwnerID.Hex()
	}
	vars := t.Variables
	if vars == nil {
		vars = map[string]string{}
	}
	return gin.H{
		"_id":         t.ID.Hex(),
		"name":        t.Name,
		"description": t.Description,
		"mode":        t.Mode,
		"variables":   vars,
		"isSystem":    t.IsSystem,
		"ownerId":     ownerID,
		"status":      t.Status,
		"reviewNote":  t.ReviewNote,
		"isDefault":   t.IsDefault,
		"enabled":     t.Enabled,
		"createdAt":   t.CreatedAt,
		"updatedAt":   t.UpdatedAt,
		"__v":         t.VersionKey,
	}
}

// themePublicJSON 组装主题公开视图（GET /list / /active，隐藏审核备注与作者）。
func themePublicJSON(t *model.Theme) gin.H {
	vars := t.Variables
	if vars == nil {
		vars = map[string]string{}
	}
	return gin.H{
		"_id":         t.ID.Hex(),
		"name":        t.Name,
		"description": t.Description,
		"mode":        t.Mode,
		"variables":   vars,
		"isSystem":    t.IsSystem,
		"isDefault":   t.IsDefault,
	}
}

// cssVarKeyRe 合法 CSS 变量名（--primary / --primary-bg 等）。
var cssVarKeyRe = regexp.MustCompile(`^--[a-zA-Z][a-zA-Z0-9-]*$`)

// sanitizeThemeVariables 校验并规范化主题变量表：
//   - key 必须形如 --xxx（防注入任意属性）；
//   - value 去首尾空白、限长 500；
//   - 最多 300 个键值对。
func sanitizeThemeVariables(input map[string]any) (map[string]string, string) {
	if input == nil {
		return map[string]string{}, ""
	}
	if len(input) > 300 {
		return nil, "主题变量数量不能超过 300 个"
	}
	out := make(map[string]string, len(input))
	for k, v := range input {
		if !cssVarKeyRe.MatchString(k) {
			return nil, "主题变量名不合法: " + k
		}
		s, ok := v.(string)
		if !ok {
			return nil, "主题变量值必须为字符串: " + k
		}
		s = strings.TrimSpace(s)
		if len(s) > 500 {
			return nil, "主题变量值过长: " + k
		}
		out[k] = s
	}
	return out, ""
}

// asStringMap 提取 map[string]any（非对象返回 nil）。
func asStringMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// themeModeOr 归一化基础模式（默认 dark）。
func themeModeOr(v any) string {
	s, _ := v.(string)
	if s == model.ThemeModeLight {
		return model.ThemeModeLight
	}
	return model.ThemeModeDark
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

// MySelection GET /api/themes/my/selection：获取当前用户生效主题（用户选择 > 默认）。
func (h *Themes) MySelection(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	if !user.ThemeID.IsZero() {
		t, err := h.Repos.Themes.FindByID(c.Request.Context(), user.ThemeID)
		if err == nil && h.accessibleTheme(c, t) {
			c.JSON(200, gin.H{"theme": themePublicJSON(t)})
			return
		}
		// 引用失效（主题被删/禁用）：静默回收引用。
		_ = h.Repos.Users.UpdateThemeID(c.Request.Context(), user.ID, primitive.NilObjectID)
	}
	c.JSON(200, gin.H{"theme": nil})
}

// SetSelection PUT /api/themes/selection：设置用户当前使用主题（多端同步）。
func (h *Themes) SetSelection(c *gin.Context) {
	body := cmsReadBody(c)
	user, _ := middleware.GetUser(c)
	rawID, _ := body["themeId"].(string)
	if strings.TrimSpace(rawID) == "" {
		if err := h.Repos.Users.UpdateThemeID(c.Request.Context(), user.ID, primitive.NilObjectID); err != nil {
			c.JSON(500, gin.H{"message": "设置主题失败"})
			return
		}
		c.JSON(200, gin.H{"theme": nil})
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
	if err := h.Repos.Users.UpdateThemeID(c.Request.Context(), user.ID, oid); err != nil {
		c.JSON(500, gin.H{"message": "设置主题失败"})
		return
	}
	c.JSON(200, gin.H{"theme": themePublicJSON(t)})
}

// Create POST /api/themes：创建主题。
// 普通用户创建个人主题（draft，仅自己可见）；超管可传 isSystem:true 直接创建
// 启用的系统主题。
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
	vars, errMsg := sanitizeThemeVariables(asStringMap(body["variables"]))
	if errMsg != "" {
		c.JSON(400, gin.H{"message": errMsg})
		return
	}
	user, _ := middleware.GetUser(c)
	t := &model.Theme{
		Name:        name,
		Description: description,
		Mode:        themeModeOr(body["mode"]),
		Variables:   vars,
		Enabled:     true,
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

// Update PUT /api/themes/:id：更新主题（名称/描述/变量/模式）。
// 系统主题仅超管可改；个人主题仅 owner 可改。
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
	if v, ok := body["mode"]; ok {
		set["mode"] = themeModeOr(v)
	}
	if v, ok := body["variables"]; ok {
		vars, errMsg := sanitizeThemeVariables(asStringMap(v))
		if errMsg != "" {
			c.JSON(400, gin.H{"message": errMsg})
			return
		}
		set["variables"] = vars
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
	c.JSON(200, themeJSON(updated))
}

// SetDefault POST /api/themes/:id/default（superadmin）：设为站点默认主题（全站唯一）。
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
	if !t.IsSystem || !t.Enabled {
		c.JSON(400, gin.H{"message": "只有启用的系统主题才能设为默认"})
		return
	}
	updated, err := h.Repos.Themes.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": bson.M{
		"isDefault": true,
		"updatedAt": time.Now().UTC().Truncate(time.Millisecond),
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
	}
	c.JSON(200, themeJSON(updated))
}
