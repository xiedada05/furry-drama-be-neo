package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Theme 主题文档（themes 集合）。
//
// 主题是「壁纸 + 图标」的组合外观包（不含按钮调色：调色由左下角强调色面板承担）：
//   - 仅壁纸（wallpaperUrl 非空、icons 为空）；
//   - 仅图标（icons 非空、wallpaperUrl 为空）；
//   - 全套（壁纸 + 图标）。
// 应用主题时可自由勾选生效组合（仅壁纸 / 仅图标 / 两者）。
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
	// WallpaperURL 是主题壁纸地址（空串表示不含壁纸）。
	WallpaperURL string `bson:"wallpaperUrl" json:"wallpaperUrl"`
	// WallpaperThumb 是壁纸缩略图地址（卡片预览用，可空，回退 WallpaperURL）。
	WallpaperThumb string `bson:"wallpaperThumb" json:"wallpaperThumb"`
	// Icons 是主题图标映射（组件 key → SVG URL；空表示不含图标）。
	Icons map[string]string `bson:"icons" json:"icons"`
	// AccentColor 是主题可选强调色（#rrggbb；空串表示不改变用户主题色，
	// 应用主题时「有则应用、无则保持用户当前主题色」）。
	AccentColor string `bson:"accentColor" json:"accentColor"`
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

// 主题内容类型常量（由 WallpaperURL / Icons 推导）。
const (
	// ThemeTypeFull 全套：壁纸 + 图标。
	ThemeTypeFull = "full"
	// ThemeTypeWallpaper 仅壁纸。
	ThemeTypeWallpaper = "wallpaper"
	// ThemeTypeIcons 仅图标。
	ThemeTypeIcons = "icons"
	// ThemeTypeLegacy 旧版配色主题（既无壁纸也无图标，仅历史数据）。
	ThemeTypeLegacy = "legacy"
)

// ThemeType 返回主题内容类型（full / wallpaper / icons / legacy）。
func (t *Theme) ThemeType() string {
	hasWallpaper := t.WallpaperURL != ""
	hasIcons := len(t.Icons) > 0
	switch {
	case hasWallpaper && hasIcons:
		return ThemeTypeFull
	case hasWallpaper:
		return ThemeTypeWallpaper
	case hasIcons:
		return ThemeTypeIcons
	default:
		return ThemeTypeLegacy
	}
}

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
