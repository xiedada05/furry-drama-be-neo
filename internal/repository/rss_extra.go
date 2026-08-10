package repository

// 本文件为 rss 域（routes/rss.js）跨集合查询方法，挂到已有 repo 类型上。
// 方法名统一 RSS 前缀，避免与其它域新增方法冲突；不改动他人的 repo 文件。

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// RSSEpisode 是 RSS feed 剧集条目投影
// （对齐 routes/rss.js select('title currentEpisodes totalEpisodes status updatedAt')）。
type RSSEpisode struct {
	ID              primitive.ObjectID `bson:"_id"`
	Title           string             `bson:"title"`
	CurrentEpisodes int                `bson:"currentEpisodes"`
	TotalEpisodes   *int               `bson:"totalEpisodes"`
	Status          string             `bson:"status"`
	UpdatedAt       time.Time          `bson:"updatedAt"`
}

// RSSFindRecent 查询最近 7 天内已审核通过的剧集（对齐
// Episode.find({reviewStatus:'approved', updatedAt:{$gte: sevenDaysAgo}}).
// sort({updatedAt:-1}).limit(20).select('title currentEpisodes totalEpisodes status updatedAt')）。
func (r *EpisodeRepo) RSSFindRecent(ctx context.Context, since time.Time) ([]RSSEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{
		"reviewStatus": "approved",
		"updatedAt":    bson.M{"$gte": since},
	}, options.Find().
		SetSort(bson.D{{Key: "updatedAt", Value: -1}}).
		SetLimit(20).
		SetProjection(bson.M{"title": 1, "currentEpisodes": 1, "totalEpisodes": 1, "status": 1, "updatedAt": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]RSSEpisode, 0, 20)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// RSSSingleEpisode 是 RSS feed 单集条目投影
// （对齐 routes/rss.js 的 SingleEpisode.find(...).populate('episodeId','title') 所需字段）。
type RSSSingleEpisode struct {
	ID            primitive.ObjectID `bson:"_id"`
	EpisodeID     primitive.ObjectID `bson:"episodeId"`
	EpisodeNumber int                `bson:"episodeNumber"`
	Title         string             `bson:"title"`
	CreatedAt     time.Time          `bson:"createdAt"`
}

// RSSFindRecent 查询最近 7 天创建的单集（对齐
// SingleEpisode.find({createdAt:{$gte: sevenDaysAgo}}).sort({createdAt:-1}).limit(20)）。
func (r *SingleEpisodeRepo) RSSFindRecent(ctx context.Context, since time.Time) ([]RSSSingleEpisode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"createdAt": bson.M{"$gte": since}},
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetLimit(20).
			SetProjection(bson.M{"episodeId": 1, "episodeNumber": 1, "title": 1, "createdAt": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]RSSSingleEpisode, 0, 20)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// RSSFindEpisodeTitles 批量取剧集标题，返回 hex → title 映射
// （对齐 populate('episodeId', 'title')：被引用剧集不存在时条目缺失 → 调用方跳过）。
func (r *EpisodeRepo) RSSFindEpisodeTitles(ctx context.Context, ids []primitive.ObjectID) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"title": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var docs []struct {
		ID    primitive.ObjectID `bson:"_id"`
		Title string             `bson:"title"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		out[d.ID.Hex()] = d.Title
	}
	return out, nil
}

// RSSFindSince 查询接口统计（对齐
// ApiUsage.find({date:{$gte: since}}).sort({date:-1,count:-1})）。返回原始 bson.M 文档，
// 由 handler 层负责 JSON 化（对齐 mongoose toJSON 输出 _id/endpoint/method/count/date/__v）。
func (r *ApiUsageRepo) RSSFindSince(ctx context.Context, since string) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"date": bson.M{"$gte": since}},
		options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "count", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]bson.M, 0)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}
