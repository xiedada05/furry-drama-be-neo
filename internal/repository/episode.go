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

// EpisodeBasic 是剧集基础字段（导出/列表 populate 用；完整 Episode 模型在内容域实现）。
type EpisodeBasic struct {
	ID         primitive.ObjectID `bson:"_id" json:"_id"`
	Title      string             `bson:"title" json:"title"`
	CoverImage string             `bson:"coverImage" json:"coverImage"`
	Status     string             `bson:"status" json:"status"`
}

// EpisodeRepo 剧集仓储（第一段仅基础查询；内容域补齐）。
type EpisodeRepo struct{ coll *mongo.Collection }

// NewEpisodeRepo 构造剧集仓储。
func NewEpisodeRepo(coll *mongo.Collection) *EpisodeRepo { return &EpisodeRepo{coll: coll} }

// FindBasicByID 按 ID 查基础字段。
func (r *EpisodeRepo) FindBasicByID(ctx context.Context, id any) (*EpisodeBasic, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	e := &EpisodeBasic{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(bson.M{"title": 1, "coverImage": 1, "status": 1})).Decode(e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return e, err
}

// FindByID 按 ID 查完整剧集（对齐 Episode.findById；不存在返回 ErrNotFound）。
func (r *EpisodeRepo) FindByID(ctx context.Context, id any) (*model.Episode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	e := &model.Episode{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return e, err
}

// Create 插入剧集；未显式设置时补 _id/createdAt/updatedAt/__v 默认值
// （对齐 mongoose 自动 _id、default: Date.now 与 __v:0）。
func (r *EpisodeRepo) Create(ctx context.Context, e *model.Episode) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if e.ID.IsZero() {
		e.ID = primitive.NewObjectID()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	_, err := r.coll.InsertOne(ctx, e)
	return err
}

// FindOneAndUpdate 按 ID 更新并返回新文档（对齐 findByIdAndUpdate(id, update, {new:true})）。
func (r *EpisodeRepo) FindOneAndUpdate(ctx context.Context, id any, update bson.M) (*model.Episode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	e := &model.Episode{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return e, err
}

// IncViews 浏览量 +1 并返回新文档（对齐 PUT /:id/view 的 $inc {views:1} new:true）。
func (r *EpisodeRepo) IncViews(ctx context.Context, id any) (*model.Episode, error) {
	return r.FindOneAndUpdate(ctx, id, bson.M{"$inc": bson.M{"views": 1}})
}

// AddSingleEpisodeUpdate 记录新增单集后的剧集变化：设置 updatedAt，
// 可观看集（非预告）同时 currentEpisodes +1（对齐 POST /:id/episodes）。
func (r *EpisodeRepo) AddSingleEpisodeUpdate(ctx context.Context, id any, updatedAt time.Time, incCurrent bool) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	update := bson.M{"$set": bson.M{"updatedAt": updatedAt}}
	if incCurrent {
		update["$inc"] = bson.M{"currentEpisodes": 1}
	}
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, update)
	return err
}

// IncCurrentEpisodes currentEpisodes 增减 delta（对齐单集转可观看 +1 / 删除单集 -1）。
func (r *EpisodeRepo) IncCurrentEpisodes(ctx context.Context, id any, delta int) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$inc": bson.M{"currentEpisodes": delta}})
	return err
}

// Save 整文档覆盖保存（对齐 episode.save()，resubmit 用；__v 自增对齐 mongoose）。
func (r *EpisodeRepo) Save(ctx context.Context, e *model.Episode) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	e.VersionKey++
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": e.ID}, e)
	return err
}

// DeleteByID 按 ID 删除剧集（对齐 Episode.findByIdAndDelete）。
func (r *EpisodeRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": ToObjectID(id)})
	return err
}

// CountDocuments 按过滤条件计数。
func (r *EpisodeRepo) CountDocuments(ctx context.Context, filter any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, filter)
}

// FindList 按过滤条件查询剧集（对齐 Episode.find(filter).sort(sort).skip().limit()）。
func (r *EpisodeRepo) FindList(ctx context.Context, filter any, sort bson.D, skip, limit int64) ([]model.Episode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	opts := options.Find()
	if sort != nil {
		opts.SetSort(sort)
	}
	// skip 恒设置（含负值）：负 skip 由 mongo 拒绝 → 500（对齐 Express 的
	// skip((pageNum-1)*limit) 在 page<1 时的行为）。
	opts.SetSkip(skip)
	if limit > 0 {
		opts.SetLimit(limit)
	}
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Episode
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Aggregate 执行聚合管道（对齐 Episode.aggregate，popular-tags 用）。
func (r *EpisodeRepo) Aggregate(ctx context.Context, pipeline mongo.Pipeline) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []bson.M
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}
