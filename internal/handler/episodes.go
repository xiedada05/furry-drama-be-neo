package handler

import (
	"context"
	"fmt"
	"math"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	apperrors "github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/upload"
)

// 观看去重冷却（对齐 routes/episodes.js VIEW_COOLDOWN 10 分钟）。
const viewCooldown = 10 * time.Minute

// 邮件通知去重冷却（对齐 utils/episodeNotify.js EMAIL_NOTIFY_COOLDOWN 1 小时）。
const emailNotifyCooldown = time.Hour

// Episodes 是剧集域（/api/episodes）handler 容器，行为逐端点对齐
// backend/routes/episodes.js。16 个端点通过 Register 挂载到
// /api/episodes（由 server.RegisterRoutes 的 MountDual 双版本镜像）。
type Episodes struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	Mail   *email.Client

	// viewLog 观看去重：key = "<episodeId>_<ip>" → 上次计数时间
	// （对齐 Express viewTracker Map，10min 冷却）。
	viewMu  sync.Mutex
	viewLog map[string]time.Time

	// emailLog 邮件去重：key = "<episodeId>_<epNum>_<eventType>" → 上次发送时间
	// （对齐 shouldSendEpisodeEmail，1h 冷却）。
	emailMu  sync.Mutex
	emailLog map[string]time.Time
}

// NewEpisodes 构造剧集 handler 容器。mail 为邮件客户端（可为 nil，跳过发信）；
// rl 为限流中间件工厂（挂 view 限流器）。
func NewEpisodes(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc, mail *email.Client) *Episodes {
	return &Episodes{
		Repos:    repos,
		Config:   cfg,
		AuthMW:   amw,
		RL:       rl,
		Mail:     mail,
		viewLog:  map[string]time.Time{},
		emailLog: map[string]time.Time{},
	}
}

// Register 挂载全部剧集路由（路径照抄 Express 子路径，不含 /api 前缀）。
// 顺序对齐 routes/episodes.js：静态路由在前，参数路由在后。
func (h *Episodes) Register(g *gin.RouterGroup) {
	creatorRoles := []string{middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin}
	g.POST("/upload", h.AuthMW.Protect(creatorRoles...), h.Upload)
	g.GET("", h.List)
	g.GET("/search-suggestions", h.SearchSuggestions)
	g.GET("/popular-tags", h.PopularTags)
	g.GET("/search", h.Search)
	g.GET("/:id/user-status", h.AuthMW.Protect(), h.UserStatus)
	g.GET("/:id", h.AuthMW.OptionalAuth(), h.Detail)
	g.PUT("/:id/view", h.RL(ratelimit.ViewSpec), h.IncView)
	g.PUT("/single/:id/view", h.RL(ratelimit.ViewSpec), h.SingleIncView)
	g.PUT("/single/:id", h.AuthMW.Protect(creatorRoles...), h.UpdateSingle)
	g.DELETE("/single/:id", h.AuthMW.Protect(creatorRoles...), h.DeleteSingle)
	g.POST("", h.AuthMW.Protect(creatorRoles...), h.Create)
	g.POST("/:id/episodes", h.AuthMW.Protect(creatorRoles...), h.CreateSingle)
	g.PUT("/:id", h.AuthMW.Protect(creatorRoles...), h.Update)
	g.POST("/:id/resubmit", h.AuthMW.Protect(creatorRoles...), h.Resubmit)
	g.DELETE("/:id", h.AuthMW.Protect(creatorRoles...), h.Delete)
}

// ---- 端点 ----

// Upload POST /api/episodes/upload（creatorProtect + 封面 ≤5MB）。
// @Summary 上传封面图片
// @Tags 剧集
// @Security bearerAuth
// @Accept multipart/form-data
// @Param image formData file true "图片（≤5MB）"
// @Success 200 {object} map[string]string "url"
// @Router /episodes/upload [post]
func (h *Episodes) Upload(c *gin.Context) {
	url, err := upload.SaveImage(c, "image", "cover", 5<<20)
	if err != nil {
		if err == upload.ErrNoFile {
			c.JSON(400, gin.H{"message": "请选择要上传的图片"})
			return
		}
		if ue, ok := err.(*apperrors.UploadError); ok {
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

// List GET /api/episodes（公开列表，带内存缓存）。
// @Summary 剧集列表
// @Tags 剧集
// @Param page query int false "页码"
// @Param limit query int false "每页数量（≤100）"
// @Param category query string false "分类"
// @Param sort query string false "latest|views|premiere|rating"
// @Param status query string false "状态"
// @Param tag query string false "标签"
// @Param search query string false "关键词（标题/简介）"
// @Param minRating query number false "最低评分"
// @Param year query string false "年份或 earlier"
// @Param order query string false "desc|asc"
// @Success 200 {object} map[string]any "episodes/page/limit/total/totalPages"
// @Router /episodes [get]
func (h *Episodes) List(c *gin.Context) {
	category := c.Query("category")
	sortBy := c.Query("sort")
	status := c.Query("status")
	tag := c.Query("tag")
	search := c.Query("search")
	minRating := c.Query("minRating")
	year := c.Query("year")
	order := c.Query("order")
	pageStr := c.Query("page")
	limitStr := c.Query("limit")

	usePagination := pageStr != "" && limitStr != ""
	pageNum := 1
	limitNum := 100
	if usePagination {
		if p, err := strconv.Atoi(pageStr); err == nil {
			pageNum = p
		}
		if l, err := strconv.Atoi(limitStr); err == nil {
			limitNum = l
			if limitNum < 1 {
				limitNum = 1
			}
			if limitNum > 100 {
				limitNum = 100
			}
		}
	}

	catOrAll := category
	if catOrAll == "" {
		catOrAll = "all"
	}
	sortOrLatest := sortBy
	if sortOrLatest == "" {
		sortOrLatest = "latest"
	}
	orderOrDesc := order
	if orderOrDesc == "" {
		orderOrDesc = "desc"
	}
	cacheKey := "episodes_" + catOrAll + "_" + sortOrLatest + "_" + orderOrDesc + "_" +
		status + "_" + tag + "_" + search + "_" + minRating + "_" + year + "_" +
		strconv.Itoa(pageNum) + "_" + strconv.Itoa(limitNum)
	if v, ok := middleware.EpisodeCache.Get(cacheKey); ok {
		c.JSON(200, v)
		return
	}

	ctx := c.Request.Context()
	baseQuery := bson.M{
		"$or": bson.A{
			bson.M{"reviewStatus": "approved"},
			bson.M{"reviewStatus": bson.M{"$exists": false}},
		},
	}
	if category != "" {
		baseQuery["category"] = bson.M{"$in": bson.A{category}}
	}
	if status != "" {
		baseQuery["status"] = status
	}
	if tag != "" {
		baseQuery["tags"] = bson.M{"$in": bson.A{tag}}
	}
	if minRating != "" {
		r, err := strconv.ParseFloat(minRating, 64)
		if err != nil {
			r = math.NaN() // 对齐 parseFloat 非数字 → NaN → 无匹配
		}
		baseQuery["averageRating"] = bson.M{"$gte": r}
	}

	var query bson.M
	if search != "" {
		escaped := escapeRegex(search)
		searchCondition := bson.M{
			"$or": bson.A{
				bson.M{"title": bson.M{"$regex": escaped, "$options": "i"}},
				bson.M{"description": bson.M{"$regex": escaped, "$options": "i"}},
			},
		}
		query = bson.M{"$and": bson.A{baseQuery, searchCondition}}
	} else {
		query = copyM(baseQuery)
	}
	if year != "" {
		var yearCondition bson.M
		if year == "earlier" {
			cutoff := time.Date(2016, 1, 1, 0, 0, 0, 0, time.Local)
			yearCondition = bson.M{
				"$or": bson.A{
					bson.M{"premiereDate": bson.M{"$lt": cutoff}},
					bson.M{"createdAt": bson.M{"$lt": cutoff}},
				},
			}
		} else if yearNum, err := strconv.Atoi(year); err == nil {
			start := time.Date(yearNum, 1, 1, 0, 0, 0, 0, time.Local)
			end := time.Date(yearNum+1, 1, 1, 0, 0, 0, 0, time.Local)
			yearCondition = bson.M{
				"$or": bson.A{
					bson.M{"premiereDate": bson.M{"$gte": start, "$lt": end}},
					bson.M{"createdAt": bson.M{"$gte": start, "$lt": end}},
				},
			}
		}
		if yearCondition != nil {
			if andArr, ok := query["$and"].(bson.A); ok {
				query["$and"] = append(andArr, yearCondition)
			} else {
				query = bson.M{"$and": bson.A{baseQuery, yearCondition}}
			}
		}
	}

	sortOrder := -1
	if order == "asc" {
		sortOrder = 1
	}
	var sortOption bson.D
	switch sortBy {
	case "views":
		sortOption = bson.D{{Key: "views", Value: sortOrder}}
	case "premiere":
		sortOption = bson.D{{Key: "premiereDate", Value: sortOrder}}
	case "rating":
		sortOption = bson.D{{Key: "averageRating", Value: sortOrder}, {Key: "ratingCount", Value: sortOrder}}
	default:
		sortOption = bson.D{{Key: "updatedAt", Value: sortOrder}}
	}

	total, err := h.Repos.Episodes.CountDocuments(ctx, query)
	if err != nil {
		serverError(c)
		return
	}
	var skip, limit int64
	if usePagination {
		skip = int64((pageNum - 1) * limitNum)
		limit = int64(limitNum)
	} else {
		limit = int64(limitNum)
	}
	episodes, err := h.Repos.Episodes.FindList(ctx, query, sortOption, skip, limit)
	if err != nil {
		serverError(c)
		return
	}
	users, err := h.userRefsForEpisodes(ctx, episodes)
	if err != nil {
		serverError(c)
		return
	}
	list := make([]gin.H, 0, len(episodes))
	for i := range episodes {
		list = append(list, h.episodeJSON(&episodes[i], users))
	}
	var result gin.H
	if usePagination {
		totalPages := (int(total) + limitNum - 1) / limitNum
		result = gin.H{
			"episodes":   list,
			"page":       pageNum,
			"limit":      limitNum,
			"total":      total,
			"totalPages": totalPages,
		}
	} else {
		result = gin.H{"episodes": list, "total": total}
	}
	middleware.EpisodeCache.Set(cacheKey, result)
	c.JSON(200, result)
}

// SearchSuggestions GET /api/episodes/search-suggestions。
// @Summary 搜索建议（标题/标签）
// @Tags 剧集
// @Param q query string true "关键词"
// @Success 200 {object} map[string]any "titles/tags"
// @Router /episodes/search-suggestions [get]
func (h *Episodes) SearchSuggestions(c *gin.Context) {
	q := c.Query("q")
	if q == "" || len(strings.TrimSpace(q)) < 1 {
		c.JSON(200, gin.H{"titles": []gin.H{}, "tags": []string{}})
		return
	}
	trimmed := strings.TrimSpace(q)
	escaped := escapeRegex(trimmed)
	ctx := c.Request.Context()
	approved := bson.M{
		"$or": bson.A{
			bson.M{"reviewStatus": "approved"},
			bson.M{"reviewStatus": bson.M{"$exists": false}},
		},
	}

	eps, err := h.Repos.Episodes.FindList(ctx, bson.M{"$and": bson.A{approved, bson.M{
		"$or": bson.A{
			bson.M{"title": bson.M{"$regex": escaped, "$options": "i"}},
			bson.M{"titleEn": bson.M{"$regex": escaped, "$options": "i"}},
		},
	}}}, bson.D{{Key: "views", Value: -1}}, 0, 5)
	if err != nil {
		serverError(c)
		return
	}
	titles := make([]gin.H, 0, len(eps))
	for _, e := range eps {
		titles = append(titles, gin.H{"title": e.Title, "titleEn": e.TitleEn})
	}

	tagDocs, err := h.Repos.Episodes.FindList(ctx, bson.M{"$and": bson.A{approved, bson.M{
		"tags": bson.M{"$regex": escaped, "$options": "i"},
	}}}, nil, 0, 5)
	if err != nil {
		serverError(c)
		return
	}
	re := regexp.MustCompile("(?i)" + escaped)
	seen := map[string]bool{}
	tags := []string{}
	for _, e := range tagDocs {
		for _, t := range e.Tags {
			if re.MatchString(t) && !seen[t] {
				seen[t] = true
				tags = append(tags, t)
			}
		}
	}
	if len(tags) > 5 {
		tags = tags[:5]
	}
	c.JSON(200, gin.H{"titles": titles, "tags": tags})
}

// PopularTags GET /api/episodes/popular-tags。
// @Summary 热门标签 Top20
// @Tags 剧集
// @Success 200 {array} map[string]any "name/count"
// @Router /episodes/popular-tags [get]
func (h *Episodes) PopularTags(c *gin.Context) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"$or": bson.A{
				bson.M{"reviewStatus": "approved"},
				bson.M{"reviewStatus": bson.M{"$exists": false}},
			},
		}}},
		{{Key: "$unwind", Value: "$tags"}},
		{{Key: "$group", Value: bson.M{"_id": "$tags", "count": bson.M{"$sum": 1}}}},
		// count 并列时按名称排序兜底：$group 输出顺序不确定，无次级键会导致
		// 相同请求多次返回不同顺序（前端标签云跳动、测试不稳定）。
		{{Key: "$sort", Value: bson.D{{Key: "count", Value: -1}, {Key: "_id", Value: 1}}}},
		{{Key: "$limit", Value: 20}},
		{{Key: "$project", Value: bson.M{"_id": 0, "name": "$_id", "count": 1}}},
	}
	result, err := h.Repos.Episodes.Aggregate(c.Request.Context(), pipeline)
	if err != nil {
		serverError(c)
		return
	}
	// 空结果返回 [] 而非 null（对齐 Express 的 []）。
	if len(result) == 0 {
		c.JSON(200, []bson.M{})
		return
	}
	c.JSON(200, result)
}

// Search GET /api/episodes/search。
// @Summary 搜索剧集
// @Tags 剧集
// @Param q query string true "关键词"
// @Param limit query int false "条数（默认10，≤50）"
// @Success 200 {array} map[string]any
// @Router /episodes/search [get]
func (h *Episodes) Search(c *gin.Context) {
	q := c.Query("q")
	if q == "" || strings.TrimSpace(q) == "" {
		c.JSON(200, []gin.H{})
		return
	}
	limit := 10
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limit = l
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	escaped := escapeRegex(strings.TrimSpace(q))
	approved := bson.M{
		"$or": bson.A{
			bson.M{"reviewStatus": "approved"},
			bson.M{"reviewStatus": bson.M{"$exists": false}},
		},
	}
	eps, err := h.Repos.Episodes.FindList(c.Request.Context(), bson.M{"$and": bson.A{approved, bson.M{
		"$or": bson.A{
			bson.M{"title": bson.M{"$regex": escaped, "$options": "i"}},
			bson.M{"description": bson.M{"$regex": escaped, "$options": "i"}},
		},
	}}}, bson.D{{Key: "views", Value: -1}}, 0, int64(limit))
	if err != nil {
		serverError(c)
		return
	}
	out := make([]gin.H, 0, len(eps))
	for _, e := range eps {
		out = append(out, gin.H{
			"_id":           e.ID.Hex(),
			"title":         e.Title,
			"coverImage":    e.CoverImage,
			"category":      orEmptyStrings(e.Category),
			"averageRating": e.AverageRating,
		})
	}
	c.JSON(200, out)
}

// UserStatus GET /api/episodes/:id/user-status（protect）。
// @Summary 当前用户对剧集的状态
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Success 200 {object} map[string]any "isFollowing/followedAtEpisodes/watchedEpisodes/score/isFavorite"
// @Router /episodes/{id}/user-status [get]
func (h *Episodes) UserStatus(c *gin.Context) {
	episodeID, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	user, _ := middleware.GetUser(c)
	ctx := c.Request.Context()

	follow, err := h.Repos.Follows.FollowFindByUserEpisode(ctx, user.ID, episodeID)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	history, err := h.Repos.Histories.EpisodesFindByUserEpisode(ctx, user.ID, episodeID)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	rating, err := h.Repos.Ratings.EpisodesFindByUserEpisode(ctx, user.ID, episodeID)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	favorite, err := h.Repos.Favorites.FavoriteFindByUserEpisode(ctx, user.ID, episodeID)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}

	var followedAtEpisodes any // nil → JSON null
	if follow != nil {
		followedAtEpisodes = follow.FollowedAtEpisodes
	}
	watched := []int{}
	if history != nil && len(history.WatchedEpisodes) > 0 {
		watched = history.WatchedEpisodes
	}
	score := 0
	if rating != nil && rating.Score != 0 {
		score = rating.Score
	}
	c.JSON(200, gin.H{
		"isFollowing":        follow != nil,
		"followedAtEpisodes": followedAtEpisodes,
		"watchedEpisodes":    watched,
		"score":              score,
		"isFavorite":         favorite != nil,
	})
}

// Detail GET /api/episodes/:id（optionalAuth）。
// @Summary 剧集详情
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Success 200 {object} map[string]any "剧集对象 + episodes 单集数组"
// @Router /episodes/{id} [get]
func (h *Episodes) Detail(c *gin.Context) {
	idStr := c.Param("id")
	cacheKey := "episode_" + idStr
	if v, ok := middleware.EpisodeCache.Get(cacheKey); ok {
		c.JSON(200, v)
		return
	}
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	episode, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}

	// 审核态可见性：仅"已通过"或"无审核态字段"对公众可见；待审核/未通过仅
	// 创作者本人、被授权编辑、管理员可见（未授权按 404 处理，避免泄露）。
	isApproved := episode.ReviewStatus == "" || episode.ReviewStatus == "approved"
	if !isApproved {
		user, ok := middleware.GetUser(c)
		if !ok || !canViewUnapproved(user, episode) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		// 待审核/未通过内容禁止被搜索引擎索引（对有权查看者返回时同样生效）。
		c.Header("X-Robots-Tag", "noindex, nofollow")
	}

	singles, err := h.Repos.SingleEpisodes.FindByEpisode(ctx, oid)
	if err != nil {
		serverError(c)
		return
	}
	users, err := h.userRefsForEpisode(ctx, episode)
	if err != nil {
		serverError(c)
		return
	}
	obj := h.episodeJSON(episode, users)
	singlesList := make([]gin.H, 0, len(singles))
	for i := range singles {
		singlesList = append(singlesList, singleEpisodeJSON(&singles[i]))
	}
	obj["episodes"] = singlesList
	if isApproved {
		middleware.EpisodeCache.Set(cacheKey, obj)
	}
	c.JSON(200, obj)
}

// IncView PUT /api/episodes/:id/view（viewLimiter）。
// @Summary 观看计数 +1
// @Tags 剧集
// @Param id path string true "剧集 ID"
// @Success 200 {object} map[string]any "剧集对象"
// @Router /episodes/{id}/view [put]
func (h *Episodes) IncView(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()

	// 先校验审核状态：未审核通过的剧集不计浏览量。
	existing, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	if existing.ReviewStatus != "" && existing.ReviewStatus != "approved" {
		c.JSON(404, gin.H{"message": "Episode not found"})
		return
	}

	viewKey := idStr + "_" + h.clientIP(c)
	now := time.Now()
	h.viewMu.Lock()
	lastView, ok := h.viewLog[viewKey]
	h.viewMu.Unlock()
	if ok && now.Sub(lastView) < viewCooldown {
		// 冷却期内：不计数，直接返回当前剧集。
		c.JSON(200, h.episodeJSON(existing, nil))
		return
	}
	h.viewMu.Lock()
	h.viewLog[viewKey] = now
	h.pruneViewLogLocked(now)
	h.viewMu.Unlock()

	episode, err := h.Repos.Episodes.IncViews(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + idStr)
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	c.JSON(200, h.episodeJSON(episode, nil))
}

// SingleIncView PUT /api/episodes/single/:id/view（viewLimiter）。
// @Summary 单集观看计数 +1
// @Tags 剧集
// @Param id path string true "单集 ID"
// @Success 200 {object} map[string]any "单集对象"
// @Router /episodes/single/{id}/view [put]
func (h *Episodes) SingleIncView(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	s, err := h.Repos.SingleEpisodes.IncViews(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Single episode not found"})
			return
		}
		serverError(c)
		return
	}
	c.JSON(200, singleEpisodeJSON(s))
}

// UpdateSingle PUT /api/episodes/single/:id（adminProtect，即 creator/admin/superadmin）。
// @Summary 更新单集
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "单集 ID"
// @Param body body object true "单集可更新字段"
// @Success 200 {object} map[string]any "单集对象"
// @Router /episodes/single/{id} [put]
func (h *Episodes) UpdateSingle(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		serverError(c)
		return
	}
	body := h.readJSONBody(c)
	ctx := c.Request.Context()

	updateData := bson.M{}
	if v, ok := body["episodeNumber"]; ok {
		updateData["episodeNumber"] = toInt(v)
	}
	if v, ok := body["title"]; ok {
		updateData["title"] = asString(v)
	}
	if v, ok := body["titleEn"]; ok {
		updateData["titleEn"] = asString(v)
	}
	if v, ok := body["titleJa"]; ok {
		updateData["titleJa"] = asString(v)
	}
	if v, ok := body["duration"]; ok {
		updateData["duration"] = asString(v)
	}
	if v, ok := body["platformLinks"]; ok {
		updateData["platformLinks"] = toStrMap(v)
	}
	if v, ok := body["scheduledDate"]; ok {
		updateData["scheduledDate"] = toDate(v)
	}
	var isScheduledVal *bool
	if v, ok := body["isScheduled"]; ok {
		b := truthy(v)
		isScheduledVal = &b
		// isUpcoming 自动同步 isScheduled，避免前端维护两个字段。
		updateData["isScheduled"] = b
		updateData["isUpcoming"] = b
	}
	if v, ok := body["premiereDate"]; ok {
		updateData["premiereDate"] = toDate(v)
	}
	if isScheduledVal != nil && *isScheduledVal {
		updateData["releaseDate"] = nil
	} else if v, ok := body["releaseDate"]; ok {
		updateData["releaseDate"] = toDate(v)
	}

	// 先取旧记录，用于检测 isUpcoming true→false（预告转可观看）触发追番通知。
	oldSingle, err := h.Repos.SingleEpisodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Single episode not found"})
			return
		}
		serverError(c)
		return
	}
	becameAvailable := oldSingle.IsUpcoming && (updateData["isUpcoming"] == false)

	// mongoose findByIdAndUpdate 对无 $ 运算符的普通对象默认按 $set 处理（overwrite:false），
	// 这里显式包裹 $set 保持同一语义（仅更新提供的字段）。
	single, err := h.Repos.SingleEpisodes.FindOneAndUpdate(ctx, oid, bson.M{"$set": updateData})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Single episode not found"})
			return
		}
		serverError(c)
		return
	}

	middleware.EpisodeCache.Delete("episode_" + single.EpisodeID.Hex())
	middleware.EpisodeCache.DeleteByPrefix("episodes_")

	// 预告集变为可观看集：currentEpisodes +1 并通知追番用户。
	if becameAvailable {
		if err := h.Repos.Episodes.IncCurrentEpisodes(ctx, single.EpisodeID, 1); err != nil {
			serverError(c)
			return
		}
		episode, err := h.Repos.Episodes.FindByID(ctx, single.EpisodeID)
		if err != nil && !repository.IsNotFound(err) {
			serverError(c)
			return
		}
		if episode != nil {
			if err := h.notifyNewEpisode(ctx, single.EpisodeID, episode, single.EpisodeNumber, "available"); err != nil {
				serverError(c)
				return
			}
		}
	}

	// 预告集被编辑（仍为预告）：区分预告视频更新 vs 预告信息更新。
	stillPreview := oldSingle.IsUpcoming && !becameAvailable
	if stillPreview {
		videoChanged := false
		if v, ok := updateData["platformLinks"]; ok {
			oldLinks := toStrMap(oldSingle.PlatformLinks)
			newLinks := toStrMap(v)
			if len(newLinks) != len(oldLinks) {
				videoChanged = true
			} else {
				for k, nv := range newLinks {
					ov, exists := oldLinks[k]
					if !exists || fmt.Sprintf("%v", ov) != fmt.Sprintf("%v", nv) {
						videoChanged = true
						break
					}
				}
			}
		}
		infoChanged := false
		infoFields := []string{"title", "titleEn", "titleJa", "duration", "scheduledDate",
			"isScheduled", "premiereDate", "releaseDate", "episodeNumber"}
		for _, f := range infoFields {
			if nv, ok := updateData[f]; ok {
				// 日期字段在 Express 中 String(新串) 恒不等于 String(旧 Date)，视为已变更。
				if isDateField(f) {
					infoChanged = true
					break
				}
				if fmt.Sprintf("%v", nv) != infoFieldString(singleFieldValue(oldSingle, f)) {
					infoChanged = true
					break
				}
			}
		}
		if videoChanged || infoChanged {
			eventType := "preview_info"
			if videoChanged {
				eventType = "preview_video"
			}
			episode, err := h.Repos.Episodes.FindByID(ctx, single.EpisodeID)
			if err != nil && !repository.IsNotFound(err) {
				serverError(c)
				return
			}
			if episode != nil {
				if err := h.notifyNewEpisode(ctx, single.EpisodeID, episode, single.EpisodeNumber, eventType); err != nil {
					serverError(c)
					return
				}
			}
		}
	}

	c.JSON(200, singleEpisodeJSON(single))
}

// DeleteSingle DELETE /api/episodes/single/:id（adminProtect）。
// @Summary 删除单集
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "单集 ID"
// @Success 200 {object} map[string]string "message"
// @Router /episodes/single/{id} [delete]
func (h *Episodes) DeleteSingle(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	single, err := h.Repos.SingleEpisodes.FindOneAndDelete(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Single episode not found"})
			return
		}
		serverError(c)
		return
	}
	// 仅可观看集才扣减 currentEpisodes（预告集添加时未计入）。
	if !single.IsUpcoming {
		if err := h.Repos.Episodes.IncCurrentEpisodes(c.Request.Context(), single.EpisodeID, -1); err != nil {
			serverError(c)
			return
		}
	}
	middleware.EpisodeCache.Delete("episode_" + single.EpisodeID.Hex())
	c.JSON(200, gin.H{"message": "Single episode deleted"})
}

// Create POST /api/episodes（creatorProtect）。
// @Summary 创建剧集
// @Tags 剧集
// @Security bearerAuth
// @Accept json
// @Param body body object true "剧集信息"
// @Success 201 {object} map[string]any "剧集对象"
// @Router /episodes [post]
func (h *Episodes) Create(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	body := h.readJSONBody(c)

	// 对齐 mongoose required 校验 → ValidationError → 400。
	var msgs []string
	if asString(body["title"]) == "" {
		msgs = append(msgs, "Path `title` is required.")
	}
	if asString(body["description"]) == "" {
		msgs = append(msgs, "Path `description` is required.")
	}
	if asString(body["coverImage"]) == "" {
		msgs = append(msgs, "Path `coverImage` is required.")
	}
	status := "ongoing"
	if v, ok := body["status"]; ok {
		s := asString(v)
		if s != "ongoing" && s != "completed" && s != "upcoming" {
			msgs = append(msgs, fmt.Sprintf("`%s` is not a valid enum value for path `status`.", s))
		} else {
			status = s
		}
	}
	if len(msgs) > 0 {
		c.JSON(400, gin.H{"message": strings.Join(msgs, ", ")})
		return
	}

	isCreator := user.Role == "creator"
	reviewStatus := "approved"
	if isCreator {
		reviewStatus = "pending"
	}
	e := &model.Episode{
		Title:           asString(body["title"]),
		TitleEn:         orDefaultString(body["titleEn"], ""),
		TitleJa:         orDefaultString(body["titleJa"], ""),
		Description:     asString(body["description"]),
		DescriptionEn:   orDefaultString(body["descriptionEn"], ""),
		DescriptionJa:   orDefaultString(body["descriptionJa"], ""),
		CoverImage:      asString(body["coverImage"]),
		TotalEpisodes:   toTotalEpisodes(body["totalEpisodes"]),
		CurrentEpisodes: toCurrentEpisodes(body["currentEpisodes"]),
		Status:          status,
		Category:        toStringSlice(body["category"]),
		Tags:            toStringSlice(body["tags"]),
		PlatformLinks:   toStrMap(body["platformLinks"]),
		UpdateDay:       orDefaultString(body["updateDay"], ""),
		PremiereDate:    toDate(body["premiereDate"]),
		CreatedBy:       &user.ID,
		HideCreator:     truthy(body["hideCreator"]),
		CustomAuthors:   toObjectIDs(body["customAuthors"]),
		QQGroupLink:     orDefaultString(body["qqGroupLink"], ""),
		QQGroupNumber:   orDefaultString(body["qqGroupNumber"], ""),
		ReviewStatus:    reviewStatus,
	}
	if err := h.Repos.Episodes.Create(c.Request.Context(), e); err != nil {
		serverError(c)
		return
	}
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	c.JSON(201, h.episodeJSON(e, nil))
}

// CreateSingle POST /api/episodes/:id/episodes（creatorProtect）。
// @Summary 为剧集添加单集
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Param body body object true "单集信息"
// @Success 201 {object} map[string]any "单集对象"
// @Router /episodes/{id}/episodes [post]
func (h *Episodes) CreateSingle(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	episode, err := h.Repos.Episodes.FindByID(ctx, oid)
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
		if !canEdit(episode, user.ID) {
			c.JSON(403, gin.H{"message": "No permission to add episodes"})
			return
		}
	}
	body := h.readJSONBody(c)
	// required 字段缺失/为空 → 该路由 catch-all → 500。
	if asString(body["title"]) == "" {
		serverError(c)
		return
	}
	if _, ok := body["episodeNumber"]; !ok {
		serverError(c)
		return
	}

	isScheduled := truthy(body["isScheduled"])
	single := &model.SingleEpisode{
		EpisodeID:     oid,
		EpisodeNumber: toInt(body["episodeNumber"]),
		Title:         asString(body["title"]),
		TitleEn:       orDefaultString(body["titleEn"], ""),
		TitleJa:       orDefaultString(body["titleJa"], ""),
		Duration:      orDefaultString(body["duration"], ""),
		PlatformLinks: toStrMap(body["platformLinks"]),
		ScheduledDate: toDate(body["scheduledDate"]),
		IsScheduled:   isScheduled,
		PremiereDate:  toDate(body["premiereDate"]),
		IsUpcoming:    isScheduled,
		ReleaseDate:   nil,
	}
	if isScheduled {
		single.ReleaseDate = nil
	} else {
		single.ReleaseDate = toDateOrDefaultNow(body["releaseDate"])
	}

	if err := h.Repos.SingleEpisodes.Create(ctx, single); err != nil {
		serverError(c)
		return
	}
	// 预告集不计入 currentEpisodes（不可观看），变可观看时才 +1。
	if err := h.Repos.Episodes.AddSingleEpisodeUpdate(ctx, oid,
		time.Now().UTC().Truncate(time.Millisecond), !isScheduled); err != nil {
		serverError(c)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + idStr)

	updatedEpisode, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		serverError(c)
		return
	}
	followers, err := h.Repos.Follows.EpisodesFindByEpisode(ctx, oid)
	if err != nil {
		serverError(c)
		return
	}
	if len(followers) > 0 {
		isPreview := isScheduled
		links := toStrMap(body["platformLinks"])
		hasVideo := false
		for k, v := range links {
			if k != "" && v != "" {
				hasVideo = true
				break
			}
		}
		var eventType, notifMessage string
		var metadata primitive.M
		if !isPreview {
			eventType = "available"
			notifMessage = fmt.Sprintf("《%s》更新了第%d集", updatedEpisode.Title, single.EpisodeNumber)
			metadata = primitive.M{"episodeNumber": single.EpisodeNumber, "isPreview": false}
		} else if hasVideo {
			eventType = "preview"
			notifMessage = fmt.Sprintf("《%s》发布了第%d集预告", updatedEpisode.Title, single.EpisodeNumber)
			metadata = primitive.M{"episodeNumber": single.EpisodeNumber, "isPreview": true}
		} else {
			eventType = "preview_info"
			notifMessage = fmt.Sprintf("《%s》第%d集预告信息已更新", updatedEpisode.Title, single.EpisodeNumber)
			metadata = primitive.M{"episodeNumber": single.EpisodeNumber, "isPreview": true, "previewUpdateType": "info"}
		}
		if err := h.insertFollowNotifications(ctx, followers, oid, updatedEpisode, notifMessage, metadata); err != nil {
			serverError(c)
			return
		}
		pushTitle := fmt.Sprintf("《%s》更新了", updatedEpisode.Title)
		pushBody := fmt.Sprintf("第%d集已更新", single.EpisodeNumber)
		if isPreview {
			if hasVideo {
				pushTitle = fmt.Sprintf("《%s》新预告", updatedEpisode.Title)
				pushBody = fmt.Sprintf("第%d集预告已发布", single.EpisodeNumber)
			} else {
				pushTitle = fmt.Sprintf("《%s》预告信息更新", updatedEpisode.Title)
				pushBody = fmt.Sprintf("第%d集预告信息已更新", single.EpisodeNumber)
			}
		}
		h.sendPushToUser(uniqueUserIDs(followers), pushTitle, pushBody, "/episode/"+idStr)
		if h.shouldSendEpisodeEmail(idStr, single.EpisodeNumber, eventType) {
			h.sendEpisodeUpdateEmails(ctx, oid, single.EpisodeNumber, eventType, updatedEpisode.Title, uniqueUserIDs(followers))
		}
	}
	c.JSON(201, singleEpisodeJSON(single))
}

// Update PUT /api/episodes/:id（creatorProtect）。
// @Summary 更新剧集
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Param body body object true "可更新字段 + changeSummary"
// @Success 200 {object} map[string]any "剧集对象"
// @Router /episodes/{id} [put]
func (h *Episodes) Update(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	episode, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	user, _ := middleware.GetUser(c)
	isCreatorRole := user.Role == "creator"
	if isCreatorRole {
		if !canEdit(episode, user.ID) {
			c.JSON(403, gin.H{"message": "You do not have permission to edit this episode"})
			return
		}
	}
	oldCurrentEpisodes := episode.CurrentEpisodes
	body := h.readJSONBody(c)

	// 版本快照：保存编辑前的完整剧集（对齐 EpisodeVersion.create + data: episode.toObject()）。
	lastVersion, err := h.Repos.EpisodeVersions.FindLatest(ctx, oid)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	newVersionNum := 1
	if lastVersion != nil {
		newVersionNum = lastVersion.Version + 1
	}
	if err := h.Repos.EpisodeVersions.Create(ctx, &model.EpisodeVersion{
		EpisodeID:     oid,
		Version:       newVersionNum,
		Data:          episode.ToVersionData(),
		ChangedBy:     &user.ID,
		ChangeSummary: orDefaultString(body["changeSummary"], ""),
	}); err != nil {
		serverError(c)
		return
	}
	// 限制版本数量为 50。
	versionCount, err := h.Repos.EpisodeVersions.CountByEpisode(ctx, oid)
	if err != nil {
		serverError(c)
		return
	}
	if versionCount > 50 {
		oldest, err := h.Repos.EpisodeVersions.FindOldestN(ctx, oid, versionCount-50)
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

	allowedFields := []string{"title", "titleEn", "titleJa", "description", "descriptionEn",
		"descriptionJa", "coverImage", "totalEpisodes", "currentEpisodes", "status",
		"category", "tags", "updateDay", "premiereDate", "platformLinks", "hideCreator",
		"customAuthors", "qqGroupLink", "qqGroupNumber"}
	now := time.Now().UTC().Truncate(time.Millisecond)

	// 创作者编辑已审核（approved）剧集：修改暂存到 pendingChanges，原内容继续公开展示。
	if isCreatorRole && episode.ReviewStatus == "approved" {
		pendingChanges := primitive.M{}
		for _, field := range allowedFields {
			if v, ok := body[field]; ok {
				pendingChanges[field] = v
			}
		}
		updated, err := h.Repos.Episodes.FindOneAndUpdate(ctx, oid, bson.M{"$set": bson.M{
			"pendingChanges":       pendingChanges,
			"hasPendingChanges":    true,
			"pendingChangeSummary": orDefaultString(body["changeSummary"], ""),
			"updatedAt":            now,
		}})
		if err != nil {
			serverError(c)
			return
		}
		middleware.EpisodeCache.Delete("episode_" + idStr)
		middleware.EpisodeCache.DeleteByPrefix("episodes_")
		c.JSON(200, h.episodeJSON(updated, nil))
		return
	}

	// 非已审核剧集（pending/rejected）或管理员编辑：直接更新正式字段。
	setFields := bson.M{"updatedAt": now}
	for _, field := range allowedFields {
		if v, ok := body[field]; ok {
			setFields[field] = typedFieldValue(field, v)
		}
	}
	if isCreatorRole {
		setFields["reviewStatus"] = "pending"
	}
	updated, err := h.Repos.Episodes.FindOneAndUpdate(ctx, oid, bson.M{"$set": setFields})
	if err != nil {
		serverError(c)
		return
	}

	// 集数增加时通知追番用户（非已审核剧集的直接编辑场景）。
	newCurrent := 0
	if v, ok := body["currentEpisodes"]; ok {
		newCurrent = toCurrentEpisodes(v)
	}
	if newCurrent > oldCurrentEpisodes {
		if err := h.notifyEpisodeNumberChange(ctx, oid, oldCurrentEpisodes, newCurrent, updated); err != nil {
			serverError(c)
			return
		}
	}
	middleware.EpisodeCache.Delete("episode_" + idStr)
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	c.JSON(200, h.episodeJSON(updated, nil))
}

// Resubmit POST /api/episodes/:id/resubmit（creatorProtect）。
// @Summary 重新提交审核
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Success 200 {object} map[string]any "剧集对象"
// @Router /episodes/{id}/resubmit [post]
func (h *Episodes) Resubmit(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	episode, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	// 只有 rejected 状态的剧集可以重新提交。
	if episode.ReviewStatus != "rejected" {
		c.JSON(400, gin.H{"message": "只有被拒绝的剧集才能重新提交审核"})
		return
	}
	user, _ := middleware.GetUser(c)
	if !canEdit(episode, user.ID) {
		c.JSON(403, gin.H{"message": "You do not have permission to resubmit this episode"})
		return
	}
	episode.ReviewStatus = "pending"
	episode.ReviewNote = ""
	episode.ReviewedBy = nil
	episode.ReviewedAt = nil
	episode.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	if err := h.Repos.Episodes.Save(ctx, episode); err != nil {
		serverError(c)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + idStr)
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	c.JSON(200, h.episodeJSON(episode, nil))
}

// Delete DELETE /api/episodes/:id（adminProtect）。
// 删除不直接物理清除：剧集整体移入回收站（episodetrash 集合），前台即刻
// 不可见（episodes 集合中已移除）；管理员可在后台回收站恢复或彻底删除
// （彻底删除时才清理单集/版本/追番等关联数据，释放服务器资源）。
// @Summary 删除剧集（移入回收站）
// @Tags 剧集
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Success 200 {object} map[string]string "message"
// @Router /episodes/{id} [delete]
func (h *Episodes) Delete(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	episode, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	user, _ := middleware.GetUser(c)
	if err := h.Repos.EpisodeTrash.MoveToTrash(ctx, episode, "deleted", "", &user.ID); err != nil {
		serverError(c)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + idStr)
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	c.JSON(200, gin.H{"message": "Episode deleted"})
}

// ---- 通知 / 推送 / 邮件 ----

// insertFollowNotifications 为追番用户批量写入站内通知（对齐 Notification.insertMany）。
func (h *Episodes) insertFollowNotifications(ctx context.Context, followers []model.Follow,
	episodeID primitive.ObjectID, episode *model.Episode, message string, metadata primitive.M) error {
	notifs := make([]model.Notification, 0, len(followers))
	for _, f := range followers {
		notifs = append(notifs, model.Notification{
			UserID:         f.UserID,
			EpisodeID:      &episodeID,
			EpisodeTitle:   episode.Title,
			EpisodeTitleEn: episode.TitleEn,
			Type:           "new_episode",
			Message:        message,
			Metadata:       metadata,
		})
	}
	return h.Repos.Notifications.EpisodesInsertMany(ctx, notifs)
}

// notifyNewEpisode 在单集可观看/预告发布/预告更新时通知追番用户
// （站内通知 + Push + 邮件去重发送），对齐 episodes.js 对应分支。
// eventType: available | preview | preview_video | preview_info。
func (h *Episodes) notifyNewEpisode(ctx context.Context, episodeID primitive.ObjectID,
	episode *model.Episode, epNum int, eventType string) error {
	followers, err := h.Repos.Follows.EpisodesFindByEpisode(ctx, episodeID)
	if err != nil {
		return err
	}
	if len(followers) == 0 {
		return nil
	}
	var notifMessage, pushTitle, pushBody string
	var metadata primitive.M
	switch eventType {
	case "available":
		notifMessage = fmt.Sprintf("《%s》更新了第%d集", episode.Title, epNum)
		pushTitle = fmt.Sprintf("《%s》更新了", episode.Title)
		pushBody = fmt.Sprintf("第%d集已更新", epNum)
		metadata = primitive.M{"episodeNumber": epNum, "isPreview": false}
	case "preview":
		notifMessage = fmt.Sprintf("《%s》发布了第%d集预告", episode.Title, epNum)
		pushTitle = fmt.Sprintf("《%s》新预告", episode.Title)
		pushBody = fmt.Sprintf("第%d集预告已发布", epNum)
		metadata = primitive.M{"episodeNumber": epNum, "isPreview": true}
	case "preview_video":
		notifMessage = fmt.Sprintf("《%s》第%d集预告视频已更新", episode.Title, epNum)
		pushTitle = fmt.Sprintf("《%s》预告视频更新", episode.Title)
		pushBody = fmt.Sprintf("第%d集预告视频已更新", epNum)
		metadata = primitive.M{"episodeNumber": epNum, "isPreview": true, "previewUpdateType": "video"}
	default: // preview_info
		notifMessage = fmt.Sprintf("《%s》第%d集预告信息已更新", episode.Title, epNum)
		pushTitle = fmt.Sprintf("《%s》预告信息更新", episode.Title)
		pushBody = fmt.Sprintf("第%d集预告信息已更新", epNum)
		metadata = primitive.M{"episodeNumber": epNum, "isPreview": true, "previewUpdateType": "info"}
	}
	if err := h.insertFollowNotifications(ctx, followers, episodeID, episode, notifMessage, metadata); err != nil {
		return err
	}
	uids := uniqueUserIDs(followers)
	h.sendPushToUser(uids, pushTitle, pushBody, "/episode/"+episodeID.Hex())
	if h.shouldSendEpisodeEmail(episodeID.Hex(), epNum, eventType) {
		h.sendEpisodeUpdateEmails(ctx, episodeID, epNum, eventType, episode.Title, uids)
	}
	return nil
}

// notifyEpisodeNumberChange 在剧集直接编辑导致 currentEpisodes 增加时，为每个
// 新增集数给全部追番用户写通知（对齐 utils/episodeNotify.js notifyEpisodeNumberChange）。
func (h *Episodes) notifyEpisodeNumberChange(ctx context.Context, episodeID primitive.ObjectID,
	oldCurrentEpisodes, newCurrentEpisodes int, episode *model.Episode) error {
	if newCurrentEpisodes <= oldCurrentEpisodes {
		return nil
	}
	follows, err := h.Repos.Follows.EpisodesFindByEpisode(ctx, episodeID)
	if err != nil {
		return err
	}
	if len(follows) == 0 {
		return nil
	}
	notifs := make([]model.Notification, 0)
	for _, f := range follows {
		for epNum := oldCurrentEpisodes + 1; epNum <= newCurrentEpisodes; epNum++ {
			notifs = append(notifs, model.Notification{
				UserID:         f.UserID,
				EpisodeID:      &episodeID,
				EpisodeTitle:   episode.Title,
				EpisodeTitleEn: episode.TitleEn,
				Type:           "new_episode",
				Message:        fmt.Sprintf("《%s》更新了第%d集", episode.Title, epNum),
				Metadata:       primitive.M{"episodeNumber": epNum},
			})
		}
	}
	if len(notifs) == 0 {
		return nil
	}
	if err := h.Repos.Notifications.EpisodesInsertMany(ctx, notifs); err != nil {
		return err
	}
	uids := uniqueUserIDs(follows)
	h.sendPushToUser(uids, fmt.Sprintf("《%s》更新了", episode.Title),
		fmt.Sprintf("更新至第%d集", newCurrentEpisodes), "/episode/"+episodeID.Hex())
	for epNum := oldCurrentEpisodes + 1; epNum <= newCurrentEpisodes; epNum++ {
		if h.shouldSendEpisodeEmail(episodeID.Hex(), epNum, "available") {
			h.sendEpisodeUpdateEmails(ctx, episodeID, epNum, "available", episode.Title, uids)
		}
	}
	return nil
}

// sendPushToUser 发送 Web Push（对齐 Express routes/notifications.js sendPushToUser，
// fire-and-forget 语义）。neo-server 当前未实现 PushSubscription 集合与 webpush 发送，
// 本函数为 no-op 占位；由主 agent 接入推送域后替换实现。
func (h *Episodes) sendPushToUser(userIDs []primitive.ObjectID, title, body, url string) {
	_ = userIDs
	_ = title
	_ = body
	_ = url
}

// shouldSendEpisodeEmail 邮件去重：同一剧集+集数+事件类型 1 小时内不重复发送。
func (h *Episodes) shouldSendEpisodeEmail(episodeID string, epNum int, eventType string) bool {
	key := fmt.Sprintf("%s_%d_%s", episodeID, epNum, eventType)
	now := time.Now()
	h.emailMu.Lock()
	defer h.emailMu.Unlock()
	last, ok := h.emailLog[key]
	if ok && now.Sub(last) < emailNotifyCooldown {
		return false
	}
	h.emailLog[key] = now
	if len(h.emailLog) > 2000 {
		for k, ts := range h.emailLog {
			if now.Sub(ts) > emailNotifyCooldown {
				delete(h.emailLog, k)
			}
		}
	}
	return true
}

// sendEpisodeUpdateEmails 批量发送追番更新邮件（对齐 notifyHelper.js
// sendBatchNotificationEmails：仅发邮箱已验证且未显式关闭 episodeUpdate 偏好的用户）。
// fire-and-forget：SMTP 发送在独立 goroutine 中进行，失败静默。
func (h *Episodes) sendEpisodeUpdateEmails(ctx context.Context, episodeID primitive.ObjectID,
	epNum int, eventType string, episodeTitle string, userIDs []primitive.ObjectID) {
	if h.Mail == nil || len(userIDs) == 0 {
		return
	}
	targets, err := h.Repos.Users.EpisodesFindMailTargetsByIDs(ctx, userIDs)
	if err != nil {
		return
	}
	for _, uid := range userIDs {
		t, ok := targets[uid.Hex()]
		if !ok || !t.IsEmailVerified {
			continue
		}
		if t.EpisodeUpdatePref != nil && !*t.EpisodeUpdatePref {
			continue
		}
		to, title, body, preheader := h.buildEpisodeUpdateEmail(episodeTitle, epNum, eventType, t.Email)
		go func(to, subject, html, preheader string) {
			h.Mail.SendNotificationEmail(context.Background(), to, subject, html, preheader)
		}(to, title, body, preheader)
	}
}

// buildEpisodeUpdateEmail 构造追番更新邮件（subject/body 对齐 Express utils/email.js
// sendEpisodeUpdateEmail 的 available/preview/preview_video/preview_info 分支）。
func (h *Episodes) buildEpisodeUpdateEmail(episodeTitle string, epNum int, eventType, to string) (string, string, string, string) {
	url := h.Config.Server.FrontendURL
	if url == "" {
		url = h.Config.Server.SiteURL
	}
	if url == "" {
		url = "http://localhost:3000"
	}
	switch eventType {
	case "preview":
		subject := fmt.Sprintf("《%s》发布了第%d集预告", episodeTitle, epNum)
		body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">追番预告提醒</h2>` +
			`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您关注的剧集发布了新预告！</p>` +
			email.EmailInfoBox(fmt.Sprintf(`<p style="margin:0 0 4px;font-size:16px;font-weight:600;">《%s》</p><p style="margin:0;color:#64748b;">发布了第 %d 集预告</p>`, episodeTitle, epNum), "info") +
			`<p style="margin:20px 0;">` + email.EmailButton("前往查看", url, "primary") + `</p>` +
			`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
		return to, subject, body, "您关注的剧集发布了新预告"
	case "preview_video":
		subject := fmt.Sprintf("《%s》第%d集预告视频已更新", episodeTitle, epNum)
		body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">预告视频更新</h2>` +
			`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您关注的剧集预告视频有更新！</p>` +
			email.EmailInfoBox(fmt.Sprintf(`<p style="margin:0 0 4px;font-size:16px;font-weight:600;">《%s》</p><p style="margin:0;color:#64748b;">第 %d 集预告视频已更新</p>`, episodeTitle, epNum), "info") +
			`<p style="margin:20px 0;">` + email.EmailButton("前往查看", url, "primary") + `</p>` +
			`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
		return to, subject, body, "您关注的剧集预告视频已更新"
	case "preview_info":
		subject := fmt.Sprintf("《%s》第%d集预告信息已更新", episodeTitle, epNum)
		body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">预告信息更新</h2>` +
			`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您关注的剧集预告信息有更新！</p>` +
			email.EmailInfoBox(fmt.Sprintf(`<p style="margin:0 0 4px;font-size:16px;font-weight:600;">《%s》</p><p style="margin:0;color:#64748b;">第 %d 集预告信息已更新</p>`, episodeTitle, epNum), "info") +
			`<p style="margin:20px 0;">` + email.EmailButton("前往查看", url, "primary") + `</p>` +
			`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
		return to, subject, body, "您关注的剧集预告信息已更新"
	default: // available
		subject := fmt.Sprintf("《%s》更新了第%d集", episodeTitle, epNum)
		body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">追番更新提醒</h2>` +
			`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您关注的剧集有新更新啦！</p>` +
			email.EmailInfoBox(fmt.Sprintf(`<p style="margin:0 0 4px;font-size:16px;font-weight:600;">《%s》</p><p style="margin:0;color:#64748b;">已更新至第 %d 集</p>`, episodeTitle, epNum), "info") +
			`<p style="margin:20px 0;">` + email.EmailButton("前往观看", url, "primary") + `</p>` +
			`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
		return to, subject, body, "您关注的剧集有新更新"
	}
}

// ---- DTO 组装（对齐 mongoose toObject + populate 语义）----

// episodeJSON 组装剧集响应对象。users 为 nil 时 refs 输出 hex 字符串
// （未 populate 场景）；非 nil 时输出 {_id, accountId, username} 对象。
func (h *Episodes) episodeJSON(e *model.Episode, users map[string]repository.EpisodesUserRef) gin.H {
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
		"createdBy":            refJSON(e.CreatedBy, users),
		"hideCreator":          e.HideCreator,
		"allowedEditors":       refsJSON(e.AllowedEditors, users),
		"customAuthors":        refsJSON(e.CustomAuthors, users),
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

// singleEpisodeJSON 组装单集响应对象。
func singleEpisodeJSON(s *model.SingleEpisode) gin.H {
	return gin.H{
		"_id":           s.ID.Hex(),
		"episodeId":     s.EpisodeID.Hex(),
		"episodeNumber": s.EpisodeNumber,
		"title":         s.Title,
		"titleEn":       s.TitleEn,
		"titleJa":       s.TitleJa,
		"duration":      s.Duration,
		"platformLinks": orEmptyM(s.PlatformLinks),
		"views":         s.Views,
		"releaseDate":   s.ReleaseDate,
		"scheduledDate": s.ScheduledDate,
		"isScheduled":   s.IsScheduled,
		"premiereDate":  s.PremiereDate,
		"isUpcoming":    s.IsUpcoming,
		"__v":           s.VersionKey,
	}
}

// refJSON 渲染单个 ref 字段（populate 或 hex）。
func refJSON(oid *primitive.ObjectID, users map[string]repository.EpisodesUserRef) any {
	if oid == nil {
		return nil
	}
	if users == nil {
		return oid.Hex()
	}
	if u, ok := users[oid.Hex()]; ok {
		return gin.H{"_id": u.ID.Hex(), "accountId": u.AccountID, "username": u.Username}
	}
	return nil
}

// refsJSON 渲染数组 ref 字段（populate 或 hex 数组）。
func refsJSON(ids []primitive.ObjectID, users map[string]repository.EpisodesUserRef) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		if users == nil {
			out = append(out, id.Hex())
			continue
		}
		if u, ok := users[id.Hex()]; ok {
			out = append(out, gin.H{"_id": u.ID.Hex(), "accountId": u.AccountID, "username": u.Username})
		} else {
			out = append(out, nil)
		}
	}
	return out
}

// userRefsForEpisode 查询单个剧集的 populate 用户引用。
func (h *Episodes) userRefsForEpisode(ctx context.Context, e *model.Episode) (map[string]repository.EpisodesUserRef, error) {
	ids := make([]primitive.ObjectID, 0, 2+len(e.AllowedEditors)+len(e.CustomAuthors))
	if e.CreatedBy != nil {
		ids = append(ids, *e.CreatedBy)
	}
	ids = append(ids, e.AllowedEditors...)
	ids = append(ids, e.CustomAuthors...)
	return h.Repos.Users.EpisodesFindUserRefsByIDs(ctx, dedupIDs(ids))
}

// userRefsForEpisodes 查询一批剧集的 populate 用户引用（列表接口用）。
func (h *Episodes) userRefsForEpisodes(ctx context.Context, eps []model.Episode) (map[string]repository.EpisodesUserRef, error) {
	ids := []primitive.ObjectID{}
	for i := range eps {
		if eps[i].CreatedBy != nil {
			ids = append(ids, *eps[i].CreatedBy)
		}
		ids = append(ids, eps[i].AllowedEditors...)
		ids = append(ids, eps[i].CustomAuthors...)
	}
	return h.Repos.Users.EpisodesFindUserRefsByIDs(ctx, dedupIDs(ids))
}

// canEdit 判断用户是否为创作者本人或允许的编辑者（对齐 createdBy/allowedEditors 校验）。
func canEdit(e *model.Episode, userID primitive.ObjectID) bool {
	if e.CreatedBy != nil && *e.CreatedBy == userID {
		return true
	}
	for _, ed := range e.AllowedEditors {
		if ed == userID {
			return true
		}
	}
	return false
}

// canViewUnapproved 判断用户能否查看未审核剧集（管理员/本人/被授权编辑）。
func canViewUnapproved(user *model.User, e *model.Episode) bool {
	if user.Role == "admin" || user.Role == "superadmin" {
		return true
	}
	return canEdit(e, user.ID)
}

// ---- 工具函数 ----

// readJSONBody 读取（已过 SanitizeInput 的）JSON 请求体为 map；非对象/空体返回空 map。
func (h *Episodes) readJSONBody(c *gin.Context) map[string]any {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		return map[string]any{}
	}
	return body
}

// serverError 渲染 500（对齐 Express 各路由 catch 分支的 {message:'Server error'}）。
func serverError(c *gin.Context) {
	c.JSON(500, gin.H{"message": "Server error"})
}

// clientIP 提取客户端 IP（生产信任 X-Forwarded-For 首值，对齐 req.ip）。
func (h *Episodes) clientIP(c *gin.Context) string {
	if !h.Config.IsDev {
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			if first := ratelimit.NormalizeXFF(xff); first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

// pruneViewLogLocked 清理过期/超限的观看去重记录（对齐 Express setInterval 清理）。
func (h *Episodes) pruneViewLogLocked(now time.Time) {
	if len(h.viewLog) <= 10000 {
		return
	}
	for k, ts := range h.viewLog {
		if now.Sub(ts) >= viewCooldown {
			delete(h.viewLog, k)
		}
	}
	if len(h.viewLog) > 5000 {
		type kv struct {
			key string
			ts  time.Time
		}
		entries := make([]kv, 0, len(h.viewLog))
		for k, ts := range h.viewLog {
			entries = append(entries, kv{k, ts})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].ts.After(entries[j].ts) })
		for _, e := range entries[5000:] {
			delete(h.viewLog, e.key)
		}
	}
}

// uniqueUserIDs 去重返回追番用户 ID（对齐 [...new Set(followers.map(f => String(f.userId)))]）。
func uniqueUserIDs(follows []model.Follow) []primitive.ObjectID {
	seen := map[string]bool{}
	out := make([]primitive.ObjectID, 0, len(follows))
	for _, f := range follows {
		key := f.UserID.Hex()
		if !seen[key] {
			seen[key] = true
			out = append(out, f.UserID)
		}
	}
	return out
}

// dedupIDs 去重返回 ObjectID 列表。
func dedupIDs(ids []primitive.ObjectID) []primitive.ObjectID {
	seen := map[string]bool{}
	out := make([]primitive.ObjectID, 0, len(ids))
	for _, id := range ids {
		key := id.Hex()
		if !seen[key] {
			seen[key] = true
			out = append(out, id)
		}
	}
	return out
}

// copyM 浅拷贝 bson.M。
func copyM(src bson.M) bson.M {
	out := bson.M{}
	for k, v := range src {
		out[k] = v
	}
	return out
}

// escapeRegex 转义正则特殊字符（对齐 utils/helpers.js escapeRegex）。
func escapeRegex(s string) string {
	var b strings.Builder
	for _, ch := range s {
		if strings.ContainsRune(`.*+?^${}()|[]\`, ch) {
			b.WriteByte('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// asString 提取字符串值（非字符串返回 ""）。
func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// orDefaultString 提取字符串，缺失/空返回默认值。
func orDefaultString(v any, def string) string {
	s := asString(v)
	if s == "" {
		return def
	}
	return s
}

// truthy 判断 JS 真值语义（nil/false/0/"" → false，其余 → true）。
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	default:
		return true
	}
}

// toInt 提取整数（float64/json.Number/string → int）。
func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}

// toCurrentEpisodes 对齐 `req.body.currentEpisodes || 0`。
func toCurrentEpisodes(v any) int {
	if n := toInt(v); n != 0 {
		return n
	}
	return 0
}

// toTotalEpisodes 对齐 Episode schema totalEpisodes 的 set：
// null/undefined/” → nil，否则 Number(v)。
func toTotalEpisodes(v any) *int {
	switch t := v.(type) {
	case nil:
		return nil
	case float64:
		n := int(t)
		return &n
	case string:
		if t == "" {
			return nil
		}
		if n, err := strconv.Atoi(t); err == nil {
			return &n
		}
		return nil
	default:
		return nil
	}
}

// toStringSlice 提取字符串数组；非数组返回空切片。
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// toObjectIDs 提取 ObjectID 数组（customAuthors/allowedEditors 用）。
func toObjectIDs(v any) []primitive.ObjectID {
	arr, ok := v.([]any)
	if !ok {
		return []primitive.ObjectID{}
	}
	out := make([]primitive.ObjectID, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			continue
		}
		if oid, err := primitive.ObjectIDFromHex(s); err == nil {
			out = append(out, oid)
		}
	}
	return out
}

// toStrMap 提取字符串 Map（platformLinks 用）；非对象返回空 Map。
func toStrMap(v any) primitive.M {
	switch t := v.(type) {
	case map[string]any:
		return primitive.M(t)
	case primitive.M:
		return t
	default:
		return primitive.M{}
	}
}

// toDate 解析日期值（ISO 字符串 / 毫秒时间戳 / time.Time）；无法解析返回 nil。
func toDate(v any) *time.Time {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return nil
		}
		if tm, err := time.Parse(time.RFC3339, t); err == nil {
			return &tm
		}
		if tm, err := time.Parse("2006-01-02", t); err == nil {
			return &tm
		}
		return nil
	case float64:
		tm := time.UnixMilli(int64(t))
		return &tm
	case time.Time:
		return &t
	default:
		return nil
	}
}

// toDateOrDefaultNow 解析日期，缺失/无效返回当前时间（对齐 `req.body.x || Date.now()`）。
func toDateOrDefaultNow(v any) *time.Time {
	if d := toDate(v); d != nil {
		return d
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &now
}

// typedFieldValue 更新剧集正式字段时的类型转换（对齐 mongoose 按 schema 类型 cast）。
func typedFieldValue(field string, v any) any {
	switch field {
	case "title", "titleEn", "titleJa", "description", "descriptionEn", "descriptionJa",
		"coverImage", "updateDay", "qqGroupLink", "qqGroupNumber":
		return asString(v)
	case "status":
		return asString(v)
	case "totalEpisodes":
		return toTotalEpisodes(v)
	case "currentEpisodes":
		return toCurrentEpisodes(v)
	case "category", "tags":
		return toStringSlice(v)
	case "premiereDate":
		return toDate(v)
	case "platformLinks":
		return toStrMap(v)
	case "hideCreator":
		return truthy(v)
	case "customAuthors":
		return toObjectIDs(v)
	default:
		return v
	}
}

// isDateField 判断单集信息字段是否为日期（Express 的 String 比较对日期恒不等）。
func isDateField(f string) bool {
	return f == "scheduledDate" || f == "premiereDate" || f == "releaseDate"
}

// singleFieldValue 取单集旧文档字段值。
func singleFieldValue(s *model.SingleEpisode, f string) any {
	switch f {
	case "title":
		return s.Title
	case "titleEn":
		return s.TitleEn
	case "titleJa":
		return s.TitleJa
	case "duration":
		return s.Duration
	case "episodeNumber":
		return s.EpisodeNumber
	case "isScheduled":
		return s.IsScheduled
	case "premiereDate":
		return s.PremiereDate
	case "scheduledDate":
		return s.ScheduledDate
	case "releaseDate":
		return s.ReleaseDate
	default:
		return nil
	}
}

// infoFieldString 把字段值转成 String() 等价字符串（对齐 JS String()）。
func infoFieldString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case string:
		return t
	case time.Time:
		return t.String()
	case *time.Time:
		if t == nil {
			return "null"
		}
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// orEmptyStrings 空切片补 []（避免 JSON null）。
func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// orEmptyM 空 Map 补 {}（避免 JSON null）。
func orEmptyM(m primitive.M) primitive.M {
	if m == nil {
		return primitive.M{}
	}
	return m
}
