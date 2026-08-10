package handler

import (
	"context"
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

// 本文件实现 /api/creator-profile 子域（行为逐分支照抄 backend/routes/creatorProfiles.js）。
//
// Express 只有 4 个端点（GET/PUT /my-profile 为 creatorProtect，即
// creator/admin/superadmin；GET /public/:id 与 GET /by-creator/:creatorId 公开）。

// CreatorProfiles 是 /api/creator-profile 域 handler 容器。
type CreatorProfiles struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewCreatorProfiles 构造创作者主页 handler 容器。
func NewCreatorProfiles(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *CreatorProfiles {
	return &CreatorProfiles{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/creator-profile 全部端点（不含 /api 前缀；路径对齐
// creatorProfiles.js 子路径）。
func (h *CreatorProfiles) Register(g *gin.RouterGroup) {
	creatorRoles := []string{middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin}
	g.GET("/my-profile", h.AuthMW.Protect(creatorRoles...), h.MyProfile)
	g.PUT("/my-profile", h.AuthMW.Protect(creatorRoles...), h.UpdateMyProfile)
	g.GET("/public/:id", h.PublicProfile)
	g.GET("/by-creator/:creatorId", h.ByCreator)
}

// MyProfile GET /api/creator-profile/my-profile（creatorProtect）。
// 返回创作者自己的主页（含待审核修改）；首次访问自动创建默认资料。
// @Summary 获取我的创作者主页
// @Tags 创作者主页
// @Security bearerAuth
// @Success 200 {object} map[string]any "创作者主页文档"
// @Router /creator-profile/my-profile [get]
func (h *CreatorProfiles) MyProfile(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	ctx := c.Request.Context()
	profile, err := h.Repos.CreatorProfiles.FindByCreator(ctx, user.ID)
	if repository.IsNotFound(err) {
		displayName := user.Username
		if displayName == "" {
			displayName = "创作者"
		}
		profile = &model.CreatorProfile{
			CreatorID:   user.ID,
			DisplayName: displayName,
			Bio:         "这位创作者还没有填写个人简介。",
			SocialLinks: map[string]string{},
		}
		if err := h.Repos.CreatorProfiles.Create(ctx, profile); err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	} else if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, creatorProfileJSON(profile))
}

// UpdateMyProfile PUT /api/creator-profile/my-profile（creatorProtect）。
// 把修改暂存为 pendingChanges 并置 reviewStatus=pending，正式字段不变。
// @Summary 更新我的创作者主页（暂存待审核）
// @Tags 创作者主页
// @Security bearerAuth
// @Accept json
// @Param body body object true "displayName/avatar/bio/socialLinks/qqGroupLink"
// @Success 200 {object} map[string]any "创作者主页文档"
// @Router /creator-profile/my-profile [put]
func (h *CreatorProfiles) UpdateMyProfile(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	body := h.profileBody(c)
	bio := asString(body["bio"])
	if runes := []rune(bio); len(runes) > 500 {
		bio = string(runes[:500])
	}
	pending := primitive.M{
		"displayName": asString(body["displayName"]),
		"avatar":      asString(body["avatar"]),
		"bio":         bio,
		"socialLinks": toStrMap(body["socialLinks"]),
		"qqGroupLink": orDefaultString(body["qqGroupLink"], ""),
	}
	ctx := c.Request.Context()
	profile, err := h.Repos.CreatorProfiles.CreatorProfilesUpsertPending(ctx, user.ID,
		pending, time.Now().UTC().Truncate(time.Millisecond))
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	// 若是新创建的文档缺少正式 displayName，先用 pendingChanges 兜底初始化。
	if profile.DisplayName == "" {
		profile.DisplayName = orDefaultString(pending["displayName"], "创作者")
		if err := h.Repos.CreatorProfiles.CreatorProfilesSave(ctx, profile); err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
	}
	c.JSON(200, creatorProfileJSON(profile))
}

// PublicProfile GET /api/creator-profile/public/:id（公开）。
// 只返回已审核的正式字段（剥离 pendingChanges/reviewNote/reviewStatus），
// 并附带该创作者的已审核剧集列表。
// @Summary 公开创作者主页
// @Tags 创作者主页
// @Param id path string true "创作者主页 ID"
// @Success 200 {object} map[string]any "profile + episodes"
// @Failure 404 {object} map[string]string "Profile not found"
// @Router /creator-profile/public/{id} [get]
func (h *CreatorProfiles) PublicProfile(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		// 对齐 Express 非法 ID → CastError → catch → 500。
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	ctx := c.Request.Context()
	profile, err := h.Repos.CreatorProfiles.CreatorProfilesFindByID(ctx, oid)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "Profile not found"})
		return
	}
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	episodes, err := h.creatorEpisodes(ctx, profile.CreatorID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"profile": publicCreatorProfileJSON(profile), "episodes": episodes})
}

// ByCreator GET /api/creator-profile/by-creator/:creatorId（公开）。
// 按创作者用户 ID 查询主页（曾用路径 /by-admin/:adminId，已重命名）。
// @Summary 按创作者用户 ID 查询公开主页
// @Tags 创作者主页
// @Param creatorId path string true "创作者用户 ID"
// @Success 200 {object} map[string]any "profile + episodes"
// @Failure 404 {object} map[string]string "Profile not found"
// @Router /creator-profile/by-creator/{creatorId} [get]
func (h *CreatorProfiles) ByCreator(c *gin.Context) {
	creatorID, err := primitive.ObjectIDFromHex(c.Param("creatorId"))
	if err != nil {
		// 对齐 Express findOne({creatorId}) 非法 ID → CastError → catch → 500。
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	ctx := c.Request.Context()
	profile, err := h.Repos.CreatorProfiles.FindByCreator(ctx, creatorID)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "Profile not found"})
		return
	}
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	episodes, err := h.creatorEpisodes(ctx, profile.CreatorID)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"profile": publicCreatorProfileJSON(profile), "episodes": episodes})
}

// ---- helpers ----

// profileBody 读取（已过 SanitizeInput 的）JSON 请求体为 map；非对象/空体返回空 map。
func (h *CreatorProfiles) profileBody(c *gin.Context) map[string]any {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		return map[string]any{}
	}
	return body
}

// creatorEpisodes 查询某创作者的全部已审核剧集（对齐 creatorProfiles.js 的
// Episode.find({$or:[{createdBy, hideCreator:{$ne:true}},{allowedEditors},
// {customAuthors}], reviewStatus:'approved'}).sort({createdAt:-1})）。
func (h *CreatorProfiles) creatorEpisodes(ctx context.Context, creatorID primitive.ObjectID) ([]gin.H, error) {
	filter := bson.M{
		"$or": bson.A{
			bson.M{"createdBy": creatorID, "hideCreator": bson.M{"$ne": true}},
			bson.M{"allowedEditors": creatorID},
			bson.M{"customAuthors": creatorID},
		},
		"reviewStatus": "approved",
	}
	eps, err := h.Repos.Episodes.FindList(ctx, filter, bson.D{{Key: "createdAt", Value: -1}}, 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]gin.H, 0, len(eps))
	for i := range eps {
		out = append(out, episodeDocJSON(&eps[i]))
	}
	return out, nil
}

// creatorProfileJSON 组装创作者主页完整文档（创作者视角，含待审核字段）。
// 对齐 mongoose toObject：缺失字段回填 schema 默认值（avatar/qqGroupLink ''、
// reviewStatus 'approved'、pendingChanges 嵌套默认对象、__v 0）。
func creatorProfileJSON(p *model.CreatorProfile) gin.H {
	social := p.SocialLinks
	if social == nil {
		social = map[string]string{}
	}
	reviewStatus := p.ReviewStatus
	if reviewStatus == "" {
		reviewStatus = "approved"
	}
	pending := p.PendingChanges
	if pending == nil {
		pending = primitive.M{
			"displayName": "",
			"avatar":      "",
			"bio":         "",
			"socialLinks": primitive.M{},
			"qqGroupLink": "",
		}
	}
	return gin.H{
		"_id":           p.ID.Hex(),
		"creatorId":     p.CreatorID.Hex(),
		"displayName":   p.DisplayName,
		"avatar":        p.Avatar,
		"bio":           p.Bio,
		"socialLinks":   social,
		"qqGroupLink":   p.QqGroupLink,
		"reviewStatus":  reviewStatus,
		"reviewNote":    p.ReviewNote,
		"pendingChanges": pending,
		"createdAt":     p.CreatedAt,
		"updatedAt":     p.UpdatedAt,
		"__v":           p.VersionKey,
	}
}

// publicCreatorProfileJSON 组装公开创作者主页（剥离 pendingChanges/reviewNote/
// reviewStatus，避免泄露待审核字段；对齐 toPublicProfile）。
func publicCreatorProfileJSON(p *model.CreatorProfile) gin.H {
	social := p.SocialLinks
	if social == nil {
		social = map[string]string{}
	}
	return gin.H{
		"_id":         p.ID.Hex(),
		"creatorId":   p.CreatorID.Hex(),
		"displayName": p.DisplayName,
		"avatar":      p.Avatar,
		"bio":         p.Bio,
		"socialLinks": social,
		"qqGroupLink": p.QqGroupLink,
		"createdAt":   p.CreatedAt,
		"updatedAt":   p.UpdatedAt,
		"__v":         p.VersionKey,
	}
}

// episodeDocJSON 组装完整剧集文档（未 populate 场景，ref 字段输出 hex 字符串）。
// 字段集与 episodes.go episodeJSON(users=nil) 一致，供 creator / creator-profile
// 等未 populate 的列表/详情复用。
func episodeDocJSON(e *model.Episode) gin.H {
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
