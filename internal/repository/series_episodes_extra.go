package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 series 域（series.js）跨域访问 episodes 集合的查询方法，挂到已有
// EpisodeRepo 类型上。方法名带 Series 后缀，避免与 episodes/folders 域并行
// 新增的方法重名（如 FindFolderItemsByIDs）。series.go 本身不改动。

// FindByIDsForSeries 按 ID 批量查询系列引用的剧集（对齐 series.js GET / 与
// GET /:id 的 .populate('episodes', ...) 字段投影）。
// 返回详情端点的字段超集；列表端点由 handler 构造精简视图。
// 不存在的 ID 直接跳过（mongoose populate 对已删除剧集在数组中留空）。
func (r *EpisodeRepo) FindByIDsForSeries(ctx context.Context, ids []primitive.ObjectID) ([]model.SeriesEpisodeDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if len(ids) == 0 {
		return []model.SeriesEpisodeDetail{}, nil
	}
	projection := bson.M{
		"title": 1, "coverImage": 1, "currentEpisodes": 1, "totalEpisodes": 1,
		"status": 1, "averageRating": 1, "description": 1, "category": 1,
		"tags": 1, "views": 1,
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(projection))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]model.SeriesEpisodeDetail, 0)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	// 对齐 mongoose 数组默认 []：缺失字段 hydrate 后为 []，JSON 输出 [] 而非 null。
	for i := range list {
		if list[i].Category == nil {
			list[i].Category = []string{}
		}
		if list[i].Tags == nil {
			list[i].Tags = []string{}
		}
	}
	return list, nil
}
