package service

import (
	"context"
	"strings"
	"time"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/code"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// RegisterInput 是注册请求的解析结果。
type RegisterInput struct {
	AccountID   string
	Username    string
	Email       string
	Password    string
	DeviceInfo  *auth.DeviceInfoPayload
	Altcha      string
	Ua          string
	DevToken    string
	AcceptLang  string
	IP          string
}

// RegisterResult 是注册成功响应数据。
type RegisterResult struct {
	Email           string
	NeedVerification bool
}

// Register 注册流程，对齐 routes/auth/session.js L154-247。
// 返回错误时是 *errors.AppError（handler 用 AbortWithAppError 渲染）。
func (s *AuthService) Register(ctx context.Context, in RegisterInput) (*RegisterResult, error) {
	accountID := strings.TrimSpace(in.AccountID)
	username := strings.TrimSpace(in.Username)
	email := strings.TrimSpace(in.Email)

	if !s.VerifyAltcha(in.Altcha, in.DevToken) {
		return nil, errors.New(400, "验证码错误或已过期")
	}
	if !EmailRegex.MatchString(email) {
		return nil, errors.New(400, "邮箱格式不正确")
	}
	if len(accountID) < 3 || len(accountID) > 20 {
		return nil, errors.New(400, "账号ID长度需在3-20个字符之间")
	}
	if !AccountIDRegex.MatchString(accountID) {
		return nil, errors.New(400, "账号ID只能包含字母、数字和下划线")
	}
	if len(username) < 1 || len(username) > 20 {
		return nil, errors.New(400, "昵称长度需在1-20个字符之间")
	}
	if msg := ValidatePassword(in.Password); msg != "" {
		return nil, errors.New(400, msg)
	}
	// DEMO_EMAILS：注册时拒绝新建演示邮箱（必须已存在），对齐 L180-185。
	if containsFoldSlice(s.Config.JWT.DemoEmails, strings.ToLower(email)) {
		exists, err := s.Repos.Users.ExistsByEmail(ctx, email)
		if err != nil {
			return nil, errors.New(500, "服务器错误")
		}
		if !exists {
			return nil, errors.New(400, "该邮箱不可注册")
		}
	}
	if exists, err := s.Repos.Users.ExistsByEmail(ctx, email); err != nil {
		return nil, errors.New(500, "服务器错误")
	} else if exists {
		return nil, errors.New(400, "该邮箱已被注册")
	}
	if exists, err := s.Repos.Users.ExistsByAccountID(ctx, accountID); err != nil {
		return nil, errors.New(500, "服务器错误")
	} else if exists {
		return nil, errors.New(400, "该账号ID已被占用")
	}

	hash, err := auth.Hash(in.Password)
	if err != nil {
		return nil, errors.New(500, "服务器错误")
	}
	parsed := auth.ParseUserAgent(in.Ua)
	di := auth.BuildDeviceInfo(in.DeviceInfo, parsed, in.Ua, in.AcceptLang)
	region := "未知"
	if s.IPRegion != nil {
		region = s.IPRegion.GetRegion(ctx, in.IP)
	}
	user := &model.User{
		AccountID:              accountID,
		Username:               username,
		Email:                  email,
		Password:               hash,
		IsEmailVerified:        false,
		DeviceInfo:             di,
		LastLoginAt:            nil,
		LastLoginIP:            in.IP,
		LastLoginRegion:        region,
		Role:                   "user",
		EmailNotificationPrefs: model.DefaultEmailNotificationPrefs(),
		BackgroundPrefs:        model.DefaultBackgroundPrefs(),
		PersonalWallpapers:     []model.Wallpaper{},
	}
	last := time.Now()
	user.LastLoginAt = &last
	if err := s.Repos.Users.Create(ctx, user); err != nil {
		if repository.IsDuplicateKey(err) {
			return nil, errors.New(400, "该信息已被使用")
		}
		return nil, errors.New(500, "服务器错误")
	}

	// 生成 6 位验证码并发送（10 分钟有效），对齐 L227-234。
	verifyCode := code.GenerateCode()
	s.EmailVerifyCodes.Set(verifyCode, code.Entry{
		UserID:    user.ID.Hex(),
		Email:     user.Email,
		ExpiresAt: time.Now().Add(CodeTTL),
		Attempts:  0,
	})
	if s.Mail != nil {
		go s.Mail.SendVerificationCodeEmail(context.Background(), user.Email, verifyCode, "register")
	}

	return &RegisterResult{Email: user.Email, NeedVerification: true}, nil
}

// ValidatePassword 校验密码规则，返回错误文案或空串（对齐 middlewares/security.js validatePassword）。
func ValidatePassword(p string) string {
	if len([]rune(p)) < 8 {
		return "密码长度至少8位"
	}
	hasLetter := false
	hasDigit := false
	for _, r := range p {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			hasLetter = true
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter {
		return "密码必须包含至少一个字母"
	}
	if !hasDigit {
		return "密码必须包含至少一个数字"
	}
	return ""
}
