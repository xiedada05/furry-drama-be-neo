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

// EpisodeVersionRepo 剧集版本仓储（models/EpisodeVersion.js，episodeId+version 唯一）。
type EpisodeVersionRepo struct{ coll *mongo.Collection }

// NewEpisodeVersionRepo 构造剧集版本仓储。
func NewEpisodeVersionRepo(coll *mongo.Collection) *EpisodeVersionRepo {
	return &EpisodeVersionRepo{coll: coll}
}

// Create 插入版本快照；补 _id/createdAt/__v 默认值。
func (r *EpisodeVersionRepo) Create(ctx context.Context, v *model.EpisodeVersion) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if v.ID.IsZero() {
		v.ID = primitive.NewObjectID()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	_, err := r.coll.InsertOne(ctx, v)
	return err
}

// FindLatest 查某剧集最大 version 的版本（对齐 findOne().sort({version:-1})）。
func (r *EpisodeVersionRepo) FindLatest(ctx context.Context, episodeID any) (*model.EpisodeVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	v := &model.EpisodeVersion{}
	err := r.coll.FindOne(ctx, bson.M{"episodeId": ToObjectID(episodeID)},
		options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})).Decode(v)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return v, err
}

// CountByEpisode 统计某剧集版本数。
func (r *EpisodeVersionRepo) CountByEpisode(ctx context.Context, episodeID any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"episodeId": ToObjectID(episodeID)})
}

// FindOldestN 查某剧集最旧的前 n 个版本（version 升序；版本裁剪用）。
func (r *EpisodeVersionRepo) FindOldestN(ctx context.Context, episodeID any, n int64) ([]model.EpisodeVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"episodeId": ToObjectID(episodeID)},
		options.Find().SetSort(bson.D{{Key: "version", Value: 1}}).SetLimit(n).
			SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.EpisodeVersion
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindAllByEpisode 查某剧集全部版本（version 倒序；回收站日志查看用）。
func (r *EpisodeVersionRepo) FindAllByEpisode(ctx context.Context, episodeID any) ([]model.EpisodeVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"episodeId": ToObjectID(episodeID)},
		options.Find().SetSort(bson.D{{Key: "version", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.EpisodeVersion
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// DeleteManyByIDs 批量删除版本（对齐 deleteMany({_id:{$in:[...]}})）。
func (r *EpisodeVersionRepo) DeleteManyByIDs(ctx context.Context, ids []any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	res, err := r.coll.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// DeleteByEpisode 删除某剧集全部版本（对齐 DELETE /:id 清理 episodeId 关联）。
func (r *EpisodeVersionRepo) DeleteByEpisode(ctx context.Context, episodeID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"episodeId": ToObjectID(episodeID)})
	return err
}
