package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
)

// 本文件为 episodes 域（routes/episodes.js）跨集合访问 Favorite 集合的方法，
// 挂到已有 *FavoriteRepo 类型上。方法统一以 Episodes 前缀命名，避免与其它域 agent 重名。

// EpisodesDeleteManyByEpisode 删除某剧集全部收藏（对齐 DELETE /:id 的 Favorite.deleteMany）。
func (r *FavoriteRepo) EpisodesDeleteManyByEpisode(ctx context.Context, episodeID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"episodeId": ToObjectID(episodeID)})
	return err
}
