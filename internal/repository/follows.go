package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件给 *FollowRepo 补齐追番域（/api/follows）专用查询方法。
// 方法名统一 Follow 前缀，避免与其它域 agent 添加的方法重名。

// FollowInsert 插入追番记录；userId+episodeId 唯一键冲突返回 errDuplicateKey
// （对齐 follows.js 的 error.code===11000 → 'Already following'）。
func (r *FollowRepo) FollowInsert(ctx context.Context, f *model.Follow) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.InsertOne(ctx, f)
	if mongo.IsDuplicateKeyError(err) {
		return errDuplicateKey
	}
	return err
}

// FollowDeleteOne 删除用户对某剧集的追番。不存在也返回成功（对齐 deleteOne 幂等语义）。
func (r *FollowRepo) FollowDeleteOne(ctx context.Context, userID, episodeID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"userId": userID, "episodeId": episodeID})
	return err
}

// FollowCount 按筛选条件统计追番数（对齐 countDocuments(filter)）。
func (r *FollowRepo) FollowCount(ctx context.Context, filter bson.M) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, filter)
}

// FollowListPaged 分页查询追番，按 createdAt 倒序（对齐 follows.js GET /list
// 默认分支的 sort({createdAt:-1}).skip().limit()）。
func (r *FollowRepo) FollowListPaged(ctx context.Context, filter bson.M, page, limit int) ([]model.Follow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(int64((page-1)*limit)).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Follow
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FollowListLimited 取前 limit 条追番、不排序（对齐 follows.js name/rating/lastWatched
// 分支的 find(filter).limit(maxItems)，内存排序后再切片）。
func (r *FollowRepo) FollowListLimited(ctx context.Context, filter bson.M, limit int64) ([]model.Follow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, filter, options.Find().SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Follow
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FollowFindByUserEpisode 查用户对某剧集的追番（对齐 findOne({userId, episodeId})）；
// 不存在返回 ErrNotFound。
func (r *FollowRepo) FollowFindByUserEpisode(ctx context.Context, userID, episodeID any) (*model.Follow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	f := &model.Follow{}
	err := r.coll.FindOne(ctx, bson.M{"userId": userID, "episodeId": episodeID}).Decode(f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return f, err
}
