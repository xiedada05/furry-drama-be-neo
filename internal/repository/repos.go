// Package repository 实现 MongoDB 访问层（官方驱动，无 ODM）。
//
// 每个 collection 一个 repo 文件，方法按业务动词命名（FindByEmail / IncLoginAttempts 等）。
// Repos 聚合结构持有全部 repo，作为构造依赖注入 handler/service，避免全局单例。
//
// 集合名对齐 Mongoose 默认小写复数（src/indexes.js 确认的实际集合名）。
package repository

import (
	"time"

	"go.mongodb.org/mongo-driver/mongo"
)

// Repos 聚合全部仓储。
type Repos struct {
	Users           *UserRepo
	Sessions        *SessionRepo
	UsedTokens      *UsedTokenRepo
	Notifications   *NotificationRepo
	AuditLogs       *AuditLogRepo
	ApiUsage        *ApiUsageRepo
	SiteContents    *SiteContentRepo
	Settings        *SettingRepo
	Follows         *FollowRepo
	Histories       *HistoryRepo
	Favorites       *FavoriteRepo
	Ratings         *RatingRepo
	Reports         *ReportRepo
	Feedbacks       *FeedbackRepo
	CreatorProfiles *CreatorProfileRepo
	Episodes        *EpisodeRepo
	EpisodeTrash    *EpisodeTrashRepo
	SingleEpisodes  *SingleEpisodeRepo
	EpisodeVersions *EpisodeVersionRepo
	Folders         *FolderRepo
	SavedFolders    *SavedFolderRepo
	Series          *SeriesRepo
	Categories      *CategoryRepo
	Banners         *BannerRepo
	Announcements   *AnnouncementRepo
	Wallpapers      *WallpaperRepo
	FriendLinks     *FriendLinkRepo
	Themes          *ThemeRepo
	Icons           *IconRepo
}

// NewRepos 基于已连接的数据库构造 Repos。
// loginMaxAttempts / loginLockMinutes 为账号锁定策略（来自 config.Security）。
func NewRepos(db *mongo.Database, loginMaxAttempts, loginLockMinutes int) *Repos {
	return &Repos{
		Users:           NewUserRepo(db.Collection("users"), loginMaxAttempts, loginLockMinutes),
		Sessions:        NewSessionRepo(db.Collection("usersessions")),
		UsedTokens:      NewUsedTokenRepo(db.Collection("usedtokens")),
		Notifications:   NewNotificationRepo(db.Collection("notifications")),
		AuditLogs:       NewAuditLogRepo(db.Collection("auditlogs")),
		ApiUsage:        NewApiUsageRepo(db.Collection("apiusages")),
		SiteContents:    NewSiteContentRepo(db.Collection("sitecontents")),
		Settings:        NewSettingRepo(db.Collection("settings")),
		Follows:         NewFollowRepo(db.Collection("follows")),
		Histories:       NewHistoryRepo(db.Collection("histories")),
		Favorites:       NewFavoriteRepo(db.Collection("favorites")),
		Ratings:         NewRatingRepo(db.Collection("ratings")),
		Reports:         NewReportRepo(db.Collection("reports")),
		Feedbacks:       NewFeedbackRepo(db.Collection("feedbacks")),
		CreatorProfiles: NewCreatorProfileRepo(db.Collection("creatorprofiles")),
		Episodes:        NewEpisodeRepo(db.Collection("episodes")),
		EpisodeTrash:    NewEpisodeTrashRepo(db,
			db.Collection("episodes"), db.Collection("episodeversions"),
			db.Collection("singleepisodes"), db.Collection("follows"),
			db.Collection("favorites"), db.Collection("histories"),
			db.Collection("ratings"), db.Collection("notifications")),
		SingleEpisodes:  NewSingleEpisodeRepo(db.Collection("singleepisodes")),
		EpisodeVersions: NewEpisodeVersionRepo(db.Collection("episodeversions")),
		Folders:         NewFolderRepo(db.Collection("folders")),
		SavedFolders:    NewSavedFolderRepo(db.Collection("savedfolders")),
		Series:          NewSeriesRepo(db.Collection("series")),
		Categories:      NewCategoryRepo(db.Collection("categories")),
		Banners:         NewBannerRepo(db.Collection("banners")),
		Announcements:   NewAnnouncementRepo(db.Collection("announcements")),
		Wallpapers:      NewWallpaperRepo(db.Collection("systemwallpapers")),
		FriendLinks:     NewFriendLinkRepo(db.Collection("friendlinks")),
		Themes:          NewThemeRepo(db.Collection("themes")),
		Icons:           NewIconRepo(db.Collection("icons")),
	}
}

// contextTimeout 是仓储方法默认使用的上下文超时（防止驱动默认无限等待）。
const contextTimeout = 10 * time.Second
