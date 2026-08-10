package handler

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/stats 子域（行为逐分支照抄 backend/routes/stats.js）。
// 8 个端点：overview / calendar / recommendations（3 个）/ activity-heatmap /
// episode-lifecycle / realtime。缓存键为 stats_<requestURI>（对齐 Express
// cacheMiddleware 的 `stats_${req.originalUrl}`），TTL 300s。

// Stats 是 /api/stats 域 handler 容器。
type Stats struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc

	// cache 是 stats_ 前缀的内存缓存（对齐 Express 共享 cache Map，5min/200 条）。
	cache *middleware.Cache
}

// NewStats 构造统计 handler 容器。
func NewStats(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Stats {
	return &Stats{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, cache: middleware.NewCache(5*time.Minute, 200)}
}

// Register 挂载 /api/stats 全部端点（不含 /api 前缀；路径对齐 stats.js 子路径）。
// 角色对齐：overview/activity-heatmap/episode-lifecycle/realtime 为 adminOnlyProtect
// （admin+superadmin）；recommendations/collaborative 与 personalized 为 protect
// （任意登录用户）；calendar 与 recommendations/:episodeId 公开。
func (h *Stats) Register(g *gin.RouterGroup) {
	adminOnly := h.AuthMW.Protect(middleware.RoleAdmin, middleware.RoleSuperAdmin)
	g.GET("/overview", adminOnly, h.Overview)
	g.GET("/calendar", h.Calendar)
	g.GET("/recommendations/collaborative", h.AuthMW.Protect(), h.Collaborative)
	g.GET("/recommendations/personalized", h.AuthMW.Protect(), h.Personalized)
	g.GET("/recommendations/:episodeId", h.RelatedRecommendations)
	g.GET("/activity-heatmap", adminOnly, h.ActivityHeatmap)
	g.GET("/episode-lifecycle", adminOnly, h.EpisodeLifecycle)
	g.GET("/realtime", adminOnly, h.Realtime)
}

// cached 读取 stats_ 缓存；命中返回 true 并已写响应。
func (h *Stats) cached(c *gin.Context) bool {
	key := "stats_" + c.Request.URL.RequestURI()
	if v, ok := h.cache.Get(key); ok {
		c.JSON(200, v)
		return true
	}
	return false
}

// store 写入 stats_ 缓存（key 与 cached 同构）。
func (h *Stats) store(c *gin.Context, data any) {
	h.cache.Set("stats_"+c.Request.URL.RequestURI(), data)
}

// Overview GET /api/stats/overview（adminOnlyProtect + cache 300s）。
// @Summary 后台数据总览
// @Tags 统计
// @Security bearerAuth
// @Param period query string false "周期 7d|30d（默认 7d）"
// @Success 200 {object} map[string]any "各类聚合统计"
// @Router /stats/overview [get]
func (h *Stats) Overview(c *gin.Context) {
	if h.cached(c) {
		return
	}
	ctx := c.Request.Context()
	period := c.Query("period")
	if period == "" {
		period = "7d"
	}
	days := 7
	if period == "30d" {
		days = 30
	}
	now := time.Now()
	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)

	// 第一组并发聚合（对齐 Promise.all）。
	totalEpisodes, err := h.Repos.Episodes.CountDocuments(ctx, bson.M{"reviewStatus": "approved"})
	if err != nil {
		serverError(c)
		return
	}
	pendingEpisodes, err := h.Repos.Episodes.CountDocuments(ctx, bson.M{"reviewStatus": "pending"})
	if err != nil {
		serverError(c)
		return
	}
	totalUsers, err := h.Repos.Users.StatsUserCount(ctx, bson.M{})
	if err != nil {
		serverError(c)
		return
	}
	totalFollows, err := h.Repos.Follows.FollowCount(ctx, bson.M{})
	if err != nil {
		serverError(c)
		return
	}
	totalRatings, err := h.Repos.Ratings.StatsRatingCount(ctx, bson.M{})
	if err != nil {
		serverError(c)
		return
	}
	pendingReports, err := h.Repos.Reports.ReportsCount(ctx, bson.M{"status": "pending"})
	if err != nil {
		serverError(c)
		return
	}
	newUsers, err := h.Repos.Users.StatsUserCount(ctx, bson.M{"createdAt": bson.M{"$gte": thirtyDaysAgo}})
	if err != nil {
		serverError(c)
		return
	}
	newEpisodes, err := h.Repos.Episodes.CountDocuments(ctx, bson.M{"createdAt": bson.M{"$gte": thirtyDaysAgo}})
	if err != nil {
		serverError(c)
		return
	}
	totalViews, err := h.Repos.Episodes.StatsTotalViews(ctx)
	if err != nil {
		serverError(c)
		return
	}
	ratingDistribution, err := h.Repos.Ratings.StatsRatingDistribution(ctx)
	if err != nil {
		serverError(c)
		return
	}
	episodeStatusDist, err := h.Repos.Episodes.StatsEpisodeStatusDist(ctx)
	if err != nil {
		serverError(c)
		return
	}

	// 第二组：Top 榜单（对齐第二组 Promise.all）。
	topRated, err := h.Repos.Episodes.StatsTopRated(ctx, 5)
	if err != nil {
		serverError(c)
		return
	}
	mostViewed, err := h.Repos.Episodes.StatsTopViewed(ctx, 5)
	if err != nil {
		serverError(c)
		return
	}
	mostFollowedRows, err := h.Repos.Follows.StatsTopFollowed(ctx, 5)
	if err != nil {
		serverError(c)
		return
	}
	mostFollowedTitles, err := h.Repos.Episodes.StatsFindEpisodeTitles(ctx, statsRowIDs(mostFollowedRows))
	if err != nil {
		serverError(c)
		return
	}

	// 活跃度趋势（UTC 日期键；startDate = trendDays[0] 的 UTC 零点）。
	trendDays := make([]string, 0, days)
	for i := days - 1; i >= 0; i-- {
		trendDays = append(trendDays, now.Add(-time.Duration(i)*24*time.Hour).UTC().Format("2006-01-02"))
	}
	startDate, _ := time.Parse("2006-01-02", trendDays[0])
	activityAgg, err := h.Repos.Histories.StatsActivityTrend(ctx, startDate)
	if err != nil {
		serverError(c)
		return
	}
	activityMap := make(map[string]int64, len(activityAgg))
	for _, a := range activityAgg {
		activityMap[a.Date] = a.Count
	}
	activityTrend := make([]gin.H, 0, len(trendDays))
	for _, ds := range trendDays {
		activityTrend = append(activityTrend, gin.H{"date": ds, "activeUsers": activityMap[ds]})
	}

	activeUsers, err := h.Repos.Histories.StatsDistinctActiveUsers(ctx, thirtyDaysAgo)
	if err != nil {
		serverError(c)
		return
	}
	activeUsers7d, err := h.Repos.Histories.StatsDistinctActiveUsers(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		serverError(c)
		return
	}

	// 每日活跃用户（对齐 DAU：本地零点 6 天前起，UTC 日期键）。
	sevenDaysAgoDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -6)
	dauAgg, err := h.Repos.Histories.StatsDAU(ctx, sevenDaysAgoDate)
	if err != nil {
		serverError(c)
		return
	}
	dauMap := make(map[string]int64, len(dauAgg))
	for _, d := range dauAgg {
		dauMap[d.Date] = d.Count
	}
	dailyActiveUsers := make([]gin.H, 0, 7)
	for i := 6; i >= 0; i-- {
		ds := now.Add(-time.Duration(i) * 24 * time.Hour).UTC().Format("2006-01-02")
		dailyActiveUsers = append(dailyActiveUsers, gin.H{"date": ds, "count": dauMap[ds]})
	}

	// Top 8 榜单。
	topEpisodesByViews, err := h.Repos.Episodes.StatsTopViewed(ctx, 8)
	if err != nil {
		serverError(c)
		return
	}
	topFollowRows, err := h.Repos.Follows.StatsTopFollowed(ctx, 8)
	if err != nil {
		serverError(c)
		return
	}
	topFollowTitles, err := h.Repos.Episodes.StatsFindEpisodeTitles(ctx, statsRowIDs(topFollowRows))
	if err != nil {
		serverError(c)
		return
	}
	topEpisodesByRating, err := h.Repos.Episodes.StatsTopByRating(ctx, 8)
	if err != nil {
		serverError(c)
		return
	}

	// 用户留存率（对齐 retention：lastWatched ≥ UTC 7 天前，7 天滑窗）。
	retentionAgg, err := h.Repos.Histories.StatsRetention(ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		serverError(c)
		return
	}
	retentionMap := make(map[string]int64, len(retentionAgg))
	for _, r := range retentionAgg {
		retentionMap[r.Date] = r.Count
	}
	retention := make([]gin.H, 0, 7)
	for d := 1; d <= 7; d++ {
		ds := now.Add(-time.Duration(7-d) * 24 * time.Hour).UTC().Format("2006-01-02")
		rate := 0
		if totalUsers > 0 {
			rate = int(math.Round(float64(retentionMap[ds]) / float64(totalUsers) * 100))
		}
		if rate > 100 {
			rate = 100
		}
		retention = append(retention, gin.H{"day": fmt.Sprintf("第%d天", d), "rate": rate})
	}

	// 用户注册趋势。
	userTrendAgg, err := h.Repos.Users.StatsUserTrend(ctx, startDate)
	if err != nil {
		serverError(c)
		return
	}
	userTrendMap := make(map[string]int64, len(userTrendAgg))
	for _, u := range userTrendAgg {
		userTrendMap[u.Date] = u.Count
	}
	userTrend := make([]gin.H, 0, len(trendDays))
	for _, ds := range trendDays {
		userTrend = append(userTrend, gin.H{"date": ds, "count": userTrendMap[ds]})
	}

	data := gin.H{
		"totalEpisodes":        totalEpisodes,
		"pendingEpisodes":      pendingEpisodes,
		"totalUsers":           totalUsers,
		"totalFollows":         totalFollows,
		"totalRatings":         totalRatings,
		"pendingReports":       pendingReports,
		"newUsers":             newUsers,
		"newEpisodes":          newEpisodes,
		"totalViews":           totalViews,
		"topRated":             statsRankTopJSON(topRated),
		"mostViewed":           statsRankViewsJSON(mostViewed),
		"mostFollowed":         statsFollowJSON(mostFollowedRows, mostFollowedTitles),
		"userTrend":            userTrend,
		"activeUsers":          activeUsers,
		"activeUsers7d":        activeUsers7d,
		"activeUsers30d":       activeUsers,
		"ratingDistribution":   orEmptyRaw(ratingDistribution),
		"dailyActiveUsers":     dailyActiveUsers,
		"episodeStatusDist":    orEmptyRaw(episodeStatusDist),
		"activityTrend":        activityTrend,
		"topEpisodesByViews":   statsRankViewsJSON(topEpisodesByViews),
		"topEpisodesByFollows": statsFollowJSON(topFollowRows, topFollowTitles),
		"topEpisodesByRating":  statsRankRatingJSON(topEpisodesByRating),
		"retention":            retention,
	}
	h.store(c, data)
	c.JSON(200, data)
}

// Calendar GET /api/stats/calendar（公开 + cache 300s）。
// @Summary 发布日历（按年/月查询已发布/排期/首播）
// @Tags 统计
// @Param year query int false "年份（默认当前年）"
// @Param month query int false "月份 1-12；0 或省略为整年"
// @Success 200 {object} map[string]any "year/month/calendar"
// @Router /stats/calendar [get]
func (h *Stats) Calendar(c *gin.Context) {
	if h.cached(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now()
	targetYear := now.Year()
	if ys := c.Query("year"); ys != "" {
		if y, err := strconv.Atoi(ys); err == nil {
			targetYear = y
		}
	}
	targetMonth := 0
	if ms := c.Query("month"); ms != "" {
		if m, err := strconv.Atoi(ms); err == nil {
			targetMonth = m
		}
	}
	isFullYear := targetMonth == 0
	var start, end time.Time
	if isFullYear {
		start = time.Date(targetYear, 1, 1, 0, 0, 0, 0, time.Local)
		end = time.Date(targetYear+1, 1, 1, 0, 0, 0, 0, time.Local)
	} else {
		start = time.Date(targetYear, time.Month(targetMonth-1), 1, 0, 0, 0, 0, time.Local)
		end = time.Date(targetYear, time.Month(targetMonth), 1, 0, 0, 0, 0, time.Local)
	}

	released, err := h.Repos.SingleEpisodes.StatsCalendarReleased(ctx, start, end)
	if err != nil {
		serverError(c)
		return
	}
	scheduled, err := h.Repos.SingleEpisodes.StatsCalendarScheduled(ctx, start, end)
	if err != nil {
		serverError(c)
		return
	}
	premieres, err := h.Repos.Episodes.StatsFindPremieres(ctx, start, end)
	if err != nil {
		serverError(c)
		return
	}
	upcomingSingles, err := h.Repos.SingleEpisodes.StatsCalendarUpcoming(ctx, start, end)
	if err != nil {
		serverError(c)
		return
	}

	// 收集 populate 所需剧集 ID。
	epIDs := make([]primitive.ObjectID, 0, len(released)+len(scheduled)+len(upcomingSingles))
	for _, se := range released {
		epIDs = append(epIDs, se.EpisodeID)
	}
	for _, se := range scheduled {
		epIDs = append(epIDs, se.EpisodeID)
	}
	for _, se := range upcomingSingles {
		epIDs = append(epIDs, se.EpisodeID)
	}
	epDocs, err := h.Repos.Episodes.StatsCalendarEpisodeDocs(ctx, dedupIDs(epIDs))
	if err != nil {
		serverError(c)
		return
	}

	type calendarDay struct {
		released  []gin.H
		scheduled []gin.H
		premieres []gin.H
	}
	calendar := map[string]*calendarDay{}
	day := func(dateKey string) *calendarDay {
		d, ok := calendar[dateKey]
		if !ok {
			d = &calendarDay{released: []gin.H{}, scheduled: []gin.H{}, premieres: []gin.H{}}
			calendar[dateKey] = d
		}
		return d
	}

	for _, se := range released {
		ep, ok := epDocs[se.EpisodeID]
		if !ok {
			continue
		}
		d := day(utcDateKey(se.ReleaseDate))
		d.released = append(d.released, gin.H{
			"_id":             ep.ID.Hex(),
			"title":           ep.Title,
			"titleEn":         ep.TitleEn,
			"coverImage":      ep.CoverImage,
			"episodeNumber":   se.EpisodeNumber,
			"singleTitle":     se.Title,
			"singleTitleEn":   se.TitleEn,
			"currentEpisodes": ep.CurrentEpisodes,
			"totalEpisodes":   ep.TotalEpisodes,
			"status":          ep.Status,
		})
	}
	for _, se := range scheduled {
		ep, ok := epDocs[se.EpisodeID]
		if !ok {
			continue
		}
		d := day(utcDateKey(se.ScheduledDate))
		d.scheduled = append(d.scheduled, gin.H{
			"_id":             ep.ID.Hex(),
			"title":           ep.Title,
			"titleEn":         ep.TitleEn,
			"coverImage":      ep.CoverImage,
			"episodeNumber":   se.EpisodeNumber,
			"singleTitle":     se.Title,
			"singleTitleEn":   se.TitleEn,
			"currentEpisodes": ep.CurrentEpisodes,
			"totalEpisodes":   ep.TotalEpisodes,
			"status":          ep.Status,
			"scheduledId":     se.ID.Hex(),
		})
	}
	for _, ep := range premieres {
		d := day(utcDateKey(ep.PremiereDate))
		d.premieres = append(d.premieres, gin.H{
			"_id":           ep.ID.Hex(),
			"title":         ep.Title,
			"titleEn":       ep.TitleEn,
			"coverImage":    ep.CoverImage,
			"totalEpisodes": ep.TotalEpisodes,
			"status":        ep.Status,
		})
	}
	for _, se := range upcomingSingles {
		ep, ok := epDocs[se.EpisodeID]
		if !ok {
			continue
		}
		d := day(utcDateKey(se.PremiereDate))
		d.premieres = append(d.premieres, gin.H{
			"_id":              ep.ID.Hex(),
			"title":            ep.Title,
			"titleEn":          ep.TitleEn,
			"coverImage":       ep.CoverImage,
			"episodeNumber":    se.EpisodeNumber,
			"singleTitle":      se.Title,
			"singleTitleEn":    se.TitleEn,
			"totalEpisodes":    ep.TotalEpisodes,
			"status":           ep.Status,
			"isSinglePremiere": true,
		})
	}

	calOut := make(gin.H, len(calendar))
	for dateKey, d := range calendar {
		calOut[dateKey] = gin.H{"released": d.released, "scheduled": d.scheduled, "premieres": d.premieres}
	}
	data := gin.H{"year": targetYear, "month": targetMonth, "calendar": calOut}
	h.store(c, data)
	c.JSON(200, data)
}

// Collaborative GET /api/stats/recommendations/collaborative（protect）。
// @Summary 协作过滤推荐（基于相似用户高分评分）
// @Tags 统计
// @Security bearerAuth
// @Success 200 {array} map[string]any "剧集 + matchScore"
// @Router /stats/recommendations/collaborative [get]
func (h *Stats) Collaborative(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	ctx := c.Request.Context()

	myHighRated, err := h.Repos.Ratings.StatsMyHighRated(ctx, user.ID)
	if err != nil {
		serverError(c)
		return
	}
	if len(myHighRated) == 0 {
		c.JSON(200, []gin.H{})
		return
	}

	similarRatings, err := h.Repos.Ratings.StatsSimilarUserRatings(ctx, myHighRated, user.ID)
	if err != nil {
		serverError(c)
		return
	}
	similarUserIds := uniqueRatingUserIDs(similarRatings)
	if len(similarUserIds) == 0 {
		c.JSON(200, []gin.H{})
		return
	}

	myRated, err := h.Repos.Ratings.StatsMyRatedEpisodeIDs(ctx, user.ID)
	if err != nil {
		serverError(c)
		return
	}
	myFollowed, err := h.Repos.Follows.StatsMyFollowedEpisodeIDs(ctx, user.ID)
	if err != nil {
		serverError(c)
		return
	}
	exclude := dedupIDs(append(myRated, myFollowed...))

	candidates, err := h.Repos.Ratings.StatsCandidateRatings(ctx, similarUserIds, exclude)
	if err != nil {
		serverError(c)
		return
	}

	episodeMap := map[string]*collabEntry{}
	for _, r := range candidates {
		eid := r.EpisodeID.Hex()
		entry, ok := episodeMap[eid]
		if !ok {
			entry = &collabEntry{}
			episodeMap[eid] = entry
		}
		entry.matchScore += 1
		entry.totalScore += r.Score
		entry.count += 1
	}
	sorted := make([]collabSorted, 0, len(episodeMap))
	for eid, data := range episodeMap {
		sorted = append(sorted, collabSorted{
			episodeID:  eid,
			matchScore: data.matchScore,
			avgRating:  data.totalScore / float64(data.count),
		})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].matchScore != sorted[j].matchScore {
			return sorted[i].matchScore > sorted[j].matchScore
		}
		return sorted[i].avgRating > sorted[j].avgRating
	})
	if len(sorted) > 10 {
		sorted = sorted[:10]
	}
	if len(sorted) == 0 {
		c.JSON(200, []gin.H{})
		return
	}

	episodeIDs := make([]primitive.ObjectID, 0, len(sorted))
	for _, s := range sorted {
		if oid, err := primitive.ObjectIDFromHex(s.episodeID); err == nil {
			episodeIDs = append(episodeIDs, oid)
		}
	}
	eps, err := h.Repos.Episodes.StatsFindRecEpisodesByIDs(ctx, episodeIDs)
	if err != nil {
		serverError(c)
		return
	}
	epMap := make(map[string]repository.StatsRecEpisode, len(eps))
	for _, e := range eps {
		epMap[e.ID.Hex()] = e
	}
	results := make([]gin.H, 0, len(sorted))
	for _, s := range sorted {
		ep, ok := epMap[s.episodeID]
		if !ok {
			continue
		}
		results = append(results, gin.H{
			"_id":             ep.ID.Hex(),
			"title":           ep.Title,
			"titleEn":         ep.TitleEn,
			"coverImage":      ep.CoverImage,
			"totalEpisodes":   ep.TotalEpisodes,
			"currentEpisodes": ep.CurrentEpisodes,
			"averageRating":   ep.AverageRating,
			"ratingCount":     ep.RatingCount,
			"matchScore":      s.matchScore,
		})
	}
	c.JSON(200, results)
}

// Personalized GET /api/stats/recommendations/personalized（protect）。
// @Summary 个性化推荐（标签/分类/热门混合打分）
// @Tags 统计
// @Security bearerAuth
// @Success 200 {array} map[string]any "剧集 + reason/reasonName"
// @Router /stats/recommendations/personalized [get]
func (h *Stats) Personalized(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	ctx := c.Request.Context()

	myFollowed, err := h.Repos.Follows.StatsMyFollowedEpisodeIDs(ctx, user.ID)
	if err != nil {
		serverError(c)
		return
	}
	myRatings, err := h.Repos.Ratings.StatsMyRatedEpisodeIDs(ctx, user.ID)
	if err != nil {
		serverError(c)
		return
	}
	excludeIds := dedupIDs(append(append([]primitive.ObjectID{}, myFollowed...), myRatings...))

	interactedIDs := dedupIDs(append(append([]primitive.ObjectID{}, myFollowed...), myRatings...))
	var interactedEpisodes []repository.StatsInteractedEpisode
	if len(interactedIDs) > 0 {
		interactedEpisodes, err = h.Repos.Episodes.StatsInteractedEpisodes(ctx, interactedIDs)
		if err != nil {
			serverError(c)
			return
		}
	}

	userTags := map[string]bool{}
	userCategories := map[string]bool{}
	for i := range interactedEpisodes {
		ep := &interactedEpisodes[i]
		for _, t := range ep.Tags {
			userTags[t] = true
		}
		for _, cat := range ep.Category {
			userCategories[cat] = true
		}
	}

	// 候选容器：保持 JS Map 插入顺序（tag → category → popular）。
	var candidates []*personalCand
	idx := map[string]int{}
	addCand := func(ep repository.StatsRecEpisode) *personalCand {
		id := ep.ID.Hex()
		if i, ok := idx[id]; ok {
			return candidates[i]
		}
		idx[id] = len(candidates)
		c := &personalCand{ep: ep}
		candidates = append(candidates, c)
		return c
	}

	var tagBased []repository.StatsRecEpisode
	if len(userTags) > 0 {
		tagBased, err = h.Repos.Episodes.StatsRecByTags(ctx, mapKeys(userTags), excludeIds)
		if err != nil {
			serverError(c)
			return
		}
	}
	for _, ep := range tagBased {
		entry := addCand(ep)
		common := 0
		for _, t := range ep.Tags {
			if userTags[t] {
				common++
			}
		}
		entry.score += float64(common * 2)
		if common > 0 {
			entry.reasons = append(entry.reasons, "tag")
		}
	}

	var categoryBased []repository.StatsRecEpisode
	if len(userCategories) > 0 {
		categoryBased, err = h.Repos.Episodes.StatsRecByCategory(ctx, mapKeys(userCategories), excludeIds)
		if err != nil {
			serverError(c)
			return
		}
	}
	for _, ep := range categoryBased {
		entry := addCand(ep)
		common := 0
		for _, cat := range ep.Category {
			if userCategories[cat] {
				common++
			}
		}
		entry.score += float64(common)
		if common > 0 && !containsStr(entry.reasons, "category") {
			entry.reasons = append(entry.reasons, "category")
		}
	}

	popularEpisodes, err := h.Repos.Episodes.StatsRecPopular(ctx, excludeIds)
	if err != nil {
		serverError(c)
		return
	}
	for _, ep := range popularEpisodes {
		entry := addCand(ep)
		entry.score += ep.AverageRating*0.5 + float64(ep.Views)*0.001
		if !containsStr(entry.reasons, "popular") {
			entry.reasons = append(entry.reasons, "popular")
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	if len(candidates) > 8 {
		candidates = candidates[:8]
	}

	firstFollowedTitle := ""
	hasFirstFollowed := false
	if len(interactedEpisodes) > 0 {
		firstFollowedTitle = interactedEpisodes[0].Title
		hasFirstFollowed = true
	}

	results := make([]gin.H, 0, len(candidates))
	for _, cand := range candidates {
		ep := cand.ep
		reason := ""
		if containsStr(cand.reasons, "tag") && hasFirstFollowed {
			reason = "becauseYouFollow"
		} else if containsStr(cand.reasons, "category") {
			reason = "popularInCategory"
		} else if containsStr(cand.reasons, "popular") {
			reason = "similarUsersLiked"
		}
		reasonName := ""
		if hasFirstFollowed {
			reasonName = firstFollowedTitle
		}
		results = append(results, gin.H{
			"_id":             ep.ID.Hex(),
			"title":           ep.Title,
			"titleEn":         ep.TitleEn,
			"coverImage":      ep.CoverImage,
			"totalEpisodes":   ep.TotalEpisodes,
			"currentEpisodes": ep.CurrentEpisodes,
			"averageRating":   ep.AverageRating,
			"ratingCount":     ep.RatingCount,
			"reason":          reason,
			"reasonName":      reasonName,
		})
	}
	c.JSON(200, results)
}

// RelatedRecommendations GET /api/stats/recommendations/:episodeId（公开 + cache 300s）。
// @Summary 相关剧集推荐（标签/分类/浏览量混合打分）
// @Tags 统计
// @Param episodeId path string true "剧集 ID"
// @Success 200 {array} map[string]any "相关剧集（含 __v）"
// @Failure 404 {object} map[string]string "Episode not found"
// @Router /stats/recommendations/{episodeId} [get]
func (h *Stats) RelatedRecommendations(c *gin.Context) {
	if h.cached(c) {
		return
	}
	ctx := c.Request.Context()
	oid, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		serverError(c)
		return
	}
	episode, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}

	total, err := h.Repos.Episodes.StatsCountRelated(ctx, oid)
	if err != nil {
		serverError(c)
		return
	}
	skip := int64(0)
	if total > 200 {
		randMax := total - 200
		if randMax < 1 {
			randMax = 1
		}
		skip = rand.Int63n(randMax)
	}
	allEpisodes, err := h.Repos.Episodes.StatsRelatedEpisodes(ctx, oid, skip)
	if err != nil {
		serverError(c)
		return
	}

	maxViews := 1.0
	for _, e := range allEpisodes {
		if float64(e.Views) > maxViews {
			maxViews = float64(e.Views)
		}
	}
	episodeTags := map[string]bool{}
	for _, t := range episode.Tags {
		episodeTags[t] = true
	}
	episodeCats := map[string]bool{}
	for _, cat := range episode.Category {
		episodeCats[cat] = true
	}

	type scoredRelated struct {
		ep    repository.StatsRelatedEpisode
		score float64
	}
	scored := make([]scoredRelated, 0, len(allEpisodes))
	for _, ep := range allEpisodes {
		tagScore := 0
		if len(episode.Tags) > 0 && len(ep.Tags) > 0 {
			for _, t := range ep.Tags {
				if episodeTags[t] {
					tagScore++
				}
			}
		}
		categoryScore := 0.0
		if len(episode.Category) > 0 && len(ep.Category) > 0 {
			common := 0
			for _, cat := range ep.Category {
				if episodeCats[cat] {
					common++
				}
			}
			if common > 0 {
				categoryScore = ep.AverageRating
			}
		}
		viewsScore := float64(ep.Views) / maxViews
		totalScore := float64(tagScore)*0.4 + categoryScore*0.3 + viewsScore*0.3
		scored = append(scored, scoredRelated{ep: ep, score: totalScore})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if len(scored) > 8 {
		scored = scored[:8]
	}

	results := make([]gin.H, 0, len(scored))
	for _, s := range scored {
		e := s.ep
		results = append(results, gin.H{
			"_id":             e.ID.Hex(),
			"title":           e.Title,
			"coverImage":      e.CoverImage,
			"currentEpisodes": e.CurrentEpisodes,
			"totalEpisodes":   e.TotalEpisodes,
			"status":          e.Status,
			"averageRating":   e.AverageRating,
			"views":           e.Views,
			"category":        orEmptyStrings(e.Category),
			"tags":            orEmptyStrings(e.Tags),
			"__v":             e.VersionKey,
		})
	}
	h.store(c, results)
	c.JSON(200, results)
}

// ActivityHeatmap GET /api/stats/activity-heatmap（adminOnlyProtect + cache 300s）。
// @Summary 一年活跃度热力图
// @Tags 统计
// @Security bearerAuth
// @Success 200 {array} map[string]any "date/count（365 天）"
// @Router /stats/activity-heatmap [get]
func (h *Stats) ActivityHeatmap(c *gin.Context) {
	if h.cached(c) {
		return
	}
	ctx := c.Request.Context()
	now := time.Now()
	startDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -364)

	followAgg, err := h.Repos.Follows.StatsHeatmapByFollow(ctx, startDate)
	if err != nil {
		serverError(c)
		return
	}
	ratingAgg, err := h.Repos.Ratings.StatsHeatmapByRating(ctx, startDate)
	if err != nil {
		serverError(c)
		return
	}
	episodeAgg, err := h.Repos.Episodes.StatsHeatmapByEpisode(ctx, startDate)
	if err != nil {
		serverError(c)
		return
	}

	dateMap := make(map[string]int64, 365)
	for i := 0; i < 365; i++ {
		dateMap[startDate.AddDate(0, 0, i).UTC().Format("2006-01-02")] = 0
	}
	for _, a := range followAgg {
		if _, ok := dateMap[a.Date]; ok {
			dateMap[a.Date] += a.Count
		}
	}
	for _, a := range ratingAgg {
		if _, ok := dateMap[a.Date]; ok {
			dateMap[a.Date] += a.Count
		}
	}
	for _, a := range episodeAgg {
		if _, ok := dateMap[a.Date]; ok {
			dateMap[a.Date] += a.Count
		}
	}
	result := make([]gin.H, 0, 365)
	for i := 0; i < 365; i++ {
		ds := startDate.AddDate(0, 0, i).UTC().Format("2006-01-02")
		result = append(result, gin.H{"date": ds, "count": dateMap[ds]})
	}
	h.store(c, result)
	c.JSON(200, result)
}

// EpisodeLifecycle GET /api/stats/episode-lifecycle（adminOnlyProtect + cache 300s）。
// @Summary 热门剧集生命周期（周浏览量曲线）
// @Tags 统计
// @Security bearerAuth
// @Success 200 {array} map[string]any "episodeId/title/weeks"
// @Router /stats/episode-lifecycle [get]
func (h *Stats) EpisodeLifecycle(c *gin.Context) {
	if h.cached(c) {
		return
	}
	ctx := c.Request.Context()
	topEpisodes, err := h.Repos.Episodes.StatsLifecycleEpisodes(ctx, 20)
	if err != nil {
		serverError(c)
		return
	}
	now := time.Now()
	result := make([]gin.H, 0, len(topEpisodes))
	for _, ep := range topEpisodes {
		totalWeeks := int(math.Ceil(now.Sub(ep.CreatedAt).Hours() / (7 * 24)))
		if totalWeeks < 1 {
			totalWeeks = 1
		}
		weeks := make([]gin.H, 0, totalWeeks)
		for w := 1; w <= totalWeeks; w++ {
			views := int(math.Round(float64(ep.Views) * float64(w) / float64(totalWeeks)))
			weeks = append(weeks, gin.H{"week": w, "views": views})
		}
		result = append(result, gin.H{"episodeId": ep.ID.Hex(), "title": ep.Title, "weeks": weeks})
	}
	h.store(c, result)
	c.JSON(200, result)
}

// Realtime GET /api/stats/realtime（adminOnlyProtect）。
// @Summary 实时统计（在线/今日访问/新用户/新剧集）
// @Tags 统计
// @Security bearerAuth
// @Success 200 {object} map[string]any "onlineUsers/todayVisits/todayNewUsers/todayNewEpisodes"
// @Router /stats/realtime [get]
func (h *Stats) Realtime(c *gin.Context) {
	ctx := c.Request.Context()
	now := time.Now()
	fiveMinAgo := now.Add(-5 * time.Minute)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)

	onlineUsers, err := h.Repos.Sessions.StatsSessionCountActiveSince(ctx, fiveMinAgo)
	if err != nil {
		serverError(c)
		return
	}
	todayVisits, err := h.Repos.Sessions.StatsSessionCountActiveSince(ctx, todayStart)
	if err != nil {
		serverError(c)
		return
	}
	todayNewUsers, err := h.Repos.Users.StatsUserCount(ctx, bson.M{"createdAt": bson.M{"$gte": todayStart}})
	if err != nil {
		serverError(c)
		return
	}
	todayNewEpisodes, err := h.Repos.Episodes.CountDocuments(ctx, bson.M{"createdAt": bson.M{"$gte": todayStart}})
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{
		"onlineUsers":      onlineUsers,
		"todayVisits":      todayVisits,
		"todayNewUsers":    todayNewUsers,
		"todayNewEpisodes": todayNewEpisodes,
	})
}

// ---- 工具 ----

// collabEntry 是协作过滤 episodeMap 的中间累加结构。
type collabEntry struct {
	matchScore int
	totalScore float64
	count      int
}

// collabSorted 是排序后的协作过滤候选。
type collabSorted struct {
	episodeID  string
	matchScore int
	avgRating  float64
}

// personalCand 是个性化推荐候选（ep + 累计分 + 命中理由）。
type personalCand struct {
	ep      repository.StatsRecEpisode
	score   float64
	reasons []string
}

// statsRowIDs 提取 Follow 聚合行的剧集 ID。
func statsRowIDs(rows []repository.StatsFollowCountRow) []primitive.ObjectID {
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.EpisodeID)
	}
	return ids
}

// uniqueRatingUserIDs 去重返回评分中的用户 ID（对齐 [...new Set(...)]）。
func uniqueRatingUserIDs(rows []repository.StatsSimilarRating) []primitive.ObjectID {
	seen := map[string]bool{}
	out := make([]primitive.ObjectID, 0, len(rows))
	for _, r := range rows {
		key := r.UserID.Hex()
		if !seen[key] {
			seen[key] = true
			out = append(out, r.UserID)
		}
	}
	return out
}

// statsRankTopJSON 渲染 topRated（含评分/评分人数/浏览量）。
func statsRankTopJSON(list []repository.StatsEpisodeRank) []gin.H {
	out := make([]gin.H, 0, len(list))
	for _, e := range list {
		out = append(out, gin.H{
			"_id":           e.ID.Hex(),
			"title":         e.Title,
			"averageRating": e.AverageRating,
			"ratingCount":   e.RatingCount,
			"views":         e.Views,
			"__v":           e.VersionKey,
		})
	}
	return out
}

// statsRankViewsJSON 渲染 mostViewed / topEpisodesByViews（仅 title/views）。
func statsRankViewsJSON(list []repository.StatsEpisodeRank) []gin.H {
	out := make([]gin.H, 0, len(list))
	for _, e := range list {
		out = append(out, gin.H{"_id": e.ID.Hex(), "title": e.Title, "views": e.Views, "__v": e.VersionKey})
	}
	return out
}

// statsRankRatingJSON 渲染 topEpisodesByRating（仅 title/averageRating）。
func statsRankRatingJSON(list []repository.StatsEpisodeRank) []gin.H {
	out := make([]gin.H, 0, len(list))
	for _, e := range list {
		out = append(out, gin.H{"_id": e.ID.Hex(), "title": e.Title, "averageRating": e.AverageRating, "__v": e.VersionKey})
	}
	return out
}

// statsFollowJSON 渲染 mostFollowed / topEpisodesByFollows（title + count/followCount）。
func statsFollowJSON(rows []repository.StatsFollowCountRow, titles map[primitive.ObjectID]string) []gin.H {
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		title, ok := titles[r.EpisodeID]
		if !ok {
			title = "Unknown"
		}
		out = append(out, gin.H{"title": title, "count": r.Count})
	}
	return out
}

// utcDateKey 取日期字段的 UTC 日期串（对齐 toISOString().split('T')[0]）。
func utcDateKey(t *time.Time) string {
	if t == nil {
		return "1970-01-01"
	}
	return t.UTC().Format("2006-01-02")
}

// orEmptyRaw 空聚合结果补 []（避免 null）。
func orEmptyRaw(raw []bson.M) []bson.M {
	if raw == nil {
		return []bson.M{}
	}
	return raw
}

// mapKeys 返回 map 的键切片。
func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// containsStr 判断切片是否包含目标字符串。
func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
