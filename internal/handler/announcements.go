package handler

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/announcements 子域（行为逐分支照抄 backend/routes/announcements.js）。
// 公开端点：GET /active、GET /:id；超管 CRUD：GET/POST /、PUT/DELETE /:id、
// POST /:id/send-email、POST /:id/send-notification。

// 公告通知推送分批大小（对齐 announcements.js 常量）。
const (
	announcementNotifBatch = 1000
	announcementEmailBatch = 50
)

// Announcements 是公告域（/api/announcements）handler 容器。
type Announcements struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	Mail   *email.Client // 可为 nil：跳过邮件推送（sendAnnouncementEmails 返回 0）
}

// NewAnnouncements 构造公告 handler 容器。mail 可为 nil（不发信）。
func NewAnnouncements(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc, mail *email.Client) *Announcements {
	return &Announcements{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, Mail: mail}
}

// Register 挂载 /api/announcements 全部端点（路径照抄 Express 子路径，不含 /api 前缀）。
// 公开端点在前，超管 CRUD 用 Protect("superadmin") + RequireEmailChanged 保护
// （对齐 router.use(superAdminProtect); router.use(requireEmailChanged)）。
func (h *Announcements) Register(g *gin.RouterGroup) {
	super := h.AuthMW.Protect(middleware.RoleSuperAdmin)
	emailChanged := h.AuthMW.RequireEmailChanged()
	g.GET("/active", h.Active)
	g.GET("/:id", h.PublicDetail)
	g.GET("", super, emailChanged, h.AdminList)
	g.POST("", super, emailChanged, h.Create)
	g.PUT("/:id", super, emailChanged, h.Update)
	g.DELETE("/:id", super, emailChanged, h.Delete)
	g.POST("/:id/send-email", super, emailChanged, h.SendEmail)
	g.POST("/:id/send-notification", super, emailChanged, h.SendNotification)
}

// ---- 公开端点 ----

// Active GET /api/announcements/active：获取生效中的公告（弹窗/横幅）。
// @Summary 获取生效中的公告
// @Tags 公告
// @Param channel query string false "渠道过滤 popup|banner"
// @Success 200 {array} map[string]any "公告列表（≤20）"
// @Router /announcements/active [get]
func (h *Announcements) Active(c *gin.Context) {
	channel := c.Query("channel")
	list, err := h.Repos.Announcements.FindActive(c.Request.Context(), channel)
	if err != nil {
		serverError(c)
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, cmsAnnouncementJSON(&list[i]))
	}
	c.JSON(200, out)
}

// PublicDetail GET /api/announcements/:id：获取单条公告详情。
// @Summary 获取公告详情
// @Tags 公告
// @Param id path string true "公告 ID"
// @Success 200 {object} map[string]any "公告对象"
// @Failure 404 {object} map[string]string "Announcement not found"
// @Router /announcements/{id} [get]
func (h *Announcements) PublicDetail(c *gin.Context) {
	a, ok := h.findAnnouncement(c)
	if !ok {
		return
	}
	c.JSON(200, cmsAnnouncementJSON(a))
}

// ---- 超管 CRUD ----

// AdminList GET /api/announcements/（superadmin）：公告全量列表。
// @Summary 公告列表（管理后台）
// @Tags 公告
// @Security bearerAuth
// @Success 200 {array} map[string]any "公告列表（createdAt 倒序）"
// @Router /announcements [get]
func (h *Announcements) AdminList(c *gin.Context) {
	list, err := h.Repos.Announcements.FindAll(c.Request.Context())
	if err != nil {
		serverError(c)
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, cmsAnnouncementJSON(&list[i]))
	}
	c.JSON(200, out)
}

// Create POST /api/announcements/（superadmin + requireEmailChanged）：创建公告。
// @Summary 创建公告
// @Tags 公告
// @Security bearerAuth
// @Accept json
// @Param body body object true "title/content 必填；其余字段可选"
// @Success 200 {object} map[string]any "公告对象"
// @Failure 400 {object} map[string]string "标题和内容不能为空"
// @Router /announcements [post]
func (h *Announcements) Create(c *gin.Context) {
	body := cmsReadBody(c)
	title := asString(body["title"])
	content := asString(body["content"])
	if title == "" || content == "" {
		c.JSON(400, gin.H{"message": "标题和内容不能为空"})
		return
	}
	// 对齐 mongoose enum 校验：type 非法 → ValidationError → 500。
	annType := orDefaultString(body["type"], "info")
	switch annType {
	case "info", "warning", "maintenance", "update":
	default:
		serverError(c)
		return
	}
	user, _ := middleware.GetUser(c)
	a := &model.Announcement{
		Title:            title,
		TitleEn:          orDefaultString(body["titleEn"], ""),
		Content:          content,
		ContentEn:        orDefaultString(body["contentEn"], ""),
		Type:             annType,
		ShowPopup:        truthy(body["showPopup"]),
		ShowBanner:       truthy(body["showBanner"]),
		SendNotification: truthy(body["sendNotification"]),
		SendEmail:        truthy(body["sendEmail"]),
		Dismissible:      body["dismissible"] != false, // dismissible !== false
		Active:           body["active"] != false,      // active !== false
		Pinned:           truthy(body["pinned"]),
		PublishAt:        cmsPublishAtOrNow(body["publishAt"]),
		ExpireAt:         cmsDateOrNil(body["expireAt"]),
		Link:             orDefaultString(body["link"], ""),
		CreatedBy:        &user.ID,
	}
	if err := h.Repos.Announcements.Create(c.Request.Context(), a); err != nil {
		serverError(c)
		return
	}
	// 发布时自动推送通知与邮件（仅在 active 且已到发布时间）。
	if a.Active && !a.PublishAt.After(time.Now()) {
		h.broadcastAnnouncement(c.Request.Context(), a)
	}
	c.JSON(200, cmsAnnouncementJSON(a))
}

// Update PUT /api/announcements/:id（superadmin + requireEmailChanged）：更新公告，
// 检测 active/通知/邮件开关从 false→true 时重新触发推送。
// @Summary 更新公告
// @Tags 公告
// @Security bearerAuth
// @Accept json
// @Param id path string true "公告 ID"
// @Param body body object true "可更新字段"
// @Success 200 {object} map[string]any "公告对象"
// @Failure 404 {object} map[string]string "Announcement not found"
// @Router /announcements/{id} [put]
func (h *Announcements) Update(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	body := cmsReadBody(c)

	// 对齐 mongoose enum 校验（findByIdAndUpdate runValidators:true）：type 非法 → 500。
	if v, ok := body["type"]; ok {
		switch asString(v) {
		case "info", "warning", "maintenance", "update":
		default:
			serverError(c)
			return
		}
	}

	allowedFields := []string{
		"title", "titleEn", "content", "contentEn", "type",
		"showPopup", "showBanner", "sendNotification", "sendEmail",
		"dismissible", "active", "pinned", "publishAt", "expireAt", "link",
	}
	updateData := bson.M{}
	for _, f := range allowedFields {
		if v, ok := body[f]; ok {
			updateData[f] = cmsCastAnnouncementField(f, v)
		}
	}
	updateData["updatedAt"] = time.Now().UTC().Truncate(time.Millisecond)

	// 记录更新前状态，用于判断是否需要重新触发推送。
	oldAnn, err := h.Repos.Announcements.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Announcement not found"})
			return
		}
		serverError(c)
		return
	}
	announcement, err := h.Repos.Announcements.FindOneAndUpdate(ctx, oid, bson.M{"$set": updateData})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Announcement not found"})
			return
		}
		serverError(c)
		return
	}

	// 检测是否需要重新推送：active/通知/邮件 任一从 false→true 且已到发布时间。
	activeTurnedOn := !oldAnn.Active && announcement.Active
	notifTurnedOn := !oldAnn.SendNotification && announcement.SendNotification
	emailTurnedOn := !oldAnn.SendEmail && announcement.SendEmail
	isPublished := announcement.Active && !announcement.PublishAt.After(time.Now())
	if isPublished && (activeTurnedOn || notifTurnedOn || emailTurnedOn) {
		// 重置对应标志位以允许重新推送（broadcastAnnouncement 内部会检查标志位防重复）。
		if activeTurnedOn || notifTurnedOn {
			announcement.NotificationSent = false
		}
		if activeTurnedOn || emailTurnedOn {
			announcement.EmailSent = false
			announcement.EmailSentAt = nil
			announcement.EmailSentCount = 0
		}
		if err := h.Repos.Announcements.Save(ctx, announcement); err != nil {
			serverError(c)
			return
		}
		h.broadcastAnnouncement(ctx, announcement)
	}
	c.JSON(200, cmsAnnouncementJSON(announcement))
}

// Delete DELETE /api/announcements/:id（superadmin + requireEmailChanged）：删除公告并清理通知。
// @Summary 删除公告
// @Tags 公告
// @Security bearerAuth
// @Param id path string true "公告 ID"
// @Success 200 {object} map[string]string "已删除"
// @Failure 404 {object} map[string]string "Announcement not found"
// @Router /announcements/{id} [delete]
func (h *Announcements) Delete(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	a, err := h.Repos.Announcements.FindOneAndDelete(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Announcement not found"})
			return
		}
		serverError(c)
		return
	}
	// 同步清理通知中心中相关条目（忽略清理失败）。
	_, _ = h.Repos.Notifications.AnnouncementsDeleteByAnnouncement(ctx, a.ID)
	c.JSON(200, gin.H{"message": "已删除"})
}

// SendEmail POST /api/announcements/:id/send-email（superadmin）：手动触发邮件推送。
// @Summary 手动触发公告邮件推送
// @Tags 公告
// @Security bearerAuth
// @Param id path string true "公告 ID"
// @Success 200 {object} map[string]any "message/sent"
// @Failure 400 {object} map[string]string "该公告未启用邮件推送"
// @Failure 404 {object} map[string]string "Announcement not found"
// @Router /announcements/{id}/send-email [post]
func (h *Announcements) SendEmail(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	a, err := h.Repos.Announcements.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Announcement not found"})
			return
		}
		serverError(c)
		return
	}
	if !a.SendEmail {
		c.JSON(400, gin.H{"message": "该公告未启用邮件推送"})
		return
	}
	sent, err := h.sendAnnouncementEmails(ctx, a)
	if err != nil {
		serverError(c)
		return
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	a.EmailSent = true
	a.EmailSentAt = &now
	a.EmailSentCount = sent
	if err := h.Repos.Announcements.Save(ctx, a); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": fmt.Sprintf("邮件推送完成，成功发送 %d 封", sent), "sent": sent})
}

// SendNotification POST /api/announcements/:id/send-notification（superadmin）：手动触发通知中心推送。
// @Summary 手动触发公告通知推送
// @Tags 公告
// @Security bearerAuth
// @Param id path string true "公告 ID"
// @Success 200 {object} map[string]any "message/count"
// @Failure 400 {object} map[string]string "该公告未启用通知推送"
// @Failure 404 {object} map[string]string "Announcement not found"
// @Router /announcements/{id}/send-notification [post]
func (h *Announcements) SendNotification(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	a, err := h.Repos.Announcements.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Announcement not found"})
			return
		}
		serverError(c)
		return
	}
	if !a.SendNotification {
		c.JSON(400, gin.H{"message": "该公告未启用通知推送"})
		return
	}
	count, err := h.sendAnnouncementNotifications(ctx, a)
	if err != nil {
		serverError(c)
		return
	}
	a.NotificationSent = true
	if err := h.Repos.Announcements.Save(ctx, a); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": fmt.Sprintf("通知推送完成，发送 %d 条", count), "count": count})
}

// ---- 工具函数（对齐 announcements.js 底部 broadcast/send 系列）----

// findAnnouncement 解析路径 ID 并查询公告；错误已渲染响应，ok=false 表示已返回。
func (h *Announcements) findAnnouncement(c *gin.Context) (*model.Announcement, bool) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return nil, false
	}
	a, err := h.Repos.Announcements.FindByID(c.Request.Context(), oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Announcement not found"})
			return nil, false
		}
		serverError(c)
		return nil, false
	}
	return a, true
}

// broadcastAnnouncement 发布时综合推送：按开关发送通知 + 邮件。
// 对齐 broadcastAnnouncement：内部吞掉一切错误（Express try/catch 仅打日志），
// 由调用方忽略返回值。
func (h *Announcements) broadcastAnnouncement(ctx context.Context, a *model.Announcement) {
	if a.SendNotification && !a.NotificationSent {
		_, _ = h.sendAnnouncementNotifications(ctx, a)
		a.NotificationSent = true
	}
	if a.SendEmail && !a.EmailSent {
		sent, _ := h.sendAnnouncementEmails(ctx, a)
		now := time.Now().UTC().Truncate(time.Millisecond)
		a.EmailSent = true
		a.EmailSentAt = &now
		a.EmailSentCount = sent
	}
	_ = h.Repos.Announcements.Save(ctx, a)
}

// sendAnnouncementNotifications 给所有用户推送通知中心条目（分批 insertMany），
// 返回 { count }。对齐 announcements.js sendAnnouncementNotifications。
func (h *Announcements) sendAnnouncementNotifications(ctx context.Context, a *model.Announcement) (int, error) {
	total := 0
	skip := 0
	for {
		ids, err := h.Repos.Users.AnnouncementsFindAllUserIDs(ctx, int64(skip), announcementNotifBatch)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}
		docs := make([]model.Notification, 0, len(ids))
		for _, uid := range ids {
			docs = append(docs, model.Notification{
				UserID:  uid,
				Type:    "announcement",
				Message: a.Title,
				Link:    a.Link,
				Metadata: primitive.M{
					"announcementId": a.ID,
					"type":           a.Type,
					"content":        a.Content,
				},
				IsRead: false,
			})
		}
		if err := h.Repos.Notifications.CmsInsertMany(ctx, docs); err != nil {
			return total, err
		}
		total += len(ids)
		skip += len(ids)
		if len(ids) < announcementNotifBatch {
			break
		}
	}
	return total, nil
}

// sendAnnouncementEmails 给所有已验证邮箱且未关闭公告偏好的用户发邮件（分批），
// 返回成功发送数。对齐 announcements.js sendAnnouncementEmails。
func (h *Announcements) sendAnnouncementEmails(ctx context.Context, a *model.Announcement) (int, error) {
	if h.Mail == nil {
		return 0, nil
	}
	subject := "[公告] " + a.Title
	html := h.buildAnnouncementHTML(a)
	total := 0
	skip := 0
	for {
		targets, err := h.Repos.Users.AnnouncementsFindEmailTargets(ctx, int64(skip), announcementEmailBatch)
		if err != nil {
			return total, err
		}
		if len(targets) == 0 {
			break
		}
		for _, t := range targets {
			if h.Mail.SendNotificationEmail(ctx, t.Email, subject, html, "") {
				total++
			}
		}
		skip += len(targets)
		if len(targets) < announcementEmailBatch {
			break
		}
	}
	return total, nil
}

// buildAnnouncementHTML 构造公告邮件正文（对齐 announcements.js buildAnnouncementHtml）。
func (h *Announcements) buildAnnouncementHTML(a *model.Announcement) string {
	typeLabels := map[string]string{
		"info":        "📢 站点公告",
		"warning":     "⚠️ 重要提醒",
		"maintenance": "🔧 维护通知",
		"update":      "✨ 更新公告",
	}
	label := typeLabels[a.Type]
	if label == "" {
		label = "📢 站点公告"
	}
	url := cmsFrontendURL(h.Config)
	variantMap := map[string]string{
		"info": "info", "warning": "warning", "maintenance": "warning", "update": "success",
	}
	variant := variantMap[a.Type]
	if variant == "" {
		variant = "info"
	}
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">` + label + `</h2>` +
		email.EmailInfoBox(`<p style="margin:0;font-size:16px;font-weight:600;">`+a.Title+`</p>`, variant) +
		`<div style="margin:16px 0;color:#334155;font-size:14px;line-height:1.7;white-space:pre-wrap;">` + a.Content + `</div>` +
		`<p style="margin:20px 0;">`
	if a.Link != "" {
		body += email.EmailButton("查看详情", a.Link, "primary") + " "
	}
	body += email.EmailButton("访问站点", url, "secondary") + `</p>` +
		`<p style="margin:0;color:#94a3b8;font-size:12px;">这是一封来自站点的公告通知邮件，如不希望接收此类邮件可在账户设置中关闭公告邮件偏好。</p>`
	return body
}

// cmsCastAnnouncementField 对齐 mongoose 按 schema 类型 cast 的更新字段转换。
func cmsCastAnnouncementField(field string, v any) any {
	switch field {
	case "title", "titleEn", "content", "contentEn", "type", "link":
		return asString(v)
	case "showPopup", "showBanner", "sendNotification", "sendEmail", "dismissible", "active", "pinned":
		return cmsCastBool(v)
	case "publishAt", "expireAt":
		return cmsDateOrNil(v)
	default:
		return v
	}
}
