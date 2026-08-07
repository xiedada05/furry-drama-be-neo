// Package email 封装 SMTP 邮件发送，对齐 Express utils/email.js。
//
// 配置两级：SiteContent key='email' 的 JSON（5 分钟缓存，pass 字段用
// sha256(JWT_SECRET) 独立解密）→ 回退环境变量 EMAIL_*。
// 目标邮箱限流：每收件邮箱 1 小时最多 10 封（内存窗口）。
package email

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/wneessen/go-mail"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// Config 是 SMTP 配置（对齐 email.js getEmailConfig 返回的 data）。
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Pass     string `json:"pass"`
	Enabled  bool   `json:"enabled"`
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

	mu           sync.Mutex
	configCache  *clientCache
	settingsCache *clientCache
	aboutCache   *clientCache

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

// GetConfig 返回当前 SMTP 配置（SiteContent email → env 回退），未配置返回 ok=false。
func (c *Client) GetConfig(ctx context.Context) (Config, bool) {
	if cfg, ok := c.getDBCached(ctx); ok {
		return cfg, true
	}
	ec := c.envConfig()
	return ec, ec.Host != "" && ec.User != "" && ec.Pass != ""
}

// getDBCached 读 SiteContent email 配置（5 分钟缓存）。
func (c *Client) getDBCached(ctx context.Context) (Config, bool) {
	c.mu.Lock()
	if c.configCache != nil && time.Since(c.configCache.at) < cacheTTL {
		c.mu.Unlock()
		if v, ok := c.configCache.value.(Config); ok {
			return v, ok
		}
		return Config{}, false
	}
	c.mu.Unlock()

	if c.sc == nil {
		return Config{}, false
	}
	doc, err := c.sc.FindByKey(ctx, "email")
	if err != nil {
		return Config{}, false
	}
	var data Config
	if err := json.Unmarshal([]byte(doc.Content), &data); err != nil {
		return Config{}, false
	}
	// pass 独立解密：sha256(JWT_SECRET) 的 AES-256-CBC（对齐 email.js L54-67）。
	if data.Pass != "" {
		data.Pass = decryptEmailPass(data.Pass, c.cfg.JWT.Secret)
	}
	c.mu.Lock()
	c.configCache = &clientCache{value: data, at: time.Now()}
	c.mu.Unlock()
	if data.Enabled && data.Host != "" && data.User != "" && data.Pass != "" {
		return data, true
	}
	return Config{}, false
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
func (c *Client) Send(ctx context.Context, to, subject, html string) bool {
	to = toLowerTrim(to)
	if !c.target.Allow(to) {
		return false
	}
	cfg, ok := c.GetConfig(ctx)
	if !ok {
		return false
	}
	fromName := cfg.FromName
	if fromName == "" {
		fromName = firstNonEmpty(c.cfg.Email.FromName, "兽剧聚合平台")
	}
	okSend, _ := c.sendMail(cfg.Host, cfg.Port, cfg.User, cfg.Pass, fromName, to, subject, html)
	return okSend
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

// decryptEmailPass 独立解密 SiteContent email.pass：key = sha256(JWT_SECRET) raw，
// AES-256-CBC，格式 enc:<iv hex>:<ct hex>（对齐 email.js L54-67，注意与 fieldcrypto
// 的 ENCRYPTION_KEY 派生不同）。
func decryptEmailPass(text, jwtSecret string) string {
	if len(text) < 5 || text[:4] != "enc:" {
		return text
	}
	key := sha256.Sum256([]byte(jwtSecret))
	parts := splitN(text[4:], ":", 2)
	if len(parts) != 2 {
		return text
	}
	iv, err1 := hex.DecodeString(parts[0])
	ct, err2 := hex.DecodeString(parts[1])
	if err1 != nil || err2 != nil || len(iv) != aes.BlockSize {
		return text
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return text
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	// PKCS7 去填充（对齐 decipher.final 语义）。
	if len(plain) > 0 {
		if padLen := int(plain[len(plain)-1]); padLen > 0 && padLen <= aes.BlockSize && padLen <= len(plain) {
			plain = plain[:len(plain)-padLen]
		}
	}
	return string(plain)
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

func splitN(s, sep string, n int) []string {
	for i := 0; i+n-1 < len(s); i++ {
		if string(s[i:i+1]) == sep {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

// isLocalhost 判断主机是否为本地（email.js requireTLS 排除）。
func isLocalhost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
