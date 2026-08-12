package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

// 本文件实现 /api/versions 子域（行为逐分支照抄 backend/routes/versions.js）。
// 集合为 episodeversions（models/EpisodeVersion.js），复用已实现的 EpisodeVersionRepo。

// Versions 是 /api/versions 域 handler 容器。
type Versions struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewVersions 构造版本历史 handler 容器。
func NewVersions(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Versions {
	return &Versions{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/versions 全部端点（不含 /api 前缀；路径对齐 versions.js 子路径）。
// 全部为 adminProtect（creator/admin/superadmin）。注册顺序对齐 Express 子路由声明顺序。
func (h *Versions) Register(g *gin.RouterGroup) {
	protect := h.AuthMW.Protect(middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin)
	g.GET("/:episodeId", protect, h.List)
	g.GET("/:episodeId/:version", protect, h.Get)
	g.GET("/:episodeId/diff/:v1/:v2", protect, h.Diff)
	g.POST("/:episodeId/rollback/:version", protect, h.Rollback)
}

// versionDiffFieldOrder 是 Episode 文档 toObject() 的字段序（mongoose schema 顺序，
// _id 在前）。diff 端点按此顺序遍历字段，保证与 Express 的 Object.keys 输出顺序一致。
var versionDiffFieldOrder = []string{
	"_id", "title", "titleEn", "titleJa", "description", "descriptionEn", "descriptionJa",
	"coverImage", "totalEpisodes", "currentEpisodes", "status", "category", "tags",
	"platformLinks", "views", "averageRating", "ratingCount", "updateDay", "premiereDate",
	"createdBy", "hideCreator", "allowedEditors", "customAuthors", "qqGroupLink", "qqGroupNumber",
	"reviewStatus", "reviewNote", "pendingChanges", "hasPendingChanges", "pendingChangeSummary",
	"reviewedBy", "reviewedAt", "createdAt", "updatedAt", "__v",
}

// List GET /api/versions/:episodeId（adminProtect）。分页对齐 versions.js 内联逻辑：
// page 默认 1、limit 默认 20（parseInt 语义，无上限钳制）；响应 {versions,page,limit,total,totalPages}。
// @Summary 剧集版本历史
// @Tags 版本
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Param page query int false "页码（默认1）"
// @Param limit query int false "每页数量（默认20）"
// @Success 200 {object} map[string]any "versions/page/limit/total/totalPages"
// @Router /versions/{episodeId} [get]
func (h *Versions) List(c *gin.Context) {
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		serverError(c)
		return
	}
	page, limit, ok := versionsPage(c)
	if !ok {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	total, err := h.Repos.EpisodeVersions.CountByEpisode(ctx, episodeID)
	if err != nil {
		serverError(c)
		return
	}
	versions, err := h.Repos.EpisodeVersions.VersionsFindByEpisodePage(ctx, episodeID, page, limit)
	if err != nil {
		serverError(c)
		return
	}
	users, err := h.versionsUserRefs(ctx, changedByIDs(versions))
	if err != nil {
		serverError(c)
		return
	}
	list := make([]gin.H, 0, len(versions))
	for i := range versions {
		list = append(list, versionsVersionJSON(&versions[i], users))
	}
	// limit=0 时 Express 的 Math.ceil(total/0) = Infinity → JSON null。
	var totalPages any
	if limit > 0 {
		totalPages = (int(total) + limit - 1) / limit
	}
	c.JSON(200, gin.H{
		"versions":   list,
		"page":       page,
		"limit":      limit,
		"total":      total,
		"totalPages": totalPages,
	})
}

// Get GET /api/versions/:episodeId/:version（adminProtect）。
// @Summary 单个版本详情
// @Tags 版本
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Param version path int true "版本号"
// @Success 200 {object} map[string]any "版本对象（含 data 快照与 changedBy）"
// @Router /versions/{episodeId}/{version} [get]
func (h *Versions) Get(c *gin.Context) {
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		serverError(c)
		return
	}
	versionNum, ok := versionsParseInt(c.Param("version"))
	if !ok {
		c.JSON(404, gin.H{"message": "Version not found"})
		return
	}
	versionDoc, err := h.Repos.EpisodeVersions.VersionsFindOneByEpisodeVersion(c.Request.Context(), episodeID, versionNum)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Version not found"})
			return
		}
		serverError(c)
		return
	}
	users, err := h.versionsUserRefs(c.Request.Context(), changedByIDs([]model.EpisodeVersion{*versionDoc}))
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, versionsVersionJSON(versionDoc, users))
}

// Diff GET /api/versions/:episodeId/diff/:v1/:v2（adminProtect）。
// @Summary 版本差异对比
// @Tags 版本
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Param v1 path int true "旧版本号"
// @Param v2 path int true "新版本号"
// @Success 200 {array} map[string]any "field/oldValue/newValue 差异数组"
// @Router /versions/{episodeId}/diff/{v1}/{v2} [get]
func (h *Versions) Diff(c *gin.Context) {
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		serverError(c)
		return
	}
	v1Num, v1OK := versionsParseInt(c.Param("v1"))
	v2Num, v2OK := versionsParseInt(c.Param("v2"))
	// parseInt 失败 → findOne({version: NaN}) 无匹配 → 404 'Version not found'。
	if !v1OK || !v2OK {
		c.JSON(404, gin.H{"message": "Version not found"})
		return
	}
	ctx := c.Request.Context()
	v1Doc, err := h.Repos.EpisodeVersions.VersionsFindOneByEpisodeVersion(ctx, episodeID, v1Num)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Version not found"})
			return
		}
		serverError(c)
		return
	}
	v2Doc, err := h.Repos.EpisodeVersions.VersionsFindOneByEpisodeVersion(ctx, episodeID, v2Num)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Version not found"})
			return
		}
		serverError(c)
		return
	}
	c.JSON(200, versionsDiff(v1Doc, v2Doc))
}

// Rollback POST /api/versions/:episodeId/rollback/:version（adminProtect）。
// 回滚逻辑逐分支对齐 versions.js：先写回滚快照，再裁剪版本到 50，最后
// 用快照 data（去 _id/__v、updatedAt 置当前时间）$set 更新剧集。
// @Summary 回滚到指定版本
// @Tags 版本
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Param version path int true "目标版本号"
// @Success 200 {object} map[string]any "回滚后的剧集对象"
// @Router /versions/{episodeId}/rollback/{version} [post]
func (h *Versions) Rollback(c *gin.Context) {
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		serverError(c)
		return
	}
	versionNum, ok := versionsParseInt(c.Param("version"))
	if !ok {
		c.JSON(404, gin.H{"message": "Version not found"})
		return
	}
	ctx := c.Request.Context()
	versionDoc, err := h.Repos.EpisodeVersions.VersionsFindOneByEpisodeVersion(ctx, episodeID, versionNum)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Version not found"})
			return
		}
		serverError(c)
		return
	}
	currentEpisode, err := h.Repos.Episodes.FindByID(ctx, episodeID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	user, _ := middleware.GetUser(c)
	if user.Role == "creator" {
		isOwner := currentEpisode.CreatedBy != nil && *currentEpisode.CreatedBy == user.ID
		isAllowed := false
		for _, ed := range currentEpisode.AllowedEditors {
			if ed == user.ID {
				isAllowed = true
				break
			}
		}
		if !isOwner && !isAllowed {
			c.JSON(403, gin.H{"message": "No permission to rollback this episode"})
			return
		}
	}

	lastVersion, err := h.Repos.EpisodeVersions.FindLatest(ctx, episodeID)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	newVersionNum := 1
	if lastVersion != nil {
		newVersionNum = lastVersion.Version + 1
	}
	if err := h.Repos.EpisodeVersions.Create(ctx, &model.EpisodeVersion{
		EpisodeID:     episodeID,
		Version:       newVersionNum,
		Data:          currentEpisode.ToVersionData(),
		ChangedBy:     &user.ID,
		ChangeSummary: fmt.Sprintf("Rolled back to version %s", c.Param("version")),
	}); err != nil {
		serverError(c)
		return
	}

	// 限制版本数量为 50（对齐 versions.js 裁剪逻辑）。
	versionCount, err := h.Repos.EpisodeVersions.CountByEpisode(ctx, episodeID)
	if err != nil {
		serverError(c)
		return
	}
	if versionCount > 50 {
		oldest, err := h.Repos.EpisodeVersions.FindOldestN(ctx, episodeID, versionCount-50)
		if err != nil {
			serverError(c)
			return
		}
		ids := make([]any, 0, len(oldest))
		for _, v := range oldest {
			ids = append(ids, v.ID)
		}
		if _, err := h.Repos.EpisodeVersions.DeleteManyByIDs(ctx, ids); err != nil {
			serverError(c)
			return
		}
	}

	// rollbackData = {...versionDoc.data, updatedAt: Date.now()}，删除 _id/__v。
	// mongoose findByIdAndUpdate 对普通对象默认按 $set 处理，故显式包裹 $set。
	rollbackData := copyM(versionDoc.Data)
	rollbackData["updatedAt"] = time.Now().UTC().Truncate(time.Millisecond)
	delete(rollbackData, "_id")
	delete(rollbackData, "__v")
	updated, err := h.Repos.Episodes.FindOneAndUpdate(ctx, episodeID, bson.M{"$set": rollbackData})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	c.JSON(200, versionsEpisodeJSON(updated))
}

// ---- helpers ----

// versionsPage 解析版本列表分页参数（对齐 versions.js GET /:episodeId 内联逻辑）：
// page 默认 1、limit 默认 20；parseInt 语义，无上限钳制。
// 非法值（非数字/负数）在 Express 中触发 mongoose CastError → 500，返回 ok=false。
func versionsPage(c *gin.Context) (page, limit int, ok bool) {
	pageStr, hasPage := c.GetQuery("page")
	limitStr, hasLimit := c.GetQuery("limit")
	page = 1
	if hasPage {
		n, p := versionsParseInt(pageStr)
		if !p || n < 1 {
			return 0, 0, false
		}
		page = n
	}
	limit = 20
	if hasLimit {
		n, p := versionsParseInt(limitStr)
		if !p || n < 0 {
			return 0, 0, false
		}
		limit = n
	}
	return page, limit, true
}

// versionsParseInt 对齐 JS parseInt（base 10）：解析前导空白、可选符号与前导数字；
// 无有效数字返回 ok=false（对应 JS 的 NaN）。
func versionsParseInt(s string) (int, bool) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\f' || s[i] == '\v') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var n int64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int64(s[i]-'0')
		i++
	}
	if i == start {
		return 0, false
	}
	if neg {
		n = -n
	}
	return int(n), true
}

// versionsUserRefs 批量查 changedBy 用户引用（对齐 populate('changedBy', 'accountId username')）。
func (h *Versions) versionsUserRefs(ctx context.Context, ids []primitive.ObjectID) (map[string]repository.EpisodesUserRef, error) {
	return h.Repos.Users.EpisodesFindUserRefsByIDs(ctx, dedupIDs(ids))
}

// changedByIDs 收集一批版本的 changedBy ID。
func changedByIDs(versions []model.EpisodeVersion) []primitive.ObjectID {
	ids := []primitive.ObjectID{}
	for i := range versions {
		if versions[i].ChangedBy != nil {
			ids = append(ids, *versions[i].ChangedBy)
		}
	}
	return ids
}

// versionsVersionJSON 组装版本响应对象（对齐 mongoose 文档 toJSON：_id/episodeId hex、
// data 为完整快照、changedBy populate 或 null、时间 RFC3339、含 __v）。
func versionsVersionJSON(v *model.EpisodeVersion, users map[string]repository.EpisodesUserRef) gin.H {
	return gin.H{
		"_id":           v.ID.Hex(),
		"episodeId":     v.EpisodeID.Hex(),
		"version":       v.Version,
		"data":          v.Data,
		"changedBy":     refJSON(v.ChangedBy, users),
		"changeSummary": v.ChangeSummary,
		"createdAt":     v.CreatedAt,
		"__v":           v.VersionKey,
	}
}

// versionsDiff 计算 v1/v2 两个版本 data 的差异（对齐 versions.js diff 端点）：
// 逐字段 JSON.stringify 比较；字段仅在一边存在时（另一侧 JS undefined）必然入 diff，
// 且缺失侧不输出该键（对齐 res.json 对 undefined 的省略）。
func versionsDiff(v1, v2 *model.EpisodeVersion) []gin.H {
	diff := make([]gin.H, 0)
	seen := map[string]bool{}
	check := func(field string) {
		oldVal, hasOld := v1.Data[field]
		newVal, hasNew := v2.Data[field]
		item := gin.H{"field": field}
		changed := false
		if hasOld && hasNew {
			ob, err1 := json.Marshal(oldVal)
			nb, err2 := json.Marshal(newVal)
			if err1 != nil || err2 != nil || string(ob) != string(nb) {
				changed = true
			}
			item["oldValue"] = oldVal
			item["newValue"] = newVal
		} else if hasOld {
			changed = true
			item["oldValue"] = oldVal
		} else if hasNew {
			changed = true
			item["newValue"] = newVal
		}
		if changed {
			diff = append(diff, item)
		}
	}
	present := func(m primitive.M, f string) bool {
		_, ok := m[f]
		return ok
	}
	for _, f := range versionDiffFieldOrder {
		if present(v1.Data, f) || present(v2.Data, f) {
			check(f)
			seen[f] = true
		}
	}
	// 剩余未知字段（非 schema 字段）按字典序追加，保证输出确定。
	extra := []string{}
	for f := range v1.Data {
		if !seen[f] {
			extra = append(extra, f)
		}
	}
	for f := range v2.Data {
		if !seen[f] {
			extra = append(extra, f)
		}
	}
	sort.Strings(extra)
	for _, f := range extra {
		check(f)
	}
	return diff
}

// versionsEpisodeJSON 组装回滚后的剧集响应（对齐 episodes.go episodeJSON(users=nil)：
// 引用字段输出 hex 字符串、空切片补 []、含 __v）。
func versionsEpisodeJSON(e *model.Episode) gin.H {
	var totalEpisodes any
	if e.TotalEpisodes != nil {
		totalEpisodes = *e.TotalEpisodes
	}
	return gin.H{
		"_id":                  e.ID.Hex(),
		"title":                e.Title,
		"titleEn":              e.TitleEn,
		"titleJa":              e.TitleJa,
		"description":          e.Description,
		"descriptionEn":        e.DescriptionEn,
		"descriptionJa":        e.DescriptionJa,
		"coverImage":           e.CoverImage,
		"totalEpisodes":        totalEpisodes,
		"currentEpisodes":      e.CurrentEpisodes,
		"status":               e.Status,
		"category":             orEmptyStrings(e.Category),
		"tags":                 orEmptyStrings(e.Tags),
		"platformLinks":        orEmptyM(e.PlatformLinks),
		"views":                e.Views,
		"averageRating":        e.AverageRating,
		"ratingCount":          e.RatingCount,
		"updateDay":            e.UpdateDay,
		"premiereDate":         e.PremiereDate,
		"createdBy":            refJSON(e.CreatedBy, nil),
		"hideCreator":          e.HideCreator,
		"allowedEditors":       refsJSON(e.AllowedEditors, nil),
		"customAuthors":        refsJSON(e.CustomAuthors, nil),
		"qqGroupLink":          e.QQGroupLink,
		"qqGroupNumber":        e.QQGroupNumber,
		"reviewStatus":         e.ReviewStatus,
		"reviewNote":           e.ReviewNote,
		"pendingChanges":       e.PendingChanges,
		"hasPendingChanges":    e.HasPendingChanges,
		"pendingChangeSummary": e.PendingChangeSummary,
		"reviewedBy":           e.ReviewedBy,
		"reviewedAt":           e.ReviewedAt,
		"createdAt":            e.CreatedAt,
		"updatedAt":            e.UpdatedAt,
		"__v":                  e.VersionKey,
	}
}
