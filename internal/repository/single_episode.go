package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// SingleEpisodeRepo 单集仓储（models/SingleEpisode.js）。
type SingleEpisodeRepo struct{ coll *mongo.Collection }

// NewSingleEpisodeRepo 构造单集仓储。
func NewSingleEpisodeRepo(coll *mongo.Collection) *SingleEpisodeRepo {
	return &SingleEpisodeRepo{coll: coll}
}

// FindByEpisode 查询剧集全部单集，按 episodeNumber 升序（对齐 GET /:id 详情）。
func (r *SingleEpisodeRepo) FindByEpisode(ctx context.Context, episodeID any) ([]model.SingleEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"episodeId": ToObjectID(episodeID)},
		options.Find().SetSort(bson.D{{Key: "episodeNumber", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.SingleEpisode
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByID 按 ID 查单集（对齐 SingleEpisode.findById().lean()）。
func (r *SingleEpisodeRepo) FindByID(ctx context.Context, id any) (*model.SingleEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	s := &model.SingleEpisode{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// Create 插入单集；补 _id/__v 默认值（mongoose 自动 _id 与 __v）。
func (r *SingleEpisodeRepo) Create(ctx context.Context, s *model.SingleEpisode) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if s.ID.IsZero() {
		s.ID = primitive.NewObjectID()
	}
	_, err := r.coll.InsertOne(ctx, s)
	return err
}

// FindOneAndUpdate 按 ID 更新并返回新文档（对齐 findByIdAndUpdate(id, update, {new:true})）。
func (r *SingleEpisodeRepo) FindOneAndUpdate(ctx context.Context, id any, update bson.M) (*model.SingleEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	s := &model.SingleEpisode{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// IncViews 单集浏览量 +1 并返回新文档（对齐 PUT /single/:id/view）。
func (r *SingleEpisodeRepo) IncViews(ctx context.Context, id any) (*model.SingleEpisode, error) {
	return r.FindOneAndUpdate(ctx, id, bson.M{"$inc": bson.M{"views": 1}})
}

// FindOneAndDelete 按 ID 删除并返回被删文档（对齐 SingleEpisode.findByIdAndDelete）。
func (r *SingleEpisodeRepo) FindOneAndDelete(ctx context.Context, id any) (*model.SingleEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	s := &model.SingleEpisode{}
	err := r.coll.FindOneAndDelete(ctx, bson.M{"_id": ToObjectID(id)}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// DeleteByEpisode 删除某剧集全部单集（对齐 DELETE /:id 清理 episodeId 关联）。
func (r *SingleEpisodeRepo) DeleteByEpisode(ctx context.Context, episodeID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"episodeId": ToObjectID(episodeID)})
	return err
}
