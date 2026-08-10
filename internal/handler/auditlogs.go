package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// AuditLogs 是审计日志域（/api/audit-logs）handler 容器，行为逐分支对齐
// backend/routes/auditLogs.js（单个端点）。superAdminProtect 保护。
type AuditLogs struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewAuditLogs 构造审计日志 handler 容器。
func NewAuditLogs(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc) *AuditLogs {
	return &AuditLogs{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载审计日志路由（superAdminProtect）。同时注册 "" 与 "/"：
// gin 默认对 /api/audit-logs 尾斜杠缺失发起 307 重定向，而 Express（strict
// routing 关闭）直接匹配，需消除该差异。
func (h *AuditLogs) Register(g *gin.RouterGroup) {
	protect := h.AuthMW.Protect(middleware.RoleSuperAdmin)
	g.GET("", protect, h.List)
	g.GET("/", protect, h.List)
}

// List GET /api/audit-logs（superAdminProtect）：审计日志分页列表，
// 可按操作/操作者/用户过滤。
// @Summary 审计日志列表
// @Tags 审计日志
// @Security bearerAuth
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 50）"
// @Param action query string false "操作关键词"
// @Param admin query string false "操作者昵称关键词"
// @Param user query string false "被操作用户关键词"
// @Success 200 {object} map[string]any "logs/total/page/totalPages"
// @Router /audit-logs [get]
func (h *AuditLogs) List(c *gin.Context) {
	page := 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		page = p
	}
	limit := 50
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limit = l
	}
	query := bson.M{}
	if action := c.Query("action"); action != "" {
		query["action"] = bson.M{"$regex": escapeRegex(action), "$options": "i"}
	}
	if admin := c.Query("admin"); admin != "" {
		query["adminName"] = bson.M{"$regex": escapeRegex(admin), "$options": "i"}
	}
	if user := c.Query("user"); user != "" {
		query["userName"] = bson.M{"$regex": escapeRegex(user), "$options": "i"}
	}
	ctx := c.Request.Context()
	total, err := h.Repos.AuditLogs.AdminCount(ctx, query)
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	logs, err := h.Repos.AuditLogs.AdminFindPaged(ctx, query, page, limit)
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	list := make([]gin.H, 0, len(logs))
	for i := range logs {
		list = append(list, auditLogJSON(&logs[i]))
	}
	totalPages := 0
	if limit > 0 {
		totalPages = (int(total) + limit - 1) / limit
	}
	// 响应键对齐 auditLogs.js：logs/total/page/totalPages（无 limit 键）。
	c.JSON(200, gin.H{"logs": list, "total": total, "page": page, "totalPages": totalPages})
}

// auditLogJSON 组装审计日志响应对象。对齐 mongoose AuditLog toJSON：未设置的
// adminId/adminName/userId/userAgent 键省略（schema 无 default），已设置的输出。
func auditLogJSON(l *model.AuditLog) gin.H {
	g := gin.H{
		"_id":       l.ID.Hex(),
		"userName":  l.UserName,
		"action":    l.Action,
		"target":    l.Target,
		"details":   l.Details,
		"ip":        l.IP,
		"createdAt": l.CreatedAt,
	}
	if l.AdminID != nil {
		g["adminId"] = l.AdminID.Hex()
	}
	if l.AdminName != "" {
		g["adminName"] = l.AdminName
	}
	if l.UserID != nil {
		g["userId"] = l.UserID.Hex()
	}
	if l.UserAgent != "" {
		g["userAgent"] = l.UserAgent
	}
	return g
}
