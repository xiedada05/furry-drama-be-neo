package email

import (
	"context"
	"fmt"
	"time"

	"github.com/wneessen/go-mail"
)

// 本文件为 /api/site-content 域（routes/siteContent.js）补充两个挂在已有
// *Client 类型上的方法：SendSiteTestEmail（POST /test-email）与 ClearCache
// （PUT /:key 中 key=email 时的 clearEmailCache）。不改动既有文件。

// SendSiteTestEmail 用调用方提供的 SMTP 配置发送一封测试邮件（对齐
// siteContent.js POST /test-email 的 nodemailer.createTransport + sendMail）：
//   - secure = port === 465（465 端口启用 SSL，其余按 TLS 策略）
//   - socketTimeout 15s（go-mail WithTimeout，go-mail v0.8.1 无独立连接超时选项）
//   - From 显示名 "fromName || '兽剧聚合平台'"，主题 '邮件服务测试 - 兽剧聚合平台'
//
// 发送失败返回错误，由 handler 渲染 400 '邮件发送失败，请检查邮件服务配置'。
func (c *Client) SendSiteTestEmail(ctx context.Context, host string, port int, user, pass, fromName, to string) error {
	siteURL := c.siteURL()
	display := firstNonEmpty(fromName, "兽剧聚合平台")
	body := `<h2 style="margin:0 0 16px;color:#1e293b;font-size:22px;font-weight:700;">邮件服务测试</h2>` +
		`<p style="margin:0 0 16px;color:#475569;font-size:14px;">这是一封测试邮件，用于验证邮件服务配置是否正确。</p>` +
		EmailInfoBox("如果您收到了此邮件，说明邮件服务配置成功！", "success") +
		`<p style="margin:20px 0;">` + EmailButton("访问站点", siteURL, "secondary") + `</p>`
	html := c.BuildEmailHTML(ctx, display, siteURL, body, "邮件服务测试")
	return sendSiteTestMail(host, port, user, pass, display, to, "邮件服务测试 - 兽剧聚合平台", html)
}

// ClearCache 清空站点设置/关于信息的缓存（对齐 email.js clearEmailCache）。
func (c *Client) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settingsCache = nil
	c.aboutCache = nil
}

// sendSiteTestMail 按 Express siteContent.js 的 transporter 语义发送测试邮件。
func sendSiteTestMail(host string, port int, user, pass, fromName, to, subject, html string) error {
	m := mail.NewMsg()
	if err := m.FromFormat(fromName, user); err != nil {
		return err
	}
	if err := m.To(to); err != nil {
		return err
	}
	m.Subject(subject)
	m.SetBodyString(mail.TypeTextHTML, html)

	opts := []mail.Option{
		mail.WithPort(port),
		mail.WithTimeout(15 * time.Second),
	}
	if port == 465 {
		opts = append(opts, mail.WithSSL())
	} else if isLocalhost(host) {
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	} else {
		opts = append(opts, mail.WithTLSPolicy(mail.TLSOpportunistic))
	}
	if user != "" {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthPlain), mail.WithUsername(user), mail.WithPassword(pass))
	}
	client, err := mail.NewClient(host, opts...)
	if err != nil {
		return fmt.Errorf("create smtp client: %w", err)
	}
	defer client.Close()
	return client.DialAndSend(m)
}
