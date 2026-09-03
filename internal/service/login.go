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

// LoginInput 是登录请求解析结果。
type LoginInput struct {
	Email      string
	Password   string
	DeviceInfo *auth.DeviceInfoPayload
	Altcha     string
	Ua         string
	DevToken   string
	AcceptLang string
	IP         string
}

// LoginResult 表达登录各分支的响应数据。
type LoginResult struct {
	Email              string
	NeedVerification   bool
	NeedDeviceVerify   bool
	DeviceVerifyInfo   gin.H
	Need2FA            bool
	TwoFactorChallenge string
	User               *model.User
}

// Login 登录分支流程，对齐 routes/auth/session.js L288-470。分支顺序必须严格保持。
// 需要 *gin.Context（登录成功时设置 access/refresh cookie）。
func (s *AuthService) Login(c *gin.Context, in LoginInput) (*LoginResult, error) {
	ctx := c.Request.Context()
	emailAddr := strings.TrimSpace(in.Email)

	if !s.VerifyAltcha(in.Altcha, in.DevToken) {
		return nil, errors.New(400, "验证码错误或已过期")
	}

	user, err := s.Repos.Users.FindByEmailWithAuth(ctx, emailAddr)
	if err != nil {
		// 邮箱不存在：附带 accountNotFound 标志，前端引导去注册。
		//（注册接口已暴露邮箱存在性判断，此处不额外增加枚举面。）
		return nil, errors.NewExtra(400, "该邮箱尚未注册，请先注册账号", map[string]any{"accountNotFound": true})
	}
	if repository.IsLocked(user) {
		return nil, errors.New(400, "用户名或密码错误")
	}

	// 注销宽限已过 → 物理删除全部关联数据后仍返回统一错误（不泄露）。
	if user.DeletionRequestedAt != nil {
		deleteAt := user.DeletionRequestedAt.Add(DeletionGraceDays * 24 * time.Hour)
		if time.Now().After(deleteAt) {
			if err := s.purgeUser(ctx, user.ID); err == nil {
				return nil, errors.New(400, "用户名或密码错误")
			}
		}
	}

	if !auth.Compare(user.Password, in.Password) {
		_ = s.Repos.Users.IncLoginAttempts(ctx, user.ID)
		return nil, errors.New(400, "用户名或密码错误")
	}
	_ = s.Repos.Users.ResetLoginAttempts(ctx, user.ID)

	// 邮箱未验证：发验证码（除非 skipVerification 或 dev-token 绕过）。
	if !user.IsEmailVerified && !s.SkipVerification(user) && !s.devTokenActive(in.DevToken) {
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
		return &LoginResult{Email: user.Email, NeedVerification: true}, nil
	}

	// skipVerification 且未验证 → 自动置 verified（对齐 L345-348）。
	if s.SkipVerification(user) && !user.IsEmailVerified {
		user.IsEmailVerified = true
		_ = s.Repos.Users.Save(ctx, user)
	}

	// 新设备检测：无同 UA active session 且已有 session 数 > 0。
	knownSessions, _ := s.Repos.Sessions.FindActiveByUser(ctx, user.ID)
	isKnownDevice := false
	for _, sess := range knownSessions {
		if sess.DeviceInfo.UserAgent == in.Ua {
			isKnownDevice = true
			break
		}
	}
	if !isKnownDevice && len(knownSessions) > 0 && !s.SkipVerification(user) && !s.devTokenActive(in.DevToken) {
		loginCode := code.GenerateCode()
		s.DeviceLoginCodes.Set(loginCode, code.Entry{
			UserID:    user.ID.Hex(),
			ExpiresAt: time.Now().Add(CodeTTL),
			Need2FA:   user.TwoFactorEnabled,
			Attempts:  0,
		})
		parsed := auth.ParseUserAgent(in.Ua)
		if s.Mail != nil {
			go s.Mail.SendNewDeviceLoginEmail(context.Background(), user.Email, email.DeviceInfo{
				Browser:        parsed.Browser,
				BrowserVersion: parsed.BrowserVersion,
				OS:             parsed.OS,
				OSVersion:      parsed.OSVersion,
				DeviceType:     parsed.DeviceType,
			}, in.IP, "", loginCode, time.Now())
		}
		return &LoginResult{
			Email:            user.Email,
			NeedDeviceVerify: true,
			DeviceVerifyInfo: gin.H{
				"browser":        parsed.Browser,
				"browserVersion": parsed.BrowserVersion,
				"os":             parsed.OS,
				"osVersion":      parsed.OSVersion,
				"deviceType":     parsed.DeviceType,
				"ip":             in.IP,
			},
		}, nil
	}

	// 2FA 已开启 → 返回 2FA 挑战（JWT 5m purpose=2fa-challenge），对齐 L408-419。
	if user.TwoFactorEnabled {
		challenge, err := s.Signer.Sign(user.ID.Hex(), "2fa-challenge", 5*time.Minute, nil)
		if err != nil {
			return nil, errors.New(500, "服务器错误")
		}
		return &LoginResult{Email: user.Email, Need2FA: true, TwoFactorChallenge: challenge}, nil
	}

	// 登录成功：更新设备信息 + 签发会话 + 审计 + 新设备提醒。
	parsed := auth.ParseUserAgent(in.Ua)
	di := auth.BuildDeviceInfo(in.DeviceInfo, parsed, in.Ua, in.AcceptLang)
	region := "未知"
	if s.IPRegion != nil {
		region = s.IPRegion.GetRegion(ctx, in.IP)
	}
	now := time.Now()
	user.DeviceInfo = di
	user.LastLoginAt = &now
	user.LastLoginIP = in.IP
	user.LastLoginRegion = region
	_ = s.Repos.Users.Save(ctx, user)

	if err := s.IssueSession(c, user, di, in.IP, "LOGIN"); err != nil {
		return nil, errors.New(500, "服务器错误")
	}
	// 新设备登录提醒邮件（已注册 session 前提下，跳过设备验证时也发送），对齐 L446-453。
	if s.Mail != nil {
		go s.sendNewDeviceNotification(user, parsed, in.IP, region)
	}
	return &LoginResult{User: user}, nil
}

func (s *AuthService) devTokenActive(devToken string) bool {
	return s.Config.JWT.DevAPIToken != "" && devToken == s.Config.JWT.DevAPIToken
}

// purgeUser 注销宽限已过时的物理删除（对齐 session.js L312-321）。
func (s *AuthService) purgeUser(ctx context.Context, userID interface{}) error {
	_ = s.Repos.Follows.DeleteByUser(ctx, userID)
	_ = s.Repos.Histories.DeleteByUser(ctx, userID)
	_ = s.Repos.Notifications.DeleteByUser(ctx, userID)
	_ = s.Repos.Favorites.DeleteByUser(ctx, userID)
	_ = s.Repos.Ratings.DeleteByUser(ctx, userID)
	_ = s.Repos.Reports.DeleteByReporter(ctx, userID)
	_ = s.Repos.Feedbacks.DeleteByUser(ctx, userID)
	_ = s.Repos.Sessions.DeleteByUser(ctx, userID)
	return s.Repos.Users.DeleteByID(ctx, userID)
}

// sendNewDeviceNotification 新设备登录提醒邮件（受 emailNotificationPrefs.newDeviceLogin 控制）。
func (s *AuthService) sendNewDeviceNotification(u *model.User, parsed auth.ParsedUA, ip, region string) {
	if !u.EmailNotificationPrefs.NewDeviceLogin || !u.IsEmailVerified {
		return
	}
	_ = s.Mail.SendNotificationEmail(context.Background(), u.Email, "新设备登录提醒 - 兽剧聚合平台",
		`<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">新设备登录提醒</h2>`+
			`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您的账号检测到一次新的设备登录：</p>`+
			email.EmailInfoBox(
				`<p style="margin:4px 0;"><strong>浏览器：</strong>`+orUnknown(parsed.Browser)+` `+parsed.BrowserVersion+`</p>`+
					`<p style="margin:4px 0;"><strong>操作系统：</strong>`+orUnknown(parsed.OS)+` `+parsed.OSVersion+`</p>`+
					`<p style="margin:4px 0;"><strong>IP地址：</strong>`+ip+`</p>`+
					`<p style="margin:4px 0;"><strong>地域：</strong>`+orUnknown(region)+`</p>`, "info"),
		"检测到新设备登录")
}

func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}
