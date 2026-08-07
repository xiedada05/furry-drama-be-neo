package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
)

// ---- device ----

// VerifyDevice POST /api/auth/verify-device：邮箱验证链接换取设备登录码。
func (h *Auth) VerifyDevice(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	_ = c.ShouldBindJSON(&req)
	res, err := h.Svc.VerifyDevice(c.Request.Context(), req.Token)
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"verified": true, "loginCode": res.LoginCode})
}

// ConfirmDeviceLogin POST /api/auth/confirm-device-login：输入 6 位码完成登录或进入 2FA。
func (h *Auth) ConfirmDeviceLogin(c *gin.Context) {
	var req struct {
		LoginCode string `json:"loginCode"`
	}
	_ = c.ShouldBindJSON(&req)
	res, err := h.Svc.ConfirmDeviceLogin(c, req.LoginCode)
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	switch {
	case res.Need2FA:
		c.JSON(200, gin.H{
			"need2FA":            true,
			"email":              res.Email,
			"twoFactorChallenge": res.TwoFactorChallenge,
		})
	case res.User != nil:
		// 对齐 device.js L149-152：仅这些字段。
		c.JSON(200, gin.H{
			"_id":             res.User.ID.Hex(),
			"accountId":       res.User.AccountID,
			"username":        res.User.Username,
			"email":           res.User.Email,
			"isEmailVerified": res.User.IsEmailVerified,
			"role":            res.User.Role,
		})
	default:
		c.JSON(500, gin.H{"message": "服务器错误"})
	}
}

// Login2FA POST /api/auth/login-2fa：TOTP/备份码登录。
func (h *Auth) Login2FA(c *gin.Context) {
	var req struct {
		Email            string                  `json:"email"`
		TwoFactorToken   string                  `json:"twoFactorToken"`
		TwoFactorChallenge string                `json:"twoFactorChallenge"`
		DeviceInfo       *auth.DeviceInfoPayload `json:"deviceInfo"`
	}
	_ = c.ShouldBindJSON(&req)
	res, err := h.Svc.Login2FA(c, req.Email, req.TwoFactorChallenge, req.TwoFactorToken, req.DeviceInfo)
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	if res.User != nil {
		c.JSON(200, h.Svc.SessionUserJSON(res.User))
		return
	}
	c.JSON(500, gin.H{"message": "服务器错误"})
}

// ---- password ----

// ChangePassword PUT /api/auth/change-password（protect）。
func (h *Auth) ChangePassword(c *gin.Context) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	if err := h.Svc.ChangePassword(c, user.ID, req.CurrentPassword, req.NewPassword); err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "密码修改成功"})
}

// ForgotPassword POST /api/auth/forgot-password。
func (h *Auth) ForgotPassword(c *gin.Context) {
	var req struct {
		Email  string `json:"email"`
		Altcha string `json:"altcha"`
	}
	_ = c.ShouldBindJSON(&req)
	message, err := h.Svc.ForgotPassword(c.Request.Context(), req.Email, req.Altcha, h.devToken(c))
	if err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": message})
}

// ResetPassword POST /api/auth/reset-password。
func (h *Auth) ResetPassword(c *gin.Context) {
	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	_ = c.ShouldBindJSON(&req)
	if err := h.Svc.ResetPassword(c.Request.Context(), req.Token, req.NewPassword); err != nil {
		errors.AbortWithAppError(c, err, h.Config.IsDev)
		return
	}
	c.JSON(200, gin.H{"message": "密码重置成功"})
}
