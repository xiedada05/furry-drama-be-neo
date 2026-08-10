package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/creator 子域（行为逐分支照抄 backend/routes/creator.js）。
// 只有 2 个端点（GET /my-episodes 与 GET /editable，均 creatorProtect 即
// creator/admin/superadmin），返回创作者本人参与创作的剧集列表。

// Creator 是 /api/creator 域 handler 容器。
type Creator struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewCreator 构造创作者中心 handler 容器。
func NewCreator(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Creator {
	return &Creator{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/creator 全部端点（不含 /api 前缀；路径对齐 creator.js 子路径）。
func (h *Creator) Register(g *gin.RouterGroup) {
	creatorRoles := []string{middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin}
	g.GET("/my-episodes", h.AuthMW.Protect(creatorRoles...), h.MyEpisodes)
	g.GET("/editable", h.AuthMW.Protect(creatorRoles...), h.Editable)
}

// MyEpisodes GET /api/creator/my-episodes（creatorProtect）。
// 创作者参与创作的全部剧集（createdBy/allowedEditors/customAuthors 任一命中）。
// @Summary 创作中心-我的剧集
// @Tags 创作者
// @Security bearerAuth
// @Param page query int false "页码"
// @Param limit query int false "每页数量（默认20，上限100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /creator/my-episodes [get]
func (h *Creator) MyEpisodes(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	page, limit := creatorPage(c)
	filter := bson.M{
		"$or": bson.A{
			bson.M{"createdBy": user.ID},
			bson.M{"allowedEditors": user.ID},
			bson.M{"customAuthors": user.ID},
		},
	}
	h.creatorEpisodeList(c, filter, page, limit)
}

// Editable GET /api/creator/editable（creatorProtect）。
// 创作者可编辑的已审核剧集列表。
// @Summary 创作中心-可编辑剧集
// @Tags 创作者
// @Security bearerAuth
// @Param page query int false "页码"
// @Param limit query int false "每页数量（默认20，上限100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /creator/editable [get]
func (h *Creator) Editable(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	page, limit := creatorPage(c)
	filter := bson.M{
		"$or": bson.A{
			bson.M{"createdBy": user.ID},
			bson.M{"allowedEditors": user.ID},
			bson.M{"customAuthors": user.ID},
		},
		"reviewStatus": "approved",
	}
	h.creatorEpisodeList(c, filter, page, limit)
}

// creatorEpisodeList 按过滤条件分页返回剧集列表（对齐 creator.js 两个端点的
// countDocuments + find().sort({updatedAt:-1}).skip().limit()）。
func (h *Creator) creatorEpisodeList(c *gin.Context, filter bson.M, page, limit int) {
	ctx := c.Request.Context()
	total, err := h.Repos.Episodes.CountDocuments(ctx, filter)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	eps, err := h.Repos.Episodes.FindList(ctx, filter,
		bson.D{{Key: "updatedAt", Value: -1}}, int64((page-1)*limit), int64(limit))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	list := make([]gin.H, 0, len(eps))
	for i := range eps {
		list = append(list, episodeDocJSON(&eps[i]))
	}
	c.JSON(200, gin.H{
		"list":       list,
		"page":       page,
		"limit":      limit,
		"total":      total,
		"totalPages": creatorTotalPages(total, limit),
	})
}

// creatorPage 解析创作中心分页参数（对齐 creator.js 内联逻辑）：
// page 默认 1（parseInt，无最小钳制）；limit 默认 20，上限 100（Math.min(parseInt,100)）。
func creatorPage(c *gin.Context) (page, limit int) {
	page = 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		page = p
	}
	limit = 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limit = l
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit
}

// creatorTotalPages 计算总页数（对齐 Math.ceil(total/limit)，total==0 → 0）。
func creatorTotalPages(total int64, limit int) int {
	if total == 0 {
		return 0
	}
	if limit <= 0 {
		return 0
	}
	return int((total + int64(limit) - 1) / int64(limit))
}
