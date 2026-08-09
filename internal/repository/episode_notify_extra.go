package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 episodes 域（routes/episodes.js）跨集合访问 Notification 集合的方法，
// 挂到已有 *NotificationRepo 类型上。方法统一以 Episodes 前缀命名。

// EpisodesInsertMany 批量插入通知（对齐 Notification.insertMany）；
// 未显式设置时补 createdAt 默认值。
func (r *NotificationRepo) EpisodesInsertMany(ctx context.Context, items []model.Notification) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if len(items) == 0 {
		return nil
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	docs := make([]any, 0, len(items))
	for i := range items {
		if items[i].CreatedAt.IsZero() {
			items[i].CreatedAt = now
		}
		docs = append(docs, &items[i])
	}
	_, err := r.coll.InsertMany(ctx, docs)
	return err
}

// EpisodesDeleteManyByEpisode 删除某剧集全部通知（对齐 DELETE /:id 的 Notification.deleteMany）。
func (r *NotificationRepo) EpisodesDeleteManyByEpisode(ctx context.Context, episodeID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"episodeId": ToObjectID(episodeID)})
	return err
}
