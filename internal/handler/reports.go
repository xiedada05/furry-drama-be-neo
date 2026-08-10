package handler

import (
	"strconv"
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

// 本文件实现 /api/reports 子域（行为逐分支照抄 backend/routes/reports.js）。
// 3 个端点：POST /（提交举报，protect）、GET /list（举报列表，adminOnly）、
// PUT /resolve/:id（处理举报，adminOnly）。

// Reports 是 /api/reports 域 handler 容器。
type Reports struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewReports 构造举报 handler 容器。
func NewReports(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *Reports {
	return &Reports{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/reports 全部端点（不含 /api 前缀；路径对齐 reports.js 子路径）。
func (h *Reports) Register(g *gin.RouterGroup) {
	adminOnly := h.AuthMW.Protect(middleware.RoleAdmin, middleware.RoleSuperAdmin)
	g.POST("", h.AuthMW.Protect(), h.Create)
	g.POST("/", h.AuthMW.Protect(), h.Create)
	g.GET("/list", adminOnly, h.List)
	g.PUT("/resolve/:id", adminOnly, h.Resolve)
}

// Create POST /api/reports（protect）。
// @Summary 提交举报
// @Tags 举报
// @Security bearerAuth
// @Accept json
// @Param body body object true "targetType/targetId/reason/description"
// @Success 201 {object} map[string]any "举报记录"
// @Failure 400 {object} map[string]string "Invalid target type / Invalid reason / 描述不能超过2000个字符 / Already reported"
// @Router /reports [post]
func (h *Reports) Create(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		TargetType  string `json:"targetType"`
		TargetID    string `json:"targetId"`
		Reason      string `json:"reason"`
		Description string `json:"description"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.TargetType != "episode" && req.TargetType != "creator" {
		c.JSON(400, gin.H{"message": "Invalid target type"})
		return
	}
	if !validReportReason(req.Reason) {
		c.JSON(400, gin.H{"message": "Invalid reason"})
		return
	}
	if utf16Len(req.Description) > 2000 {
		c.JSON(400, gin.H{"message": "描述不能超过2000个字符"})
		return
	}
	// targetId 非法 hex → CastError → 500（对齐 mongoose）。
	targetID, err := primitive.ObjectIDFromHex(req.TargetID)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	existing, err := h.Repos.Reports.ReportsFindPending(ctx, user.ID, req.TargetType, targetID)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	if existing != nil {
		c.JSON(400, gin.H{"message": "Already reported"})
		return
	}
	report := &model.Report{
		ReporterID:  user.ID,
		TargetType:  req.TargetType,
		TargetID:    targetID,
		Reason:      req.Reason,
		Description: req.Description,
		Status:      "pending",
	}
	doc, err := h.Repos.Reports.ReportsCreate(ctx, report)
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(201, reportsJSON(doc, doc.ReporterID.Hex()))
}

// List GET /api/reports/list（adminOnly）。
// @Summary 举报列表（可按状态筛选）
// @Tags 举报
// @Security bearerAuth
// @Param status query string false "pending|resolved|dismissed"
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 20）"
// @Success 200 {object} map[string]any "reports/total/page/totalPages"
// @Router /reports/list [get]
func (h *Reports) List(c *gin.Context) {
	ctx := c.Request.Context()
	filter := bson.M{}
	if status := c.Query("status"); status != "" {
		filter["status"] = status
	}
	page := 1
	if ps := c.Query("page"); ps != "" {
		if p, err := strconv.Atoi(ps); err == nil {
			page = p
		}
	}
	limit := 20
	if ls := c.Query("limit"); ls != "" {
		if l, err := strconv.Atoi(ls); err == nil {
			limit = l
		}
	}
	skip := int64((page - 1) * limit)
	reports, err := h.Repos.Reports.ReportsList(ctx, filter, skip, int64(limit))
	if err != nil {
		serverError(c)
		return
	}
	total, err := h.Repos.Reports.ReportsCount(ctx, filter)
	if err != nil {
		serverError(c)
		return
	}

	ids := make([]primitive.ObjectID, 0, len(reports))
	for i := range reports {
		ids = append(ids, reports[i].ReporterID)
	}
	refs, err := h.Repos.Users.ReportsFindReporterRefs(ctx, dedupIDs(ids))
	if err != nil {
		serverError(c)
		return
	}
	list := make([]gin.H, 0, len(reports))
	for i := range reports {
		var reporter any // nil → null（对齐 populate 已删除举报者）
		if r, ok := refs[reports[i].ReporterID.Hex()]; ok {
			reporter = gin.H{"_id": r.ID.Hex(), "accountId": r.AccountID, "username": r.Username}
		}
		list = append(list, reportsJSON(&reports[i], reporter))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}
	c.JSON(200, gin.H{"reports": list, "total": total, "page": page, "totalPages": totalPages})
}

// Resolve PUT /api/reports/resolve/:id（adminOnly）。
// @Summary 处理举报（已采纳/未采纳，通知举报者）
// @Tags 举报
// @Security bearerAuth
// @Accept json
// @Param id path string true "举报 ID"
// @Param body body object true "status/resolveNote"
// @Success 200 {object} map[string]any "处理后的举报记录"
// @Failure 400 {object} map[string]string "Invalid status"
// @Failure 404 {object} map[string]string "Report not found"
// @Router /reports/resolve/{id} [put]
func (h *Reports) Resolve(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		Status      string `json:"status"`
		ResolveNote string `json:"resolveNote"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Status != "resolved" && req.Status != "dismissed" {
		c.JSON(400, gin.H{"message": "Invalid status"})
		return
	}
	ctx := c.Request.Context()
	doc, err := h.Repos.Reports.ReportsResolve(ctx, c.Param("id"), req.Status, req.ResolveNote, user.ID)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "Report not found"})
			return
		}
		serverError(c)
		return
	}

	// 通知举报者处理结果：站内通知 + Web Push（fire-and-forget）。
	isResolved := req.Status == "resolved"
	reporterMessage := "您的举报已处理：未采纳"
	if isResolved {
		reporterMessage = "您的举报已处理：已采纳"
	}
	if doc.ReporterID != (primitive.ObjectID{}) {
		h.Repos.Notifications.Create(ctx, &model.Notification{
			UserID:    doc.ReporterID,
			Type:      "report_result",
			Message:   reporterMessage,
			Metadata:  primitive.M{"status": req.Status, "resolveNote": req.ResolveNote},
			CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		})
		h.sendPushToUser([]primitive.ObjectID{doc.ReporterID}, "举报处理结果", reporterMessage, "/")
	}
	c.JSON(200, reportsJSON(doc, doc.ReporterID.Hex()))
}

// reportsJSON 渲染举报文档（reporter 为 hex 或 populate 对象）。
func reportsJSON(doc *repository.ReportsDoc, reporter any) gin.H {
	return gin.H{
		"_id":         doc.ID.Hex(),
		"reporterId":  reporter,
		"targetType":  doc.TargetType,
		"targetId":    doc.TargetID.Hex(),
		"reason":      doc.Reason,
		"description": doc.Description,
		"status":      doc.Status,
		"resolvedBy":  doc.ResolvedBy,
		"resolveNote": doc.ResolveNote,
		"createdAt":   doc.CreatedAt,
		"updatedAt":   doc.UpdatedAt,
		"__v":         doc.VersionKey,
	}
}

// validReportReason 判断举报原因是否合法（对齐 reason enum）。
func validReportReason(reason string) bool {
	switch reason {
	case "inappropriate", "copyright", "spam", "misleading", "other":
		return true
	}
	return false
}

// utf16Len 计算字符串的 UTF-16 码元长度（对齐 JS String.prototype.length）。
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// sendPushToUser 发送 Web Push（对齐 routes/notifications.js sendPushToUser，
// fire-and-forget 语义）。neo-server 当前未实现 PushSubscription 集合与 webpush
// 发送，本函数为 no-op 占位；由主 agent 接入推送域后替换实现。
func (h *Reports) sendPushToUser(userIDs []primitive.ObjectID, title, body, url string) {
	_ = userIDs
	_ = title
	_ = body
	_ = url
}
