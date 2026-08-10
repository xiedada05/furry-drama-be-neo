package handler

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/feedback 子域（行为逐分支照抄 backend/routes/feedback.js）。
//
// 端点角色（authFactory.js）：POST/GET /my 为 protect（任意登录用户）；
// GET / 与 PUT /:id 为 adminOnlyProtect（admin/superadmin）。
// POST / 附带反馈限流器（5/1h，对齐 feedbackLimiter）。

// feedbackSpec 是反馈提交限流器（对齐 feedback.js 的 feedbackLimiter）。
var feedbackSpec = ratelimit.Spec{
	Name:    "feedback",
	Mounts:  []string{"/feedback"},
	Window:  time.Hour,
	Max:     5,
	Message: "反馈提交过于频繁，请1小时后再试",
}

// Feedback 是 /api/feedback 域 handler 容器。
type Feedback struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	Mail   *email.Client
}

// NewFeedback 构造反馈 handler 容器。mail 为邮件客户端（可为 nil，跳过发信）。
func NewFeedback(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc, mail *email.Client) *Feedback {
	return &Feedback{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, Mail: mail}
}

// Register 挂载 /api/feedback 全部端点（不含 /api 前缀；路径对齐 feedback.js 子路径）。
// POST 注册 "" 与 "/"（Express strict routing 关闭对尾斜杠宽容），限流器仅施加于 POST。
func (h *Feedback) Register(g *gin.RouterGroup) {
	adminRoles := []string{middleware.RoleAdmin, middleware.RoleSuperAdmin}
	g.POST("", h.AuthMW.Protect(), h.RL(feedbackSpec), h.Create)
	g.POST("/", h.AuthMW.Protect(), h.RL(feedbackSpec), h.Create)
	g.GET("/my", h.AuthMW.Protect(), h.My)
	g.GET("", h.AuthMW.Protect(adminRoles...), h.List)
	g.GET("/", h.AuthMW.Protect(adminRoles...), h.List)
	g.PUT("/:id", h.AuthMW.Protect(adminRoles...), h.Reply)
}

// Create POST /api/feedback（protect + feedbackLimiter）。
// @Summary 提交反馈
// @Tags 反馈
// @Security bearerAuth
// @Accept json
// @Param body body object true "type/content"
// @Success 200 {object} map[string]string "message"
// @Failure 400 {object} map[string]string "无效的反馈类型/内容不能为空/内容不能超过2000字"
// @Router /feedback [post]
func (h *Feedback) Create(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	typ := asString(body["type"])
	switch typ {
	case "", "suggestion", "bug", "complaint", "other":
	default:
		c.JSON(400, gin.H{"message": "无效的反馈类型"})
		return
	}
	content := asString(body["content"])
	if strings.TrimSpace(content) == "" {
		c.JSON(400, gin.H{"message": "内容不能为空"})
		return
	}
	if len([]rune(content)) > 2000 {
		c.JSON(400, gin.H{"message": "内容不能超过2000字"})
		return
	}
	user, _ := middleware.GetUser(c)
	username := user.Username
	if username == "" {
		username = user.AccountID
	}
	if username == "" {
		username = user.Email
	}
	f := &model.Feedback{
		UserID:   user.ID,
		Username: username,
		Type:     orDefaultString(body["type"], "suggestion"),
		Content:  strings.TrimSpace(content),
		Status:   "pending",
	}
	if runes := []rune(f.Content); len(runes) > 2000 {
		f.Content = string(runes[:2000])
	}
	if err := h.Repos.Feedbacks.FeedbackCreate(c.Request.Context(), f); err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "反馈已提交，感谢您的建议！"})
}

// My GET /api/feedback/my（protect）：当前用户的历史反馈。
// @Summary 我的反馈列表
// @Tags 反馈
// @Security bearerAuth
// @Param page query int false "页码"
// @Param limit query int false "每页数量（默认20，上限100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /feedback/my [get]
func (h *Feedback) My(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	page, limit := myFeedbackPage(c)
	filter := bson.M{"userId": user.ID}
	ctx := c.Request.Context()
	total, err := h.Repos.Feedbacks.FeedbackCount(ctx, filter)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	list, err := h.Repos.Feedbacks.FeedbackFindPaged(ctx, filter,
		bson.D{{Key: "createdAt", Value: -1}}, page, limit)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, feedbackJSON(&list[i]))
	}
	c.JSON(200, gin.H{
		"list":       out,
		"page":       page,
		"limit":      limit,
		"total":      total,
		"totalPages": creatorTotalPages(total, limit),
	})
}

// List GET /api/feedback（adminOnlyProtect）：管理端反馈列表，可按 status 过滤。
// @Summary 反馈列表（管理）
// @Tags 反馈
// @Security bearerAuth
// @Param status query string false "pending|read|replied"
// @Param page query int false "页码"
// @Param limit query int false "每页数量（默认20，无上限钳制）"
// @Success 200 {object} map[string]any "list/total/page/totalPages"
// @Router /feedback [get]
func (h *Feedback) List(c *gin.Context) {
	filter := bson.M{}
	if status := c.Query("status"); status != "" {
		filter["status"] = status
	}
	page, limit := adminFeedbackPage(c)
	ctx := c.Request.Context()
	total, err := h.Repos.Feedbacks.FeedbackCount(ctx, filter)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	list, err := h.Repos.Feedbacks.FeedbackFindPaged(ctx, filter,
		bson.D{{Key: "createdAt", Value: -1}}, page, limit)
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, feedbackJSON(&list[i]))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}
	c.JSON(200, gin.H{"list": out, "total": total, "page": page, "totalPages": totalPages})
}

// Reply PUT /api/feedback/:id（adminOnlyProtect）。
// 更新状态/回复；回复时创建 feedback_reply 通知并尝试发回复邮件。
// @Summary 处理/回复反馈
// @Tags 反馈
// @Security bearerAuth
// @Accept json
// @Param id path string true "反馈 ID"
// @Param body body object true "status/reply"
// @Success 200 {object} map[string]any "反馈文档"
// @Failure 400 {object} map[string]string "无效的状态值"
// @Failure 404 {object} map[string]string "Not found"
// @Router /feedback/{id} [put]
func (h *Feedback) Reply(c *gin.Context) {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	statusVal := asString(body["status"])
	switch statusVal {
	case "", "pending", "read", "replied":
	default:
		c.JSON(400, gin.H{"message": "无效的状态值"})
		return
	}
	update := bson.M{}
	if statusVal != "" {
		update["status"] = statusVal
	}
	replyVal, hasReply := body["reply"]
	if hasReply {
		reply := asString(replyVal)
		if runes := []rune(reply); len(runes) > 1000 {
			reply = string(runes[:1000])
		}
		update["reply"] = reply
	}
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		// 对齐 Express 非法 ID → CastError → catch → 500。
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	fb, err := h.Repos.Feedbacks.FeedbackFindOneAndUpdate(c.Request.Context(), oid, bson.M{"$set": update})
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "Not found"})
		return
	}
	if err != nil {
		errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
		return
	}
	if truthy(replyVal) {
		preview := replyPreview(asString(replyVal), 50)
		if err := h.Repos.Notifications.Create(c.Request.Context(), &model.Notification{
			UserID:    fb.UserID,
			Type:      "feedback_reply",
			Message:   "您的反馈已收到回复：" + preview + "...",
			Metadata:  primitive.M{"reply": preview},
			CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		}); err != nil {
			errors.AbortWithAppError(c, errors.New(500, "Server error"), h.Config.IsDev)
			return
		}
		reply200 := replyPreview(asString(replyVal), 200)
		h.sendFeedbackReplyEmail(fb.UserID, reply200)
	}
	c.JSON(200, feedbackJSON(fb))
}

// ---- helpers ----

// feedbackJSON 组装反馈文档（对齐 mongoose toObject）。
func feedbackJSON(f *model.Feedback) gin.H {
	return gin.H{
		"_id":       f.ID.Hex(),
		"userId":    f.UserID.Hex(),
		"username":  f.Username,
		"type":      f.Type,
		"content":   f.Content,
		"status":    f.Status,
		"reply":     f.Reply,
		"createdAt": f.CreatedAt,
		"__v":       f.VersionKey,
	}
}

// myFeedbackPage 解析 GET /my 分页（对齐 feedback.js：page 默认 1，limit 默认 20
// 上限 100）。
func myFeedbackPage(c *gin.Context) (page, limit int) {
	page, limit = creatorPage(c)
	return page, limit
}

// adminFeedbackPage 解析管理端 GET / 分页（对齐 feedback.js：page 默认 1，
// limit 默认 20，无上限钳制）。
func adminFeedbackPage(c *gin.Context) (page, limit int) {
	page = 1
	if p, err := parseIntQuery(c.Query("page")); err == nil {
		page = p
	}
	limit = 20
	if l, err := parseIntQuery(c.Query("limit")); err == nil {
		limit = l
	}
	return page, limit
}

// replyPreview 截取回复前 n 个字符（对齐 reply.slice(0, n)）。
func replyPreview(s string, n int) string {
	if runes := []rune(s); len(runes) > n {
		return string(runes[:n])
	}
	return s
}

// sendFeedbackReplyEmail 发送反馈回复邮件（对齐 notifyHelper.js
// sendNotificationEmailToUser(fb.userId, 'feedbackReply', reply)：
// 邮箱已验证且未显式关闭 feedbackReply 偏好才发送；fire-and-forget）。
func (h *Feedback) sendFeedbackReplyEmail(userID primitive.ObjectID, reply string) {
	if h.Mail == nil {
		return
	}
	go func() {
		ctx := context.Background()
		u, err := h.Repos.Users.FindByID(ctx, userID)
		if err != nil || !u.IsEmailVerified {
			return
		}
		if !u.EmailNotificationPrefs.FeedbackReply {
			return
		}
		url := h.feedbackSiteURL() + "/profile"
		body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">反馈回复通知</h2>` +
			`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您提交的反馈已收到管理员的回复：</p>` +
			email.EmailInfoBox(`<p style="margin:0;">`+reply+`</p>`, "info") +
			`<p style="margin:20px 0;">` + email.EmailButton("查看详情", url, "primary") + `</p>` +
			`<p style="margin:0;color:#94a3b8;font-size:12px;">您可以在账号设置中关闭此类邮件通知。</p>`
		h.Mail.SendNotificationEmail(ctx, u.Email, "您的反馈已收到回复", body, "您的反馈已收到回复")
	}()
}

// feedbackSiteURL 返回站点 URL（对齐 getSiteUrl）。
func (h *Feedback) feedbackSiteURL() string {
	if h.Config.Server.FrontendURL != "" {
		return h.Config.Server.FrontendURL
	}
	if h.Config.Server.SiteURL != "" {
		return h.Config.Server.SiteURL
	}
	return "http://localhost:3000"
}

// parseIntQuery 解析整型查询参数（对齐 parseInt 语义，返回错误由调用方回退默认）。
func parseIntQuery(s string) (int, error) {
	return strconv.Atoi(s)
}
