package repository

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 announcements / wallpapers / friend-links 三域跨集合访问
// Notification / User 集合的方法，挂到已有 *NotificationRepo / *UserRepo 类型上。
// 方法名带域前缀（Cms/Announcements/FriendLinks/Wallpapers），避免与他域方法冲突。

// ---- NotificationRepo ----

// CmsInsertMany 批量插入通知（对齐 Notification.insertMany）；未设置时补 createdAt。
// 供 announcements 的站内推送与 friend-links 的友链申请通知共用。
func (r *NotificationRepo) CmsInsertMany(ctx context.Context, items []model.Notification) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	docs := make([]any, 0, len(items))
	for i := range items {
		if items[i].CreatedAt.IsZero() {
			items[i].CreatedAt = now
		}
		docs = append(docs, &items[i])
	}
	_, err := r.coll.InsertMany(ctx, docs)
	return err
}

// AnnouncementsDeleteByAnnouncement 删除某公告的通知中心条目
//
//	（对齐 announcements.js DELETE /:id 的 Notification.deleteMany({
//	  type: 'announcement', 'metadata.announcementId': announcement._id }))。
//
// 返回删除数。
func (r *NotificationRepo) AnnouncementsDeleteByAnnouncement(ctx context.Context, announcementID any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	res, err := r.coll.DeleteMany(ctx, bson.M{
		"type":                    "announcement",
		"metadata.announcementId": ToObjectID(announcementID),
	})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// ---- UserRepo ----

// AnnouncementsFindAllUserIDs 分批查询用户 _id（对齐 sendAnnouncementNotifications 的
// User.find({}, '_id').skip().limit()）。
func (r *UserRepo) AnnouncementsFindAllUserIDs(ctx context.Context, skip, limit int64) ([]primitive.ObjectID, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().
			SetProjection(bson.M{"_id": 1}).
			SetSkip(skip).
			SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var ids []primitive.ObjectID
	for cur.Next(ctx) {
		var doc struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		ids = append(ids, doc.ID)
	}
	return ids, cur.Err()
}

// AnnouncementsMailTarget 公告邮件收件人视图（email + isEmailVerified）。
type AnnouncementsMailTarget struct {
	ID              primitive.ObjectID `bson:"_id"`
	Email           string             `bson:"email"`
	IsEmailVerified bool               `bson:"isEmailVerified"`
}

// AnnouncementsFindEmailTargets 分批查询公告邮件收件人（对齐 sendAnnouncementEmails 的
// User.find({ isEmailVerified: true, 'emailNotificationPrefs.announcement': { $ne: false } },
// 'email emailNotificationPrefs')）。偏好键缺失的用户也会命中（$ne:false 语义）。
func (r *UserRepo) AnnouncementsFindEmailTargets(ctx context.Context, skip, limit int64) ([]AnnouncementsMailTarget, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{
		"isEmailVerified":                     true,
		"emailNotificationPrefs.announcement": bson.M{"$ne": false},
	}, options.Find().
		SetProjection(bson.M{"email": 1, "isEmailVerified": 1}).
		SetSkip(skip).
		SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []AnnouncementsMailTarget
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FriendLinksAdminTarget 超管通知收件人视图（ID + email + friendLinkApply 偏好；
// 偏好键缺失（nil）视为未关闭）。
type FriendLinksAdminTarget struct {
	ID                  primitive.ObjectID `bson:"_id"`
	Email               string             `bson:"email"`
	IsEmailVerified     bool               `bson:"isEmailVerified"`
	FriendLinkApplyPref *bool              `bson:"friendLinkApply"`
}

// FriendLinksFindSuperAdmins 查询全部超管的通知信息（对齐 friendLinks.js apply 的
// User.find({ role: 'superadmin' }) + notifyHelper 的 select email isEmailVerified
// emailNotificationPrefs）。
func (r *UserRepo) FriendLinksFindSuperAdmins(ctx context.Context) ([]FriendLinksAdminTarget, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"role": "superadmin"},
		options.Find().SetProjection(bson.M{
			"email": 1, "isEmailVerified": 1, "emailNotificationPrefs.friendLinkApply": 1,
		}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID              primitive.ObjectID `bson:"_id"`
		Email           string             `bson:"email"`
		IsEmailVerified bool               `bson:"isEmailVerified"`
		Prefs           struct {
			FriendLinkApply *bool `bson:"friendLinkApply"`
		} `bson:"emailNotificationPrefs"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]FriendLinksAdminTarget, 0, len(rows))
	for _, row := range rows {
		out = append(out, FriendLinksAdminTarget{
			ID:                  row.ID,
			Email:               row.Email,
			IsEmailVerified:     row.IsEmailVerified,
			FriendLinkApplyPref: row.Prefs.FriendLinkApply,
		})
	}
	return out, nil
}

// FriendLinksMailTarget 友链邮件收件人视图（email + isEmailVerified +
// emailNotificationPrefs.friendLinkStatus 偏好；偏好键缺失（nil）视为未关闭）。
type FriendLinksMailTarget struct {
	ID                   primitive.ObjectID `bson:"_id"`
	Email                string             `bson:"email"`
	IsEmailVerified      bool               `bson:"isEmailVerified"`
	FriendLinkStatusPref *bool              `bson:"friendLinkStatus"`
}

// FriendLinksFindEmailTarget 查询单用户邮件信息（对齐 sendNotificationEmailToUser 的
// User.findById(userId).select('email isEmailVerified emailNotificationPrefs')）。
func (r *UserRepo) FriendLinksFindEmailTarget(ctx context.Context, id any) (*FriendLinksMailTarget, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	var row struct {
		ID              primitive.ObjectID `bson:"_id"`
		Email           string             `bson:"email"`
		IsEmailVerified bool               `bson:"isEmailVerified"`
		Prefs           struct {
			FriendLinkStatus *bool `bson:"friendLinkStatus"`
		} `bson:"emailNotificationPrefs"`
	}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)},
		options.FindOne().SetProjection(bson.M{
			"email": 1, "isEmailVerified": 1, "emailNotificationPrefs.friendLinkStatus": 1,
		})).Decode(&row)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &FriendLinksMailTarget{
		ID:                   row.ID,
		Email:                row.Email,
		IsEmailVerified:      row.IsEmailVerified,
		FriendLinkStatusPref: row.Prefs.FriendLinkStatus,
	}, nil
}

// WallpapersPushPersonal 向用户个人壁纸数组追加一条（对齐 wallpapers.js POST /personal 的
// user.personalWallpapers.push(...); user.save()）。用定点 $push 而非整文档覆盖，
// 避免加载用户时的 publicProjection 把 password 等敏感字段清空。
func (r *UserRepo) WallpapersPushPersonal(ctx context.Context, userID any, wp model.Wallpaper) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(userID)},
		bson.M{"$push": bson.M{"personalWallpapers": wp}})
	return err
}

// WallpapersRemovePersonal 按 url 从用户个人壁纸数组移除一项（对齐 wallpapers.js
// DELETE /personal 的 filter + save）。url 不匹配时为 no-op，不影响结果。
func (r *UserRepo) WallpapersRemovePersonal(ctx context.Context, userID any, url string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(userID)},
		bson.M{"$pull": bson.M{"personalWallpapers": bson.M{"url": url}}})
	return err
}
