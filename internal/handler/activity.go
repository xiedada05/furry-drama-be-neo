package handler

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/activity 子域（行为逐分支照抄 backend/routes/activity.js）。
//
// 注意：Express activity.js 没有独立 Activity collection——动态流由
// Follow/SingleEpisode/Episode/Rating 实时聚合计算，本实现同样如此
// （ActivityRepo 未注册到 repos.go，按约束不改动 repos.go）。

// Activity 是 /api/activity 域 handler 容器。
type Activity struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewActivity 构造动态流 handler 容器。
func NewActivity(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Activity {
	return &Activity{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/activity 全部端点（不含 /api 前缀；路径对齐 activity.js 子路径）。
func (h *Activity) Register(g *gin.RouterGroup) {
	g.GET("", h.AuthMW.Protect(), h.Feed)
	g.GET("/", h.AuthMW.Protect(), h.Feed)
	g.GET("/public", h.PublicFeed)
}

// Feed GET /api/activity（protect）：追番用户的个性化动态流。
// 聚合新单集/状态变更/高分评分，按日期倒序后内存分页。
// @Summary 我的动态流
// @Tags 动态
// @Security bearerAuth
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} map[string]any "activities/page/limit/total/totalPages"
// @Router /activity [get]
func (h *Activity) Feed(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	page, limit := activityPage(c)
	ctx := c.Request.Context()
	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour)

	follows, err := h.Repos.Follows.FindByUser(ctx, user.ID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	followedIDs := followEpisodeIDs(follows)
	if len(followedIDs) == 0 {
		c.JSON(200, gin.H{"activities": []gin.H{}, "page": page, "limit": limit, "total": 0, "totalPages": 0})
		return
	}

	items := []activityItem{}

	// 1) 追番剧集的新单集（releaseDate 近 30 天）。
	newEpisodes, err := h.Repos.SingleEpisodes.ActivitySinglesFollowedNew(ctx, followedIDs, thirtyDaysAgo)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	allIDs := append(append([]primitive.ObjectID{}, followedIDs...), singleEpisodeIDs(newEpisodes)...)
	epMap, err := h.episodeMapByID(ctx, dedupIDs(allIDs))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	for i := range newEpisodes {
		se := &newEpisodes[i]
		ep, ok := epMap[se.EpisodeID.Hex()]
		if !ok {
			continue
		}
		items = append(items, newEpisodeItem(ep, *se))
	}

	// 2) 追番剧集的最近状态变更（updatedAt 近 30 天）。
	recentlyUpdated, err := h.Repos.Episodes.FindList(ctx, bson.M{
		"_id":       bson.M{"$in": followedIDs},
		"updatedAt": bson.M{"$gte": thirtyDaysAgo},
	}, bson.D{{Key: "updatedAt", Value: -1}}, 0, 0)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	for i := range recentlyUpdated {
		items = append(items, statusChangeItem(recentlyUpdated[i]))
	}

	// 3) 追番剧集的近期高分评分（score>=4，近 30 天，最多 50 条）。
	highRatings, err := h.Repos.Ratings.ActivityHighRatings(ctx, followedIDs, thirtyDaysAgo, 50)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	for i := range highRatings {
		rt := &highRatings[i]
		ep, ok := epMap[rt.EpisodeID.Hex()]
		if !ok {
			continue
		}
		items = append(items, highRatingItem(ep, *rt))
	}

	sort.Slice(items, func(i, j int) bool { return items[i].sortTime.After(items[j].sortTime) })
	total := len(items)
	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	paged := sliceActivityItems(items, (page-1)*limit, page*limit)
	out := make([]gin.H, 0, len(paged))
	for _, it := range paged {
		out = append(out, it.obj)
	}
	c.JSON(200, gin.H{
		"activities": out,
		"page":       page,
		"limit":      limit,
		"total":      total,
		"totalPages": totalPages,
	})
}

// PublicFeed GET /api/activity/public（公开）：全站动态流，无需登录。
// @Summary 公开动态流
// @Tags 动态
// @Success 200 {object} map[string]any "activities/total"
// @Router /activity/public [get]
func (h *Activity) PublicFeed(c *gin.Context) {
	ctx := c.Request.Context()
	thirtyDaysAgo := time.Now().Add(-30 * 24 * time.Hour)
	items := []activityItem{}

	// 1) 全站近期新单集（最多 30 条，仅取已审核剧集）。
	newEpisodes, err := h.Repos.SingleEpisodes.ActivitySinglesRecent(ctx, thirtyDaysAgo, 30)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	epMap, err := h.episodeMapByIDApproved(ctx, singleEpisodeIDs(newEpisodes))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	for i := range newEpisodes {
		se := &newEpisodes[i]
		ep, ok := epMap[se.EpisodeID.Hex()]
		if !ok {
			continue
		}
		items = append(items, newEpisodeItem(ep, *se))
	}

	// 2) 热门高分剧集（均分>=4 且有人评分，最多 10 条）。
	trending, err := h.Repos.Episodes.FindList(ctx, bson.M{
		"reviewStatus": "approved",
		"averageRating": bson.M{"$gte": 4},
		"ratingCount":  bson.M{"$gte": 1},
	}, bson.D{{Key: "averageRating", Value: -1}, {Key: "ratingCount", Value: -1}}, 0, 10)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	for i := range trending {
		items = append(items, trendingItem(trending[i]))
	}

	// 3) 近期状态变更的已审核剧集（最多 10 条）。
	statusChanged, err := h.Repos.Episodes.FindList(ctx, bson.M{
		"reviewStatus": "approved",
		"updatedAt":    bson.M{"$gte": thirtyDaysAgo},
	}, bson.D{{Key: "updatedAt", Value: -1}}, 0, 10)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	for i := range statusChanged {
		items = append(items, statusChangeItem(statusChanged[i]))
	}

	sort.Slice(items, func(i, j int) bool { return items[i].sortTime.After(items[j].sortTime) })
	const limitNum = 20
	paged := sliceActivityItems(items, 0, limitNum)
	total := len(items)
	if total > limitNum {
		total = limitNum
	}
	out := make([]gin.H, 0, len(paged))
	for _, it := range paged {
		out = append(out, it.obj)
	}
	c.JSON(200, gin.H{"activities": out, "total": total})
}

// ---- 动态项组装 ----

// activityItem 是一条动态：sortTime 用于按 date 倒序排序（对齐
// activities.sort((a,b) => new Date(b.date) - new Date(a.date))），obj 为响应体。
type activityItem struct {
	sortTime time.Time
	obj      gin.H
}

// newEpisodeItem 组装 new_episode 动态（对齐 activity.js 两处 newEpisodes 循环）。
func newEpisodeItem(ep model.Episode, se model.SingleEpisode) activityItem {
	var date any = se.ReleaseDate
	var sortTime time.Time
	if se.ReleaseDate != nil {
		sortTime = *se.ReleaseDate
	}
	return activityItem{
		sortTime: sortTime,
		obj: gin.H{
			"type":          "new_episode",
			"episodeId":     ep.ID.Hex(),
			"episodeTitle":  ep.Title,
			"episodeTitleEn": ep.TitleEn,
			"coverImage":    ep.CoverImage,
			"description":   fmt.Sprintf("%s 发布了新单集：第%d集 %s", ep.Title, se.EpisodeNumber, se.Title),
			"descriptionEn": fmt.Sprintf("%s released a new episode: Ep.%d %s", orFirstNonEmpty(ep.TitleEn, ep.Title), se.EpisodeNumber, orFirstNonEmpty(se.TitleEn, se.Title)),
			"date":          date,
			"metadata": gin.H{
				"episodeNumber":      se.EpisodeNumber,
				"singleEpisodeTitle": se.Title,
				"singleEpisodeTitleEn": se.TitleEn,
			},
		},
	}
}

// statusChangeItem 组装 status_change 动态（对齐 activity.js 状态变更循环）。
func statusChangeItem(ep model.Episode) activityItem {
	var st string
	switch ep.Status {
	case "ongoing":
		st = "连载中"
	case "completed":
		st = "已完结"
	default:
		st = "即将上映"
	}
	return activityItem{
		sortTime: ep.UpdatedAt,
		obj: gin.H{
			"type":          "status_change",
			"episodeId":     ep.ID.Hex(),
			"episodeTitle":  ep.Title,
			"episodeTitleEn": ep.TitleEn,
			"coverImage":    ep.CoverImage,
			"description":   fmt.Sprintf("%s 状态变更为：%s", ep.Title, st),
			"descriptionEn": fmt.Sprintf("%s status changed to: %s", orFirstNonEmpty(ep.TitleEn, ep.Title), ep.Status),
			"date":          ep.UpdatedAt,
			"metadata":      gin.H{"status": ep.Status},
		},
	}
}

// highRatingItem 组装 protected 动态流中的 new_rating（对齐 activity.js
// highRatings 循环：episodeMap 内剧集 + rating.score）。
func highRatingItem(ep model.Episode, rt model.Rating) activityItem {
	return activityItem{
		sortTime: rt.CreatedAt,
		obj: gin.H{
			"type":          "new_rating",
			"episodeId":     ep.ID.Hex(),
			"episodeTitle":  ep.Title,
			"episodeTitleEn": ep.TitleEn,
			"coverImage":    ep.CoverImage,
			"description":   fmt.Sprintf("%s 获得了高评分：%d分", ep.Title, rt.Score),
			"descriptionEn": fmt.Sprintf("%s received a high rating: %d/5", orFirstNonEmpty(ep.TitleEn, ep.Title), rt.Score),
			"date":          rt.CreatedAt,
			"metadata":      gin.H{"score": rt.Score, "averageRating": ep.AverageRating},
		},
	}
}

// trendingItem 组装公开动态流中的 new_rating（对齐 activity.js trendingEpisodes 循环）。
func trendingItem(ep model.Episode) activityItem {
	return activityItem{
		sortTime: ep.UpdatedAt,
		obj: gin.H{
			"type":          "new_rating",
			"episodeId":     ep.ID.Hex(),
			"episodeTitle":  ep.Title,
			"episodeTitleEn": ep.TitleEn,
			"coverImage":    ep.CoverImage,
			"description":   fmt.Sprintf("%s 获得了高评分：%g分（%d人评分）", ep.Title, ep.AverageRating, ep.RatingCount),
			"descriptionEn": fmt.Sprintf("%s received a high rating: %g/5 (%d ratings)", orFirstNonEmpty(ep.TitleEn, ep.Title), ep.AverageRating, ep.RatingCount),
			"date":          ep.UpdatedAt,
			"metadata":      gin.H{"score": ep.AverageRating, "ratingCount": ep.RatingCount},
		},
	}
}

// ---- helpers ----

// episodeMapByID 查询一批剧集并按 hex ID 建索引。
func (h *Activity) episodeMapByID(ctx context.Context, ids []primitive.ObjectID) (map[string]model.Episode, error) {
	m := make(map[string]model.Episode, len(ids))
	if len(ids) == 0 {
		return m, nil
	}
	eps, err := h.Repos.Episodes.FindList(ctx, bson.M{"_id": bson.M{"$in": ids}}, nil, 0, 0)
	if err != nil {
		return nil, err
	}
	for i := range eps {
		m[eps[i].ID.Hex()] = eps[i]
	}
	return m, nil
}

// episodeMapByIDApproved 查询一批已审核剧集并按 hex ID 建索引（公开动态流用）。
func (h *Activity) episodeMapByIDApproved(ctx context.Context, ids []primitive.ObjectID) (map[string]model.Episode, error) {
	m := make(map[string]model.Episode, len(ids))
	if len(ids) == 0 {
		return m, nil
	}
	eps, err := h.Repos.Episodes.FindList(ctx, bson.M{
		"_id":          bson.M{"$in": ids},
		"reviewStatus": "approved",
	}, nil, 0, 0)
	if err != nil {
		return nil, err
	}
	for i := range eps {
		m[eps[i].ID.Hex()] = eps[i]
	}
	return m, nil
}

// singleEpisodeIDs 提取单集列表中的剧集 ID（含重复，供去重）。
func singleEpisodeIDs(singles []model.SingleEpisode) []primitive.ObjectID {
	out := make([]primitive.ObjectID, 0, len(singles))
	for i := range singles {
		out = append(out, singles[i].EpisodeID)
	}
	return out
}

// activityPage 解析动态流分页参数（对齐 activity.js 内联 parseInt，无钳制）。
func activityPage(c *gin.Context) (page, limit int) {
	page = 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		page = p
	}
	limit = 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limit = l
	}
	return page, limit
}

// sliceActivityItems 复刻 Array.prototype.slice(start, end) 对正索引的钳制。
func sliceActivityItems(items []activityItem, start, end int) []activityItem {
	if start < 0 {
		start = 0
	}
	if start > len(items) {
		start = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	if end < start {
		end = start
	}
	return items[start:end]
}

// orFirstNonEmpty 取第一个非空字符串，否则用备选（对齐 `a || b` 的 JS 真值语义）。
func orFirstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
