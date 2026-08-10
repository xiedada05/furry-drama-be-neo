package handler

import (
	"context"
	"fmt"

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

// 本文件实现 /api/friend-links 子域（行为逐分支照抄 backend/routes/friendLinks.js）。
// 公开：GET /（列表）、POST /apply（可选鉴权 + altcha 校验）；
// 登录用户：GET /my-applications；超管：GET /all、POST /、PUT/DELETE /:id。

// FriendLinks 是友链域（/api/friend-links）handler 容器。
type FriendLinks struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	Mail   *email.Client // 可为 nil：跳过邮件通知

	// VerifyAltcha 校验 altcha payload（含 x-dev-token 开发口令绕过）。
	// 由装配方注入 service.AuthService.VerifyAltcha。
	VerifyAltcha func(payload, devToken string) bool
}

// NewFriendLinks 构造友链 handler 容器。mail 可为 nil；verifyAltcha 可为 nil
// （此时 /apply 一律返回"验证码错误或已过期"）。
func NewFriendLinks(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc, mail *email.Client,
	verifyAltcha func(payload, devToken string) bool) *FriendLinks {
	return &FriendLinks{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, Mail: mail, VerifyAltcha: verifyAltcha}
}

// Register 挂载 /api/friend-links 全部端点（路径照抄 Express 子路径）。
func (h *FriendLinks) Register(g *gin.RouterGroup) {
	g.GET("", h.List)
	g.POST("/apply", h.AuthMW.OptionalAuth(), h.Apply)
	g.GET("/my-applications", h.AuthMW.Protect(), h.MyApplications)
	g.GET("/all", h.AuthMW.Protect(middleware.RoleSuperAdmin), h.All)
	g.POST("", h.AuthMW.Protect(middleware.RoleSuperAdmin), h.Create)
	g.PUT("/:id", h.AuthMW.Protect(middleware.RoleSuperAdmin), h.Update)
	g.DELETE("/:id", h.AuthMW.Protect(middleware.RoleSuperAdmin), h.Delete)
}

// List GET /api/friend-links：获取对外展示的友链列表（公开）。
// @Summary 获取友链列表
// @Tags 友情链接
// @Success 200 {array} map[string]any "友链对象"
// @Router /friend-links [get]
func (h *FriendLinks) List(c *gin.Context) {
	list, err := h.Repos.FriendLinks.FindActive(c.Request.Context())
	if err != nil {
		serverError(c)
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, cmsFriendLinkJSON(&list[i]))
	}
	c.JSON(200, out)
}

// Apply POST /api/friend-links/apply（可选鉴权 + altcha 验证码）。
// @Summary 申请友情链接
// @Tags 友情链接
// @Security bearerAuth
// @Accept json
// @Param body body object true "name/url 必填；logo/description/altcha"
// @Success 200 {object} map[string]string "申请已提交，等待管理员审核"
// @Failure 400 {object} map[string]string "参数校验失败"
// @Router /friend-links/apply [post]
func (h *FriendLinks) Apply(c *gin.Context) {
	body := cmsReadBody(c)
	name := asString(body["name"])
	linkURL := asString(body["url"])
	if name == "" || linkURL == "" {
		c.JSON(400, gin.H{"message": "站点名称和链接为必填项"})
		return
	}
	// altcha 验证码（开发环境 x-dev-token 匹配可绕过，由 VerifyAltcha 内部处理）。
	if h.VerifyAltcha == nil || !h.VerifyAltcha(asString(body["altcha"]), c.GetHeader("x-dev-token")) {
		c.JSON(400, gin.H{"message": "验证码错误或已过期"})
		return
	}
	if !cmsIsValidHTTPURL(linkURL) {
		c.JSON(400, gin.H{"message": "链接格式不合法，仅支持 http/https 协议"})
		return
	}
	logo := asString(body["logo"])
	if logo != "" && !cmsIsValidHTTPURL(logo) {
		c.JSON(400, gin.H{"message": "Logo URL 格式不合法"})
		return
	}
	var applicantID *primitive.ObjectID
	if u, ok := middleware.GetUser(c); ok {
		applicantID = &u.ID
	}
	ctx := c.Request.Context()
	link := &model.FriendLink{
		Name:        name,
		URL:         linkURL,
		Logo:        logo,
		Description: orDefaultString(body["description"], ""),
		Order:       0,
		IsActive:    false,
		Status:      "pending",
		ApplicantID: applicantID,
	}
	if err := h.Repos.FriendLinks.Create(ctx, link); err != nil {
		serverError(c)
		return
	}
	superAdmins, err := h.Repos.Users.FriendLinksFindSuperAdmins(ctx)
	if err != nil {
		serverError(c)
		return
	}
	if len(superAdmins) > 0 {
		notifs := make([]model.Notification, 0, len(superAdmins))
		for _, a := range superAdmins {
			notifs = append(notifs, model.Notification{
				UserID:   a.ID,
				Type:     "friend_link_apply",
				Message:  "新友链申请：" + name,
				Metadata: primitive.M{"name": name},
			})
		}
		if err := h.Repos.Notifications.CmsInsertMany(ctx, notifs); err != nil {
			serverError(c)
			return
		}
		for _, a := range superAdmins {
			h.sendPushToUser(a.ID.Hex(), "新友链申请",
				fmt.Sprintf("收到来自「%s」的友链申请", name), "/admin/friend-links")
		}
		h.sendBatchFriendLinkApplyEmails(ctx, superAdmins, name)
	}
	c.JSON(200, gin.H{"message": "申请已提交，等待管理员审核"})
}

// MyApplications GET /api/friend-links/my-applications（protect）。
// @Summary 我的友链申请记录
// @Tags 友情链接
// @Security bearerAuth
// @Success 200 {array} map[string]any "友链对象（createdAt 倒序）"
// @Router /friend-links/my-applications [get]
func (h *FriendLinks) MyApplications(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	list, err := h.Repos.FriendLinks.FindByApplicant(c.Request.Context(), user.ID)
	if err != nil {
		serverError(c)
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, cmsFriendLinkJSON(&list[i]))
	}
	c.JSON(200, out)
}

// All GET /api/friend-links/all（superadmin）：全部友链。
// @Summary 全部友链（管理后台）
// @Tags 友情链接
// @Security bearerAuth
// @Success 200 {array} map[string]any "友链对象"
// @Router /friend-links/all [get]
func (h *FriendLinks) All(c *gin.Context) {
	list, err := h.Repos.FriendLinks.FindAll(c.Request.Context())
	if err != nil {
		serverError(c)
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, cmsFriendLinkJSON(&list[i]))
	}
	c.JSON(200, out)
}

// Create POST /api/friend-links/（superadmin）。
// @Summary 直接创建友链
// @Tags 友情链接
// @Security bearerAuth
// @Accept json
// @Param body body object true "name/url 必填；其余字段可选"
// @Success 200 {object} map[string]any "友链对象"
// @Failure 400 {object} map[string]string "参数校验失败"
// @Router /friend-links [post]
func (h *FriendLinks) Create(c *gin.Context) {
	body := cmsReadBody(c)
	name := asString(body["name"])
	linkURL := asString(body["url"])
	if name == "" || linkURL == "" {
		c.JSON(400, gin.H{"message": "名称和链接为必填项"})
		return
	}
	if !cmsIsValidHTTPURL(linkURL) {
		c.JSON(400, gin.H{"message": "链接格式不合法，仅支持 http/https 协议"})
		return
	}
	logo := asString(body["logo"])
	if logo != "" && !cmsIsValidHTTPURL(logo) {
		c.JSON(400, gin.H{"message": "Logo URL 格式不合法"})
		return
	}
	isActive := true
	if v, ok := body["isActive"]; ok {
		isActive = truthy(v)
	}
	link := &model.FriendLink{
		Name:          name,
		NameEn:        orDefaultString(body["nameEn"], ""),
		NameJa:        orDefaultString(body["nameJa"], ""),
		URL:           linkURL,
		Logo:          logo,
		Description:   asString(body["description"]),
		DescriptionEn: orDefaultString(body["descriptionEn"], ""),
		DescriptionJa: orDefaultString(body["descriptionJa"], ""),
		Order:         toInt(body["order"]), // order || 0
		IsActive:      isActive,
		Status:        "approved",
	}
	if err := h.Repos.FriendLinks.Create(c.Request.Context(), link); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, cmsFriendLinkJSON(link))
}

// Update PUT /api/friend-links/:id（superadmin）：更新友链；status 变化时通知申请者。
// @Summary 更新友链
// @Tags 友情链接
// @Security bearerAuth
// @Accept json
// @Param id path string true "友链 ID"
// @Param body body object true "可更新字段 + status"
// @Success 200 {object} map[string]any "友链对象"
// @Failure 400 {object} map[string]string "参数校验失败"
// @Failure 404 {object} map[string]string "友链不存在"
// @Router /friend-links/{id} [put]
func (h *FriendLinks) Update(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	body := cmsReadBody(c)
	linkURL := asString(body["url"])
	if linkURL != "" && !cmsIsValidHTTPURL(linkURL) {
		c.JSON(400, gin.H{"message": "链接格式不合法，仅支持 http/https 协议"})
		return
	}
	logo := asString(body["logo"])
	if logo != "" && !cmsIsValidHTTPURL(logo) {
		c.JSON(400, gin.H{"message": "Logo URL 格式不合法"})
		return
	}
	status := asString(body["status"])
	if status != "" && status != "pending" && status != "approved" && status != "rejected" {
		c.JSON(400, gin.H{"message": "无效的状态值"})
		return
	}
	updateData := bson.M{}
	for _, f := range []string{"name", "nameEn", "nameJa", "url", "logo",
		"description", "descriptionEn", "descriptionJa", "order", "isActive"} {
		if v, ok := body[f]; ok {
			updateData[f] = v
		}
	}
	if _, ok := body["status"]; ok {
		updateData["status"] = status
		if status == "approved" {
			updateData["isActive"] = true
		}
		if status == "rejected" {
			updateData["isActive"] = false
		}
	}
	link, err := h.Repos.FriendLinks.FindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": updateData})
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "友链不存在"})
			return
		}
		serverError(c)
		return
	}
	// status 变更且存在申请者：写通知 + 邮件 + 推送。
	if status != "" && link.ApplicantID != nil {
		statusLabel := status
		if status == "approved" {
			statusLabel = "已通过"
		} else if status == "rejected" {
			statusLabel = "已拒绝"
		}
		ctx := c.Request.Context()
		if err := h.Repos.Notifications.Create(ctx, &model.Notification{
			UserID:   *link.ApplicantID,
			Type:     "friend_link_status",
			Message:  fmt.Sprintf("友链「%s」申请%s", link.Name, statusLabel),
			Metadata: primitive.M{"name": link.Name, "status": statusLabel},
		}); err != nil {
			serverError(c)
			return
		}
		h.sendFriendLinkStatusEmail(ctx, *link.ApplicantID, link.Name, statusLabel)
		h.sendPushToUser(link.ApplicantID.Hex(), "友链审核结果",
			fmt.Sprintf("友链「%s」申请%s", link.Name, statusLabel), "/profile")
	}
	c.JSON(200, cmsFriendLinkJSON(link))
}

// Delete DELETE /api/friend-links/:id（superadmin）。
// @Summary 删除友链
// @Tags 友情链接
// @Security bearerAuth
// @Param id path string true "友链 ID"
// @Success 200 {object} map[string]string "删除成功"
// @Failure 404 {object} map[string]string "友链不存在"
// @Router /friend-links/{id} [delete]
func (h *FriendLinks) Delete(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	if _, err := h.Repos.FriendLinks.FindOneAndDelete(c.Request.Context(), oid); err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "友链不存在"})
			return
		}
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "删除成功"})
}

// ---- 通知 / 推送 / 邮件 ----

// sendPushToUser 发送 Web Push（对齐 friendLinks.js 的 sendPushToUser）。
// neo-server 当前未实现 PushSubscription 集合与 webpush 发送，为 no-op 占位
// （与 episodes 域 sendPushToUser 一致），由主 agent 接入推送域后替换。
func (h *FriendLinks) sendPushToUser(userID, title, body, link string) {
	_ = userID
	_ = title
	_ = body
	_ = link
}

// sendBatchFriendLinkApplyEmails 给超管批量发送友链申请邮件（对齐 notifyHelper.js
// sendBatchNotificationEmails + prefKey friendLinkApply：仅发邮箱已验证且未显式
// 关闭 friendLinkApply 偏好的用户）。fire-and-forget，失败静默。
func (h *FriendLinks) sendBatchFriendLinkApplyEmails(ctx context.Context, superAdmins []repository.FriendLinksAdminTarget, applicantName string) {
	if h.Mail == nil {
		return
	}
	subject, html, preheader := h.buildFriendLinkApplyEmail(applicantName)
	for _, a := range superAdmins {
		if !a.IsEmailVerified {
			continue
		}
		if a.FriendLinkApplyPref != nil && !*a.FriendLinkApplyPref {
			continue
		}
		to := a.Email
		go func() {
			h.Mail.SendNotificationEmail(context.Background(), to, subject, html, preheader)
		}()
	}
}

// sendFriendLinkStatusEmail 给申请者发送审核结果邮件（对齐 notifyHelper.js
// sendNotificationEmailToUser + prefKey friendLinkStatus）。失败静默。
func (h *FriendLinks) sendFriendLinkStatusEmail(ctx context.Context, userID primitive.ObjectID, linkName, statusLabel string) {
	if h.Mail == nil {
		return
	}
	t, err := h.Repos.Users.FriendLinksFindEmailTarget(ctx, userID)
	if err != nil {
		return
	}
	if !t.IsEmailVerified {
		return
	}
	if t.FriendLinkStatusPref != nil && !*t.FriendLinkStatusPref {
		return
	}
	subject, html, preheader := h.buildFriendLinkStatusEmail(linkName, statusLabel)
	go func() {
		h.Mail.SendNotificationEmail(context.Background(), t.Email, subject, html, preheader)
	}()
}

// buildFriendLinkApplyEmail 构造友链申请通知邮件（对齐 email.js sendFriendLinkApplyEmail）。
func (h *FriendLinks) buildFriendLinkApplyEmail(applicantName string) (string, string, string) {
	url := cmsFrontendURL(h.Config) + "/admin/friend-links"
	html := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">新友链申请</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">收到来自「<strong>` + applicantName + `</strong>」的友链申请，请前往管理后台审核。</p>` +
		`<p style="margin:20px 0;">` + email.EmailButton("前往审核", url, "primary") + `</p>` +
		`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
	return "新友链申请", html, "收到新的友链申请"
}

// buildFriendLinkStatusEmail 构造友链审核结果邮件（对齐 email.js sendFriendLinkStatusEmail）。
func (h *FriendLinks) buildFriendLinkStatusEmail(linkName, statusLabel string) (string, string, string) {
	url := cmsFrontendURL(h.Config) + "/profile"
	subject := "友链申请" + statusLabel
	html := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">友链审核结果</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您申请的友链「<strong>` + linkName + `</strong>」审核结果：</p>` +
		email.EmailInfoBox(`<p style="margin:0;font-size:18px;font-weight:600;">`+statusLabel+`</p>`, "success") +
		`<p style="margin:20px 0;">` + email.EmailButton("查看详情", url, "primary") + `</p>` +
		`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
	return subject, html, "友链审核结果：" + statusLabel
}
