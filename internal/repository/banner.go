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

// BannerRepo 横幅仓储（models/Banner.js → banners 集合）。
type BannerRepo struct{ coll *mongo.Collection }

// NewBannerRepo 构造横幅仓储。
func NewBannerRepo(coll *mongo.Collection) *BannerRepo { return &BannerRepo{coll: coll} }

// FindActiveSorted 查询 active=true 的轮播图，按 order 升序、createdAt 倒序
// （对齐 banners.js GET / 的 find({active:true}).sort({order:1, createdAt:-1})）。
func (r *BannerRepo) FindActiveSorted(ctx context.Context) ([]model.Banner, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.findSorted(ctx, bson.M{"active": true})
}

// FindAllSorted 查询全部轮播图，按 order 升序、createdAt 倒序
// （对齐 banners.js GET /all 的 find({}).sort({order:1, createdAt:-1})）。
func (r *BannerRepo) FindAllSorted(ctx context.Context) ([]model.Banner, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.findSorted(ctx, bson.M{})
}

// findSorted 通用：按 sort 查询轮播图。
func (r *BannerRepo) findSorted(ctx context.Context, query bson.M) ([]model.Banner, error) {
	cur, err := r.coll.Find(ctx, query,
		options.Find().SetSort(bson.D{{Key: "order", Value: 1}, {Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Banner
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByID 按 ID 查轮播图（对齐 Banner.findById）；不存在返回 ErrNotFound。
func (r *BannerRepo) FindByID(ctx context.Context, id any) (*model.Banner, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	b := &model.Banner{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return b, err
}

// Create 插入轮播图；未显式设置时补 _id/createdAt（对齐 mongoose 自动 _id 与
// default: Date.now）。
func (r *BannerRepo) Create(ctx context.Context, b *model.Banner) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if b.ID.IsZero() {
		b.ID = primitive.NewObjectID()
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	_, err := r.coll.InsertOne(ctx, b)
	return err
}

// Save 整文档覆盖保存（对齐 banner.save()；__v 自增对齐 mongoose versionKey）。
func (r *BannerRepo) Save(ctx context.Context, b *model.Banner) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	b.VersionKey++
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": b.ID}, b)
	return err
}

// DeleteByID 按 ID 删除轮播图（对齐 Banner.findByIdAndDelete）。
func (r *BannerRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": ToObjectID(id)})
	return err
}
