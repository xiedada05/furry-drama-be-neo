package service

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
)

// ChangePassword PUT /api/auth/change-password（protect）。
// 对齐 password.js L45-79：校验新密码 → 匹配当前 → 更新 → 吊销全部会话 → 清 cookie。
func (s *AuthService) ChangePassword(c *gin.Context, userID interface{}, currentPassword, newPassword string) error {
	if msg := ValidatePassword(newPassword); msg != "" {
		return errors.New(400, msg)
	}
	user, err := s.Repos.Users.FindByIDWithAuth(c.Request.Context(), userID)
	if err != nil {
		return errors.New(404, "用户不存在")
	}
	if !auth.Compare(user.Password, currentPassword) {
		return errors.New(400, "当前密码不正确")
	}
	hash, err := auth.Hash(newPassword)
	if err != nil {
		return errors.New(500, "服务器错误")
	}
	if err := s.Repos.Users.UpdatePassword(c.Request.Context(), userID, hash, time.Now()); err != nil {
		return errors.New(500, "服务器错误")
	}
	_ = s.Repos.Sessions.DeactivateAllByUser(c.Request.Context(), userID)
	s.Audit(c.Request.Context(), user, "CHANGE_PASSWORD", "auth", "User change password", s.ClientIP(c))
	auth.ClearAuthCookies(c, !s.Config.IsDev)
	return nil
}

// ForgotPassword POST /api/auth/forgot-password。
// 对齐 password.js L81-105：altcha 后统一文案（不泄露账号是否存在）。
func (s *AuthService) ForgotPassword(ctx context.Context, emailAddr, altcha, devToken string) (string, error) {
	unified := "如果该邮箱已注册，重置链接已发送至邮箱"
	if !s.VerifyAltcha(altcha, devToken) {
		return "", errors.New(400, "验证码错误或已过期")
	}
	user, err := s.Repos.Users.FindByEmail(ctx, strings.TrimSpace(emailAddr))
	if err != nil {
		// 用户不存在：仍返回统一文案（不泄露）。
		return unified, nil
	}
	token, err := s.Signer.Sign(user.ID.Hex(), "reset-password", time.Hour, nil)
	if err != nil {
		return unified, nil
	}
	if s.Mail != nil {
		go s.Mail.SendPasswordResetEmail(context.Background(), user.Email, token)
	}
	return unified, nil
}

// ResetPassword POST /api/auth/reset-password。
// 对齐 password.js L107-154：一次性令牌 + 更新密码 + 吊销会话。
func (s *AuthService) ResetPassword(ctx context.Context, token, newPassword string) error {
	if msg := ValidatePassword(newPassword); msg != "" {
		return errors.New(400, msg)
	}
	claims, err := s.Signer.Verify(token)
	if err != nil {
		if err == auth.ErrTokenExpired {
			return errors.New(400, "重置链接已过期，请重新获取")
		}
		return errors.New(400, "无效的重置令牌")
	}
	if claims.Purpose != "reset-password" {
		return errors.New(400, "无效的重置令牌")
	}
	tokenHash := auth.HashToken(token)
	if used, err := s.Repos.UsedTokens.IsUsed(ctx, tokenHash); err == nil && used {
		return errors.New(400, "该重置链接已使用，请重新获取")
	}
	user, err := s.Repos.Users.FindByIDWithAuth(ctx, claims.ID)
	if err != nil {
		return errors.New(404, "用户不存在")
	}
	hash, err := auth.Hash(newPassword)
	if err != nil {
		return errors.New(500, "服务器错误")
	}
	_ = s.Repos.UsedTokens.MarkUsed(ctx, tokenHash, "reset-password", time.Hour)
	if err := s.Repos.Users.UpdatePassword(ctx, user.ID, hash, time.Now()); err != nil {
		return errors.New(500, "服务器错误")
	}
	_ = s.Repos.Sessions.DeactivateAllByUser(ctx, user.ID)
	s.Audit(ctx, user, "PASSWORD_RESET", "auth", "User password reset", "")
	return nil
}

// DeactivateAllSessions 吊销用户全部会话（change-email 等场景复用）。
func (s *AuthService) DeactivateAllSessions(ctx context.Context, userID interface{}) error {
	return s.Repos.Sessions.DeactivateAllByUser(ctx, userID)
}
