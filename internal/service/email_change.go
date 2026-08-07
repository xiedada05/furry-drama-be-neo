package service

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/code"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// ChangeEmail PUT /api/auth/change-email（superAdminProtect，超管强制改邮箱）。
// 对齐 email.js L49-117。
func (s *AuthService) ChangeEmail(c *gin.Context, userID interface{}, newEmail, password string) error {
	ctx := c.Request.Context()
	newEmail = strings.ToLower(strings.TrimSpace(newEmail))
	if !EmailRegex.MatchString(newEmail) {
		return errors.New(400, "邮箱格式不正确")
	}
	user, err := s.Repos.Users.FindByIDWithAuth(ctx, userID)
	if err != nil {
		return errors.New(404, "用户不存在")
	}
	if !auth.Compare(user.Password, password) {
		return errors.New(400, "密码不正确")
	}
	if exists, err := s.Repos.Users.ExistsByEmail(ctx, newEmail); err == nil && exists {
		return errors.New(400, "该邮箱已被其他账号使用")
	}
	// 置新邮箱为未验证，发验证码。
	user.Email = newEmail
	user.IsEmailVerified = false
	_ = s.Repos.Users.Save(ctx, user)
	verifyCode := code.GenerateCode()
	s.EmailVerifyCodes.Set(verifyCode, code.Entry{
		UserID:    user.ID.Hex(),
		Email:     user.Email,
		ExpiresAt: time.Now().Add(CodeTTL),
		Attempts:  0,
	})
	if s.Mail != nil {
		go s.Mail.SendVerificationCodeEmail(context.Background(), user.Email, verifyCode, "changeEmail")
	}
	s.Audit(ctx, user, "CHANGE_EMAIL", "auth", "User change email", s.ClientIP(c))
	_ = s.Repos.Sessions.DeactivateAllByUser(ctx, user.ID)
	auth.ClearAuthCookies(c, !s.Config.IsDev)
	return nil
}

// EmailNotificationPrefsKeys 是偏好白名单（对齐 email.js）。
var EmailNotificationPrefsKeys = []string{
	"episodeUpdate", "newDeviceLogin", "feedbackReply", "friendLinkStatus",
	"friendLinkApply", "announcement", "reviewResult",
}

// UpdateEmailNotificationPrefs PUT /api/auth/email-notification-prefs。
// 返回更新后的完整偏好。
func (s *AuthService) UpdateEmailNotificationPrefs(ctx context.Context, userID interface{}, prefs map[string]bool) (model.EmailNotificationPrefs, error) {
	if len(prefs) == 0 {
		return model.EmailNotificationPrefs{}, errors.New(400, "没有可更新的偏好设置")
	}
	update := map[string]bool{}
	for _, k := range EmailNotificationPrefsKeys {
		if v, ok := prefs[k]; ok {
			update[k] = v
		}
	}
	if len(update) == 0 {
		return model.EmailNotificationPrefs{}, errors.New(400, "没有可更新的偏好设置")
	}
	if err := s.Repos.Users.UpdateEmailNotificationPrefs(ctx, userID, update); err != nil {
		return model.EmailNotificationPrefs{}, errors.New(500, "服务器错误")
	}
	user, err := s.Repos.Users.FindByID(ctx, userID)
	if err != nil {
		return model.EmailNotificationPrefs{}, errors.New(500, "服务器错误")
	}
	return user.EmailNotificationPrefs, nil
}

// VerifyEmail POST /api/auth/verify-email。对齐 email.js L145-190。
func (s *AuthService) VerifyEmail(ctx context.Context, verifyCode, emailOpt string) error {
	if verifyCode == "" {
		return errors.New(400, "请输入验证码")
	}
	entry, ok := s.EmailVerifyCodes.Get(verifyCode)
	if !ok || time.Now().After(entry.ExpiresAt) {
		return errors.New(400, "验证码无效或已过期，请重新获取")
	}
	entry.Attempts++
	if entry.Attempts > 5 {
		s.EmailVerifyCodes.Delete(verifyCode)
		return errors.New(400, "尝试次数过多，验证码已作废，请重新获取")
	}
	s.EmailVerifyCodes.Set(verifyCode, entry)

	if emailOpt != "" && !strings.EqualFold(emailOpt, entry.Email) {
		return errors.New(400, "验证码与邮箱不匹配")
	}
	user, err := s.Repos.Users.FindByID(ctx, entry.UserID)
	if err != nil {
		return errors.New(404, "用户不存在")
	}
	if user.IsEmailVerified {
		return nil
	}
	if !strings.EqualFold(user.Email, entry.Email) {
		return errors.New(400, "验证码已失效，请重新获取")
	}
	if err := s.Repos.Users.UpdateVerified(ctx, user.ID); err != nil {
		return errors.New(500, "服务器错误")
	}
	s.EmailVerifyCodes.Delete(verifyCode)
	return nil
}

// ResendVerification POST /api/auth/resend-verification（protect）。
// 返回 (message, error)；邮件服务未配置时 message="邮件服务未配置，请联系管理员"。
func (s *AuthService) ResendVerification(ctx context.Context, user *model.User) (string, error) {
	if user.IsEmailVerified {
		return "邮箱已验证", nil
	}
	verifyCode := code.GenerateCode()
	s.EmailVerifyCodes.Set(verifyCode, code.Entry{
		UserID:    user.ID.Hex(),
		Email:     user.Email,
		ExpiresAt: time.Now().Add(CodeTTL),
		Attempts:  0,
	})
	if s.Mail == nil || !s.Mail.SendVerificationCodeEmail(ctx, user.Email, verifyCode, "register") {
		return "邮件服务未配置，请联系管理员", nil
	}
	return "验证码已发送至您的邮箱", nil
}

// ResendVerificationByEmail POST /api/auth/resend-verification-by-email。
// 对齐 email.js L222-258：统一文案（不泄露）。
func (s *AuthService) ResendVerificationByEmail(ctx context.Context, emailAddr, altcha, devToken string) (string, error) {
	unified := "如果该邮箱已注册且未验证，验证码已发送"
	if emailAddr == "" {
		return "", errors.New(400, "请提供邮箱地址")
	}
	if !s.VerifyAltcha(altcha, devToken) {
		return "", errors.New(400, "验证码错误或已过期")
	}
	if !EmailRegex.MatchString(emailAddr) {
		return "", errors.New(400, "邮箱格式不正确")
	}
	user, err := s.Repos.Users.FindByEmail(ctx, strings.TrimSpace(emailAddr))
	if err != nil || user.IsEmailVerified {
		return unified, nil
	}
	verifyCode := code.GenerateCode()
	s.EmailVerifyCodes.Set(verifyCode, code.Entry{
		UserID:    user.ID.Hex(),
		Email:     user.Email,
		ExpiresAt: time.Now().Add(CodeTTL),
		Attempts:  0,
	})
	if s.Mail != nil {
		go s.Mail.SendVerificationCodeEmail(context.Background(), user.Email, verifyCode, "login")
	}
	return unified, nil
}

// RequestEmailChange POST /api/auth/request-email-change（protect）。
// 对齐 email.js L260-336：锁定感知 + 发确认邮件（链接 1h 有效）。
func (s *AuthService) RequestEmailChange(c *gin.Context, userID interface{}, password, newEmail, altcha string) error {
	ctx := c.Request.Context()
	if password == "" || newEmail == "" {
		return errors.New(400, "请填写密码和新邮箱")
	}
	if !s.VerifyAltcha(altcha, c.GetHeader("x-dev-token")) {
		return errors.New(400, "验证码错误或已过期")
	}
	newEmail = strings.ToLower(strings.TrimSpace(newEmail))
	if !EmailRegex.MatchString(newEmail) {
		return errors.New(400, "邮箱格式不正确")
	}
	user, err := s.Repos.Users.FindByIDWithAuth(ctx, userID)
	if err != nil {
		return errors.New(404, "用户不存在")
	}
	if repository.IsLocked(user) {
		return errors.New(423, "账号已被锁定，请30分钟后再试")
	}
	if !auth.Compare(user.Password, password) {
		_ = s.Repos.Users.IncLoginAttempts(ctx, user.ID)
		return errors.New(400, "密码不正确")
	}
	_ = s.Repos.Users.ResetLoginAttempts(ctx, user.ID)
	if strings.EqualFold(user.Email, newEmail) {
		return errors.New(400, "新邮箱与当前邮箱相同")
	}
	if exists, err := s.Repos.Users.ExistsByEmail(ctx, newEmail); err == nil && exists {
		return errors.New(400, "该邮箱已被其他账号使用")
	}
	if s.Mail == nil {
		return errors.New(503, "邮件服务暂不可用，请稍后再试")
	}
	token, err := s.Signer.Sign(user.ID.Hex(), "email-change", time.Hour, map[string]string{"newEmail": newEmail})
	if err != nil {
		return errors.New(500, "服务器错误")
	}
	siteURL := s.Config.Server.FrontendURL
	if siteURL == "" {
		siteURL = s.Config.Server.SiteURL
	}
	if siteURL == "" {
		siteURL = "http://localhost:3000"
	}
	confirmURL := siteURL + "/verify-email-change?token=" + token
	html := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">确认修改邮箱</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您正在将账号绑定邮箱修改为：<strong>` + newEmail + `</strong></p>` +
		`<p style="margin:20px 0;">` + email.EmailButton("确认修改邮箱", confirmURL, "primary") + `</p>` +
		email.EmailInfoBox("此链接 1 小时内有效。如非本人操作，请忽略此邮件。", "info")
	if !s.Mail.SendNotificationEmail(ctx, newEmail, "确认修改邮箱 - 兽剧聚合平台", html, "确认修改邮箱") {
		return errors.New(503, "邮件服务暂不可用，请稍后再试")
	}
	return nil
}

// VerifyEmailChange POST /api/auth/verify-email-change。对齐 email.js L337-378。
func (s *AuthService) VerifyEmailChange(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", errors.New(400, "缺少验证令牌")
	}
	claims, err := s.Signer.Verify(token)
	if err != nil || claims.Type != "email-change" {
		return "", errors.New(400, "无效的验证令牌")
	}
	tokenHash := auth.HashToken(token)
	if used, err := s.Repos.UsedTokens.IsUsed(ctx, tokenHash); err == nil && used {
		return "", errors.New(400, "该验证链接已使用，请重新申请")
	}
	user, err := s.Repos.Users.FindByID(ctx, claims.ID)
	if err != nil {
		return "", errors.New(404, "用户不存在")
	}
	newEmail := strings.ToLower(claims.NewEmail)
	if exists, err := s.Repos.Users.ExistsByEmail(ctx, newEmail); err == nil && exists {
		return "", errors.New(400, "该邮箱已被其他账号使用")
	}
	_ = s.Repos.UsedTokens.MarkUsed(ctx, tokenHash, "email-change", time.Hour)
	if err := s.Repos.Users.SetEmailVerifiedAndEmail(ctx, user.ID, newEmail); err != nil {
		return "", errors.New(500, "服务器错误")
	}
	_ = s.Repos.Sessions.DeactivateAllByUser(ctx, user.ID)
	return newEmail, nil
}
