package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Episode 剧集文档（models/Episode.js）。逐字段对齐 mongoose schema；
// collection 名 episodes。内容域各路由共用本模型。
type Episode struct {
	ID                   primitive.ObjectID   `bson:"_id,omitempty" json:"_id"`
	Title                string               `bson:"title" json:"title"`
	TitleEn              string               `bson:"titleEn" json:"titleEn"`
	TitleJa              string               `bson:"titleJa" json:"titleJa"`
	Description          string               `bson:"description" json:"description"`
	DescriptionEn        string               `bson:"descriptionEn" json:"descriptionEn"`
	DescriptionJa        string               `bson:"descriptionJa" json:"descriptionJa"`
	CoverImage           string               `bson:"coverImage" json:"coverImage"`
	TotalEpisodes        *int                 `bson:"totalEpisodes" json:"totalEpisodes"`
	CurrentEpisodes      int                  `bson:"currentEpisodes" json:"currentEpisodes"`
	Status               string               `bson:"status" json:"status"`
	Category             []string             `bson:"category" json:"category"`
	Tags                 []string             `bson:"tags" json:"tags"`
	PlatformLinks        primitive.M          `bson:"platformLinks" json:"platformLinks"`
	Views                int64                `bson:"views" json:"views"`
	AverageRating        float64              `bson:"averageRating" json:"averageRating"`
	RatingCount          int                  `bson:"ratingCount" json:"ratingCount"`
	UpdateDay            string               `bson:"updateDay" json:"updateDay"`
	PremiereDate         *time.Time           `bson:"premiereDate" json:"premiereDate"`
	CreatedBy            *primitive.ObjectID  `bson:"createdBy" json:"createdBy"`
	HideCreator          bool                 `bson:"hideCreator" json:"hideCreator"`
	AllowedEditors       []primitive.ObjectID `bson:"allowedEditors" json:"allowedEditors"`
	CustomAuthors        []primitive.ObjectID `bson:"customAuthors" json:"customAuthors"`
	QQGroupLink          string               `bson:"qqGroupLink" json:"qqGroupLink"`
	QQGroupNumber        string               `bson:"qqGroupNumber" json:"qqGroupNumber"`
	ReviewStatus         string               `bson:"reviewStatus" json:"reviewStatus"`
	ReviewNote           string               `bson:"reviewNote" json:"reviewNote"`
	PendingChanges       primitive.M          `bson:"pendingChanges" json:"pendingChanges"`
	HasPendingChanges    bool                 `bson:"hasPendingChanges" json:"hasPendingChanges"`
	PendingChangeSummary string               `bson:"pendingChangeSummary" json:"pendingChangeSummary"`
	ReviewedBy           *primitive.ObjectID  `bson:"reviewedBy" json:"reviewedBy"`
	ReviewedAt           *time.Time           `bson:"reviewedAt" json:"reviewedAt"`
	CreatedAt            time.Time            `bson:"createdAt" json:"createdAt"`
	UpdatedAt            time.Time            `bson:"updatedAt" json:"updatedAt"`
	// VersionKey 是 mongoose 的 __v（每次 save() 自增；findByIdAndUpdate 不变）。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}

// ToVersionData 生成 EpisodeVersion.data 快照（对齐 mongoose episode.toObject() 存入 Mixed）。
// 值为原生 BSON 类型（ObjectID/Date/数组/map），供审核域读取与应用。
func (e *Episode) ToVersionData() primitive.M {
	data := primitive.M{
		"_id":                  e.ID,
		"title":                e.Title,
		"titleEn":              e.TitleEn,
		"titleJa":              e.TitleJa,
		"description":          e.Description,
		"descriptionEn":        e.DescriptionEn,
		"descriptionJa":        e.DescriptionJa,
		"coverImage":           e.CoverImage,
		"currentEpisodes":      e.CurrentEpisodes,
		"status":               e.Status,
		"category":             e.Category,
		"tags":                 e.Tags,
		"views":                e.Views,
		"averageRating":        e.AverageRating,
		"ratingCount":          e.RatingCount,
		"updateDay":            e.UpdateDay,
		"hideCreator":          e.HideCreator,
		"qqGroupLink":          e.QQGroupLink,
		"qqGroupNumber":        e.QQGroupNumber,
		"reviewStatus":         e.ReviewStatus,
		"reviewNote":           e.ReviewNote,
		"hasPendingChanges":    e.HasPendingChanges,
		"pendingChangeSummary": e.PendingChangeSummary,
		"createdAt":            e.CreatedAt,
		"updatedAt":            e.UpdatedAt,
		"__v":                  e.VersionKey,
	}
	data["totalEpisodes"] = nil
	if e.TotalEpisodes != nil {
		data["totalEpisodes"] = *e.TotalEpisodes
	}
	data["premiereDate"] = nil
	if e.PremiereDate != nil {
		data["premiereDate"] = *e.PremiereDate
	}
	data["createdBy"] = nil
	if e.CreatedBy != nil {
		data["createdBy"] = *e.CreatedBy
	}
	data["allowedEditors"] = e.AllowedEditors
	if e.AllowedEditors == nil {
		data["allowedEditors"] = []primitive.ObjectID{}
	}
	data["customAuthors"] = e.CustomAuthors
	if e.CustomAuthors == nil {
		data["customAuthors"] = []primitive.ObjectID{}
	}
	data["platformLinks"] = e.PlatformLinks
	if e.PlatformLinks == nil {
		data["platformLinks"] = primitive.M{}
	}
	data["pendingChanges"] = nil
	if e.PendingChanges != nil {
		data["pendingChanges"] = e.PendingChanges
	}
	data["reviewedBy"] = nil
	if e.ReviewedBy != nil {
		data["reviewedBy"] = *e.ReviewedBy
	}
	data["reviewedAt"] = nil
	if e.ReviewedAt != nil {
		data["reviewedAt"] = *e.ReviewedAt
	}
	return data
}

// SingleEpisode 单集文档（models/SingleEpisode.js）。
type SingleEpisode struct {
	ID            primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	EpisodeID     primitive.ObjectID `bson:"episodeId" json:"episodeId"`
	EpisodeNumber int                `bson:"episodeNumber" json:"episodeNumber"`
	Title         string             `bson:"title" json:"title"`
	TitleEn       string             `bson:"titleEn" json:"titleEn"`
	TitleJa       string             `bson:"titleJa" json:"titleJa"`
	Duration      string             `bson:"duration" json:"duration"`
	PlatformLinks primitive.M        `bson:"platformLinks" json:"platformLinks"`
	Views         int64              `bson:"views" json:"views"`
	ReleaseDate   *time.Time         `bson:"releaseDate" json:"releaseDate"`
	ScheduledDate *time.Time         `bson:"scheduledDate" json:"scheduledDate"`
	IsScheduled   bool               `bson:"isScheduled" json:"isScheduled"`
	PremiereDate  *time.Time         `bson:"premiereDate" json:"premiereDate"`
	IsUpcoming    bool               `bson:"isUpcoming" json:"isUpcoming"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}

// EpisodeVersion 剧集版本快照（models/EpisodeVersion.js，episodeId+version 唯一）。
type EpisodeVersion struct {
	ID            primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	EpisodeID     primitive.ObjectID  `bson:"episodeId" json:"episodeId"`
	Version       int                 `bson:"version" json:"version"`
	Data          primitive.M         `bson:"data" json:"data"`
	ChangedBy     *primitive.ObjectID `bson:"changedBy" json:"changedBy"`
	ChangeSummary string              `bson:"changeSummary" json:"changeSummary"`
	CreatedAt     time.Time           `bson:"createdAt" json:"createdAt"`
	// VersionKey 是 mongoose 的 __v。
	VersionKey int `bson:"__v,omitempty" json:"__v"`
}
