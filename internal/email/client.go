// Package email 封装 SMTP 邮件发送，对齐 Express utils/email.js。
//
// 配置来源：ini [email] 段 + 环境变量 EMAIL_*（不再读取 SiteContent email 数据库配置）。
// 目标邮箱限流：每收件邮箱 1 小时最多 10 封（内存窗口）。
package email

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/wneessen/go-mail"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// Config 是 SMTP 配置（来自 ini [email] 段 / 环境变量 EMAIL_*）。
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Pass     string `json:"pass"`
	FromName string `json:"fromName"`
}

// siteSettings 是站点导航栏信息（SiteContent 'settings'）。
type siteSettings struct {
	SiteName string `json:"siteName"`
	NavLogo  string `json:"navLogo"`
}

// siteAbout 是站点 about 信息（SiteContent 'about'）。
type siteAbout struct {
	ICP          string `json:"icp"`
	PoliceRecord string `json:"policeRecord"`
	Version      string `json:"version"`
}

// clientCache 缓存配置或站点信息。
type clientCache struct {
	value any
	at    time.Time
}

// Client 是邮件客户端。
type Client struct {
	cfg *config.Config
	sc  *repository.SiteContentRepo

	target *TargetRate

	mu            sync.Mutex
	settingsCache *clientCache
	aboutCache    *clientCache

	// sendMail 是 SMTP 发送注入点（测试用）。默认用 go-mail。
	sendMail func(host string, port int, user, pass, fromName, to, subject, html string) (bool, error)
}

const cacheTTL = 5 * time.Minute

// NewClient 构造邮件客户端。
func NewClient(cfg *config.Config, sc *repository.SiteContentRepo) *Client {
	c := &Client{cfg: cfg, sc: sc, target: NewTargetRate(10, time.Hour)}
	c.sendMail = sendViaGoMail
	return c
}

// SetSendMail 覆盖发送实现（测试注入）。
func (c *Client) SetSendMail(fn func(host string, port int, user, pass, fromName, to, subject, html string) (bool, error)) {
	c.sendMail = fn
}

// GetConfig 返回当前 SMTP 配置（仅来自 ini/env，不再读取 SiteContent email 数据库配置）。
// 未配置返回 ok=false。
func (c *Client) GetConfig(ctx context.Context) (Config, bool) {
	ec := c.envConfig()
	return ec, ec.Host != "" && ec.User != "" && ec.Pass != ""
}

// envConfig 从环境配置（config.Email）构造 Config。
func (c *Client) envConfig() Config {
	return Config{
		Host:     c.cfg.Email.Host,
		Port:     c.cfg.Email.Port,
		User:     c.cfg.Email.User,
		Pass:     c.cfg.Email.Pass,
		FromName: c.cfg.Email.FromName,
	}
}

// GetSiteSettings 返回站点导航栏信息（SiteContent 'settings'，5 分钟缓存）。
func (c *Client) GetSiteSettings(ctx context.Context) siteSettings {
	c.mu.Lock()
	if c.settingsCache != nil && time.Since(c.settingsCache.at) < cacheTTL {
		v := c.settingsCache.value.(siteSettings)
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	var s siteSettings
	if c.sc != nil {
		if doc, err := c.sc.FindByKey(ctx, "settings"); err == nil {
			_ = json.Unmarshal([]byte(doc.Content), &s)
		}
	}
	c.mu.Lock()
	c.settingsCache = &clientCache{value: s, at: time.Now()}
	c.mu.Unlock()
	return s
}

// GetSiteAbout 返回站点 about 信息（SiteContent 'about'，5 分钟缓存）。
func (c *Client) GetSiteAbout(ctx context.Context) siteAbout {
	c.mu.Lock()
	if c.aboutCache != nil && time.Since(c.aboutCache.at) < cacheTTL {
		v := c.aboutCache.value.(siteAbout)
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	var a siteAbout
	if c.sc != nil {
		if doc, err := c.sc.FindByKey(ctx, "about"); err == nil {
			_ = json.Unmarshal([]byte(doc.Content), &a)
		}
	}
	c.mu.Lock()
	c.aboutCache = &clientCache{value: a, at: time.Now()}
	c.mu.Unlock()
	return a
}

// Send 发送邮件：目标限流 → SMTP；未配置或失败返回 false（对齐 Express 返回 bool）。
// 三种失败均记日志，避免静默：限流/未配置记 Warn，SMTP 发送失败记 Error（含 err 与 host:port，不含 pass）。
func (c *Client) Send(ctx context.Context, to, subject, html string) bool {
	to = toLowerTrim(to)
	if !c.target.Allow(to) {
		slog.Warn("[Email] skip: target rate-limited", "to", to, "subject", subject)
		return false
	}
	cfg, ok := c.GetConfig(ctx)
	if !ok {
		slog.Warn("[Email] skip: SMTP not configured", "to", to, "subject", subject)
		return false
	}
	fromName := cfg.FromName
	if fromName == "" {
		fromName = firstNonEmpty(c.cfg.Email.FromName, "兽剧聚合平台")
	}
	okSend, err := c.sendMail(cfg.Host, cfg.Port, cfg.User, cfg.Pass, fromName, to, subject, html)
	if err != nil {
		slog.Error("[Email] send failed", "to", to, "subject", subject, "host", cfg.Host, "port", cfg.Port, "err", err)
	}
	return okSend
}

// StartCleanup 启动后台清理：每 interval 清理一次目标限流器的过期记录。
// 不阻塞；纯内存清理，进程退出时随进程自然终止，无需优雅关停协调。
func (c *Client) StartCleanup(interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			c.target.Cleanup()
		}
	}()
}

// sendViaGoMail 用 go-mail 发送。
func sendViaGoMail(host string, port int, user, pass, fromName, to, subject, html string) (bool, error) {
	m := mail.NewMsg()
	from := firstNonEmpty(user, "no-reply@"+host)
	if err := m.From(from); err != nil {
		return false, err
	}
	if err := m.To(to); err != nil {
		return false, err
	}
	m.Subject(subject)
	m.SetBodyString(mail.TypeTextHTML, html)

	opts := []mail.Option{mail.WithPort(port), mail.WithTimeout(15 * time.Second)}
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
	c, err := mail.NewClient(host, opts...)
	if err != nil {
		return false, err
	}
	defer c.Close()
	if err := c.DialAndSend(m); err != nil {
		return false, err
	}
	return true, nil
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func toLowerTrim(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// isLocalhost 判断主机是否为本地（email.js requireTLS 排除）。
func isLocalhost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
