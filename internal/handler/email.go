package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
)

// ---- email ----

// ChangeEmail PUT /api/auth/change-email（superAdminProtect + requireEmailChanged）。
func (h *Auth) ChangeEmail(c *gin.Context) {
	var req struct {
		NewEmail string `json:"newEmail"`
		Password string `json:"password"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	if err := h.Svc.ChangeEmail(c, user.ID, req.NewEmail, req.Password); err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{
		"message":          "邮箱修改成功，请查收验证邮件后重新登录",
		"email":            req.NewEmail,
		"isEmailVerified":  false,
		"forceEmailChange": false,
	})
}

// EmailNotificationPrefs PUT /api/auth/email-notification-prefs（protect）。
func (h *Auth) EmailNotificationPrefs(c *gin.Context) {
	var req map[string]bool
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	prefs, err := h.Svc.UpdateEmailNotificationPrefs(c.Request.Context(), user.ID, req)
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "通知偏好已更新", "emailNotificationPrefs": prefs})
}

// VerifyEmail POST /api/auth/verify-email。
func (h *Auth) VerifyEmail(c *gin.Context) {
	var req struct {
		Code  string `json:"code"`
		Email string `json:"email"`
	}
	_ = c.ShouldBindJSON(&req)
	if err := h.Svc.VerifyEmail(c.Request.Context(), req.Code, req.Email); err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	// 已验证用户返回不同文案（对齐 email.js：已 verified → "邮箱已验证"）。
	user, _ := middleware.GetUser(c)
	if user != nil && user.IsEmailVerified {
		c.JSON(200, gin.H{"message": "邮箱已验证"})
		return
	}
	c.JSON(200, gin.H{"message": "邮箱验证成功"})
}

// ResendVerification POST /api/auth/resend-verification（protect）。
func (h *Auth) ResendVerification(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	message, err := h.Svc.ResendVerification(c.Request.Context(), user)
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": message})
}

// ResendVerificationByEmail POST /api/auth/resend-verification-by-email。
func (h *Auth) ResendVerificationByEmail(c *gin.Context) {
	var req struct {
		Email  string `json:"email"`
		Altcha string `json:"altcha"`
	}
	_ = c.ShouldBindJSON(&req)
	message, err := h.Svc.ResendVerificationByEmail(c.Request.Context(), req.Email, req.Altcha, h.devToken(c))
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": message})
}

// RequestEmailChange POST /api/auth/request-email-change（protect）。
func (h *Auth) RequestEmailChange(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
		NewEmail string `json:"newEmail"`
		Altcha   string `json:"altcha"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	if err := h.Svc.RequestEmailChange(c, user.ID, req.Password, req.NewEmail, req.Altcha); err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "验证邮件已发送到新邮箱，请查收确认"})
}

// VerifyEmailChange POST /api/auth/verify-email-change。
func (h *Auth) VerifyEmailChange(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	_ = c.ShouldBindJSON(&req)
	newEmail, err := h.Svc.VerifyEmailChange(c.Request.Context(), req.Token)
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "邮箱修改成功，请重新登录", "email": newEmail})
}

// ---- account ----

// RequestDeletion POST /api/auth/request-deletion（protect）。
func (h *Auth) RequestDeletion(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	res, err := h.Svc.RequestDeletion(c.Request.Context(), user)
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{
		"message":             "注销申请已提交",
		"deletionRequestedAt": res.RequestedAt,
		"deleteAt":            res.DeleteAt,
	})
}

// CancelDeletion POST /api/auth/cancel-deletion（protect）。
func (h *Auth) CancelDeletion(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	if err := h.Svc.CancelDeletion(c.Request.Context(), user); err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "注销申请已取消"})
}

// DeletionStatus GET /api/auth/deletion-status（protect）。
func (h *Auth) DeletionStatus(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	c.JSON(200, h.Svc.DeletionStatus(user))
}
