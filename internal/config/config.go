// Package config 加载 neo-server 配置：ini 为主，环境变量覆盖。
//
// 设计（用户已确认 Q10/Q14）：
//   - 默认读取 OS 对应的默认路径（Linux: /etc/furry-drama-tracker/backend.ini，macOS: /Library/Application Support/furry-drama-tracker/backend.ini，Windows: C:\ProgramData\furry-drama-tracker\backend.ini），--config=/path 覆盖
//   - 同名环境变量优先于 ini（供 systemd 注入密钥、CI 注入 DEV_API_TOKEN 等）
//   - 启动致命校验：缺少 JWT secret 或 MONGO_URI，或 JWT secret < 32 字符 → 退出
//
// 兼容原 .env 变量名：JWT_SECRET / MONGO_URI / ENCRYPTION_KEY / ALTCHA_HMAC_KEY /
// FRONTEND_URL / SITE_URL / ALLOWED_ORIGINS / EMAIL_* / VAPID_* / DEMO_EMAILS /
// DEV_API_TOKEN / PORT / NODE_ENV / SKIP_RATE_LIMIT。
//
// .env 文件支持：启动时自动读取当前目录的 .env 文件（如果存在），
// 将其中未在真实环境中设置的变量注入到 os.Environ，实现与旧 Express 后端 dotenv 的向下兼容。
// 优先级：真实环境变量 > .env 文件 > ini 配置文件。
package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/ini.v1"
)

const (
	// MinSecretLen 对齐 Express 启动校验（src/index.js:58-61）。
	MinSecretLen = 32

	defaultFrontendURL = "http://localhost:3000"
	defaultUploadsDir  = "./uploads"
	defaultIconsDir    = "./uploads/icons"
	// 默认 CORS 白名单（对齐 src/index.js:198-209 硬编码的 localhost 端口）。
	defaultOrigins = "http://localhost:3000,http://localhost:3001,http://localhost:3002,http://localhost:5000"
)

// Config 是全部运行配置。
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	JWT      JWTConfig
	Email    EmailConfig
	VAPID    VAPIDConfig
	Security SecurityConfig
	// IsDev 等价于 NODE_ENV !== 'production'，控制 cookie secure、错误栈、CORS 等。
	IsDev bool
}

// ServerConfig 服务监听与站点配置。
type ServerConfig struct {
	// Listen 是监听地址，形如 "tcp:0.0.0.0:5000" 或 "unix:/run/xxx.sock"（"@name" 为 abstract）。
	Listen       string
	Port         int
	// NodeEnv 等价于 NODE_ENV（development|production）。
	NodeEnv      string
	FrontendURL  string
	SiteURL      string
	UploadsDir   string
	// IconsDir 是 SVG 图标文件的存储目录（可配置；默认 ./uploads/icons）。
	IconsDir     string
	AllowOrigins []string
}

// DatabaseConfig MongoDB 连接配置。
type DatabaseConfig struct {
	URI    string
	Name   string
	Pool   int
	Min    int
	Select int
	Sock   int
}

// JWTConfig 令牌与密钥。
type JWTConfig struct {
	Secret        string
	EncryptionKey string
	AltchaHMACKey string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
	DevAPIToken   string
	DemoEmails    []string
}

// EmailConfig SMTP 配置（EMAail_* 回退值；运行时优先 SiteContent 配置）。
type EmailConfig struct {
	Host     string
	Port     int
	User     string
	Pass     string
	FromName string
}

// VAPIDConfig Web Push 配置。
type VAPIDConfig struct {
	PublicKey  string
	PrivateKey string
	Subject    string
}

// SecurityConfig 安全与限流参数。
type SecurityConfig struct {
	LoginMaxAttempts int
	LoginLockMinutes int
	CSRFMaxAgeHours  int
	RateLimitSkip    bool
}

// Load 加载配置：先读 .env（如果存在），再读 ini（path 为空则尝试 DefaultPath，
// 缺文件静默跳过），最后用环境变量覆盖同名项。
//
// 优先级：真实环境变量 > .env 文件 > ini 配置文件 > 默认值。
func Load(path string) (*Config, error) {
	cfg := defaults()

	// 0) 加载 .env 文件（当前目录，如果存在）——仅为未在真实环境中设置的变量注入值
	loadDotEnv()
	// 1) ini
	if path == "" {
		path = DefaultPath
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := applyINI(cfg, data); err != nil {
			return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
		}
	} else if path != DefaultPath {
		return nil, fmt.Errorf("配置文件不存在: %s", path)
	}

	// 2) env 覆盖（真实环境变量 + .env 注入的变量）
	applyEnv(cfg)

	// 3) 派生 IsDev
	cfg.IsDev = !strings.EqualFold(cfg.Server.NodeEnv, "production")

	// 4) 致命校验
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadDotEnv 读取当前目录的 .env 文件（如果存在），将其中未在真实环境中
// 已设置的变量注入到 os.Environ。委托给 godotenv：支持 # 注释、export 前缀、
// 引号包裹的值等 dotenv 语法。文件不存在时静默跳过。
//
// godotenv.Load 默认 Overload=false，仅在变量尚未设置时注入，
// 因此真实环境变量优先级高于 .env 文件。
func loadDotEnv() {
	_ = godotenv.Load() // 文件不存在时静默跳过
}

// DefaultPath 是配置文件默认路径，按 OS 区分：
//   - Windows:  C:\ProgramData\furry-drama-tracker\backend.ini
//   - Linux:    /etc/furry-drama-tracker/backend.ini
//   - macOS:    /Library/Application Support/furry-drama-tracker/backend.ini
var DefaultPath string

func init() {
	switch runtime.GOOS {
	case "windows":
		DefaultPath = `C:\ProgramData\furry-drama-tracker\backend.ini`
	case "darwin":
		DefaultPath = "/Library/Application Support/furry-drama-tracker/backend.ini"
	default: // linux 及其它 Unix
		DefaultPath = "/etc/furry-drama-tracker/backend.ini"
	}
}

// defaults 返回带默认值的配置。
func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:       "tcp:0.0.0.0:5000",
			Port:         5000,
			NodeEnv:      "development",
			FrontendURL:  defaultFrontendURL,
			SiteURL:      defaultFrontendURL,
			UploadsDir:   defaultUploadsDir,
			IconsDir:     defaultIconsDir,
			AllowOrigins: splitTrim(defaultOrigins),
		},
		Database: DatabaseConfig{
			Pool: 10, Min: 2, Select: 10, Sock: 45,
		},
		JWT: JWTConfig{
			AccessTTL:  15 * time.Minute,
			RefreshTTL: 7 * 24 * time.Hour,
			DemoEmails: []string{"demo@furry09.com"},
		},
		Email: EmailConfig{Port: 465},
		Security: SecurityConfig{
			LoginMaxAttempts: 5,
			LoginLockMinutes: 30,
			CSRFMaxAgeHours:  24,
		},
	}
}

// applyINI 把 ini 内容合并进 cfg（仅覆盖已存在的键）。
func applyINI(cfg *Config, data []byte) error {
	f, err := ini.Load(data)
	if err != nil {
		return err
	}
	server := f.Section("server")
	cfg.Server.Listen = server.Key("listen").String()
	cfg.Server.FrontendURL = firstNonEmpty(server.Key("frontend_url").String(), cfg.Server.FrontendURL)
	cfg.Server.SiteURL = firstNonEmpty(server.Key("site_url").String(), cfg.Server.SiteURL)
	cfg.Server.UploadsDir = firstNonEmpty(server.Key("uploads_dir").String(), cfg.Server.UploadsDir)
	cfg.Server.IconsDir = firstNonEmpty(server.Key("icons_dir").String(), cfg.Server.IconsDir)
	if v := server.Key("allow_origins").String(); v != "" {
		cfg.Server.AllowOrigins = splitTrim(v)
	}
	if v := server.Key("node_env").String(); v != "" {
		cfg.Server.NodeEnv = v
	}
	if port := server.Key("port").MustInt(0); port > 0 {
		cfg.Server.Port = port
	}

	db := f.Section("database")
	cfg.Database.URI = db.Key("uri").String()
	cfg.Database.Name = db.Key("name").String()
	if v := db.Key("pool_size").MustInt(0); v > 0 {
		cfg.Database.Pool = v
	}

	jwt := f.Section("jwt")
	cfg.JWT.Secret = jwt.Key("secret").String()
	cfg.JWT.EncryptionKey = jwt.Key("encryption_key").String()
	cfg.JWT.AltchaHMACKey = jwt.Key("altcha_hmac_key").String()
	if v := jwt.Key("access_ttl").String(); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.JWT.AccessTTL = d
		}
	}
	if v := jwt.Key("refresh_ttl").String(); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.JWT.RefreshTTL = d
		}
	}
	cfg.JWT.DevAPIToken = jwt.Key("dev_api_token").String()
	if v := jwt.Key("demo_emails").String(); v != "" {
		cfg.JWT.DemoEmails = splitTrim(v)
	}

	email := f.Section("email")
	cfg.Email.Host = email.Key("host").String()
	cfg.Email.Port = email.Key("port").MustInt(465)
	cfg.Email.User = email.Key("user").String()
	cfg.Email.Pass = email.Key("pass").String()
	cfg.Email.FromName = email.Key("from_name").String()

	vapid := f.Section("vapid")
	cfg.VAPID.PublicKey = vapid.Key("public_key").String()
	cfg.VAPID.PrivateKey = vapid.Key("private_key").String()
	cfg.VAPID.Subject = vapid.Key("subject").String()

	sec := f.Section("security")
	cfg.Security.LoginMaxAttempts = sec.Key("login_max_attempts").MustInt(5)
	cfg.Security.LoginLockMinutes = sec.Key("login_lock_minutes").MustInt(30)
	cfg.Security.CSRFMaxAgeHours = sec.Key("csrf_max_age_h").MustInt(24)
	return nil
}

// applyEnv 用环境变量覆盖 ini 值。
func applyEnv(cfg *Config) {
	setStr(&cfg.JWT.Secret, os.Getenv("JWT_SECRET"))
	setStr(&cfg.Database.URI, os.Getenv("MONGO_URI"))
	setStr(&cfg.JWT.EncryptionKey, os.Getenv("ENCRYPTION_KEY"))
	setStr(&cfg.JWT.AltchaHMACKey, os.Getenv("ALTCHA_HMAC_KEY"))
	setStr(&cfg.Server.FrontendURL, os.Getenv("FRONTEND_URL"))
	setStr(&cfg.Server.SiteURL, os.Getenv("SITE_URL"))
	setStr(&cfg.Server.IconsDir, os.Getenv("ICONS_DIR"))
	if v := os.Getenv("ALLOWED_ORIGINS"); v != "" {
		cfg.Server.AllowOrigins = splitTrim(v)
	}
	setStr(&cfg.Email.Host, os.Getenv("EMAIL_HOST"))
	if v := os.Getenv("EMAIL_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Email.Port = p
		}
	}
	setStr(&cfg.Email.User, os.Getenv("EMAIL_USER"))
	setStr(&cfg.Email.Pass, os.Getenv("EMAIL_PASS"))
	setStr(&cfg.Email.FromName, os.Getenv("EMAIL_FROM_NAME"))
	setStr(&cfg.VAPID.PublicKey, os.Getenv("VAPID_PUBLIC_KEY"))
	setStr(&cfg.VAPID.PrivateKey, os.Getenv("VAPID_PRIVATE_KEY"))
	setStr(&cfg.VAPID.Subject, os.Getenv("VAPID_SUBJECT"))
	if v := os.Getenv("DEMO_EMAILS"); v != "" {
		cfg.JWT.DemoEmails = splitTrim(v)
	}
	setStr(&cfg.JWT.DevAPIToken, os.Getenv("DEV_API_TOKEN"))
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = p
		}
	}
	if v := os.Getenv("NODE_ENV"); v != "" {
		cfg.Server.NodeEnv = v
	}
	if v := os.Getenv("SKIP_RATE_LIMIT"); v == "1" {
		cfg.Security.RateLimitSkip = true
	}
}

// validate 执行启动致命校验（对齐 src/index.js:50-61）。
func (c *Config) validate() error {
	if c.JWT.Secret == "" {
		return fmt.Errorf("缺少 JWT secret（ini [jwt].secret 或环境变量 JWT_SECRET）")
	}
	if len(c.JWT.Secret) < MinSecretLen {
		return fmt.Errorf("JWT secret 长度必须 >= %d 字符", MinSecretLen)
	}
	if c.Database.URI == "" {
		return fmt.Errorf("缺少 MONGO_URI（ini [database].uri 或环境变量 MONGO_URI）")
	}
	return nil
}

func setStr(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// splitTrim 按逗号切分并 trim 去空。
func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
