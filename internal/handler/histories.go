package handler

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/histories 与 /api/ratings 两个子域（行为逐分支照抄
// backend/routes/histories.js 与 ratings.js）。

// Histories 是 /api/histories 域 handler 容器。
type Histories struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewHistories 构造观看历史 handler 容器。
func NewHistories(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Histories {
	return &Histories{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/histories 全部端点（不含 /api 前缀；全部 protect）。
func (h *Histories) Register(g *gin.RouterGroup) {
	g.POST("/record", h.AuthMW.Protect(), h.Record)
	g.GET("/continue-watching", h.AuthMW.Protect(), h.ContinueWatching)
	g.GET("/list", h.AuthMW.Protect(), h.List)
	g.GET("/check/:episodeId", h.AuthMW.Protect(), h.Check)
	// 对齐 Express router.delete('/')：`/histories` 与 `/histories/` 均清空。
	g.DELETE("", h.AuthMW.Protect(), h.Clear)
	g.DELETE("/", h.AuthMW.Protect(), h.Clear)
	g.DELETE("/:episodeId", h.AuthMW.Protect(), h.DeleteOne)
}

// Ratings 是 /api/ratings 域 handler 容器。
type Ratings struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewRatings 构造评分 handler 容器。
func NewRatings(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Ratings {
	return &Ratings{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/ratings 全部端点（不含 /api 前缀；全部 protect）。
func (h *Ratings) Register(g *gin.RouterGroup) {
	g.POST("/", h.AuthMW.Protect(), h.Submit)
	g.DELETE("/:episodeId", h.AuthMW.Protect(), h.Remove)
	g.GET("/check/:episodeId", h.AuthMW.Protect(), h.Check)
}

// ---- JS 语义辅助（对齐 Express 对请求值的宽松处理）----

// jsFalsy 对齐 JS 的 falsy 判定（null/undefined/false/0/""/NaN）。
func jsFalsy(v any) bool {
	switch n := v.(type) {
	case nil:
		return true
	case bool:
		return !n
	case float64:
		return n == 0 || math.IsNaN(n)
	case string:
		return n == ""
	default:
		return false
	}
}

// jsParseInt 近似 parseInt(v, 10)：数字截断取整、字符串取前导十进制整数。
// 无法解析返回 ok=false（对应 parseInt 的 NaN）。
func jsParseInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return int(n), true
	case string:
		return jsParseIntString(n)
	default:
		return 0, false
	}
}

// jsParseIntString 近似 parseInt(s, 10)：跳过前导空白，取符号 + 连续十进制数字，
// 数字前缀后停止（parseInt("3abc",10)=3）。
func jsParseIntString(s string) (int, bool) {
	s = strings.TrimLeft(s, " \t\n\r\f\v\u00a0\u3000\ufeff")
	if s == "" {
		return 0, false
	}
	i := 0
	if s[0] == '+' || s[0] == '-' {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0, false
	}
	num, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, false
	}
	return num, true
}

// jsToNumber 近似 JS ToNumber：数字原样、字符串按 Number() 解析（含 0x 十六进制）、
// 布尔 1/0、null 0；无法解析返回 ok=false（对应 NaN）。
func jsToNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case nil:
		return 0, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	case string:
		return jsToNumberString(n)
	default:
		return 0, false
	}
}

// jsToNumberString 近似 Number(s)：trim 后空串 0，支持十进制/指数/十六进制前缀。
func jsToNumberString(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		if n, err := strconv.ParseInt(s[2:], 16, 64); err == nil {
			return float64(n), true
		}
		return 0, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// episodeIDFromBody 从请求体提取 episodeId 并转为 ObjectID。
// 缺失/null 返回 ok=false；非空但非字符串、空串或非法 hex 返回 err
// （对应 mongoose CastError，handler 视为 500）。
func episodeIDFromBody(body map[string]any) (primitive.ObjectID, bool, error) {
	raw, ok := body["episodeId"]
	if !ok || raw == nil {
		return primitive.NilObjectID, false, nil
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		return primitive.NilObjectID, false, errors.New(500, "invalid episodeId")
	}
	oid, err := primitive.ObjectIDFromHex(s)
	if err != nil {
		return primitive.NilObjectID, false, errors.New(500, "invalid episodeId")
	}
	return oid, true, nil
}

// watchedOrEmpty 保证 watchedEpisodes 输出为数组而非 null（对齐 mongoose
// schema default: [] 在 hydrate 时补齐）。
func watchedOrEmpty(ws []int) []int {
	if ws == nil {
		return []int{}
	}
	return ws
}

// ---- histories ----

// historyWriteJSON 对齐 POST /record 的 res.json(history)：episodeId 为 ObjectID
// 字符串（未 populate），含 __v。
func historyWriteJSON(h *model.History, ver int) gin.H {
	return gin.H{
		"_id":                      h.ID.Hex(),
		"userId":                   h.UserID.Hex(),
		"episodeId":                h.EpisodeID.Hex(),
		"watchedEpisodes":          watchedOrEmpty(h.WatchedEpisodes),
		"lastWatchedEpisodeNumber": h.LastWatchedEpisodeNumber,
		"lastWatched":              h.LastWatched,
		"__v":                      ver,
	}
}

// historyRowJSON 对齐 GET /continue-watching / /list 的单条历史：episodeId 为
// populate 出的剧集对象；剧集不存在时输出 null（对齐 mongoose populate 行为）。
func historyRowJSON(row *repository.HistoryRow, eps map[primitive.ObjectID]*repository.HistoryEpisodeView) gin.H {
	doc := &row.History
	r := gin.H{
		"_id":                      doc.ID.Hex(),
		"userId":                   doc.UserID.Hex(),
		"watchedEpisodes":          watchedOrEmpty(doc.WatchedEpisodes),
		"lastWatchedEpisodeNumber": doc.LastWatchedEpisodeNumber,
		"lastWatched":              doc.LastWatched,
		"__v":                      row.Version,
	}
	if ep, ok := eps[doc.EpisodeID]; ok {
		r["episodeId"] = ep
	} else {
		r["episodeId"] = nil
	}
	return r
}

// historyEpisodeMap 批量拉取历史行涉及的剧集摘要。
func (h *Histories) historyEpisodeMap(c *gin.Context, rows []repository.HistoryRow) (map[primitive.ObjectID]*repository.HistoryEpisodeView, error) {
	ids := make([]primitive.ObjectID, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].History.EpisodeID)
	}
	return h.Repos.Episodes.HistoryFindEpisodePopulate(c.Request.Context(), ids)
}

// Record POST /api/histories/record（protect）。
// @Summary 记录观看历史
// @Tags 观看历史
// @Security bearerAuth
// @Accept json
// @Produce json
// @Param body body object true "episodeId/episodeNumber"
// @Success 200 {object} map[string]any "历史记录"
// @Failure 400 {object} map[string]any "Invalid episode number"
// @Failure 404 {object} map[string]any "Episode not found"
// @Router /histories/record [post]
func (h *Histories) Record(c *gin.Context) {
	body := middleware.GetBody(c)
	if body == nil {
		// 非 JSON/urlencoded 请求体：Express req.body 为 undefined，解构抛错 → 500。
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	epNum, ok := jsParseInt(body["episodeNumber"])
	if !ok {
		errors.AbortWithAppError(c, errors.New(400, "Invalid episode number"), h.Config.IsDev)
		return
	}
	// 记录端点无 !episodeId 校验：缺失/null → findById 命中空 → 404；非法 → CastError → 500。
	episodeID, present, err := episodeIDFromBody(body)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if !present {
		errors.AbortWithAppError(c, errors.New(404, "Episode not found"), h.Config.IsDev)
		return
	}
	if _, err := h.Repos.Episodes.FindBasicByID(c.Request.Context(), episodeID); err != nil {
		if repository.IsNotFound(err) {
			errors.AbortWithAppError(c, errors.New(404, "Episode not found"), h.Config.IsDev)
			return
		}
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	user, _ := middleware.GetUser(c)
	hist, ver, err := h.Repos.Histories.HistoryUpsertRecord(c.Request.Context(), user.ID, episodeID, epNum, time.Now())
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, historyWriteJSON(hist, ver))
}

// ContinueWatching GET /api/histories/continue-watching（protect）。
// @Summary 最近观看（10 条）
// @Tags 观看历史
// @Security bearerAuth
// @Produce json
// @Success 200 {array} map[string]any "历史列表（populate episodeId）"
// @Router /histories/continue-watching [get]
func (h *Histories) ContinueWatching(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	rows, err := h.Repos.Histories.HistoryFindContinueWatching(c.Request.Context(), user.ID, 10)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	eps, err := h.historyEpisodeMap(c, rows)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for i := range rows {
		out = append(out, historyRowJSON(&rows[i], eps))
	}
	c.JSON(200, out)
}

// historyPageParams 是 GET /list 的分页参数（对齐 histories.js 的 parseInt 语义；
// 非法值对应 NaN，响应输出 null 且不限制条数）。
type historyPageParams struct {
	page       int
	limit      int
	pageValid  bool
	limitValid bool
}

// parseHistoryPage 解析 page/limit（缺失默认 1/20；空串或非数字 → 非法）。
func parseHistoryPage(c *gin.Context) historyPageParams {
	p := historyPageParams{page: 1, limit: 20, pageValid: true, limitValid: true}
	q := c.Request.URL.Query()
	if _, present := q["page"]; present {
		if v, ok := jsParseInt(c.Query("page")); ok {
			p.page = v
		} else {
			p.pageValid = false
		}
	}
	if _, present := q["limit"]; present {
		if v, ok := jsParseInt(c.Query("limit")); ok {
			p.limit = v
		} else {
			p.limitValid = false
		}
	}
	return p
}

// List GET /api/histories/list（protect，分页）。
// @Summary 观看历史列表
// @Tags 观看历史
// @Security bearerAuth
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /histories/list [get]
func (h *Histories) List(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	p := parseHistoryPage(c)

	total, err := h.Repos.Histories.HistoryCountByUser(c.Request.Context(), user.ID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	var skip int64
	if p.pageValid && p.limitValid {
		skip = int64((p.page - 1) * p.limit)
	}
	var limit int64
	if p.limitValid {
		limit = int64(p.limit)
	}
	rows, err := h.Repos.Histories.HistoryFindPage(c.Request.Context(), user.ID, skip, limit)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	eps, err := h.historyEpisodeMap(c, rows)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for i := range rows {
		out = append(out, historyRowJSON(&rows[i], eps))
	}
	c.JSON(200, gin.H{
		"list":       out,
		"page":       historyPageValue(p.page, p.pageValid),
		"limit":      historyPageValue(p.limit, p.limitValid),
		"total":      total,
		"totalPages": historyTotalPages(p, total),
	})
}

// historyPageValue 非法分页参数输出 null（对齐 JSON.stringify(NaN)）。
func historyPageValue(v int, valid bool) any {
	if !valid {
		return nil
	}
	return v
}

// historyTotalPages 计算 Math.ceil(total/limit)；limit 非法或 0 时输出 null
// （对齐 Math.ceil(0/0)=NaN → JSON null）。
func historyTotalPages(p historyPageParams, total int64) any {
	if !p.limitValid || p.limit <= 0 {
		return nil
	}
	return (total + int64(p.limit) - 1) / int64(p.limit)
}

// Check GET /api/histories/check/:episodeId（protect）。
// @Summary 查询某剧集观看历史
// @Tags 观看历史
// @Security bearerAuth
// @Produce json
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]any "watchedEpisodes/lastWatchedEpisodeNumber/lastWatched"
// @Router /histories/check/{episodeId} [get]
func (h *Histories) Check(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	row, err := h.Repos.Histories.HistoryFindCheck(c.Request.Context(), user.ID, episodeID)
	if err != nil {
		if !repository.IsNotFound(err) {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		c.JSON(200, gin.H{"watchedEpisodes": []int{}, "lastWatchedEpisodeNumber": nil, "lastWatched": nil})
		return
	}
	c.JSON(200, gin.H{
		"watchedEpisodes":          watchedOrEmpty(row.WatchedEpisodes),
		"lastWatchedEpisodeNumber": row.LastWatchedEpisodeNumber,
		"lastWatched":              row.LastWatched,
	})
}

// Clear DELETE /api/histories（protect，清空全部）。
// @Summary 清空观看历史
// @Tags 观看历史
// @Security bearerAuth
// @Produce json
// @Success 200 {object} map[string]string
// @Router /histories [delete]
func (h *Histories) Clear(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	if err := h.Repos.Histories.DeleteByUser(c.Request.Context(), user.ID); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "All history cleared"})
}

// DeleteOne DELETE /api/histories/:episodeId（protect）。
// @Summary 删除单条观看历史
// @Tags 观看历史
// @Security bearerAuth
// @Produce json
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]string
// @Router /histories/{episodeId} [delete]
func (h *Histories) DeleteOne(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if err := h.Repos.Histories.HistoryDeleteOneByUserEpisode(c.Request.Context(), user.ID, episodeID); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "History deleted"})
}

// ---- ratings ----

// Submit POST /api/ratings（protect）。
// @Summary 提交评分
// @Tags 评分
// @Security bearerAuth
// @Accept json
// @Produce json
// @Param body body object true "episodeId/score"
// @Success 200 {object} map[string]any "score/averageRating/ratingCount"
// @Failure 400 {object} map[string]any "Invalid rating data"
// @Failure 403 {object} map[string]any "该剧集暂不可评分"
// @Failure 404 {object} map[string]any "Episode not found"
// @Router /ratings [post]
func (h *Ratings) Submit(c *gin.Context) {
	body := middleware.GetBody(c)
	if body == nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	scoreRaw := body["score"]
	scoreNum, ok := jsToNumber(scoreRaw)
	if jsFalsy(body["episodeId"]) || jsFalsy(scoreRaw) || !ok || scoreNum < 1 || scoreNum > 5 {
		errors.AbortWithAppError(c, errors.New(400, "Invalid rating data"), h.Config.IsDev)
		return
	}
	episodeID, _, err := episodeIDFromBody(body)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	user, _ := middleware.GetUser(c)

	episode, err := h.Repos.Episodes.FindByID(c.Request.Context(), episodeID)
	if err != nil {
		if repository.IsNotFound(err) {
			errors.AbortWithAppError(c, errors.New(404, "Episode not found"), h.Config.IsDev)
			return
		}
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// 仅允许对已审核通过的剧集评分（reviewStatus 缺失视为 approved，对齐 schema 默认值）。
	if episode.ReviewStatus != "" && episode.ReviewStatus != "approved" {
		errors.AbortWithAppError(c, errors.New(403, "该剧集暂不可评分"), h.Config.IsDev)
		return
	}

	if err := h.Repos.Ratings.RatingUpsertScore(c.Request.Context(), user.ID, episodeID, scoreNum, time.Now()); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	avg, count, err := h.Repos.Ratings.RatingAggregateStats(c.Request.Context(), episodeID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	avgRounded := math.Round(avg*10) / 10 // 对齐 Math.round(avg*10)/10（1 位小数）
	if err := h.Repos.Episodes.RatingSetEpisodeStats(c.Request.Context(), episodeID, avgRounded, count); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// 对齐 clearCache('episode_'+episodeId) + clearCacheByPrefix('episodes_')。
	middleware.EpisodeCache.Delete("episode_" + episodeID.Hex())
	middleware.EpisodeCache.DeleteByPrefix("episodes_")

	// score 回显原始请求值（对齐 res.json({ score, ... })）。
	c.JSON(200, gin.H{"score": scoreRaw, "averageRating": avgRounded, "ratingCount": count})
}

// Remove DELETE /api/ratings/:episodeId（protect）。
// @Summary 删除评分
// @Tags 评分
// @Security bearerAuth
// @Produce json
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]any "message/averageRating/ratingCount"
// @Failure 404 {object} map[string]any "Rating not found"
// @Router /ratings/{episodeId} [delete]
func (h *Ratings) Remove(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if _, err := h.Repos.Ratings.RatingFindScore(c.Request.Context(), user.ID, episodeID); err != nil {
		if repository.IsNotFound(err) {
			errors.AbortWithAppError(c, errors.New(404, "Rating not found"), h.Config.IsDev)
			return
		}
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if err := h.Repos.Ratings.RatingDeleteByUserEpisode(c.Request.Context(), user.ID, episodeID); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	avg, count, err := h.Repos.Ratings.RatingAggregateStats(c.Request.Context(), episodeID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	avgRounded := math.Round(avg*10) / 10
	if err := h.Repos.Episodes.RatingSetEpisodeStats(c.Request.Context(), episodeID, avgRounded, count); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + episodeID.Hex())
	middleware.EpisodeCache.DeleteByPrefix("episodes_")

	c.JSON(200, gin.H{"message": "Rating deleted", "averageRating": avgRounded, "ratingCount": count})
}

// Check GET /api/ratings/check/:episodeId（protect）。
// @Summary 查询我的评分
// @Tags 评分
// @Security bearerAuth
// @Produce json
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]any "score"
// @Router /ratings/check/{episodeId} [get]
func (h *Ratings) Check(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	episodeID, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	score, err := h.Repos.Ratings.RatingFindScore(c.Request.Context(), user.ID, episodeID)
	if err != nil && !repository.IsNotFound(err) {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"score": score})
}
