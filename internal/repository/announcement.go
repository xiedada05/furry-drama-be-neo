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

// AnnouncementRepo 公告仓储（models/Announcement.js → announcements 集合）。
type AnnouncementRepo struct{ coll *mongo.Collection }

// NewAnnouncementRepo 构造公告仓储。
func NewAnnouncementRepo(coll *mongo.Collection) *AnnouncementRepo {
	return &AnnouncementRepo{coll: coll}
}

// FindActive 查询生效中的公告（对齐 GET /active）：active + 已到发布时间 +
// 未过期（或永不过期），按 pinned/publishAt/createdAt 倒序，取前 20 条。
// channel 为 "popup"/"banner" 时追加对应展示渠道过滤。
func (r *AnnouncementRepo) FindActive(ctx context.Context, channel string) ([]model.Announcement, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	now := time.Now()
	query := bson.M{
		"active":    true,
		"publishAt": bson.M{"$lte": now},
		"$or": bson.A{
			bson.M{"expireAt": nil},
			bson.M{"expireAt": bson.M{"$gt": now}},
		},
	}
	if channel == "popup" {
		query["showPopup"] = true
	}
	if channel == "banner" {
		query["showBanner"] = true
	}
	cur, err := r.coll.Find(ctx, query,
		options.Find().
			SetSort(bson.D{{Key: "pinned", Value: -1}, {Key: "publishAt", Value: -1}, {Key: "createdAt", Value: -1}}).
			SetLimit(20))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Announcement
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByID 按 ID 查公告（对齐 Announcement.findById；不存在返回 ErrNotFound）。
func (r *AnnouncementRepo) FindByID(ctx context.Context, id any) (*model.Announcement, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	a := &model.Announcement{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return a, err
}

// FindAll 查询全部公告，按 createdAt 倒序（对齐管理后台列表 GET /）。
func (r *AnnouncementRepo) FindAll(ctx context.Context) ([]model.Announcement, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Announcement
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Create 插入公告；未显式设置时补 _id/createdAt/updatedAt
// （对齐 mongoose 自动 _id 与 default: Date.now）。
func (r *AnnouncementRepo) Create(ctx context.Context, a *model.Announcement) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if a.ID.IsZero() {
		a.ID = primitive.NewObjectID()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = now
	}
	_, err := r.coll.InsertOne(ctx, a)
	return err
}

// FindOneAndUpdate 按 ID 更新并返回新文档（对齐 findByIdAndUpdate(id, update, {new:true})）。
// update 需显式包 $set（对齐 mongoose 对普通对象默认按 $set 处理）。
func (r *AnnouncementRepo) FindOneAndUpdate(ctx context.Context, id any, update bson.M) (*model.Announcement, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	a := &model.Announcement{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return a, err
}

// FindOneAndDelete 按 ID 删除并返回被删文档（对齐 Announcement.findByIdAndDelete）。
func (r *AnnouncementRepo) FindOneAndDelete(ctx context.Context, id any) (*model.Announcement, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	a := &model.Announcement{}
	err := r.coll.FindOneAndDelete(ctx, bson.M{"_id": ToObjectID(id)}).Decode(a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return a, err
}

// Save 整文档覆盖保存（对齐 announcement.save()；updatedAt 刷新 + __v 自增）。
func (r *AnnouncementRepo) Save(ctx context.Context, a *model.Announcement) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	a.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	a.VersionKey++
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": a.ID}, a)
	return err
}
