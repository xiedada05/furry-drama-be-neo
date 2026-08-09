package handler

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/pagination"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/series 子域（行为逐分支照抄 backend/routes/series.js）。

// Series 是 /api/series 域 handler 容器。
type Series struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewSeries 构造系列 handler 容器。
func NewSeries(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Series {
	return &Series{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/series 全部端点（不含 /api 前缀；路径对齐 series.js 子路径）。
// Express 对 series 仅施加全局限流（/api 组级），无 per-endpoint 限流器；
// POST/PUT/DELETE 的 creatorProtect 与 adminProtect 在 authFactory.js 中角色
// 集合相同（creator/admin/superadmin），故共用同一 Protect。
// GET/POST 同时注册 "" 与 "/"：gin 默认会对 /api/series 尾斜杠缺失发起 307 重定向，
// 而 Express（strict routing 关闭）直接匹配，需消除该差异。
func (h *Series) Register(g *gin.RouterGroup) {
	protect := h.AuthMW.Protect(middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin)
	g.GET("/", h.List)
	g.GET("", h.List)
	g.GET("/:id", h.Detail)
	g.POST("/", protect, h.Create)
	g.POST("", protect, h.Create)
	g.PUT("/:id", protect, h.Update)
	g.DELETE("/:id", protect, h.Delete)
}

// seriesPage 解析系列列表的分页参数（对齐 series.js GET / 内联逻辑）：
// page 默认 1 最小 1；limit 默认 50（注意与共享 paginate 的 20 不同），
// 钳制 [1,100]。
func seriesPage(c *gin.Context) (page, limit int) {
	page = 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		page = p
	}
	if page < 1 {
		page = 1
	}
	limit = 50
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

// seriesResponse 是系列响应的 JSON 形状（对齐 series.js 各端点 toJSON 文档，
// 不含 __v——与全局约定一致）。episodes 为已 populate 的剧集对象切片或原始
// 剧集 ID 数组。
type seriesResponse struct {
	ID            primitive.ObjectID  `json:"_id"`
	Name          string              `json:"name"`
	NameEn        string              `json:"nameEn"`
	NameJa        string              `json:"nameJa"`
	Description   string              `json:"description"`
	DescriptionEn string              `json:"descriptionEn"`
	DescriptionJa string              `json:"descriptionJa"`
	Episodes      any                 `json:"episodes"`
	CreatedBy     *primitive.ObjectID `json:"createdBy,omitempty"`
	CreatedAt     time.Time           `json:"createdAt"`
	UpdatedAt     time.Time           `json:"updatedAt"`
}

// seriesJSON 组装系列响应。episodes 参数若非 nil 则原样输出；显式传 nil 输出
// null（仅 PUT 将 episodes 置 null 时使用）。
func seriesJSON(s *model.Series, episodes any) seriesResponse {
	return seriesResponse{
		ID:            s.ID,
		Name:          s.Name,
		NameEn:        s.NameEn,
		NameJa:        s.NameJa,
		Description:   s.Description,
		DescriptionEn: s.DescriptionEn,
		DescriptionJa: s.DescriptionJa,
		Episodes:      episodes,
		CreatedBy:     s.CreatedBy,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
}

// seriesEpisodeHexes 把剧集 ID 数组转为 hex 字符串数组（对齐 create/update
// 响应中未 populate 的 episodes 输出）。
func seriesEpisodeHexes(ids []primitive.ObjectID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Hex())
	}
	return out
}

// collectSeriesEpisodeIDs 汇总一批系列引用的剧集 ID（去重，保持顺序）。
func collectSeriesEpisodeIDs(list []model.Series) []primitive.ObjectID {
	ids := make([]primitive.ObjectID, 0)
	seen := make(map[primitive.ObjectID]struct{})
	for i := range list {
		for _, eid := range list[i].Episodes {
			if eid.IsZero() {
				continue
			}
			if _, ok := seen[eid]; ok {
				continue
			}
			seen[eid] = struct{}{}
			ids = append(ids, eid)
		}
	}
	return ids
}

// fetchSeriesEpisodes 批量查询系列引用的剧集并按 ID 建索引。
func (h *Series) fetchSeriesEpisodes(c *gin.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]*model.SeriesEpisodeDetail, error) {
	eps, err := h.Repos.Episodes.FindByIDsForSeries(c.Request.Context(), ids)
	if err != nil {
		return nil, err
	}
	m := make(map[primitive.ObjectID]*model.SeriesEpisodeDetail, len(eps))
	for i := range eps {
		m[eps[i].ID] = &eps[i]
	}
	return m, nil
}

// seriesListEpisodesView 按系列 episodes 数组顺序组装列表视图；被删除的剧集
// 跳过（mongoose populate 对已删除 ref 在数组中留空）。
func seriesListEpisodesView(epMap map[primitive.ObjectID]*model.SeriesEpisodeDetail, ids []primitive.ObjectID) []model.SeriesEpisodeList {
	out := make([]model.SeriesEpisodeList, 0, len(ids))
	for _, oid := range ids {
		e, ok := epMap[oid]
		if !ok {
			continue
		}
		out = append(out, model.SeriesEpisodeList{
			ID:              e.ID,
			Title:           e.Title,
			CoverImage:      e.CoverImage,
			CurrentEpisodes: e.CurrentEpisodes,
			TotalEpisodes:   e.TotalEpisodes,
			Status:          e.Status,
			AverageRating:   e.AverageRating,
		})
	}
	return out
}

// seriesDetailEpisodesView 按系列 episodes 数组顺序组装详情视图。
func seriesDetailEpisodesView(epMap map[primitive.ObjectID]*model.SeriesEpisodeDetail, ids []primitive.ObjectID) []model.SeriesEpisodeDetail {
	out := make([]model.SeriesEpisodeDetail, 0, len(ids))
	for _, oid := range ids {
		if e, ok := epMap[oid]; ok {
			out = append(out, *e)
		}
	}
	return out
}

// List GET /api/series：系列分页列表，episodes 为已 populate 的剧集摘要。
// @Summary 获取所有系列
// @Tags 系列
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 50，上限 100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /series [get]
func (h *Series) List(c *gin.Context) {
	page, limit := seriesPage(c)
	ctx := c.Request.Context()
	total, err := h.Repos.Series.Count(ctx)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	seriesList, err := h.Repos.Series.FindPage(ctx, page, limit)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// populate episodes（对齐 .populate('episodes', 'title coverImage currentEpisodes
	// totalEpisodes status averageRating')）。
	epMap, err := h.fetchSeriesEpisodes(c, collectSeriesEpisodeIDs(seriesList))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	list := make([]seriesResponse, 0, len(seriesList))
	for i := range seriesList {
		s := &seriesList[i]
		list = append(list, seriesJSON(s, seriesListEpisodesView(epMap, s.Episodes)))
	}
	c.JSON(200, pagination.Result{
		List:       list,
		Page:       page,
		Limit:      limit,
		Total:      total,
		TotalPages: pagination.Query{Page: page, Limit: limit}.TotalPages(total),
	})
}

// Detail GET /api/series/:id：系列详情，episodes 为已 populate 的剧集完整字段。
// @Summary 根据ID获取系列详情
// @Tags 系列
// @Param id path string true "系列 ID"
// @Success 200 {object} model.Series
// @Failure 404 {object} map[string]string "Not found"
// @Router /series/{id} [get]
func (h *Series) Detail(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		// 对齐 Express 非法 ID → CastError → 500。
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	s, err := h.Repos.Series.FindByID(c.Request.Context(), id)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "Not found"})
		return
	}
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	epMap, err := h.fetchSeriesEpisodes(c, s.Episodes)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, seriesJSON(s, seriesDetailEpisodesView(epMap, s.Episodes)))
}

// Create POST /api/series（creator/admin/superadmin）。
// @Summary 创建新系列（需要创作者/管理员权限）
// @Tags 系列
// @Security bearerAuth
// @Accept json
// @Param body body object true "name/description/episodes"
// @Success 201 {object} model.Series
// @Failure 400 {object} map[string]string "名称必填"
// @Router /series [post]
func (h *Series) Create(c *gin.Context) {
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Episodes    []string `json:"episodes"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Name == "" {
		c.JSON(400, gin.H{"message": "名称必填"})
		return
	}
	user, _ := middleware.GetUser(c)
	episodes := make([]primitive.ObjectID, 0, len(req.Episodes))
	for _, s := range req.Episodes {
		oid, err := primitive.ObjectIDFromHex(s)
		if err != nil {
			// 对齐 Express episodes 含非法 ID → CastError → 500。
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		episodes = append(episodes, oid)
	}
	series := &model.Series{
		Name:        req.Name,
		Description: req.Description,
		Episodes:    episodes,
		CreatedBy:   &user.ID,
	}
	if err := h.Repos.Series.Create(c.Request.Context(), series); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(201, seriesJSON(series, seriesEpisodeHexes(series.Episodes)))
}

// Update PUT /api/series/:id（creator/admin/superadmin + 归属校验）。
// @Summary 更新系列信息（需要创作者/管理员权限）
// @Tags 系列
// @Security bearerAuth
// @Accept json
// @Param id path string true "系列 ID"
// @Param body body object true "name/description/episodes"
// @Success 200 {object} model.Series
// @Failure 403 {object} map[string]string "无权修改此系列"
// @Failure 404 {object} map[string]string "Not found"
// @Router /series/{id} [put]
func (h *Series) Update(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	existing, err := h.Repos.Series.FindByID(c.Request.Context(), id)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "Not found"})
		return
	}
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// 归属校验（对齐 series.js）：非 superadmin/admin 且 createdBy 非空且非本人 → 403。
	if user.Role != middleware.RoleSuperAdmin && user.Role != middleware.RoleAdmin &&
		existing.CreatedBy != nil && existing.CreatedBy.Hex() != user.ID.Hex() {
		c.JSON(403, gin.H{"message": "无权修改此系列"})
		return
	}
	patch := bson.M{"updatedAt": time.Now()}
	var body map[string]json.RawMessage
	_ = c.ShouldBindJSON(&body)
	episodesSetNull := false
	if raw, ok := body["name"]; ok {
		if v, ok2 := bsonStringOrNull(raw); ok2 {
			patch["name"] = v
		} else {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	}
	if raw, ok := body["description"]; ok {
		if v, ok2 := bsonStringOrNull(raw); ok2 {
			patch["description"] = v
		} else {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	}
	if raw, ok := body["episodes"]; ok {
		v, ok2 := bsonEpisodesOrNull(raw)
		if !ok2 {
			// 对齐 Express episodes 含非法 ID 或非数组形态 → CastError → 500。
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		patch["episodes"] = v
		episodesSetNull = v == nil
	}
	updated, err := h.Repos.Series.UpdateByID(c.Request.Context(), id, patch)
	if repository.IsNotFound(err) {
		// 竞态：existing 校验后文档被删，findByIdAndUpdate 返回 null → res.json(null)。
		c.JSON(200, nil)
		return
	}
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	var episodesOut any = seriesEpisodeHexes(updated.Episodes)
	if episodesSetNull {
		episodesOut = nil
	}
	c.JSON(200, seriesJSON(updated, episodesOut))
}

// Delete DELETE /api/series/:id（creator/admin/superadmin；Express 的
// adminProtect 与 creatorProtect 角色集合相同）。
// @Summary 删除系列（需要管理员权限）
// @Tags 系列
// @Security bearerAuth
// @Param id path string true "系列 ID"
// @Success 200 {object} map[string]string "Deleted"
// @Router /series/{id} [delete]
func (h *Series) Delete(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if err := h.Repos.Series.DeleteByID(c.Request.Context(), id); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "Deleted"})
}

// bsonStringOrNull 把请求体字段的原始 JSON 转成 BSON 可写入值，对齐 mongoose
// String 类型的宽松 cast：
//   - 字符串 → 字符串；null → BSON null；数字/布尔 → JS String() 等价字符串；
//   - 数组/对象等不可 cast 值 → ok=false（调用方按 CastError → 500 处理）。
func bsonStringOrNull(raw json.RawMessage) (any, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	switch t := v.(type) {
	case nil:
		return nil, true
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return nil, false
}

// bsonEpisodesOrNull 把请求体 episodes 字段的原始 JSON 转成 BSON 数组：
//   - 字符串数组 → []primitive.ObjectID；null → BSON null（value 为 nil）；
//   - 含非法 hex 或非数组形态 → ok=false（对齐 mongoose CastError → 500）。
func bsonEpisodesOrNull(raw json.RawMessage) (any, bool) {
	var nilCheck any
	if err := json.Unmarshal(raw, &nilCheck); err == nil && nilCheck == nil {
		return nil, true
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false
	}
	ids := make([]primitive.ObjectID, 0, len(arr))
	for _, s := range arr {
		oid, err := primitive.ObjectIDFromHex(s)
		if err != nil {
			return nil, false
		}
		ids = append(ids, oid)
	}
	return ids, true
}
