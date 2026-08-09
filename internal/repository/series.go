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

// SeriesRepo 剧集系列仓储（collection 名 series，对齐 mongoose 默认小写复数）。
type SeriesRepo struct{ coll *mongo.Collection }

// NewSeriesRepo 构造剧集系列仓储。
func NewSeriesRepo(coll *mongo.Collection) *SeriesRepo { return &SeriesRepo{coll: coll} }

// Count 统计系列总数（对齐 series.js GET / 的 Series.countDocuments()）。
func (r *SeriesRepo) Count(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{})
}

// FindPage 分页查询系列并按 updatedAt 倒序（对齐 series.js GET / 的
// find().sort({updatedAt:-1}).skip((page-1)*limit).limit(limit)）。
// page 默认 1；limit 默认 50（series.js 的默认值与共享 paginate 不同，勿用
// pagination 包的 20），上限 100。
func (r *SeriesRepo) FindPage(ctx context.Context, page, limit int) ([]model.Series, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().
			SetSort(bson.D{{Key: "updatedAt", Value: -1}}).
			SetSkip(int64((page-1)*limit)).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]model.Series, 0)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByID 按 ID 查询系列；不存在返回 ErrNotFound（对齐 series.js findById）。
func (r *SeriesRepo) FindByID(ctx context.Context, id any) (*model.Series, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	s := &model.Series{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// Create 插入系列并回填 _id；补 createdAt/updatedAt 默认值（对齐 mongoose
// default: Date.now）。
func (r *SeriesRepo) Create(ctx context.Context, s *model.Series) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now()
	}
	res, err := r.coll.InsertOne(ctx, s)
	if err != nil {
		return err
	}
	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		s.ID = oid
	}
	return nil
}

// UpdateByID 按 ID 更新并返回更新后的文档（对齐 findByIdAndUpdate(id, patch,
// {new:true})）；patch 是 $set 内容。文档不存在返回 ErrNotFound。
func (r *SeriesRepo) UpdateByID(ctx context.Context, id any, patch bson.M) (*model.Series, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	s := &model.Series{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)},
		bson.M{"$set": patch},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// DeleteByID 按 ID 删除系列（对齐 findByIdAndDelete；目标不存在也视为成功，
// Express 仍返回 200）。
func (r *SeriesRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": ToObjectID(id)})
	return err
}
