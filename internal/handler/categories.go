package handler

import (
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/categories 子域（行为逐分支照抄 backend/routes/categories.js）。

// Categories 是 /api/categories 域 handler 容器。
type Categories struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewCategories 构造分类 handler 容器。
func NewCategories(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Categories {
	return &Categories{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/categories 全部端点（不含 /api 前缀；路径对齐 categories.js 子路径）。
// GET 公开；POST/PUT/DELETE 为 adminOnlyProtect（admin/superadmin）。
// GET/POST 同时注册 "" 与 "/"：gin 默认会对 /api/categories 尾斜杠缺失发起 307 重定向，
// 而 Express（strict routing 关闭）直接匹配，需消除该差异。
func (h *Categories) Register(g *gin.RouterGroup) {
	adminOnly := []string{middleware.RoleAdmin, middleware.RoleSuperAdmin}
	g.GET("", h.List)
	g.GET("/", h.List)
	g.POST("", h.AuthMW.Protect(adminOnly...), h.Create)
	g.POST("/", h.AuthMW.Protect(adminOnly...), h.Create)
	g.PUT("/:id", h.AuthMW.Protect(adminOnly...), h.Update)
	g.DELETE("/:id", h.AuthMW.Protect(adminOnly...), h.Delete)
}

// List GET /api/categories（公开，按 order/createdAt 升序）。
// @Summary 分类列表
// @Tags 分类
// @Success 200 {array} map[string]any "分类数组"
// @Router /categories [get]
func (h *Categories) List(c *gin.Context) {
	cats, err := h.Repos.Categories.FindAllSorted(c.Request.Context())
	if err != nil {
		serverError(c)
		return
	}
	list := make([]gin.H, 0, len(cats))
	for i := range cats {
		list = append(list, categoryJSON(&cats[i]))
	}
	c.JSON(200, list)
}

// Create POST /api/categories（adminOnlyProtect）。
// @Summary 创建分类
// @Tags 分类
// @Security bearerAuth
// @Accept json
// @Param body body object true "name/nameEn/nameJa/order"
// @Success 200 {object} map[string]any "分类对象"
// @Router /categories [post]
func (h *Categories) Create(c *gin.Context) {
	body := cmsReadBody(c)
	name := asString(body["name"])
	existing, err := h.Repos.Categories.FindByName(c.Request.Context(), name)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	if existing != nil {
		c.JSON(400, gin.H{"message": "该分类已存在"})
		return
	}
	// name 缺失/为空 → mongoose required ValidationError → catch-all 500。
	if name == "" {
		serverError(c)
		return
	}
	cat := &model.Category{
		Name:   name,
		NameEn: orDefaultString(body["nameEn"], ""),
		NameJa: orDefaultString(body["nameJa"], ""),
		Order:  toInt(body["order"]),
	}
	if err := h.Repos.Categories.Create(c.Request.Context(), cat); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, categoryJSON(cat))
}

// Update PUT /api/categories/:id（adminOnlyProtect）。
// @Summary 更新分类
// @Tags 分类
// @Security bearerAuth
// @Accept json
// @Param id path string true "分类 ID"
// @Param body body object true "可更新字段 name/nameEn/nameJa/order"
// @Success 200 {object} map[string]any "分类对象"
// @Router /categories/{id} [put]
func (h *Categories) Update(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	cat, err := h.Repos.Categories.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "分类不存在"})
			return
		}
		serverError(c)
		return
	}
	body := cmsReadBody(c)
	// 对齐 categories.js：name 仅 truthy 时更新；nameEn/nameJa/order 仅 !== undefined 时更新。
	if n := asString(body["name"]); n != "" {
		cat.Name = n
	}
	if v, ok := body["nameEn"]; ok {
		cat.NameEn = asString(v)
	}
	if v, ok := body["nameJa"]; ok {
		cat.NameJa = asString(v)
	}
	if v, ok := body["order"]; ok {
		cat.Order = toInt(v)
	}
	if err := h.Repos.Categories.Save(c.Request.Context(), cat); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, categoryJSON(cat))
}

// Delete DELETE /api/categories/:id（adminOnlyProtect）。
// @Summary 删除分类
// @Tags 分类
// @Security bearerAuth
// @Param id path string true "分类 ID"
// @Success 200 {object} map[string]string "message"
// @Router /categories/{id} [delete]
func (h *Categories) Delete(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	if _, err := h.Repos.Categories.FindByID(c.Request.Context(), oid); err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "分类不存在"})
			return
		}
		serverError(c)
		return
	}
	if err := h.Repos.Categories.DeleteByID(c.Request.Context(), oid); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "分类已删除"})
}

// categoryJSON 组装分类响应对象（对齐 mongoose 文档 toJSON：_id hex、时间 RFC3339、含 __v）。
func categoryJSON(cat *model.Category) gin.H {
	return gin.H{
		"_id":       cat.ID.Hex(),
		"name":      cat.Name,
		"nameEn":    cat.NameEn,
		"nameJa":    cat.NameJa,
		"order":     cat.Order,
		"createdAt": cat.CreatedAt,
		"__v":       cat.VersionKey,
	}
}
