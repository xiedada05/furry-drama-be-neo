package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/service"
)

// Auth 是认证/用户域 handler 容器。
type Auth struct {
	Svc    *service.AuthService
	AuthMW *middleware.Auth
	Config *config.Config
}

// NewAuth 构造 handler 容器。
func NewAuth(svc *service.AuthService, amw *middleware.Auth, cfg *config.Config) *Auth {
	return &Auth{Svc: svc, AuthMW: amw, Config: cfg}
}

// clientIP 提取请求 IP。
func (h *Auth) clientIP(c *gin.Context) string {
	return h.Svc.ClientIP(c)
}

// devToken 读取 x-dev-token 头。
func (h *Auth) devToken(c *gin.Context) string {
	return c.GetHeader("x-dev-token")
}

// ---- captcha ----

// Captcha GET /api/auth/captcha：生成 altcha 挑战。
// @Summary 获取验证码挑战
// @Tags 认证
// @Produce json
// @Success 200 {object} map[string]any "挑战参数与签名"
// @Router /auth/captcha [get]
func (h *Auth) Captcha(c *gin.Context) {
	ch, err := h.Svc.CreateCaptcha()
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, ch)
}

// ---- check-accountId ----

// CheckAccountID GET /api/auth/check-accountId?accountId=：返回账号ID是否可用。
// @Summary 检查账号ID是否可用
// @Tags 认证
// @Param accountId query string true "账号ID"
// @Success 200 {object} map[string]bool "available"
// @Router /auth/check-accountId [get]
func (h *Auth) CheckAccountID(c *gin.Context) {
	accountID := c.Query("accountId")
	if accountID == "" {
		c.JSON(400, gin.H{"message": "accountId is required"})
		return
	}
	exists, err := h.Svc.Repos.Users.ExistsByAccountID(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, gin.H{"available": !exists})
}

// ---- register ----

type registerRequest struct {
	AccountID  string                  `json:"accountId"`
	Username   string                  `json:"username"`
	Email      string                  `json:"email"`
	Password   string                  `json:"password"`
	DeviceInfo *auth.DeviceInfoPayload `json:"deviceInfo"`
	Altcha     string                  `json:"altcha"`
}

// Register POST /api/auth/register。
// @Summary 用户注册
// @Tags 认证
// @Accept json
// @Produce json
// @Param body body registerRequest true "注册信息"
// @Success 200 {object} map[string]any "message/email/needVerification"
// @Failure 400 {object} map[string]any
// @Router /auth/register [post]
func (h *Auth) Register(c *gin.Context) {
	var req registerRequest
	_ = c.ShouldBindJSON(&req)
	res, err := h.Svc.Register(c.Request.Context(), service.RegisterInput{
		AccountID:  req.AccountID,
		Username:   req.Username,
		Email:      req.Email,
		Password:   req.Password,
		DeviceInfo: req.DeviceInfo,
		Altcha:     req.Altcha,
		Ua:         c.Request.UserAgent(),
		DevToken:   h.devToken(c),
		AcceptLang: c.GetHeader("Accept-Language"),
		IP:         h.clientIP(c),
	})
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "注册成功，验证码已发送至您的邮箱", "email": res.Email, "needVerification": true})
}

// ---- login ----

type loginRequest struct {
	Email      string                  `json:"email"`
	Password   string                  `json:"password"`
	DeviceInfo *auth.DeviceInfoPayload `json:"deviceInfo"`
	Altcha     string                  `json:"altcha"`
}

// Login POST /api/auth/login。
// @Summary 用户登录
// @Tags 认证
// @Accept json
// @Produce json
// @Param body body loginRequest true "登录信息"
// @Success 200 {object} map[string]any "用户对象或 need2FA"
// @Failure 400 {object} map[string]any
// @Failure 403 {object} map[string]any "needVerification/needDeviceVerify"
// @Router /auth/login [post]
func (h *Auth) Login(c *gin.Context) {
	var req loginRequest
	_ = c.ShouldBindJSON(&req)
	res, err := h.Svc.Login(c, service.LoginInput{
		Email:      req.Email,
		Password:   req.Password,
		DeviceInfo: req.DeviceInfo,
		Altcha:     req.Altcha,
		Ua:         c.Request.UserAgent(),
		DevToken:   h.devToken(c),
		AcceptLang: c.GetHeader("Accept-Language"),
		IP:         h.clientIP(c),
	})
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	switch {
	case res.User != nil:
		c.JSON(200, h.Svc.SessionUserJSON(res.User))
	case res.NeedVerification:
		c.JSON(403, gin.H{
			"message":          "请先验证邮箱后再登录，验证码已发送至您的邮箱",
			"needVerification": true,
			"email":            res.Email,
		})
	case res.NeedDeviceVerify:
		c.JSON(403, gin.H{
			"message":          "检测到新设备登录，验证码已发送至您的邮箱",
			"needDeviceVerify": true,
			"email":            res.Email,
			"deviceInfo":       res.DeviceVerifyInfo,
		})
	case res.Need2FA:
		c.JSON(200, gin.H{
			"need2FA":            true,
			"email":              res.Email,
			"twoFactorChallenge": res.TwoFactorChallenge,
		})
	default:
		c.JSON(500, gin.H{"message": "服务器错误"})
	}
}

// ---- logout ----

// Logout POST /api/auth/logout（protect）。吊销会话并清 cookie。
// @Summary 退出登录
// @Tags 认证
// @Security bearerAuth
// @Success 200 {object} map[string]string "message"
// @Router /auth/logout [post]
func (h *Auth) Logout(c *gin.Context) {
	refreshToken := auth.GetRefreshToken(c)
	accessToken := ""
	if v, ok := c.Get(middleware.ContextAuthTokenKey); ok {
		accessToken, _ = v.(string)
	}
	h.Svc.DeactivateForLogout(c, refreshToken, accessToken)
	c.JSON(200, gin.H{"message": "退出成功"})
}

// ---- refresh ----

// Refresh POST /api/auth/refresh：校验 refresh、轮换双 token、新建会话。
// @Summary 刷新令牌
// @Tags 认证
// @Success 200 {object} map[string]any "用户对象"
// @Failure 401 {object} map[string]any
// @Failure 409 {object} map[string]any "并发刷新"
// @Router /auth/refresh [post]
func (h *Auth) Refresh(c *gin.Context) {
	refreshToken := auth.GetRefreshToken(c)
	res, err := h.Svc.VerifyRefreshToken(c.Request.Context(), refreshToken)
	if err != nil {
		if appErr, ok := err.(*errors.AppError); ok && appErr.Status == 409 {
			// 并发刷新：不清 cookie 不吊销（对齐 authFactory.js）。
			errors.AbortWithAppError(c, err, h.Config.IsDev)
			return
		}
		auth.ClearAuthCookies(c, !h.Config.IsDev)
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	if err := h.Svc.RotateRefresh(c, res.User, res.Session); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, h.Svc.SessionUserJSON(res.User))
}

// ---- me ----

// Me GET /api/auth/me（protect）。
// @Summary 获取当前用户
// @Tags 认证
// @Security bearerAuth
// @Produce json
// @Success 200 {object} map[string]any
// @Router /auth/me [get]
func (h *Auth) Me(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	c.JSON(200, h.Svc.MeJSON(user))
}

// ---- sse-ticket ----

// SSETicket GET /api/auth/sse-ticket（protect）：30s 有效的 SSE 票据。
// @Summary 获取 SSE 票据
// @Tags 认证
// @Security bearerAuth
// @Success 200 {object} map[string]string "ticket"
// @Router /auth/sse-ticket [get]
func (h *Auth) SSETicket(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	ticket, err := h.Svc.Signer.Sign(user.ID.Hex(), "sse-ticket", 30*time.Second, nil)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, gin.H{"ticket": ticket})
}
