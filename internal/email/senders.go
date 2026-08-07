package email

import (
	"context"
	"time"
)

// DeviceInfo 是邮件中展示的设备信息（对应 Express parsed 设备解析结果）。
type DeviceInfo struct {
	Browser        string
	BrowserVersion string
	OS             string
	OSVersion      string
	DeviceType     string
}

// siteURL 返回站点 URL（对齐 getSiteUrl）。
func (c *Client) siteURL() string {
	if c.cfg.Server.FrontendURL != "" {
		return c.cfg.Server.FrontendURL
	}
	if c.cfg.Server.SiteURL != "" {
		return c.cfg.Server.SiteURL
	}
	return "http://localhost:3000"
}

// SendVerificationCodeEmail 发送 6 位邮箱验证码邮件（场景 register/login/changeEmail）。
// 对齐 email.js sendVerificationCodeEmail。
func (c *Client) SendVerificationCodeEmail(ctx context.Context, to, code, scene string) bool {
	sceneMap := map[string]struct {
		subject string
		title   string
		desc    string
	}{
		"register":   {"邮箱验证码 - 兽剧聚合平台", "欢迎注册，请验证邮箱", "感谢您注册兽剧聚合平台！请使用下方验证码完成邮箱验证："},
		"login":      {"登录邮箱验证码 - 兽剧聚合平台", "登录邮箱验证", "检测到您的账号尚未验证邮箱，请使用下方验证码完成验证后继续登录："},
		"changeEmail": {"新邮箱验证码 - 兽剧聚合平台", "验证新邮箱", "您正在修改账号绑定邮箱，请使用下方验证码验证新邮箱地址："},
	}
	cfg := sceneMap[scene]
	if cfg.subject == "" {
		cfg = sceneMap["register"]
	}
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">` + cfg.title + `</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">` + cfg.desc + `</p>` +
		`<div style="text-align:center;margin:24px 0;">` +
		`<div style="display:inline-block;background-color:#6366f1;color:#ffffff;padding:16px 40px;border-radius:12px;font-size:32px;font-weight:700;letter-spacing:8px;font-family:'Courier New',monospace;">` + code + `</div></div>` +
		`<p style="margin:0 0 16px;color:#94a3b8;font-size:12px;text-align:center;">此验证码 10 分钟内有效</p>` +
		EmailInfoBox("如果您没有发起此操作，请忽略此邮件，您的账号不会被影响。", "info")
	html := c.BuildEmailHTML(ctx, "兽剧聚合平台", c.siteURL(), body, "您的验证码："+code)
	return c.Send(ctx, to, cfg.subject, html)
}

// SendPasswordResetEmail 发送密码重置链接邮件（链接 1 小时有效）。
// 对齐 email.js sendPasswordResetEmail。
func (c *Client) SendPasswordResetEmail(ctx context.Context, to, resetToken string) bool {
	resetURL := c.siteURL() + "/reset-password?token=" + resetToken
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">密码重置</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">您收到此邮件是因为您（或其他人）请求重置账户密码。请点击下方按钮完成重置：</p>` +
		`<p style="margin:20px 0;">` + EmailButton("重置密码", resetURL, "primary") + `</p>` +
		EmailInfoBox("如果您没有请求重置密码，请忽略此邮件，您的密码不会被更改。<br><br>此链接 1 小时后失效。如无法点击按钮，请复制以下地址到浏览器：<br><span style=\"color:#6366f1;word-break:break-all;\">"+resetURL+"</span>", "info")
	html := c.BuildEmailHTML(ctx, "兽剧聚合平台", c.siteURL(), body, "您请求了密码重置")
	return c.Send(ctx, to, "密码重置 - 兽剧聚合平台", html)
}

// SendNotificationEmail 发送通用通知邮件（HTML 由调用方提供，走统一模板）。
func (c *Client) SendNotificationEmail(ctx context.Context, to, subject, htmlContent, preheader string) bool {
	html := c.BuildEmailHTML(ctx, "兽剧聚合平台", c.siteURL(), htmlContent, preheader)
	return c.Send(ctx, to, subject, html)
}

// SendNewDeviceLoginEmail 发送新设备登录验证码邮件。
// 对齐 session.js 登录设备检测分支的内联邮件（含 Apple 版本号免责声明）。
func (c *Client) SendNewDeviceLoginEmail(ctx context.Context, to string, d DeviceInfo, ip, region, code string, loginTime time.Time) bool {
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">新设备登录验证</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">检测到您的账号在新设备上尝试登录：</p>`
	info := `<p style="margin:4px 0;"><strong>浏览器：</strong>` + orUnknown(d.Browser) + " " + d.BrowserVersion + `</p>` +
		`<p style="margin:4px 0;"><strong>操作系统：</strong>` + orUnknown(d.OS) + " " + d.OSVersion + `</p>` +
		`<p style="margin:4px 0;"><strong>设备类型：</strong>` + orUnknown(d.DeviceType) + `</p>` +
		`<p style="margin:4px 0;"><strong>IP地址：</strong>` + ip + `</p>`
	if d.OS == "iOS" || d.OS == "iPadOS" || d.OS == "macOS" {
		info += `<p style="margin:8px 0 0;color:#94a3b8;font-size:12px;">* 因为Apple隐私策略，版本号可能不准确</p>`
	}
	body += EmailInfoBox(info, "warning") +
		`<p style="margin:16px 0;color:#475569;font-size:14px;">如非本人操作，请忽略此邮件。如确认是本人，请使用下方验证码在登录页面完成验证：</p>` +
		`<div style="text-align:center;margin:24px 0;">` +
		`<div style="display:inline-block;background-color:#6366f1;color:#ffffff;padding:16px 40px;border-radius:12px;font-size:32px;font-weight:700;letter-spacing:8px;font-family:'Courier New',monospace;">` + code + `</div></div>` +
		`<p style="margin:0;color:#94a3b8;font-size:12px;text-align:center;">此验证码 10 分钟内有效</p>`
	html := c.BuildEmailHTML(ctx, "兽剧聚合平台", c.siteURL(), body, "您的验证码："+code)
	return c.Send(ctx, to, "新设备登录验证码", html)
}

// orUnknown 空值返回 "未知"。
func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}
