package ratelimit

import (
	"net"
	"strings"
	"time"
)

// 命名限流器名称（用于日志与 key 命名空间，对齐 rateLimits.js 各 limiter 变量名）。
const (
	GlobalName             = "global"
	CaptchaName            = "captcha"
	AuthName               = "auth"
	AdminAuthName          = "adminAuth"
	RegisterName           = "register"
	CheckAccountIDName     = "checkAccountId"
	PasswordResetName      = "passwordReset"
	ChangeEmailName        = "changeEmail"
	TwoFactorName          = "twoFactor"
	EmailVerifyName        = "emailVerify"
	RequestEmailChangeName = "requestEmailChange"
)

// Spec 描述一个命名限流器（windowMs/max/message 逐项照抄 backend/config/rateLimits.js）。
//
// Mounts 列出该限流器在 Express 中的挂载路径（不含 /api 前缀）。空表示全局型
// 限流器（挂载于 /api/，匹配全部 /api/ 前缀，含 /api/v1/ 与 /api/health）。
// 挂载语义由 middleware.RateLimit 消费：每个 Mount 只匹配 /api/<Mount>
// 精确与子路径，且【不】匹配 /api/v1/ 前缀。
type Spec struct {
	Name    string
	Mounts  []string
	Window  time.Duration
	Max     int
	Message string
}

// 限流器参数与文案（逐项对齐 rateLimits.js；Message 为 429 响应体
// {"message": <Message>} 的内容）。Mounts 为 src/index.js:297-317 的挂载路径
// （不含 /api 前缀；空 = 全局）。
var (
	// GlobalSpec 全局限流器 300/60s。跳过 /api/translate 与 /api/auth/captcha
	// 路径（对齐 globalLimiter.skip，由 middleware 施加）。
	GlobalSpec = Spec{
		Name: GlobalName, Window: time.Minute, Max: 300,
		Message: "请求过于频繁，请稍后再试",
	}
	// CaptchaSpec 验证码限流器 60/60s（挂 /api/auth/captcha）。
	CaptchaSpec = Spec{
		Name: CaptchaName, Mounts: []string{"/auth/captcha"},
		Window: time.Minute, Max: 60,
		Message: "请求过于频繁，请稍后再试",
	}
	// AuthSpec 登录限流器 5/15min（挂 /api/auth/login）。非生产且
	// SKIP_RATE_LIMIT=1 时跳过（对齐 authLimiter.skip，由 middleware 施加）。
	AuthSpec = Spec{
		Name: AuthName, Mounts: []string{"/auth/login"},
		Window: 15 * time.Minute, Max: 5,
		Message: "登录尝试过多，请15分钟后再试",
	}
	// AdminAuthSpec 管理员登录限流器 5/15min（挂 /api/admin/login）。
	AdminAuthSpec = Spec{
		Name: AdminAuthName, Mounts: []string{"/admin/login"},
		Window: 15 * time.Minute, Max: 5,
		Message: "登录尝试过多，请15分钟后再试",
	}
	// RegisterSpec 注册限流器 3/1h（挂 /api/auth/register）。
	RegisterSpec = Spec{
		Name: RegisterName, Mounts: []string{"/auth/register"},
		Window: time.Hour, Max: 3,
		Message: "注册尝试过多，请1小时后再试",
	}
	// CheckAccountIDSpec 账号ID占用检查限流器 20/60s（挂 /api/auth/check-accountId）。
	CheckAccountIDSpec = Spec{
		Name: CheckAccountIDName, Mounts: []string{"/auth/check-accountId"},
		Window: time.Minute, Max: 20,
		Message: "请求过于频繁，请稍后再试",
	}
	// PasswordResetSpec 密码重置限流器 3/1h（挂 forgot/reset/change-password 三处）。
	PasswordResetSpec = Spec{
		Name:   PasswordResetName,
		Mounts: []string{"/auth/forgot-password", "/auth/reset-password", "/auth/change-password"},
		Window: time.Hour, Max: 3,
		Message: "操作过于频繁，请稍后再试",
	}
	// ChangeEmailSpec 修改邮箱限流器 3/1h（挂 /api/auth/change-email）。
	ChangeEmailSpec = Spec{
		Name: ChangeEmailName, Mounts: []string{"/auth/change-email"},
		Window: time.Hour, Max: 3,
		Message: "操作过于频繁，请稍后再试",
	}
	// TwoFactorSpec 2FA 限流器 5/15min（挂 login-2fa/verify-enable/disable/verify/
	// refresh/verify-device/confirm-device-login 七处）。
	TwoFactorSpec = Spec{
		Name: TwoFactorName,
		Mounts: []string{"/auth/login-2fa", "/2fa/verify-enable", "/2fa/disable",
			"/2fa/verify", "/auth/refresh", "/auth/verify-device", "/auth/confirm-device-login"},
		Window: 15 * time.Minute, Max: 5,
		Message: "操作过于频繁，请15分钟后再试",
	}
	// EmailVerifySpec 邮箱验证限流器 5/1h（挂 verify-email/resend-verification/
	// resend-verification-by-email 三处）。
	EmailVerifySpec = Spec{
		Name: EmailVerifyName,
		Mounts: []string{"/auth/verify-email", "/auth/resend-verification",
			"/auth/resend-verification-by-email"},
		Window: time.Hour, Max: 5,
		Message: "操作过于频繁，请稍后再试",
	}
	// RequestEmailChangeSpec 申请更换邮箱限流器 3/1h（挂 /api/auth/request-email-change）。
	// 注意：Express 的 keyGenerator 调用 ipKeyGenerator(req)（传入请求对象而非 IP），
	// 实际生成的 key 恒为 "[object Object]:<newEmail 小写>" —— IP 被忽略，仅按邮箱限流。
	// 本实现逐字复刻该行为（middleware 按此规则生成 key），确保与 oracle 一致。
	RequestEmailChangeSpec = Spec{
		Name: RequestEmailChangeName, Mounts: []string{"/auth/request-email-change"},
		Window: time.Hour, Max: 3,
		Message: "操作过于频繁，请稍后再试",
	}
)

// IPKey 对齐 express-rate-limit 的 ipKeyGenerator 语义：
//   - IPv4（含 IPv4-mapped IPv6，如 ::ffff:127.0.0.1）→ 归一化 IPv4 字符串；
//   - 纯 IPv6 → 截断为 /56 网络前缀并附 "/56" 后缀（如 2001:db8:0:1:2:3:4:5
//     → "2001:db8::/56"，::1 → "::/56"）；
//   - 无法解析的输入原样返回。
func IPKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	masked := parsed.Mask(net.CIDRMask(56, 128))
	return masked.String() + "/56"
}

// NormalizeXFF 取 X-Forwarded-For 首值（逗号分隔，trim 空白）。
// 对齐 Express trust proxy=1 下 req.ip 取链上第一个地址的语义。
// 空串返回 ""，由调用方决定是否回退到 RemoteAddr。
func NormalizeXFF(xff string) string {
	if idx := strings.IndexByte(xff, ','); idx >= 0 {
		xff = xff[:idx]
	}
	return strings.TrimSpace(xff)
}
