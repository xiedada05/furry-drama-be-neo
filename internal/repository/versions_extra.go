package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 versions 域（routes/versions.js）在 *EpisodeVersionRepo 上新增的
// 独占方法。episode_version.go 已有的 FindLatest/CountByEpisode/FindOldestN/
// DeleteManyByIDs/Create 直接复用，不重复定义。

// VersionsFindByEpisodePage 分页查询某剧集全部版本，按 version 倒序
// （对齐 versions.js GET /:episodeId 的 find().sort({version:-1}).skip().limit()）。
func (r *EpisodeVersionRepo) VersionsFindByEpisodePage(ctx context.Context, episodeID any, page, limit int) ([]model.EpisodeVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"episodeId": ToObjectID(episodeID)},
		options.Find().
			SetSort(bson.D{{Key: "version", Value: -1}}).
			SetSkip(int64((page-1)*limit)).
			SetLimit(int64(limit)))
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

// VersionsFindOneByEpisodeVersion 查某剧集指定版本
// （对齐 findOne({episodeId, version})）；不存在返回 ErrNotFound。
func (r *EpisodeVersionRepo) VersionsFindOneByEpisodeVersion(ctx context.Context, episodeID any, version int) (*model.EpisodeVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	v := &model.EpisodeVersion{}
	err := r.coll.FindOne(ctx, bson.M{"episodeId": ToObjectID(episodeID), "version": version}).Decode(v)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return v, err
}
