package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 /api/activity 域（routes/activity.js）跨集合访问 SingleEpisode /
// Rating 集合的方法，挂到已有 *SingleEpisodeRepo / *RatingRepo 类型上。
// 方法统一以 Activity 前缀命名，避免与其它域 agent 新增方法重名。
//
// 注意：Express activity.js 并没有独立 Activity collection——动态流由
// Follow/SingleEpisode/Episode/Rating 实时聚合计算。故此处仅补充查询方法，
// 不创建新集合（repos.go 亦未注册 ActivityRepo，按约束不改动 repos.go）。

// ActivitySinglesFollowedNew 查询追番剧集近期发布的新单集（对齐 activity.js
// GET / 的 SingleEpisode.find({episodeId:{$in}, releaseDate:{$gte}})
// .sort({releaseDate:-1})，不设 limit 返回全部）。
func (r *SingleEpisodeRepo) ActivitySinglesFollowedNew(ctx context.Context, episodeIDs []primitive.ObjectID, since time.Time) ([]model.SingleEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{
		"episodeId":   bson.M{"$in": episodeIDs},
		"releaseDate": bson.M{"$gte": since},
	}, options.Find().SetSort(bson.D{{Key: "releaseDate", Value: -1}}))
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

// ActivitySinglesRecent 查询全站近期发布的新单集（对齐 activity.js GET /public
// 的 SingleEpisode.find({releaseDate:{$gte}}).sort({releaseDate:-1}).limit(30)）。
func (r *SingleEpisodeRepo) ActivitySinglesRecent(ctx context.Context, since time.Time, limit int64) ([]model.SingleEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"releaseDate": bson.M{"$gte": since}},
		options.Find().SetSort(bson.D{{Key: "releaseDate", Value: -1}}).SetLimit(limit))
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

// ActivityHighRatings 查询追番剧集近期的高评分记录（对齐 activity.js GET / 的
// Rating.find({episodeId:{$in}, score:{$gte:4}, createdAt:{$gte}})
// .sort({createdAt:-1}).limit(50)）。
func (r *RatingRepo) ActivityHighRatings(ctx context.Context, episodeIDs []primitive.ObjectID, since time.Time, limit int64) ([]model.Rating, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{
		"episodeId": bson.M{"$in": episodeIDs},
		"score":     bson.M{"$gte": 4},
		"createdAt": bson.M{"$gte": since},
	}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Rating
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}
