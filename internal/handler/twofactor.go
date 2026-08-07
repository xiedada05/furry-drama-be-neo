package handler

import (
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
)

// twoFactorKeyHex 派生 2FA 字段加密密钥（ENCRYPTION_KEY 或 JWT_SECRET）。
func (h *Auth) twoFactorKeyHex() string {
	return auth.FieldKey(h.Config.JWT.EncryptionKey, h.Config.JWT.Secret)
}

// Enable2FA POST /api/2fa/enable（protect）：生成 TOTP secret + 备份码并加密存储。
func (h *Auth) Enable2FA(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	u, err := h.Svc.Repos.Users.FindByIDWithAllSecrets(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	secret := auth.GenerateSecret()
	backup := auth.GenerateBackupCodes()
	keyHex := h.twoFactorKeyHex()
	if err := h.Svc.Repos.Users.SetTwoFactorSetup(c.Request.Context(), user.ID,
		auth.EncryptField(secret, keyHex), auth.EncryptArray(backup, keyHex), false); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	h.Svc.Audit(c.Request.Context(), u, "2FA_SETUP_INITIATED", "auth", "User initiated 2FA setup", h.clientIP(c))
	otpauthURL := "otpauth://totp/FurryDrama:" + url.QueryEscape(u.AccountID) + "?secret=" + url.QueryEscape(secret) + "&issuer=FurryDrama"
	c.JSON(200, gin.H{"secret": secret, "backupCodes": backup, "otpauthUrl": otpauthURL})
}

// VerifyEnable2FA POST /api/2fa/verify-enable（protect）：TOTP 校验后启用 2FA。
func (h *Auth) VerifyEnable2FA(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	u, err := h.Svc.Repos.Users.FindByIDWithAllSecrets(c.Request.Context(), user.ID)
	if err != nil || u.TwoFactorSecret == "" {
		c.JSON(400, gin.H{"message": "2FA not set up"})
		return
	}
	secret := auth.DecryptField(u.TwoFactorSecret, h.twoFactorKeyHex())
	if !auth.VerifyTOTP(secret, req.Token) {
		c.JSON(400, gin.H{"message": "Invalid verification code"})
		return
	}
	if err := h.Svc.Repos.Users.EnableTwoFactor(c.Request.Context(), user.ID); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	h.Svc.Audit(c.Request.Context(), u, "2FA_ENABLED", "auth", "User enabled 2FA", h.clientIP(c))
	c.JSON(200, gin.H{"message": "2FA enabled successfully"})
}

// Disable2FA POST /api/2fa/disable（protect）：TOTP/备份码校验后关闭 2FA。
func (h *Auth) Disable2FA(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	u, err := h.Svc.Repos.Users.FindByIDWithAllSecrets(c.Request.Context(), user.ID)
	if err != nil || !u.TwoFactorEnabled {
		c.JSON(400, gin.H{"message": "2FA not enabled"})
		return
	}
	keyHex := h.twoFactorKeyHex()
	secret := auth.DecryptField(u.TwoFactorSecret, keyHex)
	codes := auth.DecryptArray(u.TwoFactorBackupCodes, keyHex)
	if !auth.VerifyTOTP(secret, req.Token) && !matchBackupCode(codes, req.Token) {
		c.JSON(400, gin.H{"message": "Invalid verification code"})
		return
	}
	// 备份码命中 → 消耗。
	if matchBackupCode(codes, req.Token) {
		_ = h.Svc.Repos.Users.SetTwoFactorSetup(c.Request.Context(), user.ID,
			u.TwoFactorSecret, auth.EncryptArray(removeBackupCode(codes, req.Token), keyHex), true)
	}
	if err := h.Svc.Repos.Users.DisableTwoFactor(c.Request.Context(), user.ID); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	h.Svc.Audit(c.Request.Context(), u, "2FA_DISABLED", "auth", "User disabled 2FA", h.clientIP(c))
	c.JSON(200, gin.H{"message": "2FA disabled successfully"})
}

// Verify2FA POST /api/2fa/verify（protect）：校验 TOTP/备份码。
func (h *Auth) Verify2FA(c *gin.Context) {
	var req struct {
		Token string `json:"token"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	u, err := h.Svc.Repos.Users.FindByIDWithAllSecrets(c.Request.Context(), user.ID)
	if err != nil || !u.TwoFactorEnabled {
		c.JSON(400, gin.H{"message": "2FA not enabled for this account"})
		return
	}
	keyHex := h.twoFactorKeyHex()
	secret := auth.DecryptField(u.TwoFactorSecret, keyHex)
	codes := auth.DecryptArray(u.TwoFactorBackupCodes, keyHex)
	if !auth.VerifyTOTP(secret, req.Token) && !matchBackupCode(codes, req.Token) {
		c.JSON(400, gin.H{"message": "Invalid verification code"})
		return
	}
	if matchBackupCode(codes, req.Token) {
		_ = h.Svc.Repos.Users.SetTwoFactorSetup(c.Request.Context(), user.ID,
			u.TwoFactorSecret, auth.EncryptArray(removeBackupCode(codes, req.Token), keyHex), true)
	}
	c.JSON(200, gin.H{"verified": true})
}

func matchBackupCode(codes []string, token string) bool {
	for _, c := range codes {
		if c == token {
			return true
		}
	}
	return false
}

func removeBackupCode(codes []string, token string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		if c != token {
			out = append(out, c)
		}
	}
	return out
}
