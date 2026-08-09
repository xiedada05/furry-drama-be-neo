package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Series 系列文档（models/Series.js）。
// episodes 保存系列引用的剧集 ID 数组；handler 在响应中将其填充为剧集对象
// （对齐 Express 的 .populate('episodes', ...)）。
type Series struct {
	ID            primitive.ObjectID   `bson:"_id,omitempty" json:"_id"`
	Name          string               `bson:"name" json:"name"`
	NameEn        string               `bson:"nameEn" json:"nameEn"`
	NameJa        string               `bson:"nameJa" json:"nameJa"`
	Description   string               `bson:"description" json:"description"`
	DescriptionEn string               `bson:"descriptionEn" json:"descriptionEn"`
	DescriptionJa string               `bson:"descriptionJa" json:"descriptionJa"`
	Episodes      []primitive.ObjectID `bson:"episodes" json:"episodes"`
	CreatedBy     *primitive.ObjectID  `bson:"createdBy" json:"createdBy,omitempty"`
	CreatedAt     time.Time            `bson:"createdAt" json:"createdAt"`
	UpdatedAt     time.Time            `bson:"updatedAt" json:"updatedAt"`
}

// SeriesEpisodeList 是系列列表端点内嵌的剧集对象（对齐 series.js GET /
// 的 populate select：title coverImage currentEpisodes totalEpisodes status
// averageRating）。数字字段用 float64 兼容 BSON double/int 两种存储。
type SeriesEpisodeList struct {
	ID              primitive.ObjectID `bson:"_id" json:"_id"`
	Title           string             `bson:"title" json:"title"`
	CoverImage      string             `bson:"coverImage" json:"coverImage"`
	CurrentEpisodes float64            `bson:"currentEpisodes" json:"currentEpisodes"`
	TotalEpisodes   *float64           `bson:"totalEpisodes" json:"totalEpisodes"`
	Status          string             `bson:"status" json:"status"`
	AverageRating   float64            `bson:"averageRating" json:"averageRating"`
}

// SeriesEpisodeDetail 是系列详情端点内嵌的剧集对象（对齐 series.js GET /:id
// 的 populate select：列表字段 + description category tags views）。
type SeriesEpisodeDetail struct {
	ID              primitive.ObjectID `bson:"_id" json:"_id"`
	Title           string             `bson:"title" json:"title"`
	CoverImage      string             `bson:"coverImage" json:"coverImage"`
	CurrentEpisodes float64            `bson:"currentEpisodes" json:"currentEpisodes"`
	TotalEpisodes   *float64           `bson:"totalEpisodes" json:"totalEpisodes"`
	Status          string             `bson:"status" json:"status"`
	AverageRating   float64            `bson:"averageRating" json:"averageRating"`
	Description     string             `bson:"description" json:"description"`
	Category        []string           `bson:"category" json:"category"`
	Tags            []string           `bson:"tags" json:"tags"`
	Views           float64            `bson:"views" json:"views"`
}
