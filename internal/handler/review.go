package handler

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/pagination"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/review 子域（行为逐分支照抄 backend/routes/review.js）。
// 6 个端点：GET /pending、GET /all、PUT /approve/:id、PUT /reject/:id、
// PUT /assign-editor/:episodeId、PUT /remove-editor/:episodeId。
// 角色：Express 为 adminProtect（creator/admin/superadmin）+ adminOnly 内联校验
// （仅 admin/superadmin），故最终仅 admin/superadmin 可访问，creator 命中
// adminOnly 的 403 需要管理员权限。

// Review 是 /api/review 域 handler 容器。
type Review struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	Mail   *email.Client

	// emailLog 集数变更邮件去重（对齐 utils/episodeNotify.js shouldSendEpisodeEmail 1h）。
	emailMu  sync.Mutex
	emailLog map[string]time.Time
}

// NewReview 构造审核 handler 容器。mail 为邮件客户端（可为 nil，跳过发信）。
func NewReview(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc, mail *email.Client) *Review {
	return &Review{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, Mail: mail, emailLog: map[string]time.Time{}}
}

// Register 挂载 /api/review 全部端点（不含 /api 前缀；路径对齐 review.js 子路径）。
func (h *Review) Register(g *gin.RouterGroup) {
	protect := h.AuthMW.Protect(middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin)
	g.GET("/pending", protect, adminOnlyReview, h.Pending)
	g.GET("/all", protect, adminOnlyReview, h.All)
	g.PUT("/approve/:id", protect, adminOnlyReview, h.Approve)
	g.PUT("/reject/:id", protect, adminOnlyReview, h.Reject)
	g.PUT("/assign-editor/:episodeId", protect, adminOnlyReview, h.AssignEditor)
	g.PUT("/remove-editor/:episodeId", protect, adminOnlyReview, h.RemoveEditor)
}

// adminOnlyReview 对齐 review.js 内联 adminOnly：非 admin/superadmin → 403 需要管理员权限。
func adminOnlyReview(c *gin.Context) {
	user, ok := middleware.GetUser(c)
	if ok && (user.Role == middleware.RoleAdmin || user.Role == middleware.RoleSuperAdmin) {
		c.Next()
		return
	}
	c.AbortWithStatusJSON(403, gin.H{"message": "需要管理员权限"})
}

// reviewPage 解析审核列表分页参数（对齐 review.js 内联逻辑）：
// page 默认 1（不钳制最小，负值由 mongo skip 拒绝 → 500）；limit 默认 20，上限 100。
func reviewPage(c *gin.Context) (pageNum, limitNum int) {
	pageNum = 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		pageNum = p
	}
	limitNum = 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limitNum = l
	}
	if limitNum > 100 {
		limitNum = 100
	}
	return pageNum, limitNum
}

// Pending GET /api/review/pending（adminProtect + adminOnly）。
// @Summary 待审核剧集列表（pending 剧集 + 已审核剧集的待审核修改）
// @Tags 审核
// @Security bearerAuth
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 20，上限 100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /review/pending [get]
func (h *Review) Pending(c *gin.Context) {
	pageNum, limitNum := reviewPage(c)
	ctx := c.Request.Context()
	query := bson.M{"$or": bson.A{bson.M{"reviewStatus": "pending"}, bson.M{"hasPendingChanges": true}}}
	total, err := h.Repos.Episodes.CountDocuments(ctx, query)
	if err != nil {
		serverError(c)
		return
	}
	episodes, err := h.Repos.Episodes.FindList(ctx, query,
		bson.D{{Key: "updatedAt", Value: -1}}, int64((pageNum-1)*limitNum), int64(limitNum))
	if err != nil {
		serverError(c)
		return
	}
	refs, err := h.fetchReviewRefs(ctx, episodes)
	if err != nil {
		serverError(c)
		return
	}
	opt := reviewRefOpt{populateCreatedBy: true, createdByEmail: true, populateCustomAuthors: true, customAuthorsEmail: false}
	list := make([]gin.H, 0, len(episodes))
	for i := range episodes {
		list = append(list, h.reviewEpisodeJSON(&episodes[i], refs, opt))
	}
	c.JSON(200, reviewPagedResult(list, pageNum, limitNum, total))
}

// All GET /api/review/all（adminProtect + adminOnly）。
// @Summary 全部剧集列表（审核用）
// @Tags 审核
// @Security bearerAuth
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 20，上限 100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /review/all [get]
func (h *Review) All(c *gin.Context) {
	pageNum, limitNum := reviewPage(c)
	ctx := c.Request.Context()
	total, err := h.Repos.Episodes.CountDocuments(ctx, bson.M{})
	if err != nil {
		serverError(c)
		return
	}
	episodes, err := h.Repos.Episodes.FindList(ctx, bson.M{},
		bson.D{{Key: "updatedAt", Value: -1}}, int64((pageNum-1)*limitNum), int64(limitNum))
	if err != nil {
		serverError(c)
		return
	}
	refs, err := h.fetchReviewRefs(ctx, episodes)
	if err != nil {
		serverError(c)
		return
	}
	opt := reviewRefOpt{populateCreatedBy: true, createdByEmail: true, populateAllowedEditors: true, allowedEditorsEmail: true}
	list := make([]gin.H, 0, len(episodes))
	for i := range episodes {
		list = append(list, h.reviewEpisodeJSON(&episodes[i], refs, opt))
	}
	c.JSON(200, reviewPagedResult(list, pageNum, limitNum, total))
}

// Approve PUT /api/review/approve/:id（adminProtect + adminOnly）。
// @Summary 审核通过剧集（含应用待审核修改）
// @Tags 审核
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Param body body object true "note"
// @Success 200 {object} map[string]any "剧集对象"
// @Failure 404 {object} map[string]string "Episode not found"
// @Router /review/approve/{id} [put]
func (h *Review) Approve(c *gin.Context) {
	admin, _ := middleware.GetUser(c)
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
	note := h.readReviewNote(c)

	// 已审核剧集的待审核修改：将 pendingChanges 应用到正式字段。
	if episode.HasPendingChanges && episode.PendingChanges != nil {
		oldCurrentEpisodes := episode.CurrentEpisodes
		applyPendingChanges(episode, episode.PendingChanges)
		episode.PendingChanges = nil
		episode.HasPendingChanges = false
		episode.PendingChangeSummary = ""
		episode.ReviewNote = note
		episode.ReviewedBy = &admin.ID
		now := time.Now().UTC().Truncate(time.Millisecond)
		episode.ReviewedAt = &now
		episode.UpdatedAt = now
		if err := h.Repos.Episodes.Save(ctx, episode); err != nil {
			serverError(c)
			return
		}
		// 集数增加时通知追番用户（审核通过时触发，而非创作者提交时）。
		if episode.CurrentEpisodes > oldCurrentEpisodes {
			if err := h.notifyEpisodeNumberChange(ctx, oid, oldCurrentEpisodes, episode.CurrentEpisodes, episode); err != nil {
				serverError(c)
				return
			}
		}
		middleware.EpisodeCache.Delete("episode_" + idStr)
		middleware.EpisodeCache.DeleteByPrefix("episodes_")
		h.notifyCreatorReviewResult(ctx, episode, "approved", note)
		c.JSON(200, h.reviewEpisodeJSON(episode, nil, reviewRefOpt{}))
		return
	}

	// 新提交的 pending 剧集：审核通过后正式上线。
	updated, err := h.Repos.Episodes.FindOneAndUpdate(ctx, oid, bson.M{"$set": bson.M{
		"reviewStatus": "approved",
		"reviewNote":   note,
		"reviewedBy":   admin.ID,
		"reviewedAt":   time.Now().UTC().Truncate(time.Millisecond),
	}})
	if err != nil {
		serverError(c)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + idStr)
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	h.notifyCreatorReviewResult(ctx, updated, "approved", note)
	c.JSON(200, h.reviewEpisodeJSON(updated, nil, reviewRefOpt{}))
}

// Reject PUT /api/review/reject/:id（adminProtect + adminOnly）。
// @Summary 审核拒绝剧集（含清除待审核修改）
// @Tags 审核
// @Security bearerAuth
// @Param id path string true "剧集 ID"
// @Param body body object true "note"
// @Success 200 {object} map[string]any "剧集对象"
// @Failure 404 {object} map[string]string "Episode not found"
// @Router /review/reject/{id} [put]
func (h *Review) Reject(c *gin.Context) {
	admin, _ := middleware.GetUser(c)
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
	note := h.readReviewNote(c)

	// 已审核剧集的待审核修改被拒绝：清除 pendingChanges，原内容保持不变。
	if episode.HasPendingChanges {
		episode.PendingChanges = nil
		episode.HasPendingChanges = false
		episode.PendingChangeSummary = ""
		episode.ReviewNote = note
		episode.ReviewedBy = &admin.ID
		now := time.Now().UTC().Truncate(time.Millisecond)
		episode.ReviewedAt = &now
		if err := h.Repos.Episodes.Save(ctx, episode); err != nil {
			serverError(c)
			return
		}
		middleware.EpisodeCache.Delete("episode_" + idStr)
		middleware.EpisodeCache.DeleteByPrefix("episodes_")
		h.notifyCreatorReviewResult(ctx, episode, "rejected", note)
		c.JSON(200, h.reviewEpisodeJSON(episode, nil, reviewRefOpt{}))
		return
	}

	// 新提交的 pending 剧集被拒绝：标记审核结果后整体移入回收站，
	// 不再保留未通过的剧集页面（前台/个人列表即刻不可见）；
	// 管理员可在后台回收站查看日志、恢复或彻底删除。
	updated, err := h.Repos.Episodes.FindOneAndUpdate(ctx, oid, bson.M{"$set": bson.M{
		"reviewStatus": "rejected",
		"reviewNote":   note,
		"reviewedBy":   admin.ID,
		"reviewedAt":   time.Now().UTC().Truncate(time.Millisecond),
	}})
	if err != nil {
		serverError(c)
		return
	}
	if err := h.Repos.EpisodeTrash.MoveToTrash(ctx, updated, "rejected", note, &admin.ID); err != nil {
		serverError(c)
		return
	}
	middleware.EpisodeCache.Delete("episode_" + idStr)
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	h.notifyCreatorReviewResult(ctx, updated, "rejected", note)
	c.JSON(200, h.reviewEpisodeJSON(updated, nil, reviewRefOpt{}))
}

// AssignEditor PUT /api/review/assign-editor/:episodeId（adminProtect + adminOnly）。
// @Summary 分配剧集编辑者
// @Tags 审核
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Param body body object true "editorId"
// @Success 200 {object} map[string]any "剧集对象（createdBy/allowedEditors 已 populate）"
// @Failure 404 {object} map[string]string "Editor not found / Episode not found"
// @Router /review/assign-editor/{episodeId} [put]
func (h *Review) AssignEditor(c *gin.Context) {
	episodeIdStr := c.Param("episodeId")
	var req struct {
		EditorID string `json:"editorId"`
	}
	_ = c.ShouldBindJSON(&req)
	editorOID, err := primitive.ObjectIDFromHex(req.EditorID)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	if _, err := h.Repos.Users.FindByID(ctx, editorOID); err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Editor not found"})
			return
		}
		serverError(c)
		return
	}
	episodeOID, err := primitive.ObjectIDFromHex(episodeIdStr)
	if err != nil {
		serverError(c)
		return
	}
	episode, err := h.Repos.Episodes.FindByID(ctx, episodeOID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	if episode.AllowedEditors == nil {
		episode.AllowedEditors = []primitive.ObjectID{}
	}
	already := false
	for _, ed := range episode.AllowedEditors {
		if ed == editorOID {
			already = true
			break
		}
	}
	if !already {
		episode.AllowedEditors = append(episode.AllowedEditors, editorOID)
		if err := h.Repos.Episodes.Save(ctx, episode); err != nil {
			serverError(c)
			return
		}
	}
	middleware.EpisodeCache.Delete("episode_" + episodeIdStr)
	h.reviewRefsResponse(c, episodeOID, reviewRefOpt{populateCreatedBy: true, populateAllowedEditors: true})
}

// RemoveEditor PUT /api/review/remove-editor/:episodeId（adminProtect + adminOnly）。
// @Summary 移除剧集编辑者
// @Tags 审核
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Param body body object true "editorId"
// @Success 200 {object} map[string]any "剧集对象（createdBy/allowedEditors 已 populate）"
// @Failure 404 {object} map[string]string "Episode not found"
// @Router /review/remove-editor/{episodeId} [put]
func (h *Review) RemoveEditor(c *gin.Context) {
	episodeIdStr := c.Param("episodeId")
	var req struct {
		EditorID string `json:"editorId"`
	}
	_ = c.ShouldBindJSON(&req)
	editorOID, err := primitive.ObjectIDFromHex(req.EditorID)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	episodeOID, err := primitive.ObjectIDFromHex(episodeIdStr)
	if err != nil {
		serverError(c)
		return
	}
	episode, err := h.Repos.Episodes.FindByID(ctx, episodeOID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Episode not found"})
			return
		}
		serverError(c)
		return
	}
	if episode.AllowedEditors != nil {
		filtered := make([]primitive.ObjectID, 0, len(episode.AllowedEditors))
		for _, ed := range episode.AllowedEditors {
			if ed != editorOID {
				filtered = append(filtered, ed)
			}
		}
		episode.AllowedEditors = filtered
		if err := h.Repos.Episodes.Save(ctx, episode); err != nil {
			serverError(c)
			return
		}
	}
	// 对齐 review.js：remove-editor 不清理剧集缓存。
	h.reviewRefsResponse(c, episodeOID, reviewRefOpt{populateCreatedBy: true, createdByEmail: true, populateAllowedEditors: true, allowedEditorsEmail: true})
}

// reviewRefsResponse 重新查询剧集并 populate createdBy/allowedEditors 后输出。
func (h *Review) reviewRefsResponse(c *gin.Context, episodeOID primitive.ObjectID, opt reviewRefOpt) {
	updated, err := h.Repos.Episodes.FindByID(c.Request.Context(), episodeOID)
	if err != nil {
		serverError(c)
		return
	}
	refs, err := h.Repos.Users.ReviewFindUserRefsByIDs(c.Request.Context(), dedupIDs(episodeRefIDs(updated)))
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, h.reviewEpisodeJSON(updated, refs, opt))
}

// ---- 通知 / 推送 / 邮件 ----

// notifyCreatorReviewResult 审核结果通知创作者（站内通知 + Push + 邮件；
// fire-and-forget，对齐 review.js 各步 .catch(()=>{})）。
func (h *Review) notifyCreatorReviewResult(ctx context.Context, episode *model.Episode, status, note string) {
	if episode.CreatedBy == nil {
		return
	}
	isApproved := status == "approved"
	title := episode.Title
	message := fmt.Sprintf("您的剧集《%s》已通过审核", title)
	if !isApproved {
		message = fmt.Sprintf("您的剧集《%s》未通过审核", title)
		if note != "" {
			message += "：" + note
		}
	}
	episodeID := episode.ID
	_ = h.Repos.Notifications.Create(ctx, &model.Notification{
		UserID:         *episode.CreatedBy,
		EpisodeID:      &episodeID,
		EpisodeTitle:   title,
		EpisodeTitleEn: episode.TitleEn,
		Type:           "review_result",
		Message:        message,
		Link:           "/admin/episodes",
		Metadata:       primitive.M{"episodeId": episode.ID, "status": status, "note": note},
		CreatedAt:      time.Now().UTC().Truncate(time.Millisecond),
	})
	pushTitle := "剧集未通过审核"
	pushBody := fmt.Sprintf("《%s》未通过审核", title)
	if note != "" {
		pushBody += "：" + note
	}
	if isApproved {
		pushTitle = "剧集通过审核"
		pushBody = fmt.Sprintf("《%s》已通过审核，现已上线", title)
	}
	h.sendPushToUser([]primitive.ObjectID{*episode.CreatedBy}, pushTitle, pushBody, "/admin/episodes")
	h.sendReviewResultEmail(ctx, *episode.CreatedBy, title, status, note)
}

// notifyEpisodeNumberChange 在审核通过使 currentEpisodes 增加时，为每个新增集数
// 给全部追番用户写通知（对齐 utils/episodeNotify.js notifyEpisodeNumberChange）。
// 站内通知失败向上传播（对齐 Express await，失败 → 500）。
func (h *Review) notifyEpisodeNumberChange(ctx context.Context, episodeID primitive.ObjectID,
	oldCurrent, newCurrent int, episode *model.Episode) error {
	if newCurrent <= oldCurrent {
		return nil
	}
	followers, err := h.Repos.Follows.EpisodesFindByEpisode(ctx, episodeID)
	if err != nil {
		return err
	}
	if len(followers) == 0 {
		return nil
	}
	notifications := make([]model.Notification, 0)
	for _, f := range followers {
		for epNum := oldCurrent + 1; epNum <= newCurrent; epNum++ {
			notifications = append(notifications, model.Notification{
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
	if len(notifications) == 0 {
		return nil
	}
	if err := h.Repos.Notifications.EpisodesInsertMany(ctx, notifications); err != nil {
		return err
	}
	uids := uniqueUserIDs(followers)
	h.sendPushToUser(uids, fmt.Sprintf("《%s》更新了", episode.Title),
		fmt.Sprintf("更新至第%d集", newCurrent), "/episode/"+episodeID.Hex())
	h.sendEpisodeUpdateEmails(ctx, episodeID, oldCurrent+1, newCurrent, episode.Title, uids)
	return nil
}

// sendReviewResultEmail 审核结果邮件（对齐 utils/email.js sendReviewResultEmail，
// 受用户 reviewResult 偏好控制；fire-and-forget）。
func (h *Review) sendReviewResultEmail(ctx context.Context, creatorID primitive.ObjectID, title, status, note string) {
	if h.Mail == nil {
		return
	}
	target, err := h.Repos.Users.ReviewFindMailTargetByID(ctx, creatorID)
	if err != nil {
		return
	}
	if !target.IsEmailVerified {
		return
	}
	if target.ReviewResultPref != nil && !*target.ReviewResultPref {
		return
	}
	isApproved := status == "approved"
	url := siteURL(h.Config)
	subject := fmt.Sprintf("您的剧集《%s》已通过审核", title)
	statusLabel := "已通过审核"
	boxType := "success"
	if !isApproved {
		subject = fmt.Sprintf("您的剧集《%s》未通过审核", title)
		statusLabel = "未通过审核"
		boxType = "warning"
	}
	noteBlock := ""
	if note != "" {
		noteBlock = `<p style="margin:12px 0 0;color:#475569;font-size:14px;">审核备注：</p>` +
			email.EmailInfoBox(`<p style="margin:0;">`+note+`</p>`, boxType)
	}
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">剧集审核结果</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您提交的剧集「<strong>` + title + `</strong>」审核结果：</p>` +
		email.EmailInfoBox(`<p style="margin:0;font-size:18px;font-weight:600;">`+statusLabel+`</p>`, boxType) +
		noteBlock +
		`<p style="margin:20px 0;">` + email.EmailButton("前往管理", url, "primary") + `</p>` +
		`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
	go func(to, subject, html, preheader string) {
		h.Mail.SendNotificationEmail(context.Background(), to, subject, html, preheader)
	}(target.Email, subject, body, subject)
}

// sendEpisodeUpdateEmails 集数变更邮件（对齐 episodeNotify.js sendBatchNotificationEmails：
// 仅发邮箱已验证且未显式关闭 episodeUpdate 偏好的用户；按 (剧集,集数,事件) 去重）。
func (h *Review) sendEpisodeUpdateEmails(ctx context.Context, episodeID primitive.ObjectID,
	fromEp, toEp int, episodeTitle string, userIDs []primitive.ObjectID) {
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
		for epNum := fromEp; epNum <= toEp; epNum++ {
			if !h.shouldSendEpisodeEmail(episodeID.Hex(), epNum, "available") {
				continue
			}
			to, subject, body, preheader := h.buildAvailableEmail(episodeTitle, epNum, t.Email)
			go func(to, subject, html, preheader string) {
				h.Mail.SendNotificationEmail(context.Background(), to, subject, html, preheader)
			}(to, subject, body, preheader)
		}
	}
}

// buildAvailableEmail 构造追番更新邮件（对齐 utils/email.js sendEpisodeUpdateEmail 的
// available 分支）。
func (h *Review) buildAvailableEmail(episodeTitle string, epNum int, to string) (string, string, string, string) {
	url := siteURL(h.Config)
	subject := fmt.Sprintf("《%s》更新了第%d集", episodeTitle, epNum)
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">追番更新提醒</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您关注的剧集有新更新啦！</p>` +
		email.EmailInfoBox(fmt.Sprintf(`<p style="margin:0 0 4px;font-size:16px;font-weight:600;">《%s》</p><p style="margin:0;color:#64748b;">已更新至第 %d 集</p>`, episodeTitle, epNum), "info") +
		`<p style="margin:20px 0;">` + email.EmailButton("前往观看", url, "primary") + `</p>` +
		`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
	return to, subject, body, "您关注的剧集有新更新"
}

// shouldSendEpisodeEmail 邮件去重：同一剧集+集数+事件类型 1 小时内不重复发送
// （对齐 utils/episodeNotify.js shouldSendEpisodeEmail）。
func (h *Review) shouldSendEpisodeEmail(episodeID string, epNum int, eventType string) bool {
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

// sendPushToUser 发送 Web Push（对齐 routes/notifications.js sendPushToUser，
// fire-and-forget 语义）。neo-server 当前未实现 PushSubscription 集合与 webpush
// 发送，本函数为 no-op 占位；由主 agent 接入推送域后替换实现。
func (h *Review) sendPushToUser(userIDs []primitive.ObjectID, title, body, url string) {
	_ = userIDs
	_ = title
	_ = body
	_ = url
}

// ---- 响应组装 ----

// reviewRefOpt 控制各 ref 类型是否 populate 及是否附带 email 字段。
type reviewRefOpt struct {
	populateCreatedBy      bool
	createdByEmail         bool
	populateCustomAuthors  bool
	customAuthorsEmail     bool
	populateAllowedEditors bool
	allowedEditorsEmail    bool
}

// reviewPagedResult 组装分页响应（对齐 {list,page,limit,total,totalPages}）。
func reviewPagedResult(list []gin.H, page, limit int, total int64) gin.H {
	totalPages := 0
	if limit > 0 {
		totalPages = pagination.Query{Page: page, Limit: limit}.TotalPages(total)
	}
	return gin.H{"list": list, "page": page, "limit": limit, "total": total, "totalPages": totalPages}
}

// reviewEpisodeJSON 组装剧集响应（对齐 mongoose toObject + populate 语义）。
// refs 为 nil 或对应 populate 开关关闭时 refs 输出 hex 字符串；开启时输出
// {_id, accountId, username[, email]} 对象。
func (h *Review) reviewEpisodeJSON(e *model.Episode, refs map[string]repository.ReviewUserRef, opt reviewRefOpt) gin.H {
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
		"createdBy":            reviewRefJSON(e.CreatedBy, refs, opt.populateCreatedBy, opt.createdByEmail),
		"hideCreator":          e.HideCreator,
		"allowedEditors":       reviewRefsJSON(e.AllowedEditors, refs, opt.populateAllowedEditors, opt.allowedEditorsEmail),
		"customAuthors":        reviewRefsJSON(e.CustomAuthors, refs, opt.populateCustomAuthors, opt.customAuthorsEmail),
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

// reviewRefJSON 渲染单个 ref（populate 或 hex）。
func reviewRefJSON(oid *primitive.ObjectID, refs map[string]repository.ReviewUserRef, populate, withEmail bool) any {
	if oid == nil {
		return nil
	}
	if !populate || refs == nil {
		return oid.Hex()
	}
	if u, ok := refs[oid.Hex()]; ok {
		out := gin.H{"_id": u.ID.Hex(), "accountId": u.AccountID, "username": u.Username}
		if withEmail {
			out["email"] = u.Email
		}
		return out
	}
	return nil
}

// reviewRefsJSON 渲染数组 ref（populate 或 hex 数组）。
func reviewRefsJSON(ids []primitive.ObjectID, refs map[string]repository.ReviewUserRef, populate, withEmail bool) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		if !populate || refs == nil {
			out = append(out, id.Hex())
			continue
		}
		if u, ok := refs[id.Hex()]; ok {
			entry := gin.H{"_id": u.ID.Hex(), "accountId": u.AccountID, "username": u.Username}
			if withEmail {
				entry["email"] = u.Email
			}
			out = append(out, entry)
		} else {
			out = append(out, nil)
		}
	}
	return out
}

// fetchReviewRefs 批量查询剧集 populate 所需的用户引用。
func (h *Review) fetchReviewRefs(ctx context.Context, episodes []model.Episode) (map[string]repository.ReviewUserRef, error) {
	ids := []primitive.ObjectID{}
	for i := range episodes {
		ids = append(ids, episodeRefIDs(&episodes[i])...)
	}
	return h.Repos.Users.ReviewFindUserRefsByIDs(ctx, dedupIDs(ids))
}

// episodeRefIDs 汇总单剧集的 ref 用户 ID（createdBy/allowedEditors/customAuthors）。
func episodeRefIDs(e *model.Episode) []primitive.ObjectID {
	ids := []primitive.ObjectID{}
	if e.CreatedBy != nil {
		ids = append(ids, *e.CreatedBy)
	}
	ids = append(ids, e.AllowedEditors...)
	ids = append(ids, e.CustomAuthors...)
	return ids
}

// readReviewNote 读取请求体 note（对齐 req.body.note || ”）。
func (h *Review) readReviewNote(c *gin.Context) string {
	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req)
	return req.Note
}

// ---- pendingChanges 应用（对齐 mongoose 按 schema cast）----

// applyPendingChanges 把待审核修改应用到正式字段（对齐 review.js 的
// episode[field] = pc[field] + mongoose schema cast）。
func applyPendingChanges(e *model.Episode, pc primitive.M) {
	if v, ok := pc["title"]; ok {
		e.Title = bsonString(v)
	}
	if v, ok := pc["titleEn"]; ok {
		e.TitleEn = bsonString(v)
	}
	if v, ok := pc["titleJa"]; ok {
		e.TitleJa = bsonString(v)
	}
	if v, ok := pc["description"]; ok {
		e.Description = bsonString(v)
	}
	if v, ok := pc["descriptionEn"]; ok {
		e.DescriptionEn = bsonString(v)
	}
	if v, ok := pc["descriptionJa"]; ok {
		e.DescriptionJa = bsonString(v)
	}
	if v, ok := pc["coverImage"]; ok {
		e.CoverImage = bsonString(v)
	}
	if v, ok := pc["totalEpisodes"]; ok {
		e.TotalEpisodes = bsonTotalEpisodes(v)
	}
	if v, ok := pc["currentEpisodes"]; ok {
		e.CurrentEpisodes = bsonInt(v)
	}
	if v, ok := pc["status"]; ok {
		e.Status = bsonString(v)
	}
	if v, ok := pc["category"]; ok {
		e.Category = bsonStringSlice(v)
	}
	if v, ok := pc["tags"]; ok {
		e.Tags = bsonStringSlice(v)
	}
	if v, ok := pc["updateDay"]; ok {
		e.UpdateDay = bsonString(v)
	}
	if v, ok := pc["premiereDate"]; ok {
		e.PremiereDate = bsonTime(v)
	}
	if v, ok := pc["platformLinks"]; ok {
		e.PlatformLinks = bsonStringMap(v)
	}
	if v, ok := pc["hideCreator"]; ok {
		e.HideCreator = bsonBool(v)
	}
	if v, ok := pc["customAuthors"]; ok {
		e.CustomAuthors = bsonObjectIDs(v)
	}
	if v, ok := pc["qqGroupLink"]; ok {
		e.QQGroupLink = bsonString(v)
	}
	if v, ok := pc["qqGroupNumber"]; ok {
		e.QQGroupNumber = bsonString(v)
	}
}

// bsonString 提取 BSON 字符串（对齐 mongoose String cast）。
func bsonString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	case primitive.ObjectID:
		return t.Hex()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// bsonInt 提取整数（对齐 mongoose Number cast）。
func bsonInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case float64:
		return int(t)
	case float32:
		return int(t)
	default:
		return 0
	}
}

// bsonTotalEpisodes 对齐 Episode schema totalEpisodes 的 set：
// null/undefined/” → nil，否则 Number(v)。
func bsonTotalEpisodes(v any) *int {
	switch t := v.(type) {
	case nil:
		return nil
	case float64:
		n := int(t)
		return &n
	case int:
		n := t
		return &n
	case int32:
		n := int(t)
		return &n
	case int64:
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

// bsonStringSlice 提取字符串数组（对齐 mongoose [String] cast）。
func bsonStringSlice(v any) []string {
	switch t := v.(type) {
	case primitive.A:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, bsonString(item))
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, bsonString(item))
		}
		return out
	case []string:
		return t
	case string:
		return []string{t}
	default:
		return []string{}
	}
}

// bsonStringMap 提取字符串 Map（对齐 mongoose Map of String cast）。
func bsonStringMap(v any) primitive.M {
	switch t := v.(type) {
	case primitive.M:
		return t
	case map[string]any:
		return primitive.M(t)
	case primitive.D:
		m := primitive.M{}
		for _, e := range t {
			m[e.Key] = e.Value
		}
		return m
	default:
		return primitive.M{}
	}
}

// bsonBool 提取布尔（对齐 mongoose Boolean cast）。
func bsonBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case nil:
		return false
	case string:
		return t != "" && t != "false" && t != "0"
	case float64:
		return t != 0
	case int:
		return t != 0
	case int32:
		return t != 0
	case int64:
		return t != 0
	default:
		return true
	}
}

// bsonObjectIDs 提取 ObjectID 数组（对齐 mongoose [ObjectId] cast）。
func bsonObjectIDs(v any) []primitive.ObjectID {
	switch t := v.(type) {
	case primitive.A:
		out := make([]primitive.ObjectID, 0, len(t))
		for _, item := range t {
			if oid, ok := item.(primitive.ObjectID); ok {
				out = append(out, oid)
			}
		}
		return out
	case []any:
		out := make([]primitive.ObjectID, 0, len(t))
		for _, item := range t {
			if oid, ok := item.(primitive.ObjectID); ok {
				out = append(out, oid)
			}
		}
		return out
	case []primitive.ObjectID:
		return t
	default:
		return []primitive.ObjectID{}
	}
}

// bsonTime 提取时间（对齐 mongoose Date cast）。
func bsonTime(v any) *time.Time {
	switch t := v.(type) {
	case time.Time:
		return &t
	case *time.Time:
		return t
	case primitive.DateTime:
		tm := t.Time()
		return &tm
	case nil:
		return nil
	case string:
		if tm, err := time.Parse(time.RFC3339, t); err == nil {
			return &tm
		}
		return nil
	default:
		return nil
	}
}

// siteURL 返回站点 URL（对齐 email.js getSiteUrl）。
func siteURL(cfg *config.Config) string {
	if cfg.Server.FrontendURL != "" {
		return cfg.Server.FrontendURL
	}
	if cfg.Server.SiteURL != "" {
		return cfg.Server.SiteURL
	}
	return "http://localhost:3000"
}
