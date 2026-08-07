package service

import (
	"context"
	"crypto/subtle"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/code"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// DeviceVerifyResult 是 verify-device 的响应。
type DeviceVerifyResult struct {
	Verified  bool
	LoginCode string
}

// VerifyDevice POST /api/auth/verify-device：校验邮箱内的一次性验证链接，
// 生成设备登录码返回给前端。对齐 device.js L48-78。
func (s *AuthService) VerifyDevice(ctx context.Context, token string) (*DeviceVerifyResult, error) {
	if token == "" {
		return nil, errors.New(400, "缺少验证令牌")
	}
	tokenHash := auth.HashToken(token)
	if used, err := s.Repos.UsedTokens.IsUsed(ctx, tokenHash); err == nil && used {
		return nil, errors.New(400, "该验证链接已被使用")
	}
	claims, err := s.Signer.Verify(token)
	if err != nil {
		if err == auth.ErrTokenExpired {
			return nil, errors.New(400, "验证链接已过期，请重新登录")
		}
		return nil, errors.New(400, "验证失败")
	}
	if claims.Purpose != "device-verify" {
		return nil, errors.New(400, "无效的验证令牌")
	}
	user, err := s.Repos.Users.FindByID(ctx, claims.ID)
	if err != nil {
		return nil, errors.New(400, "用户不存在")
	}
	_ = s.Repos.UsedTokens.MarkUsed(ctx, tokenHash, "device-verify", 30*time.Minute)

	loginCode := code.GenerateCode()
	s.DeviceLoginCodes.Set(loginCode, code.Entry{
		UserID:    user.ID.Hex(),
		ExpiresAt: time.Now().Add(CodeTTL),
		Need2FA:   user.TwoFactorEnabled,
		Attempts:  0,
	})
	return &DeviceVerifyResult{Verified: true, LoginCode: loginCode}, nil
}

// ConfirmDeviceLogin POST /api/auth/confirm-device-login：用户输入 6 位码完成登录
// 或进入 2FA 流程。对齐 device.js L82-157。
func (s *AuthService) ConfirmDeviceLogin(c *gin.Context, loginCode string) (*LoginResult, error) {
	if loginCode == "" {
		return nil, errors.New(400, "请输入验证码")
	}
	entry, ok := s.DeviceLoginCodes.Get(loginCode)
	if !ok || time.Now().After(entry.ExpiresAt) {
		if ok {
			s.DeviceLoginCodes.Delete(loginCode)
		}
		return nil, errors.New(400, "验证码无效或已过期，请重新验证")
	}
	entry.Attempts++
	if entry.Attempts > 5 {
		s.DeviceLoginCodes.Delete(loginCode)
		return nil, errors.New(400, "尝试次数过多，验证码已作废，请重新验证")
	}
	// 更新计数（保留条目）
	s.DeviceLoginCodes.Set(loginCode, entry)

	user, err := s.Repos.Users.FindByIDWithAuth(c.Request.Context(), entry.UserID)
	if err != nil {
		s.DeviceLoginCodes.Delete(loginCode)
		return nil, errors.New(400, "用户不存在")
	}

	if entry.Need2FA {
		challenge, err := s.Signer.Sign(user.ID.Hex(), "2fa-challenge", 5*time.Minute, map[string]string{
			"deviceLoginCode": loginCode,
		})
		if err != nil {
			return nil, errors.New(500, "服务器错误")
		}
		return &LoginResult{Email: user.Email, Need2FA: true, TwoFactorChallenge: challenge}, nil
	}

	s.DeviceLoginCodes.Delete(loginCode)

	// 完成登录。
	ua := c.Request.UserAgent()
	parsed := auth.ParseUserAgent(ua)
	ip := s.ClientIP(c)
	di := auth.BuildDeviceInfo(nil, parsed, ua, c.GetHeader("Accept-Language"))
	now := time.Now()
	user.DeviceInfo = di
	user.LastLoginAt = &now
	user.LastLoginIP = ip
	region := "未知"
	if s.IPRegion != nil {
		region = s.IPRegion.GetRegion(c.Request.Context(), ip)
	}
	user.LastLoginRegion = region
	_ = s.Repos.Users.Save(c.Request.Context(), user)

	if err := s.IssueSession(c, user, di, ip, "LOGIN"); err != nil {
		return nil, errors.New(500, "服务器错误")
	}
	if s.Mail != nil {
		go s.sendNewDeviceNotification(user, parsed, ip, region)
	}
	return &LoginResult{User: user}, nil
}

// Login2FA POST /api/auth/login-2fa：TOTP 或备份码校验后完成登录。
// 对齐 device.js L160-264。
func (s *AuthService) Login2FA(c *gin.Context, emailAddr, challenge, token string, deviceInfo *auth.DeviceInfoPayload) (*LoginResult, error) {
	if challenge == "" {
		return nil, errors.New(400, "缺少2FA挑战令牌，请重新登录")
	}
	challengeClaims, err := s.Signer.Verify(challenge)
	if err != nil {
		return nil, errors.New(400, "2FA挑战令牌已过期或无效，请重新登录")
	}
	if challengeClaims.Purpose != "2fa-challenge" {
		return nil, errors.New(400, "无效的2FA挑战令牌")
	}

	user, err := s.Repos.Users.FindByEmailWith2FA(c.Request.Context(), strings.TrimSpace(emailAddr))
	if err != nil || !user.TwoFactorEnabled {
		return nil, errors.New(400, "该账号未启用两步验证")
	}
	if user.ID.Hex() != challengeClaims.ID {
		return nil, errors.New(400, "验证失败")
	}
	if repository.IsLocked(user) {
		return nil, errors.New(423, "账号已被锁定，请30分钟后再试")
	}
	if !user.IsEmailVerified && !s.SkipVerification(user) && !s.devTokenActive(c.GetHeader("x-dev-token")) {
		return nil, errors.New(403, "请先验证邮箱后再登录")
	}

	keyHex := auth.FieldKey(s.Config.JWT.EncryptionKey, s.Config.JWT.Secret)
	secret := auth.DecryptField(user.TwoFactorSecret, keyHex)
	backupCodes := auth.DecryptArray(user.TwoFactorBackupCodes, keyHex)

	totpOK := secret != "" && auth.VerifyTOTP(secret, token)
	backupHit := false
	for _, bc := range backupCodes {
		if timingSafeEq(bc, token) {
			backupHit = true
			break
		}
	}
	if !totpOK && !backupHit {
		_ = s.Repos.Users.IncLoginAttempts(c.Request.Context(), user.ID)
		nextAttempts := user.LoginAttempts + 1
		if repository.IsLocked(user) || nextAttempts >= s.Config.Security.LoginMaxAttempts {
			return nil, errors.New(423, "账号已被锁定，请30分钟后再试")
		}
		return nil, errors.New(400, "验证码无效")
	}
	_ = s.Repos.Users.ResetLoginAttempts(c.Request.Context(), user.ID)

	// 备份码命中 → 消耗该码。
	if backupHit {
		remaining := make([]string, 0, len(backupCodes))
		for _, bc := range backupCodes {
			if !timingSafeEq(bc, token) {
				remaining = append(remaining, bc)
			}
		}
		user.TwoFactorBackupCodes = auth.EncryptArray(remaining, keyHex)
	}

	ua := c.Request.UserAgent()
	parsed := auth.ParseUserAgent(ua)
	ip := s.ClientIP(c)
	di := auth.BuildDeviceInfo(deviceInfo, parsed, ua, c.GetHeader("Accept-Language"))
	now := time.Now()
	user.DeviceInfo = di
	user.LastLoginAt = &now
	user.LastLoginIP = ip
	region := "未知"
	if s.IPRegion != nil {
		region = s.IPRegion.GetRegion(c.Request.Context(), ip)
	}
	user.LastLoginRegion = region
	_ = s.Repos.Users.Save(c.Request.Context(), user)

	if err := s.IssueSession(c, user, di, ip, "LOGIN_2FA"); err != nil {
		return nil, errors.New(500, "服务器错误")
	}
	// 来自设备验证流程 → 删除一次性登录码。
	if challengeClaims.DeviceLoginCode != "" {
		s.DeviceLoginCodes.Delete(challengeClaims.DeviceLoginCode)
	}
	if s.Mail != nil {
		go s.sendNewDeviceNotification(user, parsed, ip, region)
	}
	return &LoginResult{User: user}, nil
}

func timingSafeEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
