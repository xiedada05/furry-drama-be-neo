package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 episodes 域（routes/episodes.js）跨集合访问 Follow 集合的方法，
// 挂到已有 *FollowRepo 类型上。方法统一以 Episodes 前缀命名，避免与其它域 agent 重名。

// EpisodesFindByEpisode 查询某剧集全部追番记录（对齐 Follow.find({episodeId})，
// 供新增单集/单集转可观看/集数变更时的通知、推送与邮件收件人查询）。
func (r *FollowRepo) EpisodesFindByEpisode(ctx context.Context, episodeID any) ([]model.Follow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"episodeId": ToObjectID(episodeID)})
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

// EpisodesDeleteManyByEpisode 删除某剧集全部追番（对齐 DELETE /:id 的 Follow.deleteMany）。
func (r *FollowRepo) EpisodesDeleteManyByEpisode(ctx context.Context, episodeID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"episodeId": ToObjectID(episodeID)})
	return err
}
