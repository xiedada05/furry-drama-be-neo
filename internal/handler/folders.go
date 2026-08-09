package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
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

// Folders 收藏夹域（/api/folders）handler 容器。
type Folders struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewFolders 构造收藏夹域 handler 容器。
func NewFolders(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Folders {
	return &Folders{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /folders 全部路由（不含 /api 前缀）。对齐 folders.js 中间件顺序：
// 除 GET /shared/:shareToken（公开）外全部 protect。收藏夹路由无 per-endpoint 限流。
// 根路径同时注册 "" 与 "/"（Express 路由器同时匹配 /api/folders 与 /api/folders/，
// Gin 默认会对无尾斜杠发 301 重定向，故需显式双注册）。
func (h *Folders) Register(g *gin.RouterGroup) {
	g.GET("", h.AuthMW.Protect(), h.ListFolders)
	g.GET("/", h.AuthMW.Protect(), h.ListFolders)
	g.POST("", h.AuthMW.Protect(), h.CreateFolder)
	g.POST("/", h.AuthMW.Protect(), h.CreateFolder)
	// 静态路径需在 :id 之前注册避免被通配吞掉（Gin 静态优先，此处显式保序对齐 Express）。
	g.PUT("/reorder", h.AuthMW.Protect(), h.ReorderFolders)
	g.POST("/share-unclassified", h.AuthMW.Protect(), h.ShareUnclassified)
	g.PUT("/:id", h.AuthMW.Protect(), h.UpdateFolder)
	g.DELETE("/:id", h.AuthMW.Protect(), h.DeleteFolder)
	g.POST("/:id/items", h.AuthMW.Protect(), h.AddFolderItem)
	g.DELETE("/:id/items/:episodeId", h.AuthMW.Protect(), h.RemoveFolderItem)
	g.POST("/:id/share", h.AuthMW.Protect(), h.ShareFolder)
	g.DELETE("/:id/share", h.AuthMW.Protect(), h.RevokeShare)
	g.GET("/shared/:shareToken", h.SharedFolder)
}

// SavedFolders 收藏夹收藏项域（/api/saved-folders）handler 容器。
type SavedFolders struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewSavedFolders 构造收藏夹收藏项域 handler 容器。
func NewSavedFolders(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *SavedFolders {
	return &SavedFolders{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /saved-folders 全部路由（不含 /api 前缀）。全部 protect。
// 根路径同时注册 "" 与 "/"（对齐 Express 对无尾斜杠路径的匹配）。
func (h *SavedFolders) Register(g *gin.RouterGroup) {
	g.GET("", h.AuthMW.Protect(), h.ListSavedFolders)
	g.GET("/", h.AuthMW.Protect(), h.ListSavedFolders)
	g.POST("", h.AuthMW.Protect(), h.CreateSavedFolder)
	g.POST("/", h.AuthMW.Protect(), h.CreateSavedFolder)
	g.DELETE("/:id", h.AuthMW.Protect(), h.DeleteSavedFolder)
}

// ---- shared helpers ----

// requireObjectID 解析路径 ID 为 ObjectID；非法 hex 写 500 并返回 false。
// 对齐 mongoose 对非法 _id 的 CastError → catch → 500 'Server error'。
func (h *Folders) requireObjectID(c *gin.Context, param string) (primitive.ObjectID, bool) {
	id, err := primitive.ObjectIDFromHex(c.Param(param))
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return primitive.NilObjectID, false
	}
	return id, true
}

// requireObjectID 见 Folders.requireObjectID（SavedFolders 容器同款）。
func (h *SavedFolders) requireObjectID(c *gin.Context, param string) (primitive.ObjectID, bool) {
	id, err := primitive.ObjectIDFromHex(c.Param(param))
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return primitive.NilObjectID, false
	}
	return id, true
}

// newShareToken 生成分享令牌，对齐 crypto.randomBytes(12).toString('hex')。
func newShareToken() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败极罕见；回退用时间戳熵（不影响 1:1 形状）。
		return hex.EncodeToString([]byte(time.Now().Format("150405.0000000000")))
	}
	return hex.EncodeToString(buf)
}

// folderListJSON 组装 GET / 响应：对齐 .populate('userId','username')，
// userId 输出 { _id, username } 对象而非 hex 字符串。
func folderListJSON(f *model.Folder, username string) gin.H {
	return gin.H{
		"_id":         f.ID.Hex(),
		"userId":      gin.H{"_id": f.UserID.Hex(), "username": username},
		"name":        f.Name,
		"type":        f.Type,
		"description": f.Description,
		"sortOrder":   f.SortOrder,
		"shareToken":  f.ShareToken,
		"createdAt":   f.CreatedAt,
	}
}

// ---- folders: GET / ----

// ListFolders GET /api/folders（protect）。
// @Summary 我的收藏夹列表
// @Tags 收藏夹
// @Security bearerAuth
// @Param type query string false "follow|favorite"
// @Success 200 {array} map[string]any
// @Router /folders [get]
func (h *Folders) ListFolders(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	folders, err := h.Repos.Folders.FindUserFolders(c.Request.Context(), user.ID, c.Query("type"))
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	ids := make([]primitive.ObjectID, 0, len(folders))
	for i := range folders {
		ids = append(ids, folders[i].UserID)
	}
	names, _ := h.Repos.Users.FindUsernamesByIDs(c.Request.Context(), ids)
	out := make([]gin.H, 0, len(folders))
	for i := range folders {
		out = append(out, folderListJSON(&folders[i], names[folders[i].UserID]))
	}
	c.JSON(200, out)
}

// CreateFolder POST /api/folders（protect）。
// @Summary 新建收藏夹
// @Tags 收藏夹
// @Security bearerAuth
// @Accept json
// @Param body body object true "name/type"
// @Success 200 {object} model.Folder
// @Failure 400 {object} map[string]string
// @Router /folders [post]
func (h *Folders) CreateFolder(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	_ = c.ShouldBindJSON(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(400, gin.H{"message": "文件夹名称不能为空"})
		return
	}
	if len([]rune(name)) > 50 {
		c.JSON(400, gin.H{"message": "文件夹名称不能超过50个字符"})
		return
	}
	if req.Type != "follow" && req.Type != "favorite" {
		c.JSON(400, gin.H{"message": "无效的文件夹类型"})
		return
	}
	folder := &model.Folder{
		UserID:    user.ID,
		Name:      name,
		Type:      req.Type,
		CreatedAt: time.Now(),
	}
	if err := h.Repos.Folders.Create(c.Request.Context(), folder); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, folder)
}

// ReorderFolders PUT /api/folders/reorder（protect）。
// @Summary 收藏夹排序
// @Tags 收藏夹
// @Security bearerAuth
// @Accept json
// @Param body body object true "folderIds 数组"
// @Success 200 {object} map[string]string "message"
// @Router /folders/reorder [put]
func (h *Folders) ReorderFolders(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		FolderIDs []string `json:"folderIds"`
	}
	_ = c.ShouldBindJSON(&req)
	ids := make([]any, len(req.FolderIDs))
	for i, id := range req.FolderIDs {
		oid, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			// 对齐 bulkWrite 中非法 _id 的 CastError → 500。
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
		// 必须转成 ObjectID：字符串 _id 在 Mongo 查询中不匹配 ObjectID 文档
		// （对齐 mongoose 对 bulkWrite 参数的自动 cast）。
		ids[i] = oid
	}
	if err := h.Repos.Folders.Reorder(c.Request.Context(), user.ID, ids); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "Reordered"})
}

// UpdateFolder PUT /api/folders/:id（protect）。
// @Summary 重命名/修改描述
// @Tags 收藏夹
// @Security bearerAuth
// @Accept json
// @Param id path string true "收藏夹 ID"
// @Param body body object true "name/description（可选）"
// @Success 200 {object} model.Folder
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /folders/{id} [put]
func (h *Folders) UpdateFolder(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, ok := h.requireObjectID(c, "id")
	if !ok {
		return
	}
	folder, err := h.Repos.Folders.FindOwnedByID(c.Request.Context(), id, user.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			c.JSON(400, gin.H{"message": "文件夹名称不能为空"})
			return
		}
		if len([]rune(name)) > 50 {
			c.JSON(400, gin.H{"message": "文件夹名称不能超过50个字符"})
			return
		}
		folder.Name = name
	}
	if req.Description != nil {
		folder.Description = strings.TrimSpace(*req.Description)
	}
	if err := h.Repos.Folders.Save(c.Request.Context(), folder); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, folder)
}

// DeleteFolder DELETE /api/folders/:id（protect）。把条目 folderId 置空后删除收藏夹。
// @Summary 删除收藏夹
// @Tags 收藏夹
// @Security bearerAuth
// @Param id path string true "收藏夹 ID"
// @Success 200 {object} map[string]string "message"
// @Failure 404 {object} map[string]string
// @Router /folders/{id} [delete]
func (h *Folders) DeleteFolder(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, ok := h.requireObjectID(c, "id")
	if !ok {
		return
	}
	folder, err := h.Repos.Folders.FindOwnedByID(c.Request.Context(), id, user.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	var clearErr error
	if folder.Type == "follow" {
		clearErr = h.Repos.Follows.FolderClearByFolderID(c.Request.Context(), id)
	} else {
		clearErr = h.Repos.Favorites.FolderClearByFolderID(c.Request.Context(), id)
	}
	if clearErr != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	if err := h.Repos.Folders.DeleteByID(c.Request.Context(), id); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "Folder deleted"})
}

// AddFolderItem POST /api/folders/:id/items（protect）。把条目归入收藏夹。
// @Summary 添加条目到收藏夹
// @Tags 收藏夹
// @Security bearerAuth
// @Accept json
// @Param id path string true "收藏夹 ID"
// @Param body body object true "episodeId"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]string
// @Router /folders/{id}/items [post]
func (h *Folders) AddFolderItem(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, ok := h.requireObjectID(c, "id")
	if !ok {
		return
	}
	folder, err := h.Repos.Folders.FindOwnedByID(c.Request.Context(), id, user.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	var req struct {
		EpisodeID string `json:"episodeId"`
	}
	_ = c.ShouldBindJSON(&req)
	episodeID, err := primitive.ObjectIDFromHex(req.EpisodeID)
	if err != nil {
		// 对齐 findOne({ episodeId }) 的 CastError → 500。
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	if folder.Type == "follow" {
		item, err := h.Repos.Follows.FolderFindByUserEpisode(c.Request.Context(), user.ID, episodeID)
		if err != nil {
			if repository.IsNotFound(err) {
				c.JSON(404, gin.H{"message": "Item not found"})
				return
			}
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
		item.FolderID = &folder.ID
		if err := h.Repos.Follows.FolderSave(c.Request.Context(), item); err != nil {
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
		c.JSON(200, item)
		return
	}
	item, err := h.Repos.Favorites.FolderFindByUserEpisode(c.Request.Context(), user.ID, episodeID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Item not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	item.FolderID = &folder.ID
	if err := h.Repos.Favorites.FolderSave(c.Request.Context(), item); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, item)
}

// RemoveFolderItem DELETE /api/folders/:id/items/:episodeId（protect）。
// @Summary 从收藏夹移除条目
// @Tags 收藏夹
// @Security bearerAuth
// @Param id path string true "收藏夹 ID"
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]string "message"
// @Failure 404 {object} map[string]string
// @Router /folders/{id}/items/{episodeId} [delete]
func (h *Folders) RemoveFolderItem(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, ok := h.requireObjectID(c, "id")
	if !ok {
		return
	}
	folder, err := h.Repos.Folders.FindOwnedByID(c.Request.Context(), id, user.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	episodeID, ok := h.requireObjectID(c, "episodeId")
	if !ok {
		return
	}
	var rmErr error
	if folder.Type == "follow" {
		rmErr = h.Repos.Follows.FolderRemoveItem(c.Request.Context(), user.ID, episodeID, id)
	} else {
		rmErr = h.Repos.Favorites.FolderRemoveItem(c.Request.Context(), user.ID, episodeID, id)
	}
	if rmErr != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "Item removed from folder"})
}

// ShareUnclassified POST /api/folders/share-unclassified（protect）。
// 分享「默认收藏夹」（name='__unclassified__' 的虚拟收藏夹），幂等：已存在则复用。
// @Summary 分享默认收藏夹
// @Tags 收藏夹
// @Security bearerAuth
// @Success 200 {object} map[string]string "shareToken"
// @Router /folders/share-unclassified [post]
func (h *Folders) ShareUnclassified(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	virtualFolder, err := h.Repos.Folders.FindUnclassified(c.Request.Context(), user.ID)
	switch {
	case err == nil:
		// 已存在
	case repository.IsNotFound(err):
		virtualFolder = &model.Folder{
			UserID:    user.ID,
			Name:      "__unclassified__",
			Type:      "favorite",
			SortOrder: -1,
			CreatedAt: time.Now(),
		}
		if err := h.Repos.Folders.Create(c.Request.Context(), virtualFolder); err != nil {
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
	default:
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	if virtualFolder.ShareToken == nil {
		token := newShareToken()
		virtualFolder.ShareToken = &token
		if err := h.Repos.Folders.Save(c.Request.Context(), virtualFolder); err != nil {
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
	}
	c.JSON(200, gin.H{"shareToken": *virtualFolder.ShareToken})
}

// ShareFolder POST /api/folders/:id/share（protect）。幂等生成 shareToken。
// @Summary 分享收藏夹
// @Tags 收藏夹
// @Security bearerAuth
// @Param id path string true "收藏夹 ID"
// @Success 200 {object} map[string]string "shareToken"
// @Failure 404 {object} map[string]string
// @Router /folders/{id}/share [post]
func (h *Folders) ShareFolder(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, ok := h.requireObjectID(c, "id")
	if !ok {
		return
	}
	folder, err := h.Repos.Folders.FindOwnedByID(c.Request.Context(), id, user.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	if folder.ShareToken == nil {
		token := newShareToken()
		folder.ShareToken = &token
		if err := h.Repos.Folders.Save(c.Request.Context(), folder); err != nil {
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
	}
	c.JSON(200, gin.H{"shareToken": *folder.ShareToken})
}

// RevokeShare DELETE /api/folders/:id/share（protect）。清除 shareToken。
// @Summary 取消分享收藏夹
// @Tags 收藏夹
// @Security bearerAuth
// @Param id path string true "收藏夹 ID"
// @Success 200 {object} map[string]string "message"
// @Failure 404 {object} map[string]string
// @Router /folders/{id}/share [delete]
func (h *Folders) RevokeShare(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, ok := h.requireObjectID(c, "id")
	if !ok {
		return
	}
	folder, err := h.Repos.Folders.FindOwnedByID(c.Request.Context(), id, user.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	folder.ShareToken = nil
	if err := h.Repos.Folders.Save(c.Request.Context(), folder); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "Share revoked"})
}

// SharedFolder GET /api/folders/shared/:shareToken（公开，无鉴权）。
// 返回收藏夹与剧集条目；过滤 pending/rejected 剧集防泄露。
// @Summary 公开访问分享收藏夹
// @Tags 收藏夹
// @Param shareToken path string true "分享令牌"
// @Success 200 {object} map[string]any "name/description/type/count/episodes/createdAt/creatorId/creatorName"
// @Failure 404 {object} map[string]string
// @Router /folders/shared/{shareToken} [get]
func (h *Folders) SharedFolder(c *gin.Context) {
	folder, err := h.Repos.Folders.FindByShareToken(c.Request.Context(), c.Param("shareToken"))
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Shared folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	isUnclassified := folder.Name == "__unclassified__"
	var filter bson.M
	if isUnclassified {
		filter = bson.M{"userId": folder.UserID, "folderId": nil}
	} else {
		filter = bson.M{"folderId": folder.ID}
	}

	// 按类型取条目，保持 createdAt 倒序；再按条目顺序组装剧集数组。
	ctx := c.Request.Context()
	var episodeIDs []primitive.ObjectID
	var itemCount int
	if folder.Type == "follow" {
		items, err := h.Repos.Follows.FolderFindShared(ctx, filter)
		if err != nil {
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
		itemCount = len(items)
		episodeIDs = make([]primitive.ObjectID, 0, itemCount)
		for i := range items {
			episodeIDs = append(episodeIDs, items[i].EpisodeID)
		}
	} else {
		items, err := h.Repos.Favorites.FolderFindShared(ctx, filter)
		if err != nil {
			c.JSON(500, gin.H{"message": "Server error"})
			return
		}
		itemCount = len(items)
		episodeIDs = make([]primitive.ObjectID, 0, itemCount)
		for i := range items {
			episodeIDs = append(episodeIDs, items[i].EpisodeID)
		}
	}
	episodes, err := h.buildSharedEpisodes(ctx, episodeIDs, itemCount)
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}

	creatorName := "Unknown"
	if creator, err := h.Repos.Users.FindByID(ctx, folder.UserID); err == nil {
		creatorName = creator.Username
	}
	name := folder.Name
	description := folder.Description
	if isUnclassified {
		name = "默认收藏夹"
		description = ""
	}
	c.JSON(200, gin.H{
		"name":        name,
		"description": description,
		"type":        folder.Type,
		"count":       len(episodes),
		"episodes":    episodes,
		"createdAt":   folder.CreatedAt,
		"creatorId":   folder.UserID.Hex(),
		"creatorName": creatorName,
	})
}

// buildSharedEpisodes 按条目顺序（createdAt 倒序）组装剧集数组，过滤已删除 /
// pending / rejected 的剧集（对齐 folders.js 的 filter + map）。
func (h *Folders) buildSharedEpisodes(ctx context.Context, episodeIDs []primitive.ObjectID, cap int) ([]model.FolderItem, error) {
	items, err := h.Repos.Episodes.FindFolderItemsByIDs(ctx, episodeIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[primitive.ObjectID]model.FolderItem, len(items))
	for i := range items {
		byID[items[i].ID] = items[i]
	}
	out := make([]model.FolderItem, 0, cap)
	for _, id := range episodeIDs {
		ep, ok := byID[id]
		if !ok {
			continue
		}
		if ep.ReviewStatus != "" && ep.ReviewStatus != "approved" {
			continue
		}
		out = append(out, ep)
	}
	return out, nil
}

// ---- saved-folders ----

// ListSavedFolders GET /api/saved-folders（protect）。
// @Summary 我收藏的他人收藏夹
// @Tags 收藏夹收藏项
// @Security bearerAuth
// @Success 200 {array} model.SavedFolder
// @Router /saved-folders [get]
func (h *SavedFolders) ListSavedFolders(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	list, err := h.Repos.SavedFolders.FindByUser(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, list)
}

// CreateSavedFolder POST /api/saved-folders（protect）。
// @Summary 收藏他人收藏夹
// @Tags 收藏夹收藏项
// @Security bearerAuth
// @Accept json
// @Param body body object true "shareToken/creatorName"
// @Success 200 {object} model.SavedFolder
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Router /saved-folders [post]
func (h *SavedFolders) CreateSavedFolder(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		ShareToken  string `json:"shareToken"`
		CreatorName string `json:"creatorName"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.ShareToken == "" {
		c.JSON(400, gin.H{"message": "shareToken is required"})
		return
	}
	folder, err := h.Repos.Folders.FindByShareToken(c.Request.Context(), req.ShareToken)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	if folder.UserID == user.ID {
		c.JSON(400, gin.H{"message": "不能收藏自己的收藏夹"})
		return
	}
	existing, err := h.Repos.SavedFolders.FindByUserAndShareToken(c.Request.Context(), user.ID, req.ShareToken)
	if err != nil && !repository.IsNotFound(err) {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	if existing != nil {
		c.JSON(400, gin.H{"message": "已收藏过该收藏夹"})
		return
	}
	isUnclassified := folder.Name == "__unclassified__"
	creatorName := req.CreatorName
	if creatorName == "" {
		creatorName = "Unknown"
	}
	description := folder.Description
	if isUnclassified {
		description = ""
	}
	sf := &model.SavedFolder{
		UserID:      user.ID,
		ShareToken:  req.ShareToken,
		FolderName:  folder.Name,
		CreatorID:   folder.UserID,
		CreatorName: creatorName,
		Description: description,
		FolderType:  folder.Type,
		CreatedAt:   time.Now(),
	}
	if isUnclassified {
		sf.FolderName = "默认收藏夹"
	}
	if err := h.Repos.SavedFolders.Create(c.Request.Context(), sf); err != nil {
		// 并发重复收藏：唯一键冲突 → Express create 抛 E11000 → 500。
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, sf)
}

// DeleteSavedFolder DELETE /api/saved-folders/:id（protect）。
// @Summary 取消收藏他人收藏夹
// @Tags 收藏夹收藏项
// @Security bearerAuth
// @Param id path string true "收藏项 ID"
// @Success 200 {object} map[string]string "message"
// @Failure 404 {object} map[string]string
// @Router /saved-folders/{id} [delete]
func (h *SavedFolders) DeleteSavedFolder(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	id, ok := h.requireObjectID(c, "id")
	if !ok {
		return
	}
	if _, err := h.Repos.SavedFolders.FindOwnedByID(c.Request.Context(), id, user.ID); err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Saved folder not found"})
			return
		}
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	if err := h.Repos.SavedFolders.DeleteByID(c.Request.Context(), id); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "Removed"})
}
