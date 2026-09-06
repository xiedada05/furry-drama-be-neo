package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/episode-trash 剧集回收站子域（仅 admin/superadmin）。
//
// 被删除（DELETE /api/episodes/:id）与审核拒绝（PUT /api/review/reject/:id）
// 的剧集整体移入 episodetrash 集合，前台即刻不可见；本域提供后台回收站能力：
//   - GET  /                分页列表（含进入原因/备注/操作人）
//   - GET  /:id/versions    编辑内容日志（版本快照记录）
//   - PUT  /:id/restore     恢复（移回 episodes，保留 _id 与版本记录，可回退）
//   - DELETE /:id           彻底删除（清理全部关联数据，释放服务器资源）

// EpisodeTrash 是剧集回收站 handler 容器。
type EpisodeTrash struct {
	Repos *repository.Repos
	AuthM *middleware.Auth
}

// NewEpisodeTrash 构造回收站 handler 容器。
func NewEpisodeTrash(repos *repository.Repos, amw *middleware.Auth) *EpisodeTrash {
	return &EpisodeTrash{Repos: repos, AuthM: amw}
}

// Register 挂载 /api/episode-trash 全部端点（仅 admin/superadmin，creator 403）。
func (h *EpisodeTrash) Register(g *gin.RouterGroup) {
	protect := h.AuthM.Protect(middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin)
	g.GET("", protect, adminOnlyReview, h.List)
	g.GET("/:id/versions", protect, adminOnlyReview, h.Versions)
	g.PUT("/:id/restore", protect, adminOnlyReview, h.Restore)
	g.DELETE("/:id", protect, adminOnlyReview, h.Purge)
}

// trashPage 解析回收站分页参数（page 默认 1；limit 默认 20，上限 100）。
func trashPage(c *gin.Context) (page, limit int) {
	page = 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		page = p
	}
	limit = 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limit = l
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit
}

// trashJSON 组装回收站条目响应（内嵌剧集字段 + 回收站元信息）。
func trashJSON(t *model.EpisodeTrash, users map[string]repository.EpisodesUserRef) gin.H {
	body := episodeTrashBodyJSON(&t.Episode)
	var trashBy any
	if t.TrashBy != nil {
		if u, ok := users[t.TrashBy.Hex()]; ok {
			trashBy = gin.H{"_id": u.ID.Hex(), "accountId": u.AccountID, "username": u.Username}
		} else {
			trashBy = t.TrashBy.Hex()
		}
	}
	body["trashReason"] = t.TrashReason
	body["trashNote"] = t.TrashNote
	body["trashBy"] = trashBy
	body["trashAt"] = t.TrashAt
	return body
}

// episodeTrashBodyJSON 内嵌剧集文档字段（对齐 episodeJSON，去掉响应组装里的 handler 依赖）。
func episodeTrashBodyJSON(e *model.Episode) gin.H {
	var totalEpisodes any
	if e.TotalEpisodes != nil {
		totalEpisodes = *e.TotalEpisodes
	}
	return gin.H{
		"_id":               e.ID.Hex(),
		"title":             e.Title,
		"titleEn":           e.TitleEn,
		"titleJa":           e.TitleJa,
		"description":       e.Description,
		"coverImage":        e.CoverImage,
		"totalEpisodes":     totalEpisodes,
		"currentEpisodes":   e.CurrentEpisodes,
		"status":            e.Status,
		"category":          orEmptyStrings(e.Category),
		"tags":              orEmptyStrings(e.Tags),
		"views":             e.Views,
		"averageRating":     e.AverageRating,
		"ratingCount":       e.RatingCount,
		"createdBy":         refJSON(e.CreatedBy, nil),
		"reviewStatus":      e.ReviewStatus,
		"reviewNote":        e.ReviewNote,
		"createdAt":         e.CreatedAt,
		"updatedAt":         e.UpdatedAt,
	}
}

// List GET /api/episode-trash（admin/superadmin）。
// @Summary 回收站剧集列表
// @Tags 回收站
// @Security bearerAuth
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 20，上限 100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /episode-trash [get]
func (h *EpisodeTrash) List(c *gin.Context) {
	page, limit := trashPage(c)
	ctx := c.Request.Context()
	total, err := h.Repos.EpisodeTrash.Count(ctx, bson.M{})
	if err != nil {
		serverError(c)
		return
	}
	items, err := h.Repos.EpisodeTrash.FindList(ctx, bson.M{}, int64((page-1)*limit), int64(limit))
	if err != nil {
		serverError(c)
		return
	}
	// 批量取操作人用户引用。
	ids := make([]primitive.ObjectID, 0, len(items))
	for i := range items {
		if items[i].TrashBy != nil {
			ids = append(ids, *items[i].TrashBy)
		}
	}
	users, err := h.Repos.Users.EpisodesFindUserRefsByIDs(ctx, dedupIDs(ids))
	if err != nil {
		users = nil
	}
	list := make([]gin.H, 0, len(items))
	for i := range items {
		list = append(list, trashJSON(&items[i], users))
	}
	c.JSON(200, reviewPagedResult(list, page, limit, total))
}

// Versions GET /api/episode-trash/:id/versions（admin/superadmin）。
// @Summary 回收站剧集的编辑内容日志（版本快照）
// @Tags 回收站
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Success 200 {array} map[string]any
// @Router /episode-trash/{id}/versions [get]
func (h *EpisodeTrash) Versions(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	if _, err := h.Repos.EpisodeTrash.FindByID(c.Request.Context(), oid); err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	versions, err := h.Repos.EpisodeVersions.FindAllByEpisode(c.Request.Context(), oid)
	if err != nil {
		serverError(c)
		return
	}
	changedByIDs := make([]primitive.ObjectID, 0, len(versions))
	for _, v := range versions {
		if v.ChangedBy != nil {
			changedByIDs = append(changedByIDs, *v.ChangedBy)
		}
	}
	users, err := h.Repos.Users.EpisodesFindUserRefsByIDs(c.Request.Context(), dedupIDs(changedByIDs))
	if err != nil {
		users = nil
	}
	out := make([]gin.H, 0, len(versions))
	for _, v := range versions {
		var changedBy any
		if v.ChangedBy != nil {
			if u, ok := users[v.ChangedBy.Hex()]; ok {
				changedBy = gin.H{"_id": u.ID.Hex(), "accountId": u.AccountID, "username": u.Username}
			} else {
				changedBy = v.ChangedBy.Hex()
			}
		}
		out = append(out, gin.H{
			"_id":           v.ID.Hex(),
			"episodeId":     v.EpisodeID.Hex(),
			"version":       v.Version,
			"changeSummary": v.ChangeSummary,
			"changedBy":     changedBy,
			"createdAt":     v.CreatedAt,
		})
	}
	c.JSON(200, out)
}

// Restore PUT /api/episode-trash/:id/restore（admin/superadmin）。
// @Summary 从回收站恢复剧集（移回正式集合，可再次编辑/重新提交审核）
// @Tags 回收站
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Success 200 {object} map[string]any "剧集对象"
// @Failure 404 {object} map[string]string "Episode not found"
// @Router /episode-trash/{id}/restore [put]
func (h *EpisodeTrash) Restore(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	episode, err := h.Repos.EpisodeTrash.Restore(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + oid.Hex())
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	c.JSON(200, episodeTrashBodyJSON(episode))
}

// Purge DELETE /api/episode-trash/:id（admin/superadmin）。
// @Summary 彻底删除回收站剧集（清理全部关联数据，释放服务器资源）
// @Tags 回收站
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Success 200 {object} map[string]string "message"
// @Failure 404 {object} map[string]string "Episode not found"
// @Router /episode-trash/{id} [delete]
func (h *EpisodeTrash) Purge(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	ok, err := h.Repos.EpisodeTrash.Purge(c.Request.Context(), oid)
	if err != nil {
		serverError(c)
		return
	}
	if !ok {
		c.JSON(404, gin.H{"message": "Episode not found"})
		return
	}
	middleware.EpisodeCache.Delete("episode_" + oid.Hex())
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	c.JSON(200, gin.H{"message": "已彻底删除"})
}

