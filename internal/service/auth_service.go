// Package service 承载认证与用户域的领域逻辑（登录分支流、refresh 轮换、
// 设备登录码链、2FA、导出）。handler 层负责 HTTP 解析与响应组装。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/altcha"
	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/code"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/ipregion"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// DefaultSuperAdminEmail 是超管默认邮箱（requireEmailChanged 判定）。
const DefaultSuperAdminEmail = "admin@furry09.com"

// DeletionGraceDays 是注销宽限期（对齐 7 天）。
const DeletionGraceDays = 7

// CodeTTL 是验证码有效期（对齐 10 分钟）。
const CodeTTL = 10 * time.Minute

// AuthService 是认证域服务，聚合认证所需的全部依赖。
type AuthService struct {
	Repos    *repository.Repos
	Signer   *auth.Signer
	Mail     *email.Client
	IPRegion *ipregion.Client
	Config   *config.Config

	EmailVerifyCodes *code.Store
	DeviceLoginCodes *code.Store
}

// NewAuthService 构造认证服务并初始化验证码内存存储。
func NewAuthService(cfg *config.Config, repos *repository.Repos, signer *auth.Signer, mail *email.Client, ipr *ipregion.Client) *AuthService {
	return &AuthService{
		Repos:            repos,
		Signer:           signer,
		Mail:             mail,
		IPRegion:         ipr,
		Config:           cfg,
		EmailVerifyCodes: code.NewStore(CodeTTL),
		DeviceLoginCodes: code.NewStore(CodeTTL),
	}
}

// AltchaHMACKey 派生 altcha HMAC 密钥：优先配置，缺省 sha256("altcha-"+JWT_SECRET) hex
// 的 UTF-8 字节（对齐 utils/altcha.js:8）。
func (s *AuthService) AltchaHMACKey() []byte {
	if k := s.Config.JWT.AltchaHMACKey; k != "" {
		return []byte(k)
	}
	sum := sha256.Sum256([]byte("altcha-" + s.Config.JWT.Secret))
	return []byte(hex.EncodeToString(sum[:]))
}

// VerifyAltcha 校验 altcha payload；DEV_API_TOKEN 配置且 x-dev-token 匹配时直接通过。
// 对齐 utils/altcha.js verifyAltcha。
func (s *AuthService) VerifyAltcha(payload, devToken string) bool {
	if s.Config.JWT.DevAPIToken != "" && devToken == s.Config.JWT.DevAPIToken {
		return true
	}
	if payload == "" {
		return false
	}
	ok, _ := altcha.Verify(payload, s.AltchaHMACKey())
	return ok
}

// CreateCaptcha 生成 altcha 挑战（SHA-256, cost 10000, 5min 过期）。
// 对齐 routes/auth/session.js L76-90。
func (s *AuthService) CreateCaptcha() (altcha.Challenge, error) {
	return altcha.CreateChallenge(altcha.ChallengeOpts{
		Cost:    10000,
		HMACKey: s.AltchaHMACKey(),
	})
}

// SkipVerification 判断是否跳过邮箱/设备验证：仅非生产且邮箱在 DEMO_EMAILS 中。
// 对齐 authHelpers.js skipVerification。
func (s *AuthService) SkipVerification(u *model.User) bool {
	if s.Config.IsDev && containsFoldSlice(s.Config.JWT.DemoEmails, strings.ToLower(u.Email)) {
		return true
	}
	return false
}

// ForceEmailChange 判断超管是否未改默认邮箱（响应中的 forceEmailChange 字段）。
func (s *AuthService) ForceEmailChange(u *model.User) bool {
	return u.Role == "superadmin" && u.Email == DefaultSuperAdminEmail
}

// SessionUserJSON 组装登录/refresh 响应的用户对象（对齐 session.js login 成功响应）。
func (s *AuthService) SessionUserJSON(u *model.User) gin.H {
	return gin.H{
		"_id":              u.ID.Hex(),
		"accountId":        u.AccountID,
		"username":         u.Username,
		"email":            u.Email,
		"isEmailVerified":  u.IsEmailVerified,
		"role":             u.Role,
		"forceEmailChange": s.ForceEmailChange(u),
		"backgroundPrefs":  u.BackgroundPrefs,
		"personalWallpapers": nonNilWallpapers(u.PersonalWallpapers),
	}
}

// MeJSON 组装 /me 响应（对齐 session.js GET /me）。
func (s *AuthService) MeJSON(u *model.User) gin.H {
	return gin.H{
		"_id":                   u.ID.Hex(),
		"accountId":             u.AccountID,
		"username":              u.Username,
		"email":                 u.Email,
		"isEmailVerified":       u.IsEmailVerified,
		"role":                  u.Role,
		"avatar":                u.Avatar,
		"emailNotificationPrefs": u.EmailNotificationPrefs,
		"backgroundPrefs":       u.BackgroundPrefs,
		"personalWallpapers":    nonNilWallpapers(u.PersonalWallpapers),
	}
}

// nonNilWallpapers 把 nil 墙纸数组归一化为空数组（对齐 Express 输出 []）。
func nonNilWallpapers(w []model.Wallpaper) []model.Wallpaper {
	if w == nil {
		return []model.Wallpaper{}
	}
	return w
}

// IssueSession 签发双 token、创建会话、设置 cookie 并写审计日志。
// 对齐 session.js 登录成功路径 L427-453。
func (s *AuthService) IssueSession(c *gin.Context, u *model.User, deviceInfo model.DeviceInfo, ip string, action string) error {
	accessToken, err := s.Signer.Sign(u.ID.Hex(), "access", s.Config.JWT.AccessTTL, nil)
	if err != nil {
		return err
	}
	refreshToken, err := s.Signer.Sign(u.ID.Hex(), "refresh", s.Config.JWT.RefreshTTL, nil)
	if err != nil {
		return err
	}
	session := &model.UserSession{
		UserID:           u.ID,
		RefreshTokenHash: auth.HashToken(refreshToken),
		DeviceInfo:       deviceInfo,
		IP:               ip,
		IsActive:         true,
		LoginAt:          time.Now(),
		LastActiveAt:     time.Now(),
	}
	if err := s.Repos.Sessions.Create(c.Request.Context(), session); err != nil {
		return err
	}
	auth.SetAuthCookies(c, accessToken, refreshToken, !s.Config.IsDev)
	// 审计（失败静默）。
	go s.Audit(c.Request.Context(), u, action, "auth", "User "+strings.ToLower(action)+" success", ip)
	return nil
}

// Audit 写审计日志（失败静默，对齐 logManual 的 catch(()=>{})）。
func (s *AuthService) Audit(ctx context.Context, u *model.User, action, target, details, ip string) {
	userID := u.ID
	userName := u.Username
	if userName == "" {
		userName = u.AccountID
	}
	_ = s.Repos.AuditLogs.Create(ctx, &model.AuditLog{
		UserID:   &userID,
		UserName: userName,
		Action:   action,
		Target:   target,
		Details:  details,
		IP:       ip,
	})
}

// ClientIP 提取客户端 IP（对齐 helpers.js getClientIp：req.ip 或 XFF 首值）。
func (s *AuthService) ClientIP(c *gin.Context) string {
	if s.Config.IsDev {
		return clientIPFromRequest(c)
	}
	return clientIPFromRequest(c)
}

// EmailRegex 是邮箱格式校验（对齐 register L169）。
var EmailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// AccountIDRegex 是 accountId 校验（对齐 register L172）。
var AccountIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func containsFoldSlice(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// clientIPFromRequest 从 X-Forwarded-For 首值或 RemoteAddr 提取客户端 IP。
func clientIPFromRequest(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			xff = xff[:idx]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}
