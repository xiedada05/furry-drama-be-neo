package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	apperrors "github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/upload"
)

// 本文件实现 /api/wallpapers 子域（行为逐分支照抄 backend/routes/wallpapers.js）。
// 公开：GET /system；超管：GET /system/all、POST /system、PUT/DELETE /system/:id；
// 登录用户：GET/POST/DELETE /personal。

// Wallpapers 是壁纸域（/api/wallpapers）handler 容器。
type Wallpapers struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewWallpapers 构造壁纸 handler 容器。
func NewWallpapers(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc) *Wallpapers {
	return &Wallpapers{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/wallpapers 全部端点（路径照抄 Express 子路径）。
func (h *Wallpapers) Register(g *gin.RouterGroup) {
	super := h.AuthMW.Protect(middleware.RoleSuperAdmin)
	g.GET("/system", h.SystemList)
	g.GET("/system/all", super, h.SystemAll)
	g.POST("/system", super, h.SystemUpload)
	g.PUT("/system/:id", super, h.SystemUpdate)
	g.DELETE("/system/:id", super, h.SystemDelete)
	g.GET("/personal", h.AuthMW.Protect(), h.PersonalList)
	g.POST("/personal", h.AuthMW.Protect(), h.PersonalUpload)
	g.DELETE("/personal", h.AuthMW.Protect(), h.PersonalDelete)
}

// ---- 系统壁纸 ----

// SystemList GET /api/wallpapers/system：获取所有启用的系统壁纸（公开）。
// @Summary 获取启用的系统壁纸
// @Tags 壁纸
// @Success 200 {array} map[string]any "name/url/thumbnailUrl/sortOrder"
// @Router /wallpapers/system [get]
func (h *Wallpapers) SystemList(c *gin.Context) {
	list, err := h.Repos.Wallpapers.FindEnabled(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"message": "获取系统壁纸失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, cmsWallpaperPublicJSON(&list[i]))
	}
	c.JSON(200, out)
}

// SystemAll GET /api/wallpapers/system/all（superadmin）：获取所有系统壁纸（含禁用）。
// @Summary 获取所有系统壁纸（管理后台）
// @Tags 壁纸
// @Security bearerAuth
// @Success 200 {array} map[string]any "完整壁纸对象"
// @Router /wallpapers/system/all [get]
func (h *Wallpapers) SystemAll(c *gin.Context) {
	list, err := h.Repos.Wallpapers.FindAll(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"message": "获取系统壁纸失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, cmsWallpaperJSON(&list[i]))
	}
	c.JSON(200, out)
}

// SystemUpload POST /api/wallpapers/system（superadmin + 8MB 图片）。
// @Summary 上传系统壁纸
// @Tags 壁纸
// @Security bearerAuth
// @Accept multipart/form-data
// @Param image formData file true "图片（≤8MB）"
// @Param name formData string false "名称"
// @Param enabled formData string false "是否启用（'false' 以外的值视为启用）"
// @Param sortOrder formData int false "排序（默认 0）"
// @Success 200 {object} map[string]any "壁纸对象"
// @Router /wallpapers/system [post]
func (h *Wallpapers) SystemUpload(c *gin.Context) {
	url, err := upload.SaveImage(c, "image", "wallpaper", 8<<20)
	if err != nil {
		h.abortWallpaperUploadError(c, err)
		return
	}
	name := c.PostForm("name")
	enabled := c.PostForm("enabled") != "false" // enabled !== 'false'
	sortOrder := cmsParseIntJS(c.PostForm("sortOrder"))
	user, _ := middleware.GetUser(c)
	w := &model.SystemWallpaper{
		URL:        url,
		Name:       name,
		Enabled:    enabled,
		SortOrder:  sortOrder,
		UploadedBy: &user.ID,
	}
	if err := h.Repos.Wallpapers.Create(c.Request.Context(), w); err != nil {
		c.JSON(500, gin.H{"message": "上传系统壁纸失败"})
		return
	}
	c.JSON(200, cmsWallpaperJSON(w))
}

// SystemUpdate PUT /api/wallpapers/system/:id（superadmin）。
// @Summary 更新系统壁纸
// @Tags 壁纸
// @Security bearerAuth
// @Accept json
// @Param id path string true "壁纸 ID"
// @Param body body object true "name/enabled/sortOrder"
// @Success 200 {object} map[string]any "壁纸对象"
// @Failure 404 {object} map[string]string "壁纸不存在"
// @Router /wallpapers/system/{id} [put]
func (h *Wallpapers) SystemUpdate(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(500, gin.H{"message": "更新系统壁纸失败"})
		return
	}
	body := cmsReadBody(c)
	update := bson.M{}
	if v, ok := body["name"]; ok {
		update["name"] = asString(v)
	}
	if v, ok := body["enabled"]; ok {
		update["enabled"] = truthy(v) // !!enabled
	}
	if v, ok := body["sortOrder"]; ok {
		update["sortOrder"] = cmsParseIntJS(asString(v)) // parseInt || 0
	}
	w, err := h.Repos.Wallpapers.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": update})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "壁纸不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "更新系统壁纸失败"})
		return
	}
	c.JSON(200, cmsWallpaperJSON(w))
}

// SystemDelete DELETE /api/wallpapers/system/:id（superadmin），同时删除磁盘文件。
// @Summary 删除系统壁纸
// @Tags 壁纸
// @Security bearerAuth
// @Param id path string true "壁纸 ID"
// @Success 200 {object} map[string]string "已删除"
// @Failure 404 {object} map[string]string "壁纸不存在"
// @Router /wallpapers/system/{id} [delete]
func (h *Wallpapers) SystemDelete(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(500, gin.H{"message": "删除系统壁纸失败"})
		return
	}
	w, err := h.Repos.Wallpapers.FindOneAndDelete(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "壁纸不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "删除系统壁纸失败"})
		return
	}
	// 删除文件（对齐 fs.unlink(path, cb) fire-and-forget）。
	if w.URL != "" && strings.HasPrefix(w.URL, "/uploads/") {
		_ = upload.RemoveFile(strings.TrimPrefix(w.URL, "/uploads/"))
	}
	c.JSON(200, gin.H{"message": "已删除"})
}

// ---- 个人壁纸 ----

// PersonalList GET /api/wallpapers/personal（protect）：获取我的个人壁纸列表。
// @Summary 获取我的个人壁纸
// @Tags 壁纸
// @Security bearerAuth
// @Success 200 {array} model.Wallpaper "个人壁纸数组"
// @Router /wallpapers/personal [get]
func (h *Wallpapers) PersonalList(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	u, err := h.Repos.Users.FindByID(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "获取个人壁纸失败"})
		return
	}
	list := u.PersonalWallpapers
	if list == nil {
		list = []model.Wallpaper{}
	}
	c.JSON(200, list)
}

// PersonalUpload POST /api/wallpapers/personal（protect + 8MB 图片，上限 20 张）。
// @Summary 上传个人壁纸
// @Tags 壁纸
// @Security bearerAuth
// @Accept multipart/form-data
// @Param image formData file true "图片（≤8MB）"
// @Param name formData string false "名称"
// @Success 200 {object} map[string]any "url/name/addedAt"
// @Router /wallpapers/personal [post]
func (h *Wallpapers) PersonalUpload(c *gin.Context) {
	url, err := upload.SaveImage(c, "image", "wallpaper", 8<<20)
	if err != nil {
		h.abortWallpaperUploadError(c, err)
		return
	}
	name := c.PostForm("name")
	user, _ := middleware.GetUser(c)
	u, err := h.Repos.Users.FindByID(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "上传个人壁纸失败"})
		return
	}
	// 限制个人壁纸数量。
	if len(u.PersonalWallpapers) >= 20 {
		// 删除刚上传的文件（对齐 fs.unlinkSync）。
		_ = upload.RemoveFile(strings.TrimPrefix(url, "/uploads/"))
		c.JSON(400, gin.H{"message": "个人壁纸最多 20 张，请先删除旧的"})
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := h.Repos.Users.WallpapersPushPersonal(c.Request.Context(), user.ID,
		model.Wallpaper{URL: url, Name: name, AddedAt: now}); err != nil {
		c.JSON(500, gin.H{"message": "上传个人壁纸失败"})
		return
	}
	c.JSON(200, gin.H{"url": url, "name": name, "addedAt": now})
}

// PersonalDelete DELETE /api/wallpapers/personal（protect），按 url 删除并清理文件。
// @Summary 删除个人壁纸
// @Tags 壁纸
// @Security bearerAuth
// @Accept json
// @Param body body object true "url"
// @Success 200 {object} map[string]string "已删除"
// @Router /wallpapers/personal [delete]
func (h *Wallpapers) PersonalDelete(c *gin.Context) {
	body := cmsReadBody(c)
	url := asString(body["url"])
	user, _ := middleware.GetUser(c)
	if err := h.Repos.Users.WallpapersRemovePersonal(c.Request.Context(), user.ID, url); err != nil {
		c.JSON(500, gin.H{"message": "删除个人壁纸失败"})
		return
	}
	// 删除文件（对齐 fs.unlink(path, cb) fire-and-forget）。
	if url != "" && strings.HasPrefix(url, "/uploads/") {
		_ = upload.RemoveFile(strings.TrimPrefix(url, "/uploads/"))
	}
	c.JSON(200, gin.H{"message": "已删除"})
}

// abortImageUploadError 渲染图片上传错误（对齐 wallpapers.js 无本地错误
// 中间件时由全局错误处理分支产生的状态码/文案）：
//   - 无文件 → 400 请选择要上传的图片
//   - 超大小 → MulterError LIMIT_FILE_SIZE → 400 "文件上传错误: File too large"
//   - 类型不符 → fileFilter 抛普通 Error → 全局 500（dev 暴露 message）
//   - 魔数不符 → 400 "文件内容与类型不匹配，仅支持图片文件"
func abortImageUploadError(c *gin.Context, cfg *config.Config, err error, fallback string) {
	if err == upload.ErrNoFile {
		c.JSON(400, gin.H{"message": "请选择要上传的图片"})
		return
	}
	if ue, ok := err.(*apperrors.UploadError); ok {
		switch ue.Code {
		case "LIMIT_FILE_SIZE":
			c.JSON(400, gin.H{"message": "文件上传错误: File too large"})
		case "LIMIT_FILE_TYPE":
			msg := "仅支持图片文件 (jpg, jpeg, png, gif, webp)"
			if !cfg.IsDev {
				msg = "服务器内部错误"
			}
			c.JSON(500, gin.H{"message": msg})
		case "BAD_MAGIC":
			c.JSON(400, gin.H{"message": "文件内容与类型不匹配，仅支持图片文件"})
		default:
			c.JSON(500, gin.H{"message": fallback})
		}
		return
	}
	c.JSON(500, gin.H{"message": fallback})
}

// abortWallpaperUploadError 是 abortImageUploadError 的壁纸域包装。
func (h *Wallpapers) abortWallpaperUploadError(c *gin.Context, err error) {
	abortImageUploadError(c, h.Config, err, "上传系统壁纸失败")
}
