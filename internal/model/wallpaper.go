package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// SystemWallpaper 系统壁纸文档（models/SystemWallpaper.js → systemwallpapers 集合）。
// 与 User.PersonalWallpapers（model.Wallpaper）不同：这是独立集合，由管理员维护。
type SystemWallpaper struct {
	ID           primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	Name         string              `bson:"name" json:"name"`
	URL          string              `bson:"url" json:"url"`
	ThumbnailURL string              `bson:"thumbnailUrl" json:"thumbnailUrl"`
	Enabled      bool                `bson:"enabled" json:"enabled"`
	SortOrder    int                 `bson:"sortOrder" json:"sortOrder"`
	UploadedBy   *primitive.ObjectID `bson:"uploadedBy" json:"uploadedBy"`
	CreatedAt    time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt    time.Time           `bson:"updatedAt" json:"updatedAt"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}
