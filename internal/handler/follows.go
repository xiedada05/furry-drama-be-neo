package handler

import (
	"sort"
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

// 本文件实现 /api/follows 与 /api/favorites 两个子域（行为逐分支照抄
// backend/routes/follows.js 与 favorites.js）。

// Follows 是 /api/follows 域 handler 容器。
type Follows struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewFollows 构造追番 handler 容器。
func NewFollows(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Follows {
	return &Follows{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/follows 全部端点（不含 /api 前缀；全部 protect）。
func (h *Follows) Register(g *gin.RouterGroup) {
	g.POST("/add", h.AuthMW.Protect(), h.Add)
	g.POST("/remove", h.AuthMW.Protect(), h.Remove)
	g.GET("/list", h.AuthMW.Protect(), h.List)
	g.GET("/check/:episodeId", h.AuthMW.Protect(), h.Check)
}

// Favorites 是 /api/favorites 域 handler 容器。
type Favorites struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewFavorites 构造收藏 handler 容器。
func NewFavorites(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Favorites {
	return &Favorites{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/favorites 全部端点（不含 /api 前缀；全部 protect）。
func (h *Favorites) Register(g *gin.RouterGroup) {
	g.POST("/add", h.AuthMW.Protect(), h.Add)
	g.POST("/remove", h.AuthMW.Protect(), h.Remove)
	g.GET("/list", h.AuthMW.Protect(), h.List)
	g.GET("/counts", h.AuthMW.Protect(), h.Counts)
	g.GET("/check/:episodeId", h.AuthMW.Protect(), h.Check)
}

// parseRef 解析 hex 引用（episodeId/folderId）。
// 空串返回 ok=false（对齐 mongoose 对 undefined 的宽松处理），非法 hex 返回 error
// （对齐 mongoose CastError，handler 视为 500）。
func parseRef(s string) (primitive.ObjectID, bool, error) {
	if s == "" {
		return primitive.NilObjectID, false, nil
	}
	oid, err := primitive.ObjectIDFromHex(s)
	if err != nil {
		return primitive.NilObjectID, false, err
	}
	return oid, true, nil
}

// ---- follows ----

// followWriteRow 对齐 follows.js POST /add 的 res.json(follow)（folderId 缺省输出 null）。
type followWriteRow struct {
	ID                 primitive.ObjectID  `json:"_id"`
	UserID             primitive.ObjectID  `json:"userId"`
	EpisodeID          primitive.ObjectID  `json:"episodeId"`
	FolderID           *primitive.ObjectID `json:"folderId"`
	FollowedAtEpisodes int                 `json:"followedAtEpisodes"`
	CreatedAt          time.Time           `json:"createdAt"`
}

// followListRow 对齐 follows.js GET /list 单条（populate episodeId/folderId 后）。
type followListRow struct {
	ID                 primitive.ObjectID `json:"_id"`
	UserID             primitive.ObjectID `json:"userId"`
	EpisodeID          any                `json:"episodeId"`
	FolderID           any                `json:"folderId"`
	FollowedAtEpisodes int                `json:"followedAtEpisodes"`
	CreatedAt          time.Time          `json:"createdAt"`
}

// Add POST /api/follows/add（protect）。
// @Summary 追番剧集
// @Tags 追番
// @Security bearerAuth
// @Accept json
// @Produce json
// @Param body body object true "episodeId/folderId"
// @Success 200 {object} map[string]any "追番记录"
// @Router /follows/add [post]
func (h *Follows) Add(c *gin.Context) {
	var req struct {
		EpisodeID string `json:"episodeId"`
		FolderID  string `json:"folderId"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)

	episode, err := h.Repos.Episodes.FollowEpisodeByID(c.Request.Context(), req.EpisodeID)
	if err != nil {
		if repository.IsNotFound(err) {
			errors.AbortWithAppError(c, errors.New(404, "Episode not found"), h.Config.IsDev)
			return
		}
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// 仅允许追番已审核通过的剧集（对齐 follows.js 的 reviewStatus 检查）。
	if episode.ReviewStatus != "" && episode.ReviewStatus != "approved" {
		errors.AbortWithAppError(c, errors.New(403, "该剧集暂不可追番"), h.Config.IsDev)
		return
	}

	follow := &model.Follow{
		ID:                 primitive.NewObjectID(),
		UserID:             user.ID,
		EpisodeID:          episode.ID,
		FollowedAtEpisodes: episode.CurrentEpisodes,
		CreatedAt:          time.Now(),
	}
	if req.FolderID != "" {
		folderID, ok, ferr := parseRef(req.FolderID)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		if ok {
			follow.FolderID = &folderID
		}
	}
	if err := h.Repos.Follows.FollowInsert(c.Request.Context(), follow); err != nil {
		if repository.IsDuplicateKey(err) {
			errors.AbortWithAppError(c, errors.New(400, "Already following"), h.Config.IsDev)
			return
		}
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, followWriteRow{
		ID:                 follow.ID,
		UserID:             follow.UserID,
		EpisodeID:          follow.EpisodeID,
		FolderID:           follow.FolderID,
		FollowedAtEpisodes: follow.FollowedAtEpisodes,
		CreatedAt:          follow.CreatedAt,
	})
}

// Remove POST /api/follows/remove（protect）。
// @Summary 取消追番
// @Tags 追番
// @Security bearerAuth
// @Accept json
// @Param body body object true "episodeId"
// @Success 200 {object} map[string]string
// @Router /follows/remove [post]
func (h *Follows) Remove(c *gin.Context) {
	var req struct {
		EpisodeID string `json:"episodeId"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)

	oid, ok, err := parseRef(req.EpisodeID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if ok {
		if err := h.Repos.Follows.FollowDeleteOne(c.Request.Context(), user.ID, oid); err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	}
	c.JSON(200, gin.H{"message": "Unfollowed successfully"})
}

// List GET /api/follows/list（protect，分页 + 剧集/收藏夹填充）。
// @Summary 追番列表
// @Tags 追番
// @Security bearerAuth
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param folderId query string false "收藏夹 ID 或 null"
// @Param sort query string false "updatedAt|name|rating|lastWatched"
// @Success 200 {object} pagination.Result
// @Router /follows/list [get]
func (h *Follows) List(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	pq := pagination.Parse(c)
	sortParam := c.Query("sort")
	if sortParam == "" {
		sortParam = "updatedAt"
	}

	filter := bson.M{"userId": user.ID}
	if folderID := c.Query("folderId"); folderID != "" {
		if folderID == "null" {
			filter["folderId"] = nil
		} else {
			oid, _, ferr := parseRef(folderID)
			if ferr != nil {
				errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
				return
			}
			filter["folderId"] = oid
		}
	}

	ctx := c.Request.Context()
	total, err := h.Repos.Follows.FollowCount(ctx, filter)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	totalPages := pq.TotalPages(total)

	var (
		pageItems  []model.Follow
		episodeMap map[primitive.ObjectID]repository.FollowsEpisodeDoc
	)

	switch sortParam {
	case "name", "rating":
		// 对齐 follows.js：取 min(total, page*limit+100) 条，内存排序后切片。
		maxItems := total
		if m := int64(pq.Page*pq.Limit + 100); maxItems > m {
			maxItems = m
		}
		allItems, ferr := h.Repos.Follows.FollowListLimited(ctx, filter, maxItems)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		episodeMap, ferr = h.Repos.Episodes.FollowEpisodesByIDs(ctx, followEpisodeIDs(allItems))
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		if sortParam == "name" {
			sort.SliceStable(allItems, func(i, j int) bool {
				return followTitle(episodeMap, allItems[i]) < followTitle(episodeMap, allItems[j])
			})
		} else {
			sort.SliceStable(allItems, func(i, j int) bool {
				return followRating(episodeMap, allItems[i]) > followRating(episodeMap, allItems[j])
			})
		}
		pageItems = followSlice(allItems, (pq.Page-1)*pq.Limit, pq.Page*pq.Limit)
	case "lastWatched":
		maxItems := total
		if m := int64(pq.Page*pq.Limit + 100); maxItems > m {
			maxItems = m
		}
		allItems, ferr := h.Repos.Follows.FollowListLimited(ctx, filter, maxItems)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		ids := followEpisodeIDs(allItems)
		episodeMap, ferr = h.Repos.Episodes.FollowEpisodesByIDs(ctx, ids)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		historyMap, ferr := h.Repos.Histories.FollowLastWatchedMap(ctx, user.ID, ids)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		// 缺 lastWatched 的按 epoch 视为最旧（对齐 historyMap[...] || new Date(0)）。
		sort.SliceStable(allItems, func(i, j int) bool {
			return historyMap[allItems[i].EpisodeID].After(historyMap[allItems[j].EpisodeID])
		})
		pageItems = followSlice(allItems, (pq.Page-1)*pq.Limit, pq.Page*pq.Limit)
	default:
		// 默认分支：sort({createdAt:-1}).skip().limit()（Follow 无 updatedAt 字段）。
		pageItems, err = h.Repos.Follows.FollowListPaged(ctx, filter, pq.Page, pq.Limit)
		if err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		episodeMap, err = h.Repos.Episodes.FollowEpisodesByIDs(ctx, followEpisodeIDs(pageItems))
		if err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	}

	folderMap, ferr := h.Repos.Folders.FollowFoldersByIDs(ctx, followFolderIDs(pageItems))
	if ferr != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}

	list := make([]followListRow, 0, len(pageItems))
	for i := range pageItems {
		item := pageItems[i]
		row := followListRow{
			ID:                 item.ID,
			UserID:             item.UserID,
			FollowedAtEpisodes: item.FollowedAtEpisodes,
			CreatedAt:          item.CreatedAt,
		}
		if ep, ok := episodeMap[item.EpisodeID]; ok {
			row.EpisodeID = ep
		}
		if item.FolderID != nil {
			if f, ok := folderMap[*item.FolderID]; ok {
				row.FolderID = f
			}
		}
		list = append(list, row)
	}
	c.JSON(200, pagination.Result{List: list, Page: pq.Page, Limit: pq.Limit, Total: total, TotalPages: totalPages})
}

// Check GET /api/follows/check/:episodeId（protect）。
// @Summary 是否追番该剧集
// @Tags 追番
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]any "isFollowing/followedAtEpisodes"
// @Router /follows/check/{episodeId} [get]
func (h *Follows) Check(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	oid, ok, err := parseRef(c.Param("episodeId"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if !ok {
		c.JSON(200, gin.H{"isFollowing": false})
		return
	}
	follow, err := h.Repos.Follows.FollowFindByUserEpisode(c.Request.Context(), user.ID, oid)
	if err != nil && !repository.IsNotFound(err) {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// 未追番时 followedAtEpisodes 省略（对齐 Express 输出 undefined 被 JSON 丢弃）。
	if follow == nil {
		c.JSON(200, gin.H{"isFollowing": false})
		return
	}
	c.JSON(200, gin.H{"isFollowing": true, "followedAtEpisodes": follow.FollowedAtEpisodes})
}

// ---- favorites ----

// favoriteListRow 对齐 favorites.js GET /list 单条（populate episodeId/folderId 后）。
type favoriteListRow struct {
	ID        primitive.ObjectID `json:"_id"`
	UserID    primitive.ObjectID `json:"userId"`
	EpisodeID any                `json:"episodeId"`
	FolderID  any                `json:"folderId"`
	CreatedAt time.Time          `json:"createdAt"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

// Add POST /api/favorites/add（protect）。
// @Summary 收藏剧集
// @Tags 收藏
// @Security bearerAuth
// @Accept json
// @Produce json
// @Param body body object true "episodeId/folderId"
// @Success 200 {object} map[string]string
// @Router /favorites/add [post]
func (h *Favorites) Add(c *gin.Context) {
	var req struct {
		EpisodeID string `json:"episodeId"`
		FolderID  string `json:"folderId"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)

	episode, err := h.Repos.Episodes.FollowEpisodeByID(c.Request.Context(), req.EpisodeID)
	if err != nil {
		if repository.IsNotFound(err) {
			errors.AbortWithAppError(c, errors.New(404, "Episode not found"), h.Config.IsDev)
			return
		}
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// 仅允许收藏已审核通过的剧集（对齐 favorites.js 的 reviewStatus 检查）。
	if episode.ReviewStatus != "" && episode.ReviewStatus != "approved" {
		errors.AbortWithAppError(c, errors.New(403, "该剧集暂不可收藏"), h.Config.IsDev)
		return
	}

	fav := &model.Favorite{
		ID:        primitive.NewObjectID(),
		UserID:    user.ID,
		EpisodeID: episode.ID,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if req.FolderID != "" {
		folderID, ok, ferr := parseRef(req.FolderID)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		if ok {
			// 收藏夹必须存在且属于当前用户（对齐 favorites.js 的归属校验）。
			if _, ferr := h.Repos.Folders.FollowFolderByIDAndUser(c.Request.Context(), folderID, user.ID); ferr != nil {
				if repository.IsNotFound(ferr) {
					errors.AbortWithAppError(c, errors.New(400, "收藏夹不存在或不属于当前用户"), h.Config.IsDev)
					return
				}
				errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
				return
			}
			fav.FolderID = &folderID
		}
	}
	if err := h.Repos.Favorites.FavoriteInsert(c.Request.Context(), fav); err != nil {
		if repository.IsDuplicateKey(err) {
			errors.AbortWithAppError(c, errors.New(400, "Already favorited"), h.Config.IsDev)
			return
		}
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "Favorited"})
}

// Remove POST /api/favorites/remove（protect）。
// @Summary 取消收藏
// @Tags 收藏
// @Security bearerAuth
// @Accept json
// @Param body body object true "episodeId"
// @Success 200 {object} map[string]string
// @Router /favorites/remove [post]
func (h *Favorites) Remove(c *gin.Context) {
	var req struct {
		EpisodeID string `json:"episodeId"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)

	oid, ok, err := parseRef(req.EpisodeID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if ok {
		if err := h.Repos.Favorites.FavoriteDeleteOne(c.Request.Context(), user.ID, oid); err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	}
	c.JSON(200, gin.H{"message": "Unfavorited"})
}

// List GET /api/favorites/list（protect，分页 + 剧集/收藏夹填充）。
// @Summary 收藏列表
// @Tags 收藏
// @Security bearerAuth
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param folderId query string false "收藏夹 ID 或 null"
// @Param sort query string false "updatedAt|name|rating|lastWatched"
// @Success 200 {object} pagination.Result
// @Router /favorites/list [get]
func (h *Favorites) List(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	pq := pagination.Parse(c)
	sortParam := c.Query("sort")
	if sortParam == "" {
		sortParam = "updatedAt"
	}

	filter := bson.M{"userId": user.ID}
	if folderID := c.Query("folderId"); folderID != "" {
		if folderID == "null" {
			filter["folderId"] = nil
		} else {
			oid, _, ferr := parseRef(folderID)
			if ferr != nil {
				errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
				return
			}
			filter["folderId"] = oid
		}
	}

	ctx := c.Request.Context()
	total, err := h.Repos.Favorites.FavoriteCount(ctx, filter)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	totalPages := pq.TotalPages(total)

	var (
		pageItems  []model.Favorite
		episodeMap map[primitive.ObjectID]repository.FollowsEpisodeDoc
	)

	switch sortParam {
	case "name", "rating":
		maxItems := total
		if m := int64(pq.Page*pq.Limit + 100); maxItems > m {
			maxItems = m
		}
		allItems, ferr := h.Repos.Favorites.FavoriteListLimited(ctx, filter, maxItems)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		episodeMap, ferr = h.Repos.Episodes.FollowEpisodesByIDs(ctx, favoriteEpisodeIDs(allItems))
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		if sortParam == "name" {
			sort.SliceStable(allItems, func(i, j int) bool {
				return favoriteTitle(episodeMap, allItems[i]) < favoriteTitle(episodeMap, allItems[j])
			})
		} else {
			sort.SliceStable(allItems, func(i, j int) bool {
				return favoriteRating(episodeMap, allItems[i]) > favoriteRating(episodeMap, allItems[j])
			})
		}
		pageItems = favoriteSlice(allItems, (pq.Page-1)*pq.Limit, pq.Page*pq.Limit)
	case "lastWatched":
		maxItems := total
		if m := int64(pq.Page*pq.Limit + 100); maxItems > m {
			maxItems = m
		}
		allItems, ferr := h.Repos.Favorites.FavoriteListLimited(ctx, filter, maxItems)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		ids := favoriteEpisodeIDs(allItems)
		episodeMap, ferr = h.Repos.Episodes.FollowEpisodesByIDs(ctx, ids)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		historyMap, ferr := h.Repos.Histories.FollowLastWatchedMap(ctx, user.ID, ids)
		if ferr != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		sort.SliceStable(allItems, func(i, j int) bool {
			return historyMap[allItems[i].EpisodeID].After(historyMap[allItems[j].EpisodeID])
		})
		pageItems = favoriteSlice(allItems, (pq.Page-1)*pq.Limit, pq.Page*pq.Limit)
	default:
		// 默认分支：sort({updatedAt:-1}).skip().limit()。
		pageItems, err = h.Repos.Favorites.FavoriteListPaged(ctx, filter, pq.Page, pq.Limit)
		if err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		episodeMap, err = h.Repos.Episodes.FollowEpisodesByIDs(ctx, favoriteEpisodeIDs(pageItems))
		if err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	}

	folderMap, ferr := h.Repos.Folders.FollowFoldersByIDs(ctx, favoriteFolderIDs(pageItems))
	if ferr != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}

	list := make([]favoriteListRow, 0, len(pageItems))
	for i := range pageItems {
		item := pageItems[i]
		row := favoriteListRow{
			ID:        item.ID,
			UserID:    item.UserID,
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt,
		}
		if ep, ok := episodeMap[item.EpisodeID]; ok {
			row.EpisodeID = ep
		}
		if item.FolderID != nil {
			if f, ok := folderMap[*item.FolderID]; ok {
				row.FolderID = f
			}
		}
		list = append(list, row)
	}
	c.JSON(200, pagination.Result{List: list, Page: pq.Page, Limit: pq.Limit, Total: total, TotalPages: totalPages})
}

// Counts GET /api/favorites/counts（protect）。
// @Summary 各收藏夹收藏计数
// @Tags 收藏
// @Security bearerAuth
// @Success 200 {object} map[string]any "total/unclassified/folders"
// @Router /favorites/counts [get]
func (h *Favorites) Counts(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	ctx := c.Request.Context()

	total, err := h.Repos.Favorites.FavoriteCount(ctx, bson.M{"userId": user.ID})
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	unclassified, err := h.Repos.Favorites.FavoriteCount(ctx, bson.M{"userId": user.ID, "folderId": nil})
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	folderIDs, err := h.Repos.Folders.FollowFavoriteFolderIDs(ctx, user.ID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	folderCounts := make(map[string]int64, len(folderIDs))
	for _, fid := range folderIDs {
		n, err := h.Repos.Favorites.FavoriteCount(ctx, bson.M{"userId": user.ID, "folderId": fid})
		if err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		folderCounts[fid.Hex()] = n
	}
	c.JSON(200, gin.H{"total": total, "unclassified": unclassified, "folders": folderCounts})
}

// Check GET /api/favorites/check/:episodeId（protect）。
// @Summary 是否收藏该剧集
// @Tags 收藏
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]bool "isFavorite"
// @Router /favorites/check/{episodeId} [get]
func (h *Favorites) Check(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	oid, ok, err := parseRef(c.Param("episodeId"))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if !ok {
		c.JSON(200, gin.H{"isFavorite": false})
		return
	}
	fav, err := h.Repos.Favorites.FavoriteFindByUserEpisode(c.Request.Context(), user.ID, oid)
	if err != nil && !repository.IsNotFound(err) {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"isFavorite": fav != nil})
}

// ---- helpers ----

// followEpisodeIDs 提取去重后的剧集 ID 列表（populate episodeId 用）。
func followEpisodeIDs(items []model.Follow) []primitive.ObjectID {
	ids := make([]primitive.ObjectID, 0, len(items))
	seen := make(map[primitive.ObjectID]struct{}, len(items))
	for _, it := range items {
		if it.EpisodeID.IsZero() {
			continue
		}
		if _, ok := seen[it.EpisodeID]; ok {
			continue
		}
		seen[it.EpisodeID] = struct{}{}
		ids = append(ids, it.EpisodeID)
	}
	return ids
}

// favoriteEpisodeIDs 提取去重后的剧集 ID 列表（populate episodeId 用）。
func favoriteEpisodeIDs(items []model.Favorite) []primitive.ObjectID {
	ids := make([]primitive.ObjectID, 0, len(items))
	seen := make(map[primitive.ObjectID]struct{}, len(items))
	for _, it := range items {
		if it.EpisodeID.IsZero() {
			continue
		}
		if _, ok := seen[it.EpisodeID]; ok {
			continue
		}
		seen[it.EpisodeID] = struct{}{}
		ids = append(ids, it.EpisodeID)
	}
	return ids
}

// followFolderIDs 提取去重后的收藏夹 ID 列表（populate folderId 用）。
func followFolderIDs(items []model.Follow) []primitive.ObjectID {
	ids := make([]primitive.ObjectID, 0)
	seen := make(map[primitive.ObjectID]struct{})
	for _, it := range items {
		if it.FolderID == nil {
			continue
		}
		if _, ok := seen[*it.FolderID]; ok {
			continue
		}
		seen[*it.FolderID] = struct{}{}
		ids = append(ids, *it.FolderID)
	}
	return ids
}

// favoriteFolderIDs 提取去重后的收藏夹 ID 列表（populate folderId 用）。
func favoriteFolderIDs(items []model.Favorite) []primitive.ObjectID {
	ids := make([]primitive.ObjectID, 0)
	seen := make(map[primitive.ObjectID]struct{})
	for _, it := range items {
		if it.FolderID == nil {
			continue
		}
		if _, ok := seen[*it.FolderID]; ok {
			continue
		}
		seen[*it.FolderID] = struct{}{}
		ids = append(ids, *it.FolderID)
	}
	return ids
}

// followTitle 取追番项对应剧集标题（缺失回退空串，对齐 a.episodeId?.title || ”）。
func followTitle(m map[primitive.ObjectID]repository.FollowsEpisodeDoc, item model.Follow) string {
	if ep, ok := m[item.EpisodeID]; ok {
		return ep.Title
	}
	return ""
}

// followRating 取追番项对应剧集均分（缺失回退 0，对齐 a.episodeId?.averageRating || 0）。
func followRating(m map[primitive.ObjectID]repository.FollowsEpisodeDoc, item model.Follow) float64 {
	if ep, ok := m[item.EpisodeID]; ok {
		return ep.AverageRating
	}
	return 0
}

// favoriteTitle 取收藏项对应剧集标题（缺失回退空串）。
func favoriteTitle(m map[primitive.ObjectID]repository.FollowsEpisodeDoc, item model.Favorite) string {
	if ep, ok := m[item.EpisodeID]; ok {
		return ep.Title
	}
	return ""
}

// favoriteRating 取收藏项对应剧集均分（缺失回退 0）。
func favoriteRating(m map[primitive.ObjectID]repository.FollowsEpisodeDoc, item model.Favorite) float64 {
	if ep, ok := m[item.EpisodeID]; ok {
		return ep.AverageRating
	}
	return 0
}

// followSlice 复刻 Array.prototype.slice(start, end) 对正索引的钳制。
func followSlice(items []model.Follow, start, end int) []model.Follow {
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

// favoriteSlice 复刻 Array.prototype.slice(start, end) 对正索引的钳制。
func favoriteSlice(items []model.Favorite, start, end int) []model.Favorite {
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
