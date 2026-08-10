package handler

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	altchalib "github.com/altcha-org/altcha-lib-go/v2"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/service"
)

// adminDefaultSuperEmail 是超管默认邮箱（对齐 requireEmailChanged 判定常量）。
const adminDefaultSuperEmail = "admin@furry09.com"

// adminManagerRoles 是管理/创作者账户角色集合（对齐 /list 与 DELETE /:id 的角色判定）。
var adminManagerRoles = []string{"admin", "superadmin", "creator"}

// adminUserRoles 是全部角色集合（对齐 PUT /role/:id 的枚举校验）。
var adminUserRoles = []string{"user", "creator", "admin", "superadmin"}

// Admin 是管理后台域（/api/admin）handler 容器，行为逐端点对齐
// backend/routes/admin.js（18 个端点）。MountDual 双版本镜像挂载到 /api/admin。
type Admin struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
	Mail   *email.Client
}

// NewAdmin 构造管理后台 handler 容器。mail 为邮件客户端（可为 nil，跳过发信）。
func NewAdmin(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc, mail *email.Client) *Admin {
	return &Admin{Repos: repos, Config: cfg, AuthMW: amw, RL: rl, Mail: mail}
}

// Register 挂载全部管理后台路由（路径照抄 Express 子路径，不含 /api 前缀）。
// 角色映射：adminProtect = creator/admin/superadmin；superAdminProtect = superadmin。
// requireEmailChanged 挂在非 GET 的 superadmin 端点（GET 本身放行，挂载无害但
// 保持中间件顺序与 Express 一致）。
func (h *Admin) Register(g *gin.RouterGroup) {
	adminProtect := h.AuthMW.Protect(middleware.RoleCreator, middleware.RoleAdmin, middleware.RoleSuperAdmin)
	superProtect := h.AuthMW.Protect(middleware.RoleSuperAdmin)
	reqEmail := h.AuthMW.RequireEmailChanged()

	g.POST("/login", h.RL(ratelimit.AdminAuthSpec), h.Login)
	g.GET("/verify", adminProtect, h.Verify)
	g.GET("/pending-counts", adminProtect, h.PendingCounts)
	g.POST("/logout", adminProtect, h.Logout)
	g.GET("/list", superProtect, h.List)
	g.POST("/register", superProtect, reqEmail, h.RegisterAccount)
	g.DELETE("/:id", superProtect, reqEmail, h.DeleteAccount)
	g.GET("/users", superProtect, h.ListUsers)
	g.DELETE("/users/:id", superProtect, reqEmail, h.DeleteUser)
	g.PUT("/role/:id", superProtect, reqEmail, h.UpdateRole)
	g.GET("/creators", adminProtect, h.ListCreators)
	g.GET("/creator-profiles", superProtect, reqEmail, h.ListCreatorProfiles)
	g.GET("/creator-profiles/:id", superProtect, reqEmail, h.GetCreatorProfile)
	g.PUT("/creator-profiles/:id", superProtect, reqEmail, h.UpdateCreatorProfile)
	g.PUT("/creator-profiles/:id/approve", superProtect, reqEmail, h.ApproveCreatorProfile)
	g.PUT("/creator-profiles/:id/reject", superProtect, reqEmail, h.RejectCreatorProfile)
	g.POST("/verify-password", adminProtect, h.VerifyPassword)
	g.PUT("/user-admin-access/:id", superProtect, reqEmail, h.ToggleAdminAccess)
}

// ---- 端点 ----

// Login POST /api/admin/login（adminAuthLimiter 5/15min）。
// @Summary 管理员登录
// @Tags 管理后台
// @Accept json
// @Produce json
// @Param body body object true "username/account/email/password/altcha/screenWidth/screenHeight/language"
// @Success 200 {object} map[string]any "用户对象 + forceEmailChange"
// @Failure 400 {object} map[string]any "验证码错误/账号或密码错误/请输入账号"
// @Failure 403 {object} map[string]any "无管理后台权限/请先验证邮箱"
// @Failure 423 {object} map[string]any "账号已锁定"
// @Router /admin/login [post]
func (h *Admin) Login(c *gin.Context) {
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	username := asString(body["username"])
	account := asString(body["account"])
	email := asString(body["email"])
	password := asString(body["password"])
	screenWidth := toInt(body["screenWidth"])
	screenHeight := toInt(body["screenHeight"])
	language := asString(body["language"])
	altcha := asString(body["altcha"])

	if !h.verifyAdminAltcha(altcha, c.GetHeader("x-dev-token")) {
		c.JSON(400, gin.H{"message": "验证码错误或已过期"})
		return
	}
	// 登录标识符：兼容 username / account / email 三种字段名。
	identifier := account
	if identifier == "" {
		identifier = email
	}
	if identifier == "" {
		identifier = username
	}
	if identifier == "" {
		c.JSON(400, gin.H{"message": "请输入账号"})
		return
	}
	ctx := c.Request.Context()
	user, err := h.Repos.Users.AdminFindByIdentifierWithAuth(ctx, identifier)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(400, gin.H{"message": "用户名或密码错误"})
			return
		}
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if user.Role != "admin" && user.Role != "superadmin" {
		c.JSON(403, gin.H{"message": "无管理后台权限"})
		return
	}
	if repository.IsLocked(user) {
		c.JSON(423, gin.H{"message": "账号已被锁定，请30分钟后再试"})
		return
	}
	if !auth.Compare(user.Password, password) {
		_ = h.Repos.Users.IncLoginAttempts(ctx, user.ID)
		c.JSON(400, gin.H{"message": "用户名或密码错误"})
		return
	}
	_ = h.Repos.Users.ResetLoginAttempts(ctx, user.ID)

	// 邮箱验证检查（非生产 DEMO_EMAILS 跳过，对齐 admin.js DEMO_EMAILS 判定）。
	if !user.IsEmailVerified && !h.adminDemoEmail(user.Email) {
		c.JSON(403, gin.H{"message": "请先验证邮箱后再登录管理后台"})
		return
	}

	accessToken, err := h.AuthMW.Signer.Sign(user.ID.Hex(), "access", h.Config.JWT.AccessTTL, nil)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	refreshToken, err := h.AuthMW.Signer.Sign(user.ID.Hex(), "refresh", h.Config.JWT.RefreshTTL,
		map[string]string{"jti": auth.RandomHex(24)})
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}

	ua := c.Request.UserAgent()
	ip := h.clientIP(c)
	parsed := auth.ParseUserAgent(ua)
	di := model.DeviceInfo{
		Browser:        parsed.Browser,
		BrowserVersion: parsed.BrowserVersion,
		OS:             parsed.OS,
		OSVersion:      parsed.OSVersion,
		DeviceType:     parsed.DeviceType,
		DeviceModel:    parsed.DeviceModel,
		UserAgent:      ua,
	}
	if screenWidth != 0 {
		di.ScreenWidth = screenWidth
	}
	if screenHeight != 0 {
		di.ScreenHeight = screenHeight
	}
	if language != "" {
		di.Language = language
	}
	now := time.Now()
	session := &model.UserSession{
		UserID:           user.ID,
		RefreshTokenHash: auth.HashToken(refreshToken),
		DeviceInfo:       di,
		IP:               ip,
		IsActive:         true,
		LoginAt:          now,
		LastActiveAt:     now,
	}
	if err := h.Repos.Sessions.Create(ctx, session); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	// 更新最后登录信息（对齐 user.lastLoginAt/lastLoginIp + save）。
	if err := h.Repos.Users.AdminUpdateLastLogin(ctx, user.ID, now, ip); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	auth.SetAuthCookies(c, accessToken, refreshToken, !h.Config.IsDev)

	c.JSON(200, gin.H{
		"_id":              user.ID.Hex(),
		"username":         user.Username,
		"accountId":        user.AccountID,
		"email":            user.Email,
		"role":             user.Role,
		"avatar":           user.Avatar,
		"forceEmailChange": user.Role == "superadmin" && user.Email == adminDefaultSuperEmail,
	})
}

// Verify GET /api/admin/verify（adminProtect）。
// @Summary 校验管理令牌有效性
// @Tags 管理后台
// @Security bearerAuth
// @Success 200 {object} map[string]any "valid/admin"
// @Router /admin/verify [get]
func (h *Admin) Verify(c *gin.Context) {
	u, _ := middleware.GetUser(c)
	c.JSON(200, gin.H{
		"valid": true,
		"admin": gin.H{
			"_id":       u.ID.Hex(),
			"username":  u.Username,
			"accountId": u.AccountID,
			"email":     u.Email,
			"role":      u.Role,
			"avatar":    u.Avatar,
		},
	})
}

// PendingCounts GET /api/admin/pending-counts（adminProtect）。
// @Summary 待办数量统计
// @Tags 管理后台
// @Security bearerAuth
// @Success 200 {object} map[string]any "episodes/reports/feedbacks/friendLinks"
// @Router /admin/pending-counts [get]
func (h *Admin) PendingCounts(c *gin.Context) {
	ctx := c.Request.Context()
	episodes, err := h.Repos.Episodes.AdminCountPendingReview(ctx)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	reports, err := h.Repos.Reports.AdminCountStatus(ctx, "pending")
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	feedbacks, err := h.Repos.Feedbacks.AdminCountStatus(ctx, "pending")
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	friendLinks, err := h.Repos.FriendLinks.AdminCountStatus(ctx, "pending")
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, gin.H{"episodes": episodes, "reports": reports, "feedbacks": feedbacks, "friendLinks": friendLinks})
}

// Logout POST /api/admin/logout（adminProtect）。双 Token 登出并清 cookie。
// @Summary 管理员退出登录
// @Tags 管理后台
// @Security bearerAuth
// @Success 200 {object} map[string]string "message"
// @Router /admin/logout [post]
func (h *Admin) Logout(c *gin.Context) {
	ctx := c.Request.Context()
	if refreshToken := auth.GetRefreshToken(c); refreshToken != "" {
		if _, err := h.Repos.Sessions.FindAndDeactivateRefresh(ctx, auth.HashToken(refreshToken)); err != nil {
			// 无匹配 session 不影响登出（对齐 findOneAndUpdate 无匹配静默）。
		}
	}
	if v, ok := c.Get(middleware.ContextAuthTokenKey); ok {
		if accessToken, ok := v.(string); ok && accessToken != "" {
			_ = h.Repos.Sessions.DeactivateByTokenHash(ctx, auth.HashToken(accessToken))
		}
	}
	auth.ClearAuthCookies(c, !h.Config.IsDev)
	c.JSON(200, gin.H{"message": "退出成功"})
}

// List GET /api/admin/list（superAdminProtect）：管理/创作者账户分页列表。
// @Summary 管理/创作者账户列表
// @Tags 管理后台
// @Security bearerAuth
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 20，上限 100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /admin/list [get]
func (h *Admin) List(c *gin.Context) {
	pageNum := 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		pageNum = p
	}
	limitNum := 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limitNum = l
	}
	if limitNum > 100 {
		limitNum = 100
	}
	ctx := c.Request.Context()
	total, err := h.Repos.Users.AdminCountRoles(ctx, adminManagerRoles)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	totalPages := adminTotalPages(total, limitNum)
	users, err := h.Repos.Users.AdminFindRoles(ctx, adminManagerRoles, pageNum, limitNum)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	list := make([]gin.H, 0, len(users))
	for i := range users {
		list = append(list, adminUserJSON(&users[i]))
	}
	c.JSON(200, gin.H{"list": list, "page": pageNum, "limit": limitNum, "total": total, "totalPages": totalPages})
}

// RegisterAccount POST /api/admin/register（superAdminProtect + requireEmailChanged）。
// @Summary 创建管理/创作者账户
// @Tags 管理后台
// @Security bearerAuth
// @Accept json
// @Param body body object true "username/email/password/role/accountId"
// @Success 200 {object} map[string]any "用户对象 + message"
// @Failure 400 {object} map[string]any "校验失败"
// @Router /admin/register [post]
func (h *Admin) RegisterAccount(c *gin.Context) {
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	username := asString(body["username"])
	email := asString(body["email"])
	password := asString(body["password"])
	role := orDefaultString(body["role"], "admin")
	accountId := asString(body["accountId"])

	if msg := service.ValidatePassword(password); msg != "" {
		c.JSON(400, gin.H{"message": msg})
		return
	}
	if email == "" {
		c.JSON(400, gin.H{"message": "请输入邮箱"})
		return
	}
	if role != "admin" && role != "creator" {
		c.JSON(400, gin.H{"message": "无效的角色，仅可创建 admin 或 creator"})
		return
	}
	ctx := c.Request.Context()
	emailExists, err := h.Repos.Users.ExistsByEmail(ctx, email)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if emailExists {
		c.JSON(400, gin.H{"message": "该邮箱已注册"})
		return
	}
	// 自动生成 accountId。
	finalAccountId := accountId
	if finalAccountId == "" {
		baseId := adminBaseAccountID(username, email)
		finalAccountId = baseId
		counter := 1
		for {
			exists, err := h.Repos.Users.ExistsByAccountID(ctx, finalAccountId)
			if err != nil {
				c.JSON(500, gin.H{"message": "服务器错误"})
				return
			}
			if !exists {
				break
			}
			finalAccountId = fmt.Sprintf("%s_%d", baseId, counter)
			counter++
		}
	} else {
		idExists, err := h.Repos.Users.ExistsByAccountID(ctx, finalAccountId)
		if err != nil {
			c.JSON(500, gin.H{"message": "服务器错误"})
			return
		}
		if idExists {
			c.JSON(400, gin.H{"message": "该账号ID已存在"})
			return
		}
	}

	hash, err := auth.Hash(password)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	user := &model.User{
		// UserRepo.Create 不像 EpisodeRepo.Create 那样补默认 _id（mongoose 自动
		// 生成），这里显式分配，保证响应与 CreatorProfile.creatorId 使用真实 ID。
		ID:                     primitive.NewObjectID(),
		AccountID:              finalAccountId,
		Username:               orDefaultString(username, finalAccountId),
		Email:                  email,
		Password:               hash,
		Role:                   role,
		IsEmailVerified:        true,
		EmailNotificationPrefs: model.DefaultEmailNotificationPrefs(),
		BackgroundPrefs:        model.DefaultBackgroundPrefs(),
		PersonalWallpapers:     []model.Wallpaper{},
		CreatedAt:              time.Now(),
	}
	if err := h.Repos.Users.Create(ctx, user); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}

	// 创作者角色账号自动创建 CreatorProfile（失败静默，对齐内层 try/catch）。
	if role == "creator" {
		_ = h.ensureCreatorProfile(ctx, user.ID, user.Username)
	}

	// 发送"账号已创建"通知邮件（fire-and-forget）。
	h.sendAccountCreatedEmail(ctx, email, role, finalAccountId)

	c.JSON(200, gin.H{
		"_id":       user.ID.Hex(),
		"username":  user.Username,
		"accountId": user.AccountID,
		"email":     user.Email,
		"role":      user.Role,
		"message":   "账号创建成功，通知邮件已发送至用户邮箱",
	})
}

// DeleteAccount DELETE /api/admin/:id（superAdminProtect + requireEmailChanged）。
// @Summary 删除管理/创作者账户
// @Tags 管理后台
// @Security bearerAuth
// @Param id path string true "账户 ID"
// @Success 200 {object} map[string]string "message"
// @Failure 400 {object} map[string]any "不能删除自己/最后一个超管/非管理账户"
// @Failure 404 {object} map[string]any "账户不存在"
// @Router /admin/{id} [delete]
func (h *Admin) DeleteAccount(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		// 对齐 Express 非法 ID → CastError → 500。
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	user, _ := middleware.GetUser(c)
	if user.ID.Hex() == idStr {
		c.JSON(400, gin.H{"message": "不能删除自己的账号"})
		return
	}
	ctx := c.Request.Context()
	target, err := h.Repos.Users.FindByID(ctx, oid)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "账户不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if target.Role != "admin" && target.Role != "superadmin" && target.Role != "creator" {
		c.JSON(400, gin.H{"message": "该账户不是管理/创作者账户"})
		return
	}
	if target.Role == "superadmin" {
		count, err := h.Repos.Users.AdminCountRole(ctx, "superadmin")
		if err != nil {
			c.JSON(500, gin.H{"message": "服务器错误"})
			return
		}
		if count <= 1 {
			c.JSON(400, gin.H{"message": "不能删除最后一个超级管理员"})
			return
		}
	}
	if err := h.Repos.Users.DeleteByID(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Sessions.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, gin.H{"message": "账户已删除"})
}

// ListUsers GET /api/admin/users（superAdminProtect）：全量用户分页列表（可搜索）。
// @Summary 用户列表
// @Tags 管理后台
// @Security bearerAuth
// @Param page query int false "页码（默认 1）"
// @Param limit query int false "每页数量（默认 20，上限 200）"
// @Param search query string false "关键词（accountId/username/email）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /admin/users [get]
func (h *Admin) ListUsers(c *gin.Context) {
	pageNum := 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil {
		pageNum = p
	}
	limitNum := 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		limitNum = l
	}
	if limitNum < 1 {
		limitNum = 1
	}
	if limitNum > 200 {
		limitNum = 200
	}
	search := c.Query("search")
	ctx := c.Request.Context()
	total, err := h.Repos.Users.AdminCountSearch(ctx, search)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	totalPages := adminTotalPages(total, limitNum)
	users, err := h.Repos.Users.AdminFindSearch(ctx, search, pageNum, limitNum)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	list := make([]gin.H, 0, len(users))
	for i := range users {
		list = append(list, adminUserJSON(&users[i]))
	}
	c.JSON(200, gin.H{"list": list, "page": pageNum, "limit": limitNum, "total": total, "totalPages": totalPages})
}

// DeleteUser DELETE /api/admin/users/:id（superAdminProtect + requireEmailChanged）。
// 完整级联删除用户关联数据并重算受影响剧集评分。
// @Summary 删除用户（级联清理）
// @Tags 管理后台
// @Security bearerAuth
// @Param id path string true "用户 ID"
// @Success 200 {object} map[string]string "message"
// @Failure 400 {object} map[string]any "不能删除自己/最后一个超管"
// @Failure 404 {object} map[string]any "用户不存在"
// @Router /admin/users/{id} [delete]
func (h *Admin) DeleteUser(c *gin.Context) {
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	user, _ := middleware.GetUser(c)
	if user.ID.Hex() == idStr {
		c.JSON(400, gin.H{"message": "不能删除自己的账号"})
		return
	}
	ctx := c.Request.Context()
	target, err := h.Repos.Users.FindByID(ctx, oid)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "用户不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if target.Role == "superadmin" {
		count, err := h.Repos.Users.AdminCountRole(ctx, "superadmin")
		if err != nil {
			c.JSON(500, gin.H{"message": "服务器错误"})
			return
		}
		if count <= 1 {
			c.JSON(400, gin.H{"message": "不能删除最后一个超级管理员"})
			return
		}
	}
	if err := h.Repos.Users.DeleteByID(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Follows.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Histories.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Notifications.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Favorites.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	// 对齐 admin.js：Report.deleteMany({ reporter: idStr })（legacy 字段名，照抄）。
	if err := h.Repos.Reports.AdminDeleteReportsByLegacyReporter(ctx, idStr); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Feedbacks.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Sessions.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Users.AdminDeletePushSubscriptionsByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Folders.AdminDeleteManyByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	// 先取该用户评分，删除后重算受影响剧集的平均分/评分人数。
	userRatings, err := h.Repos.Ratings.FindByUser(ctx, oid)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if err := h.Repos.Ratings.DeleteByUser(ctx, oid); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	affected := make([]primitive.ObjectID, 0, len(userRatings))
	seen := map[string]bool{}
	for _, r := range userRatings {
		key := r.EpisodeID.Hex()
		if !seen[key] {
			seen[key] = true
			affected = append(affected, r.EpisodeID)
		}
	}
	if len(affected) > 0 {
		if err := h.Repos.Episodes.AdminRebuildEpisodeRatings(ctx, affected); err != nil {
			c.JSON(500, gin.H{"message": "服务器错误"})
			return
		}
	}
	c.JSON(200, gin.H{"message": "用户已删除"})
}

// UpdateRole PUT /api/admin/role/:id（superAdminProtect + requireEmailChanged）。
// @Summary 修改账户角色
// @Tags 管理后台
// @Security bearerAuth
// @Accept json
// @Param id path string true "账户 ID"
// @Param body body object true "role"
// @Success 200 {object} map[string]any "用户对象"
// @Failure 400 {object} map[string]any "无效的角色/不能修改自己/最后一个超管"
// @Failure 404 {object} map[string]any "账户不存在"
// @Router /admin/role/{id} [put]
func (h *Admin) UpdateRole(c *gin.Context) {
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	role := asString(body["role"])
	if !adminContainsRole(adminUserRoles, role) {
		c.JSON(400, gin.H{"message": "无效的角色"})
		return
	}
	idStr := c.Param("id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	user, _ := middleware.GetUser(c)
	if user.ID.Hex() == idStr {
		c.JSON(400, gin.H{"message": "不能修改自己的角色"})
		return
	}
	ctx := c.Request.Context()
	target, err := h.Repos.Users.FindByID(ctx, oid)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "账户不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if target.Role == "superadmin" {
		count, err := h.Repos.Users.AdminCountRole(ctx, "superadmin")
		if err != nil {
			c.JSON(500, gin.H{"message": "服务器错误"})
			return
		}
		if count <= 1 {
			c.JSON(400, gin.H{"message": "不能降级最后一个超级管理员"})
			return
		}
	}
	updated, err := h.Repos.Users.AdminFindByIDAndSetRole(ctx, oid.Hex(), role)
	if err != nil {
		if repository.IsNotFound(err) {
			// 竞态：target 校验后文档被删 → findByIdAndUpdate null → res.json(null)。
			c.JSON(200, nil)
			return
		}
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	// 角色改为 creator 时自动创建 CreatorProfile（若尚不存在，失败静默）。
	if role == "creator" && target.Role != "creator" {
		_ = h.ensureCreatorProfile(ctx, oid, updated.Username)
	}
	c.JSON(200, adminUserJSON(updated))
}

// ListCreators GET /api/admin/creators（adminProtect）。
// @Summary 创作者列表
// @Tags 管理后台
// @Security bearerAuth
// @Success 200 {array} map[string]any
// @Router /admin/creators [get]
func (h *Admin) ListCreators(c *gin.Context) {
	creators, err := h.Repos.Users.FindCreatorsByRole(c.Request.Context(), "creator")
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	list := make([]gin.H, 0, len(creators))
	for i := range creators {
		list = append(list, adminUserJSON(&creators[i]))
	}
	c.JSON(200, list)
}

// ListCreatorProfiles GET /api/admin/creator-profiles（superAdminProtect）。
// @Summary 创作者主页列表（含创作者用户信息）
// @Tags 管理后台
// @Security bearerAuth
// @Success 200 {array} map[string]any
// @Router /admin/creator-profiles [get]
func (h *Admin) ListCreatorProfiles(c *gin.Context) {
	ctx := c.Request.Context()
	profiles, err := h.Repos.CreatorProfiles.AdminFindAllProfiles(ctx)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	ids := make([]primitive.ObjectID, 0, len(profiles))
	for i := range profiles {
		ids = append(ids, profiles[i].CreatorID)
	}
	refs, err := h.Repos.Users.AdminFindUserRefsByIDs(ctx, dedupIDs(ids))
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	list := make([]gin.H, 0, len(profiles))
	for i := range profiles {
		list = append(list, adminCreatorProfileJSON(&profiles[i], refs))
	}
	c.JSON(200, list)
}

// GetCreatorProfile GET /api/admin/creator-profiles/:id（superAdminProtect）。
// @Summary 创作者主页详情
// @Tags 管理后台
// @Security bearerAuth
// @Param id path string true "主页 ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any "创作者主页不存在"
// @Router /admin/creator-profiles/{id} [get]
func (h *Admin) GetCreatorProfile(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	ctx := c.Request.Context()
	profile, err := h.Repos.CreatorProfiles.AdminFindProfileByID(ctx, oid)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "创作者主页不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	refs, err := h.Repos.Users.AdminFindUserRefsByIDs(ctx, []primitive.ObjectID{profile.CreatorID})
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, adminCreatorProfileJSON(profile, refs))
}

// UpdateCreatorProfile PUT /api/admin/creator-profiles/:id（superAdminProtect +
// requireEmailChanged）。超管直接编辑视为终态：清空待审核改动并标记已通过。
// @Summary 编辑创作者主页
// @Tags 管理后台
// @Security bearerAuth
// @Accept json
// @Param id path string true "主页 ID"
// @Param body body object true "displayName/avatar/bio/socialLinks/qqGroupLink"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any "创作者主页不存在"
// @Router /admin/creator-profiles/{id} [put]
func (h *Admin) UpdateCreatorProfile(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	set := bson.M{}
	if v, ok := body["displayName"]; ok {
		set["displayName"] = asString(v)
	}
	if v, ok := body["avatar"]; ok {
		set["avatar"] = asString(v)
	}
	if v, ok := body["bio"]; ok {
		bio := asString(v)
		if len([]rune(bio)) > 500 {
			bio = string([]rune(bio)[:500])
		}
		set["bio"] = bio
	}
	set["socialLinks"] = toStrMap(body["socialLinks"])
	set["qqGroupLink"] = orDefaultString(body["qqGroupLink"], "")
	// 超管直接编辑视为终态：清空待审核改动并标记为已通过。
	set["pendingChanges"] = adminEmptyPendingChanges()
	set["reviewStatus"] = "approved"
	set["reviewNote"] = ""
	set["updatedAt"] = time.Now()
	ctx := c.Request.Context()
	updated, err := h.Repos.CreatorProfiles.AdminUpdateProfile(ctx, oid, set, false)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "创作者主页不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, adminCreatorProfileJSON(updated, nil))
}

// ApproveCreatorProfile PUT /api/admin/creator-profiles/:id/approve（superAdminProtect +
// requireEmailChanged）。通过 → 将 pendingChanges 应用到正式字段并通知创作者。
// @Summary 通过创作者主页修改
// @Tags 管理后台
// @Security bearerAuth
// @Param id path string true "主页 ID"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any "创作者主页不存在"
// @Router /admin/creator-profiles/{id}/approve [put]
func (h *Admin) ApproveCreatorProfile(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	ctx := c.Request.Context()
	profile, err := h.Repos.CreatorProfiles.AdminFindProfileByID(ctx, oid)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "创作者主页不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	set := adminApplyPendingChanges(profile.PendingChanges)
	set["pendingChanges"] = adminEmptyPendingChanges()
	set["reviewStatus"] = "approved"
	set["reviewNote"] = ""
	set["updatedAt"] = time.Now()
	updated, err := h.Repos.CreatorProfiles.AdminUpdateProfile(ctx, oid, set, true)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "创作者主页不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	// 通知创作者主页修改已通过（站内通知 + Web Push 占位）。
	h.notifyProfileReviewResult(updated, "approved", "")
	c.JSON(200, adminCreatorProfileJSON(updated, nil))
}

// RejectCreatorProfile PUT /api/admin/creator-profiles/:id/reject（superAdminProtect +
// requireEmailChanged）。拒绝 → 保留 pendingChanges 供创作者修改重提，正式字段不变。
// @Summary 拒绝创作者主页修改
// @Tags 管理后台
// @Security bearerAuth
// @Accept json
// @Param id path string true "主页 ID"
// @Param body body object true "note"
// @Success 200 {object} map[string]any
// @Failure 404 {object} map[string]any "创作者主页不存在"
// @Router /admin/creator-profiles/{id}/reject [put]
func (h *Admin) RejectCreatorProfile(c *gin.Context) {
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	note := asString(body["note"])
	if len([]rune(note)) > 500 {
		note = string([]rune(note)[:500])
	}
	ctx := c.Request.Context()
	set := bson.M{
		"reviewStatus": "rejected",
		"reviewNote":   note,
		"updatedAt":    time.Now(),
	}
	updated, err := h.Repos.CreatorProfiles.AdminUpdateProfile(ctx, oid, set, true)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "创作者主页不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	// 通知创作者主页修改未通过（站内通知 + Web Push 占位）。
	h.notifyProfileReviewResult(updated, "rejected", note)
	c.JSON(200, adminCreatorProfileJSON(updated, nil))
}

// VerifyPassword POST /api/admin/verify-password（adminProtect）。
// @Summary 校验管理员密码
// @Tags 管理后台
// @Security bearerAuth
// @Accept json
// @Param body body object true "password"
// @Success 200 {object} map[string]bool "verified"
// @Failure 400 {object} map[string]any "请输入密码/密码错误"
// @Failure 404 {object} map[string]any "未找到"
// @Router /admin/verify-password [post]
func (h *Admin) VerifyPassword(c *gin.Context) {
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	password := asString(body["password"])
	if password == "" {
		c.JSON(400, gin.H{"message": "请输入密码"})
		return
	}
	user, _ := middleware.GetUser(c)
	full, err := h.Repos.Users.FindByIDWithAuth(c.Request.Context(), user.ID)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "未找到"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if !auth.Compare(full.Password, password) {
		c.JSON(400, gin.H{"message": "密码错误"})
		return
	}
	c.JSON(200, gin.H{"verified": true})
}

// ToggleAdminAccess PUT /api/admin/user-admin-access/:id（superAdminProtect +
// requireEmailChanged）。切换用户的管理后台权限（user <-> admin）。
// @Summary 切换用户管理权限
// @Tags 管理后台
// @Security bearerAuth
// @Accept json
// @Param id path string true "用户 ID"
// @Param body body object true "adminAccess"
// @Success 200 {object} map[string]any "message/adminAccess/role"
// @Failure 400 {object} map[string]any "参数错误/不能修改超管或创作者权限"
// @Failure 404 {object} map[string]any "用户不存在"
// @Router /admin/user-admin-access/{id} [put]
func (h *Admin) ToggleAdminAccess(c *gin.Context) {
	var body map[string]any
	_ = c.ShouldBindJSON(&body)
	adminAccess, ok := body["adminAccess"].(bool)
	if !ok {
		c.JSON(400, gin.H{"message": "参数错误"})
		return
	}
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	ctx := c.Request.Context()
	user, err := h.Repos.Users.FindByID(ctx, oid)
	if repository.IsNotFound(err) {
		c.JSON(404, gin.H{"message": "用户不存在"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	if user.Role == "superadmin" || user.Role == "creator" {
		c.JSON(400, gin.H{"message": "不能修改超级管理员或创作者的权限"})
		return
	}
	newRole := "user"
	message := "已撤销管理后台权限"
	if adminAccess {
		newRole = "admin"
		message = "已授予管理后台权限"
	}
	if err := h.Repos.Users.AdminSetRole(ctx, oid, newRole); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, gin.H{"message": message, "adminAccess": adminAccess, "role": newRole})
}

// ---- 通知 / 邮件联动（对齐 admin.js notifyProfileReviewResult 与 register 邮件）----

// notifyProfileReviewResult 创作者主页审核结果站内通知（对齐 admin.js 的
// Notification.create，fire-and-forget）。Web Push 在 neo-server 未实现，占位跳过。
func (h *Admin) notifyProfileReviewResult(profile *model.CreatorProfile, status, note string) {
	message := "您的创作者主页修改已通过审核"
	if status != "approved" {
		message = "您的创作者主页修改未通过审核"
		if note != "" {
			message += "：" + note
		}
	}
	notif := &model.Notification{
		UserID:    profile.CreatorID,
		Type:      "profile_review",
		Message:   message,
		Link:      "/admin/creator-profile",
		Metadata:  primitive.M{"status": status, "note": note},
		CreatedAt: time.Now(),
	}
	_ = h.Repos.Notifications.Create(context.Background(), notif)
}

// ensureCreatorProfile 为角色为 creator 的账户自动创建默认创作者主页（对齐 admin.js
// 内层 try/catch：已存在则跳过，失败静默）。
func (h *Admin) ensureCreatorProfile(ctx context.Context, creatorID primitive.ObjectID, username string) error {
	existing, err := h.Repos.CreatorProfiles.FindByCreator(ctx, creatorID)
	if err == nil && existing != nil {
		return nil
	}
	if err != nil && !repository.IsNotFound(err) {
		return err
	}
	return h.Repos.CreatorProfiles.Create(ctx, &model.CreatorProfile{
		CreatorID:      creatorID,
		DisplayName:    orDefaultString(username, "创作者"),
		Bio:            "这位创作者还没有填写个人简介。",
		SocialLinks:    map[string]string{},
		PendingChanges: adminEmptyPendingChanges(),
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	})
}

// sendAccountCreatedEmail 发送"账号已创建"通知邮件（对齐 admin.js register 的
// transporter.sendMail(...).catch(()=>{})，fire-and-forget 语义）。
func (h *Admin) sendAccountCreatedEmail(ctx context.Context, to, role, accountId string) {
	if h.Mail == nil {
		return
	}
	siteUrl := h.Config.Server.FrontendURL
	if siteUrl == "" {
		siteUrl = h.Config.Server.SiteURL
	}
	if siteUrl == "" {
		siteUrl = "http://localhost:3000"
	}
	roleLabel := "管理员"
	if role != "admin" {
		roleLabel = "创作者"
	}
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">账号创建通知</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">管理员已为您创建了账号，您可以使用以下信息登录：</p>` +
		email.EmailInfoBox(
			`<p style="margin:4px 0;"><strong>账号ID：</strong>`+adminEscapeHtml(accountId)+`</p>`+
				`<p style="margin:4px 0;"><strong>邮箱：</strong>`+adminEscapeHtml(to)+`</p>`+
				`<p style="margin:4px 0;"><strong>角色：</strong>`+adminEscapeHtml(roleLabel)+`</p>`,
			"info",
		) +
		`<p style="margin:16px 0;color:#475569;font-size:14px;">请使用管理员告知的密码登录。登录后请尽快在个人设置中修改密码。</p>` +
		`<p style="margin:20px 0;">` + email.EmailButton("前往登录", siteUrl+"/login", "primary") + `</p>` +
		`<p style="margin:0;color:#94a3b8;font-size:12px;">如果您没有预期收到此邮件，请忽略。</p>`
	go h.Mail.SendNotificationEmail(context.Background(), to, "您的账号已创建", body, "")
}

// ---- DTO 组装 ----

// adminUserJSON 组装用户列表响应对象（对齐 mongoose User 文档 toJSON，
// 不含 password/2FA 密文/锁定字段）。personalWallpapers 空切片补 []。
func adminUserJSON(u *model.User) gin.H {
	wallpapers := u.PersonalWallpapers
	if wallpapers == nil {
		wallpapers = []model.Wallpaper{}
	}
	return gin.H{
		"_id":                    u.ID.Hex(),
		"accountId":              u.AccountID,
		"username":               u.Username,
		"email":                  u.Email,
		"avatar":                 u.Avatar,
		"deviceInfo":             u.DeviceInfo,
		"lastLoginAt":            u.LastLoginAt,
		"lastLoginIp":            u.LastLoginIP,
		"lastLoginRegion":        u.LastLoginRegion,
		"deletionRequestedAt":    u.DeletionRequestedAt,
		"isEmailVerified":        u.IsEmailVerified,
		"role":                   u.Role,
		"passwordChangedAt":      u.PasswordChangedAt,
		"twoFactorEnabled":       u.TwoFactorEnabled,
		"emailNotificationPrefs": u.EmailNotificationPrefs,
		"backgroundPrefs":        u.BackgroundPrefs,
		"personalWallpapers":     wallpapers,
		"createdAt":              u.CreatedAt,
		"__v":                    u.VersionKey,
	}
}

// adminCreatorProfileJSON 组装创作者主页响应对象。refs 为 nil 时 creatorId 输出
// hex 字符串（未 populate 场景，如 PUT 响应）；非 nil 时输出
// {_id, accountId, username, email}，ref 不存在则置 null（对齐 populate 行为）。
func adminCreatorProfileJSON(p *model.CreatorProfile, refs map[string]repository.AdminUserRef) gin.H {
	var creatorRef any = p.CreatorID.Hex()
	if refs != nil {
		if u, ok := refs[p.CreatorID.Hex()]; ok {
			creatorRef = gin.H{"_id": u.ID.Hex(), "accountId": u.AccountID, "username": u.Username, "email": u.Email}
		} else {
			creatorRef = nil
		}
	}
	social := p.SocialLinks
	if social == nil {
		social = map[string]string{}
	}
	return gin.H{
		"_id":            p.ID.Hex(),
		"creatorId":      creatorRef,
		"displayName":    p.DisplayName,
		"avatar":         p.Avatar,
		"bio":            p.Bio,
		"socialLinks":    social,
		"qqGroupLink":    p.QqGroupLink,
		"reviewStatus":   p.ReviewStatus,
		"reviewNote":     p.ReviewNote,
		"pendingChanges": orEmptyM(p.PendingChanges),
		"createdAt":      p.CreatedAt,
		"updatedAt":      p.UpdatedAt,
		"__v":            p.VersionKey,
	}
}

// ---- 工具函数 ----

// verifyAdminAltcha 校验 altcha payload；DEV_API_TOKEN 配置且 x-dev-token 匹配时
// 直接通过（对齐 admin.js verifyAdminAltcha）。
func (h *Admin) verifyAdminAltcha(payload, devToken string) bool {
	if h.Config.JWT.DevAPIToken != "" && devToken == h.Config.JWT.DevAPIToken {
		return true
	}
	if payload == "" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	var p altchalib.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	res, err := altchalib.VerifySolution(altchalib.VerifySolutionOptions{
		Challenge:           p.Challenge,
		Solution:            p.Solution,
		DeriveKey:           altchalib.DeriveKeySHA(),
		HMACSignatureSecret: h.altchaHMACSecret(),
	})
	return err == nil && res.Verified
}

// altchaHMACSecret 派生 altcha HMAC 密钥：优先配置，缺省 sha256("altcha-"+JWT_SECRET)
// 的 hex（对齐 utils/altcha.js:8 与 service.AuthService.AltchaHMACSecret）。
func (h *Admin) altchaHMACSecret() string {
	if k := h.Config.JWT.AltchaHMACKey; k != "" {
		return k
	}
	sum := sha256.Sum256([]byte("altcha-" + h.Config.JWT.Secret))
	return hex.EncodeToString(sum[:])
}

// adminDemoEmail 判断邮箱是否在 DEMO_EMAILS 跳过列表中（仅非生产生效，
// 对齐 admin.js DEMO_EMAILS 的 NODE_ENV 判定）。
func (h *Admin) adminDemoEmail(email string) bool {
	if !h.Config.IsDev {
		return false
	}
	lower := strings.ToLower(email)
	for _, d := range h.Config.JWT.DemoEmails {
		if strings.EqualFold(d, lower) {
			return true
		}
	}
	return false
}

// adminNonWordRe 匹配非 \w 字符（对齐 admin.js 的 /[^\w]/g）。
var adminNonWordRe = regexp.MustCompile(`[^\w]`)

// adminBaseAccountID 生成 accountId 基础串（对齐 admin.js
// (username || email).replace(/[^\w]/g,'_').toLowerCase()）。
func adminBaseAccountID(username, email string) string {
	src := username
	if src == "" {
		src = email
	}
	src = adminNonWordRe.ReplaceAllString(src, "_")
	return strings.ToLower(src)
}

// adminEmptyPendingChanges 返回待审核改动重置值（对齐 admin.js 的
// {displayName:”, avatar:”, bio:”, socialLinks:{}, qqGroupLink:”}）。
func adminEmptyPendingChanges() primitive.M {
	return primitive.M{
		"displayName": "",
		"avatar":      "",
		"bio":         "",
		"socialLinks": primitive.M{},
		"qqGroupLink": "",
	}
}

// adminApplyPendingChanges 把 pendingChanges 应用到正式字段（对齐 admin.js approve
// 分支：displayName 仅非空应用，其余字段存在即应用，socialLinks 非 falsy 应用）。
func adminApplyPendingChanges(pc primitive.M) bson.M {
	set := bson.M{}
	if s, ok := pc["displayName"].(string); ok && s != "" {
		set["displayName"] = s
	}
	if v, ok := pc["avatar"]; ok {
		set["avatar"] = v
	}
	if v, ok := pc["bio"]; ok {
		set["bio"] = v
	}
	if v, ok := pc["socialLinks"]; ok && truthy(v) {
		set["socialLinks"] = v
	}
	if v, ok := pc["qqGroupLink"]; ok {
		set["qqGroupLink"] = v
	}
	return set
}

// adminTotalPages 计算总页数（对齐 Math.ceil(total/limit)；limit<=0 时避免除零）。
func adminTotalPages(total int64, limit int) int {
	if limit <= 0 {
		return 0
	}
	return (int(total) + limit - 1) / limit
}

// adminContainsRole 判断角色枚举包含指定角色。
func adminContainsRole(roles []string, r string) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

// adminEscapeHtml 转义 HTML（对齐 utils/helpers.js escapeHtml）。
func adminEscapeHtml(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&#039;")
	return s
}

// clientIP 提取客户端 IP（对齐 helpers.js getClientIp：XFF 首值 → x-real-ip →
// RemoteAddr）。
func (h *Admin) clientIP(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		if first := ratelimit.NormalizeXFF(xff); first != "" {
			return first
		}
	}
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}
