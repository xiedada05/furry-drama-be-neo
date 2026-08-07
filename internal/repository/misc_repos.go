package repository

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// NotificationRepo 通知仓储。
type NotificationRepo struct{ coll *mongo.Collection }

// NewNotificationRepo 构造通知仓储。
func NewNotificationRepo(coll *mongo.Collection) *NotificationRepo {
	return &NotificationRepo{coll: coll}
}

// Create 插入通知。
func (r *NotificationRepo) Create(ctx context.Context, n *model.Notification) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.InsertOne(ctx, n)
	return err
}

// DeleteByUser 删除用户全部通知（注销清理）。
func (r *NotificationRepo) DeleteByUser(ctx context.Context, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"userId": userID})
	return err
}

// CountUnread 统计用户未读通知数（routes/notifications.js GET /unread-count）。
func (r *NotificationRepo) CountUnread(ctx context.Context, userID any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"userId": userID, "isRead": false})
}

// CountByUser 统计用户通知总数（routes/notifications.js GET /list 的 total）。
func (r *NotificationRepo) CountByUser(ctx context.Context, userID any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"userId": userID})
}

// FindByUser 分页查询用户通知，按 createdAt 倒序。
// 对齐 routes/notifications.js GET /list 的 find().sort({createdAt:-1}).skip(...).limit(...)。
func (r *NotificationRepo) FindByUser(ctx context.Context, userID any, page, limit int) ([]model.Notification, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	cur, err := r.coll.Find(ctx, bson.M{"userId": userID},
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(int64((page-1)*limit)).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Notification
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// MarkReadByID 将某用户的一条通知标记为已读（routes/notifications.js PUT /read/:id）。
func (r *NotificationRepo) MarkReadByID(ctx context.Context, id, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id, "userId": userID}, bson.M{"$set": bson.M{"isRead": true}})
	return err
}

// MarkAllRead 将用户全部未读通知标记为已读（PUT /read-all），返回修改数。
func (r *NotificationRepo) MarkAllRead(ctx context.Context, userID any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	res, err := r.coll.UpdateMany(ctx,
		bson.M{"userId": userID, "isRead": false},
		bson.M{"$set": bson.M{"isRead": true}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// MarkEpisodeRead 将某剧集的未读通知标记为已读（PUT /read-episode/:episodeId），返回修改数。
func (r *NotificationRepo) MarkEpisodeRead(ctx context.Context, userID, episodeID any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	res, err := r.coll.UpdateMany(ctx,
		bson.M{"userId": userID, "episodeId": episodeID, "isRead": false},
		bson.M{"$set": bson.M{"isRead": true}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// ClearRead 删除用户全部已读通知（DELETE /clear-read），返回删除数。
func (r *NotificationRepo) ClearRead(ctx context.Context, userID any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	res, err := r.coll.DeleteMany(ctx, bson.M{"userId": userID, "isRead": true})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// DeleteByIDForUser 删除用户的一条通知（DELETE /:id）。
func (r *NotificationRepo) DeleteByIDForUser(ctx context.Context, id, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": id, "userId": userID})
	return err
}

// DeleteReadOlderThan 删除 isRead=true 且 createdAt 早于 cutoff 的通知（src/index.js 每天 3 点 cron）。
// 返回删除数。
func (r *NotificationRepo) DeleteReadOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	res, err := r.coll.DeleteMany(ctx, bson.M{"isRead": true, "createdAt": bson.M{"$lt": cutoff}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// AuditLogRepo 审计日志仓储。
type AuditLogRepo struct{ coll *mongo.Collection }

// NewAuditLogRepo 构造审计日志仓储。
func NewAuditLogRepo(coll *mongo.Collection) *AuditLogRepo { return &AuditLogRepo{coll: coll} }

// Create 写入审计日志（失败静默，不阻断业务——对齐 logManual 的 catch(()=>{})）。
func (r *AuditLogRepo) Create(ctx context.Context, log *model.AuditLog) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.InsertOne(ctx, log)
	return err
}

// ApiUsageRepo 接口调用统计仓储。
type ApiUsageRepo struct{ coll *mongo.Collection }

// NewApiUsageRepo 构造统计仓储。
func NewApiUsageRepo(coll *mongo.Collection) *ApiUsageRepo { return &ApiUsageRepo{coll: coll} }

// UpsertInc 按 endpoint+date 聚合自增 count（对齐 findOneAndUpdate + $inc + upsert）。
func (r *ApiUsageRepo) UpsertInc(ctx context.Context, endpoint, method, date string, count int64) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"endpoint": endpoint, "date": date},
		bson.M{"$inc": bson.M{"count": count}, "$setOnInsert": bson.M{"method": method}},
		options.Update().SetUpsert(true))
	return err
}

// SiteContentRepo 站点内容仓储（sitecontents）。
type SiteContentRepo struct{ coll *mongo.Collection }

// NewSiteContentRepo 构造站点内容仓储。
func NewSiteContentRepo(coll *mongo.Collection) *SiteContentRepo { return &SiteContentRepo{coll: coll} }

// FindByKey 按 key 查找站点内容。
func (r *SiteContentRepo) FindByKey(ctx context.Context, key string) (*model.SiteContent, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	sc := &model.SiteContent{}
	err := r.coll.FindOne(ctx, bson.M{"key": key}).Decode(sc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return sc, err
}

// Upsert 按 key 写入站点内容。
func (r *SiteContentRepo) Upsert(ctx context.Context, key, title, content string) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"key": key},
		bson.M{"$set": bson.M{"title": title, "content": content, "updatedAt": time.Now()}},
		options.Update().SetUpsert(true))
	return err
}

// SettingRepo 迁移标记集合仓储（settings）。
type SettingRepo struct{ coll *mongo.Collection }

// NewSettingRepo 构造设置仓储。
func NewSettingRepo(coll *mongo.Collection) *SettingRepo { return &SettingRepo{coll: coll} }

// GetFlag 读取迁移标记值（不存在返回 false）。
// 兼容两种已写入的 value 形态：旧 Express 的 { value: true }，以及本实现的 { value: { done: true } }。
func (r *SettingRepo) GetFlag(ctx context.Context, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	var s struct {
		Value any `bson:"value"`
	}
	err := r.coll.FindOne(ctx, bson.M{"key": key}).Decode(&s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return flagDone(s.Value), nil
}

// flagDone 从 settings.value 中提取 done 布尔。
// 驱动把子文档解码为 bson.D（any 目标）或 bson.M（map 目标），两种形态都要处理；
// 旧 Express 直接写入 value: true，也要识别。
func flagDone(v any) bool {
	switch doc := v.(type) {
	case bool:
		return doc
	case bson.M:
		done, _ := doc["done"].(bool)
		return done
	case bson.D:
		for _, e := range doc {
			if e.Key == "done" {
				if b, ok := e.Value.(bool); ok {
					return b
				}
				return false
			}
		}
	}
	return false
}

// SetFlag 写入迁移标记。
func (r *SettingRepo) SetFlag(ctx context.Context, key string, value bson.M) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"key": key},
		bson.M{"$set": bson.M{"value": value}},
		options.Update().SetUpsert(true))
	return err
}

// deleteByUser 通用：按 userId 删除（Follow/History/Favorite/Feedback/CreatorProfile 用）。
func deleteByUser(ctx context.Context, coll *mongo.Collection, userID any) error {
	_, err := coll.DeleteMany(ctx, bson.M{"userId": userID})
	return err
}

// deleteByReporter 通用：按 reporterId 删除（Report 用）。
func deleteByReporter(ctx context.Context, coll *mongo.Collection, reporterID any) error {
	_, err := coll.DeleteMany(ctx, bson.M{"reporterId": reporterID})
	return err
}

// FollowRepo 追番仓储。
type FollowRepo struct{ coll *mongo.Collection }

// NewFollowRepo 构造追番仓储。
func NewFollowRepo(coll *mongo.Collection) *FollowRepo { return &FollowRepo{coll: coll} }

// DeleteByUser 删除用户全部追番（注销清理）。
func (r *FollowRepo) DeleteByUser(ctx context.Context, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return deleteByUser(ctx, r.coll, userID)
}

// HistoryRepo 观看历史仓储。
type HistoryRepo struct{ coll *mongo.Collection }

// NewHistoryRepo 构造历史仓储。
func NewHistoryRepo(coll *mongo.Collection) *HistoryRepo { return &HistoryRepo{coll: coll} }

// DeleteByUser 删除用户全部历史（注销清理）。
func (r *HistoryRepo) DeleteByUser(ctx context.Context, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return deleteByUser(ctx, r.coll, userID)
}

// FavoriteRepo 收藏仓储。
type FavoriteRepo struct{ coll *mongo.Collection }

// NewFavoriteRepo 构造收藏仓储。
func NewFavoriteRepo(coll *mongo.Collection) *FavoriteRepo { return &FavoriteRepo{coll: coll} }

// DeleteByUser 删除用户全部收藏（注销清理）。
func (r *FavoriteRepo) DeleteByUser(ctx context.Context, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return deleteByUser(ctx, r.coll, userID)
}

// RatingRepo 评分仓储。
type RatingRepo struct{ coll *mongo.Collection }

// NewRatingRepo 构造评分仓储。
func NewRatingRepo(coll *mongo.Collection) *RatingRepo { return &RatingRepo{coll: coll} }

// DeleteByUser 删除用户全部评分（注销清理）。
func (r *RatingRepo) DeleteByUser(ctx context.Context, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return deleteByUser(ctx, r.coll, userID)
}

// ReportRepo 举报仓储。
type ReportRepo struct{ coll *mongo.Collection }

// NewReportRepo 构造举报仓储。
func NewReportRepo(coll *mongo.Collection) *ReportRepo { return &ReportRepo{coll: coll} }

// DeleteByReporter 删除用户全部举报（注销清理，按 reporterId）。
func (r *ReportRepo) DeleteByReporter(ctx context.Context, reporterID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return deleteByReporter(ctx, r.coll, reporterID)
}

// FeedbackRepo 反馈仓储。
type FeedbackRepo struct{ coll *mongo.Collection }

// NewFeedbackRepo 构造反馈仓储。
func NewFeedbackRepo(coll *mongo.Collection) *FeedbackRepo { return &FeedbackRepo{coll: coll} }

// DeleteByUser 删除用户全部反馈（注销清理）。
func (r *FeedbackRepo) DeleteByUser(ctx context.Context, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return deleteByUser(ctx, r.coll, userID)
}

// CreatorProfileRepo 创作者资料仓储。
type CreatorProfileRepo struct{ coll *mongo.Collection }

// NewCreatorProfileRepo 构造创作者资料仓储。
func NewCreatorProfileRepo(coll *mongo.Collection) *CreatorProfileRepo {
	return &CreatorProfileRepo{coll: coll}
}

// DeleteByCreator 删除创作者资料（注销清理，按 creatorId）。
func (r *CreatorProfileRepo) DeleteByCreator(ctx context.Context, creatorID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"creatorId": creatorID})
	return err
}

// Create 插入创作者资料；唯一键冲突（creatorId 已存在）返回 IsDuplicateKey(err)。
// 未显式设置时补 createdAt / updatedAt 默认值（对齐 mongoose default: Date.now）。
func (r *CreatorProfileRepo) Create(ctx context.Context, p *model.CreatorProfile) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now()
	}
	_, err := r.coll.InsertOne(ctx, p)
	if mongo.IsDuplicateKeyError(err) {
		return errDuplicateKey
	}
	return err
}

// FindByCreator 按 creatorId 查找创作者资料；不存在返回 ErrNotFound。
func (r *CreatorProfileRepo) FindByCreator(ctx context.Context, creatorID any) (*model.CreatorProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	p := &model.CreatorProfile{}
	err := r.coll.FindOne(ctx, bson.M{"creatorId": creatorID}).Decode(p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return p, err
}
