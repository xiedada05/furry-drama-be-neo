package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Theme 主题文档（themes 集合）。
//
// 主题分两类：
//   - 系统主题（isSystem=true）：由管理员创建，或个人主题审核通过后升级而来，
//     全站用户可见可选；enabled 控制是否对用户开放。
//   - 个人主题（isSystem=false）：登录用户自制，默认仅在该用户多端同步可见；
//     通过 submit 提交后台审核，approve 后变为系统主题。
//
// Status 仅对个人主题有意义：draft（草稿）/ pending（待审核）/
// approved（已通过，此时 isSystem=true）/ rejected（已驳回）。
type Theme struct {
	ID          primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	Name        string              `bson:"name" json:"name"`
	Description string              `bson:"description" json:"description"`
	// Mode 是主题基础模式（dark/light），决定未覆盖变量的回退基底。
	Mode string `bson:"mode" json:"mode"`
	// Variables 是 CSS 变量键值对（如 "--primary": "#6366f1"），
	// 仅存与基础模式默认值的差异即可，应用时覆盖在基底之上。
	Variables map[string]string `bson:"variables" json:"variables"`
	IsSystem  bool              `bson:"isSystem" json:"isSystem"`
	// OwnerID 是个人主题的所属用户；系统主题为 nil。
	OwnerID *primitive.ObjectID `bson:"ownerId" json:"ownerId"`
	Status  string              `bson:"status" json:"status"`
	// ReviewNote 是管理员审核备注（驳回原因等）。
	ReviewNote string `bson:"reviewNote" json:"reviewNote"`
	// IsDefault 表示该主题是否站点默认主题（全站唯一，未登录/未选择用户生效）。
	IsDefault bool `bson:"isDefault" json:"isDefault"`
	Enabled   bool `bson:"enabled" json:"enabled"`
	// UsageCount 是使用该主题的用户数（定时/事件更新，仅展示参考）。
	UsageCount int64     `bson:"usageCount" json:"usageCount"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt  time.Time `bson:"updatedAt" json:"updatedAt"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}

// 主题状态常量。
const (
	ThemeStatusDraft    = "draft"
	ThemeStatusPending  = "pending"
	ThemeStatusApproved = "approved"
	ThemeStatusRejected = "rejected"
)

// 主题基础模式常量。
const (
	ThemeModeDark  = "dark"
	ThemeModeLight = "light"
)

// Icon 图标文档（icons 集合）。
//
// SVG 图标文件存储在服务器可配置目录（cfg.Server.IconsDir，默认 ./uploads/icons），
// URL 形如 /uploads/icons/<filename>。Mappings 记录该图标与页面组件的绑定关系
// （组件 key 数组，如 ["nav.home","nav.calendar"]），前端图标引擎按映射渲染。
type Icon struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	Name        string             `bson:"name" json:"name"`
	Category    string             `bson:"category" json:"category"`
	URL         string             `bson:"url" json:"url"`
	Mappings    []string           `bson:"mappings" json:"mappings"`
	Description string             `bson:"description" json:"description"`
	UploadedBy  *primitive.ObjectID `bson:"uploadedBy" json:"uploadedBy"`
	Enabled     bool               `bson:"enabled" json:"enabled"`
	CreatedAt   time.Time          `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time          `bson:"updatedAt" json:"updatedAt"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}
