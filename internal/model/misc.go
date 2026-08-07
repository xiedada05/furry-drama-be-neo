package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Follow 追番记录（models/Follow.js）。
type Follow struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID             primitive.ObjectID `bson:"userId" json:"userId"`
	EpisodeID          primitive.ObjectID `bson:"episodeId" json:"episodeId"`
	FolderID           *primitive.ObjectID `bson:"folderId" json:"folderId,omitempty"`
	FollowedAtEpisodes int                `bson:"followedAtEpisodes" json:"followedAtEpisodes"`
	CreatedAt          time.Time          `bson:"createdAt" json:"createdAt"`
}

// History 观看历史（models/History.js，lastWatched 建 365 天 TTL 索引）。
type History struct {
	ID                      primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID                  primitive.ObjectID `bson:"userId" json:"userId"`
	EpisodeID               primitive.ObjectID `bson:"episodeId" json:"episodeId"`
	WatchedEpisodes         []int              `bson:"watchedEpisodes" json:"watchedEpisodes"`
	LastWatchedEpisodeNumber *int              `bson:"lastWatchedEpisodeNumber" json:"lastWatchedEpisodeNumber"`
	LastWatched             time.Time          `bson:"lastWatched" json:"lastWatched"`
	CreatedAt               time.Time          `bson:"createdAt" json:"createdAt"`
}

// Favorite 收藏记录（models/Favorite.js）。
type Favorite struct {
	ID        primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	UserID    primitive.ObjectID  `bson:"userId" json:"userId"`
	EpisodeID primitive.ObjectID  `bson:"episodeId" json:"episodeId"`
	FolderID  *primitive.ObjectID `bson:"folderId" json:"folderId,omitempty"`
	CreatedAt time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time           `bson:"updatedAt" json:"updatedAt"`
}

// Rating 评分记录（models/Rating.js，score 1-5）。
type Rating struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID    primitive.ObjectID `bson:"userId" json:"userId"`
	EpisodeID primitive.ObjectID `bson:"episodeId" json:"episodeId"`
	Score     int                `bson:"score" json:"score"`
	CreatedAt time.Time          `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time          `bson:"updatedAt" json:"updatedAt"`
}

// Report 举报记录（models/Report.js）。
type Report struct {
	ID          primitive.ObjectID  `bson:"_id,omitempty" json:"_id"`
	ReporterID  primitive.ObjectID  `bson:"reporterId" json:"reporterId"`
	TargetType  string              `bson:"targetType" json:"targetType"`
	TargetID    primitive.ObjectID  `bson:"targetId" json:"targetId"`
	Reason      string              `bson:"reason" json:"reason"`
	Description string              `bson:"description" json:"description"`
	Status      string              `bson:"status" json:"status"`
	ResolvedBy  *primitive.ObjectID `bson:"resolvedBy" json:"resolvedBy,omitempty"`
	ResolveNote string              `bson:"resolveNote" json:"resolveNote"`
	CreatedAt   time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time           `bson:"updatedAt" json:"updatedAt"`
}

// Feedback 反馈记录（models/Feedback.js）。
type Feedback struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	UserID    primitive.ObjectID `bson:"userId" json:"userId"`
	Username  string             `bson:"username" json:"username"`
	Type      string             `bson:"type" json:"type"`
	Content   string             `bson:"content" json:"content"`
	Status    string             `bson:"status" json:"status"`
	Reply     string             `bson:"reply" json:"reply"`
	CreatedAt time.Time          `bson:"createdAt" json:"createdAt"`
}

// CreatorProfile 创作者资料（models/CreatorProfile.js，曾名 adminId 已迁移为 creatorId）。
type CreatorProfile struct {
	ID             primitive.ObjectID `bson:"_id,omitempty" json:"_id"`
	CreatorID      primitive.ObjectID `bson:"creatorId" json:"creatorId"`
	DisplayName    string             `bson:"displayName" json:"displayName"`
	Avatar         string             `bson:"avatar" json:"avatar"`
	Bio            string             `bson:"bio" json:"bio"`
	SocialLinks    map[string]string  `bson:"socialLinks" json:"socialLinks"`
	ReviewStatus   string             `bson:"reviewStatus" json:"reviewStatus"`
	ReviewNote     string             `bson:"reviewNote" json:"reviewNote"`
	PendingChanges primitive.M        `bson:"pendingChanges" json:"pendingChanges"`
	CreatedAt      time.Time          `bson:"createdAt" json:"createdAt"`
	UpdatedAt      time.Time          `bson:"updatedAt" json:"updatedAt"`
}
