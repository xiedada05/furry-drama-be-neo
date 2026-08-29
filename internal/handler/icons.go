package handler

import (
	"path"
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

// 本文件实现 /api/icons 图标管理子域。
//
// 公开：GET /list（启用图标 + 组件映射表，供前端图标引擎）；
// 超管：GET /all、POST /upload（批量上传 SVG）、PUT /:id（更新名称/分类/映射）、
// DELETE /:id（删除文档与文件）。

// Icons 是图标域（/api/icons）handler 容器。
type Icons struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewIcons 构造图标 handler 容器。
func NewIcons(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc) *Icons {
	return &Icons{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/icons 全部端点。
func (h *Icons) Register(g *gin.RouterGroup) {
	super := h.AuthMW.Protect(middleware.RoleSuperAdmin)
	g.GET("/list", h.List)
	g.GET("/all", super, h.AdminAll)
	g.POST("/upload", super, h.Upload)
	g.PUT("/:id", super, h.Update)
	g.DELETE("/:id", super, h.Delete)
}

// iconJSON 组装图标响应对象。
func iconJSON(i *model.Icon) gin.H {
	uploadedBy := ""
	if i.UploadedBy != nil {
		uploadedBy = i.UploadedBy.Hex()
	}
	mappings := i.Mappings
	if mappings == nil {
		mappings = []string{}
	}
	return gin.H{
		"_id":         i.ID.Hex(),
		"name":        i.Name,
		"category":    i.Category,
		"url":         i.URL,
		"mappings":    mappings,
		"description": i.Description,
		"uploadedBy":  uploadedBy,
		"enabled":     i.Enabled,
		"createdAt":   i.CreatedAt,
		"updatedAt":   i.UpdatedAt,
		"__v":         i.VersionKey,
	}
}

// iconMappingKeyRe 合法映射 key（组件标识，如 nav.home / button.refresh）。
var iconMappingKeyRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,99}$`)

// sanitizeMappings 校验映射 key 数组（去重、限长 64）。
func sanitizeMappings(input []any) ([]string, string) {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range input {
		s, _ := v.(string)
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !iconMappingKeyRe.MatchString(s) {
			return nil, "映射标识不合法: " + s
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 64 {
		return nil, "单个图标最多映射 64 个组件"
	}
	return out, ""
}

// iconNameFromFilename 从文件名提取图标名（去扩展名、仅保留安全字符）。
func iconNameFromFilename(filename string) string {
	base := strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	base = strings.TrimSpace(base)
	if base == "" {
		base = "icon"
	}
	if len([]rune(base)) > 50 {
		base = string([]rune(base)[:50])
	}
	return base
}

// List GET /api/icons/list：启用图标列表 + 组件映射表（公开，前端图标引擎用）。
// 响应 {"icons": [...], "mappings": {"nav.home": "/uploads/icons/x.svg", ...}}。
func (h *Icons) List(c *gin.Context) {
	list, err := h.Repos.Icons.FindEnabled(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"message": "获取图标列表失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	mappings := gin.H{}
	for i := range list {
		out = append(out, iconJSON(&list[i]))
		for _, key := range list[i].Mappings {
			mappings[key] = list[i].URL
		}
	}
	c.JSON(200, gin.H{"icons": out, "mappings": mappings})
}

// AdminAll GET /api/icons/all（superadmin）：全部图标（含禁用）。
func (h *Icons) AdminAll(c *gin.Context) {
	list, err := h.Repos.Icons.FindAll(c.Request.Context())
	if err != nil {
		c.JSON(500, gin.H{"message": "获取图标列表失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, iconJSON(&list[i]))
	}
	c.JSON(200, out)
}

// Upload POST /api/icons/upload（superadmin）：批量上传 SVG 图标。
// multipart 字段 files（多文件）或 file（单文件）；可选表单 category（默认 general）。
// 响应 {"created": [...], "failed": [{"filename", "error"}]}。
func (h *Icons) Upload(c *gin.Context) {
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(400, gin.H{"message": "请选择要上传的 SVG 图标"})
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		files = form.File["file"]
	}
	if len(files) == 0 {
		c.JSON(400, gin.H{"message": "请选择要上传的 SVG 图标"})
		return
	}
	if len(files) > 50 {
		c.JSON(400, gin.H{"message": "单次最多上传 50 个图标"})
		return
	}
	category := strings.TrimSpace(c.PostForm("category"))
	if category == "" {
		category = "general"
	}
	if len([]rune(category)) > 30 {
		c.JSON(400, gin.H{"message": "分类名不能超过 30 个字符"})
		return
	}
	user, _ := middleware.GetUser(c)
	created := []gin.H{}
	failed := []gin.H{}
	for _, fh := range files {
		url, saveErr := upload.SaveSVGHeader(fh)
		if saveErr != nil {
			failed = append(failed, gin.H{"filename": fh.Filename, "error": saveErr.Error()})
			continue
		}
		icon := &model.Icon{
			Name:       iconNameFromFilename(fh.Filename),
			Category:   category,
			URL:        url,
			UploadedBy: &user.ID,
			Enabled:    true,
		}
		if err := h.Repos.Icons.Create(c.Request.Context(), icon); err != nil {
			failed = append(failed, gin.H{"filename": fh.Filename, "error": "保存图标记录失败"})
			continue
		}
		created = append(created, iconJSON(icon))
	}
	c.JSON(200, gin.H{"created": created, "failed": failed})
}

// Update PUT /api/icons/:id（superadmin）：更新图标名称/分类/映射/描述/启用状态。
func (h *Icons) Update(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "图标 ID 不合法"})
		return
	}
	body := cmsReadBody(c)
	set := bson.M{"updatedAt": time.Now().UTC().Truncate(time.Millisecond)}
	if v, ok := body["name"]; ok {
		name := strings.TrimSpace(asString(v))
		if name == "" || len([]rune(name)) > 50 {
			c.JSON(400, gin.H{"message": "图标名称不能为空且不超过 50 个字符"})
			return
		}
		set["name"] = name
	}
	if v, ok := body["category"]; ok {
		category := strings.TrimSpace(asString(v))
		if category == "" {
			category = "general"
		}
		if len([]rune(category)) > 30 {
			c.JSON(400, gin.H{"message": "分类名不能超过 30 个字符"})
			return
		}
		set["category"] = category
	}
	if v, ok := body["description"]; ok {
		desc := strings.TrimSpace(asString(v))
		if len([]rune(desc)) > 200 {
			c.JSON(400, gin.H{"message": "描述不能超过 200 个字符"})
			return
		}
		set["description"] = desc
	}
	if v, ok := body["enabled"]; ok {
		set["enabled"] = truthy(v)
	}
	if v, ok := body["mappings"]; ok {
		rawList, _ := v.([]any)
		mappings, errMsg := sanitizeMappings(rawList)
		if errMsg != "" {
			c.JSON(400, gin.H{"message": errMsg})
			return
		}
		set["mappings"] = mappings
	}
	updated, err := h.Repos.Icons.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": set})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "图标不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "更新图标失败"})
		return
	}
	c.JSON(200, iconJSON(updated))
}

// Delete DELETE /api/icons/:id（superadmin）：删除图标文档与磁盘文件。
func (h *Icons) Delete(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "图标 ID 不合法"})
		return
	}
	icon, err := h.Repos.Icons.FindOneAndDelete(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "图标不存在"})
			return
		}
		c.JSON(500, gin.H{"message": "删除图标失败"})
		return
	}
	_ = upload.RemoveIconFile(icon.URL)
	c.JSON(200, gin.H{"message": "已删除"})
}
