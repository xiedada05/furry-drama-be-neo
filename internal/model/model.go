// Package model 定义 MongoDB 文档的 struct 映射（无 ODM，官方驱动直接读写）。
//
// 字段名对齐 Mongoose schema（backend/models/*.js），collection 名取 Mongo 默认
// 小写复数（users / usersessions / usedtokens / notifications / auditlogs /
// apiusages / sitecontents / settings）。
//
// 说明：
//   - password / twoFactorSecret / twoFactorBackupCodes 仅用于写读，JSON 响应
//     中永不输出（handler 层用 DTO 组装，这里用 json:"-" 双保险）。
//   - primitive.ObjectID 的 JSON 序列化自动输出 24 位 hex 字符串，对齐 mongoose
//     toJSON 行为。
package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// User 对应用户文档（models/User.js）。
type User struct {
	ID                     primitive.ObjectID     `bson:"_id,omitempty" json:"_id"`
	AccountID              string                 `bson:"accountId" json:"accountId"`
	Username               string                 `bson:"username" json:"username"`
	Email                  string                 `bson:"email" json:"email"`
	Password               string                 `bson:"password" json:"-"`
	Avatar                 string                 `bson:"avatar" json:"avatar"`
	DeviceInfo             DeviceInfo             `bson:"deviceInfo" json:"deviceInfo"`
	LastLoginAt            *time.Time             `bson:"lastLoginAt" json:"lastLoginAt"`
	LastLoginIP            string                 `bson:"lastLoginIp" json:"lastLoginIp"`
	LastLoginRegion        string                 `bson:"lastLoginRegion" json:"lastLoginRegion"`
	DeletionRequestedAt    *time.Time             `bson:"deletionRequestedAt" json:"deletionRequestedAt"`
	IsEmailVerified        bool                   `bson:"isEmailVerified" json:"isEmailVerified"`
	Role                   string                 `bson:"role" json:"role"`
	PasswordChangedAt      *time.Time             `bson:"passwordChangedAt" json:"passwordChangedAt"`
	TwoFactorEnabled       bool                   `bson:"twoFactorEnabled" json:"twoFactorEnabled"`
	TwoFactorSecret        string                 `bson:"twoFactorSecret" json:"-"`
	TwoFactorBackupCodes   []string               `bson:"twoFactorBackupCodes" json:"-"`
	EmailNotificationPrefs EmailNotificationPrefs `bson:"emailNotificationPrefs" json:"emailNotificationPrefs"`
	BackgroundPrefs        BackgroundPrefs        `bson:"backgroundPrefs" json:"backgroundPrefs"`
	PersonalWallpapers     []Wallpaper            `bson:"personalWallpapers" json:"personalWallpapers"`
	// ThemeID 是用户当前选择的主题（个人/系统主题均可；多端同步）。
	// 为零值表示未选择（回退站点默认主题）。
	ThemeID                primitive.ObjectID     `bson:"themeId,omitempty" json:"themeId"`
	// ThemeApplyIcons / ThemeApplyWallpaper 是应用主题时勾选的生效组合
	// （nil 表示未设置，视为全部应用）。
	ThemeApplyIcons     *bool `bson:"themeApplyIcons,omitempty" json:"themeApplyIcons"`
	ThemeApplyWallpaper *bool `bson:"themeApplyWallpaper,omitempty" json:"themeApplyWallpaper"`
	LoginAttempts          int                    `bson:"loginAttempts" json:"-"`
	LockUntil              int64                  `bson:"lockUntil" json:"-"`
	CreatedAt              time.Time              `bson:"createdAt" json:"createdAt"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}

// DeviceInfo 设备信息子文档（User 与 UserSession 共用）。
type DeviceInfo struct {
	Browser        string `bson:"browser" json:"browser"`
	BrowserVersion string `bson:"browserVersion" json:"browserVersion"`
	OS             string `bson:"os" json:"os"`
	OSVersion      string `bson:"osVersion" json:"osVersion"`
	DeviceType     string `bson:"deviceType" json:"deviceType"`
	DeviceModel    string `bson:"deviceModel" json:"deviceModel"`
	ScreenWidth    int    `bson:"screenWidth" json:"screenWidth"`
	ScreenHeight   int    `bson:"screenHeight" json:"screenHeight"`
	Language       string `bson:"language" json:"language"`
	UserAgent      string `bson:"userAgent" json:"userAgent"`
	Carrier        string `bson:"carrier" json:"carrier"`
	DeviceName     string `bson:"deviceName,omitempty" json:"deviceName,omitempty"`
}

// EmailNotificationPrefs 邮件通知偏好（7 键全默认 true）。
type EmailNotificationPrefs struct {
	EpisodeUpdate    bool `bson:"episodeUpdate" json:"episodeUpdate"`
	NewDeviceLogin   bool `bson:"newDeviceLogin" json:"newDeviceLogin"`
	FeedbackReply    bool `bson:"feedbackReply" json:"feedbackReply"`
	FriendLinkStatus bool `bson:"friendLinkStatus" json:"friendLinkStatus"`
	FriendLinkApply  bool `bson:"friendLinkApply" json:"friendLinkApply"`
	Announcement     bool `bson:"announcement" json:"announcement"`
	ReviewResult     bool `bson:"reviewResult" json:"reviewResult"`
}

// DefaultEmailNotificationPrefs 返回 7 键全 true 的默认偏好。
func DefaultEmailNotificationPrefs() EmailNotificationPrefs {
	return EmailNotificationPrefs{true, true, true, true, true, true, true}
}

// BackgroundPrefs 背景偏好子文档（默认 image:” enabled:false opacity:30 blur:0）。
type BackgroundPrefs struct {
	Image   string `bson:"image" json:"image"`
	Enabled bool   `bson:"enabled" json:"enabled"`
	Opacity int    `bson:"opacity" json:"opacity"`
	Blur    int    `bson:"blur" json:"blur"`
}

// DefaultBackgroundPrefs 返回默认背景偏好。
func DefaultBackgroundPrefs() BackgroundPrefs {
	return BackgroundPrefs{Image: "", Enabled: false, Opacity: 30, Blur: 0}
}

// Wallpaper 个人壁纸项。
type Wallpaper struct {
	URL     string    `bson:"url" json:"url"`
	Name    string    `bson:"name" json:"name"`
	AddedAt time.Time `bson:"addedAt" json:"addedAt"`
}

// UserSession 对应用户会话文档（models/UserSession.js）。
type UserSession struct {
	ID               primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID           primitive.ObjectID `bson:"userId" json:"userId"`
	TokenHash        string             `bson:"tokenHash,omitempty" json:"tokenHash,omitempty"`
	RefreshTokenHash string             `bson:"refreshTokenHash,omitempty" json:"refreshTokenHash,omitempty"`
	DeviceInfo       DeviceInfo         `bson:"deviceInfo" json:"deviceInfo"`
	IP               string             `bson:"ip" json:"ip"`
	IsActive         bool               `bson:"isActive" json:"isActive"`
	LoginAt          time.Time          `bson:"loginAt" json:"loginAt"`
	LastActiveAt     time.Time          `bson:"lastActiveAt" json:"lastActiveAt"`
	LogoutAt         *time.Time         `bson:"logoutAt" json:"logoutAt"`
}

// UsedToken 一次性令牌去重表（models/UsedToken.js，expiresAt 建 TTL 索引自动删除）。
type UsedToken struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	TokenHash string             `bson:"tokenHash" json:"tokenHash"`
	Purpose   string             `bson:"purpose" json:"purpose"`
	ExpiresAt time.Time          `bson:"expiresAt" json:"expiresAt"`
}

// Notification 通知文档（models/Notification.js）。
type Notification struct {
	ID             primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	UserID         primitive.ObjectID  `bson:"userId" json:"userId"`
	EpisodeID      *primitive.ObjectID `bson:"episodeId" json:"episodeId,omitempty"`
	EpisodeTitle   string              `bson:"episodeTitle" json:"episodeTitle"`
	EpisodeTitleEn string              `bson:"episodeTitleEn" json:"episodeTitleEn"`
	Type           string              `bson:"type" json:"type"`
	Link           string              `bson:"link" json:"link"`
	Message        string              `bson:"message" json:"message"`
	Metadata       primitive.M         `bson:"metadata" json:"metadata"`
	IsRead         bool                `bson:"isRead" json:"isRead"`
	CreatedAt      time.Time           `bson:"createdAt" json:"createdAt"`
}

// AuditLog 审计日志文档（models/AuditLog.js）。
type AuditLog struct {
	ID        primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	AdminID   *primitive.ObjectID `bson:"adminId" json:"adminId"`
	AdminName string              `bson:"adminName" json:"adminName"`
	UserID    *primitive.ObjectID `bson:"userId" json:"userId"`
	UserName  string              `bson:"userName" json:"userName"`
	Action    string              `bson:"action" json:"action"`
	Target    string              `bson:"target" json:"target"`
	Details   string              `bson:"details" json:"details"`
	IP        string              `bson:"ip" json:"ip"`
	UserAgent string              `bson:"userAgent" json:"userAgent"`
	CreatedAt time.Time           `bson:"createdAt" json:"createdAt"`
}

// ApiUsage 接口调用统计（models/ApiUsage.js，endpoint+date 唯一）。
type ApiUsage struct {
	ID       primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Endpoint string             `bson:"endpoint" json:"endpoint"`
	Method   string             `bson:"method" json:"method"`
	Count    int64              `bson:"count" json:"count"`
	Date     string             `bson:"date" json:"date"`
}

// SiteContent 站点内容（models/SiteContent.js，key 唯一；email 键存 SMTP 配置 JSON）。
type SiteContent struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Key       string             `bson:"key" json:"key"`
	Title     string             `bson:"title" json:"title"`
	Content   string             `bson:"content" json:"content"`
	UpdatedAt time.Time          `bson:"updatedAt" json:"updatedAt"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}

// Setting 迁移标记集合（src/index.js 使用 db.collection('settings')）。
type Setting struct {
	ID    primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Key   string             `bson:"key" json:"key"`
	Value primitive.M        `bson:"value" json:"value"`
}
