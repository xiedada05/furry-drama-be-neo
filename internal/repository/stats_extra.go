package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 stats 域（backend/routes/stats.js）的数据访问方法。
// 方法统一以 Stats 前缀命名，挂到已有 repo 类型上，避免与其它域 agent 重名。

// StatsDateCountRow 是按日期聚合的结果行（_id 为 'YYYY-MM-DD' UTC 日期串）。
type StatsDateCountRow struct {
	Date  string `bson:"_id"`
	Count int64  `bson:"count"`
}

// StatsFollowCountRow 是 Follow 按剧集聚合的结果行（_id 为 episodeId）。
type StatsFollowCountRow struct {
	EpisodeID primitive.ObjectID `bson:"_id"`
	Count     int64              `bson:"count"`
}

// StatsEpisodeRank 是剧集排行响应行（对齐 stats.js 各 select 字段；含 __v，
// 因 Express res.json(doc) 输出 mongoose 版本键）。
type StatsEpisodeRank struct {
	ID            primitive.ObjectID `bson:"_id"`
	Title         string             `bson:"title"`
	AverageRating float64            `bson:"averageRating"`
	RatingCount   int                `bson:"ratingCount"`
	Views         int64              `bson:"views"`
	VersionKey    int                `bson:"__v"`
}

// StatsSimilarRating 是协作过滤候选评分行（对齐 Rating.find({...}).select('userId episodeId score')）。
// Score 用 float64（Rating 的 score 为 Number，可为非整数）。
type StatsSimilarRating struct {
	UserID    primitive.ObjectID `bson:"userId"`
	EpisodeID primitive.ObjectID `bson:"episodeId"`
	Score     float64            `bson:"score"`
}

// StatsInteractedEpisode 是个性化推荐交互剧集行（tags/category/title）。
type StatsInteractedEpisode struct {
	ID       primitive.ObjectID `bson:"_id"`
	Title    string             `bson:"title"`
	Tags     []string           `bson:"tags"`
	Category []string           `bson:"category"`
}

// StatsRecEpisode 是个性化推荐候选剧集行（含评分/浏览量/标签/分类）。
type StatsRecEpisode struct {
	ID              primitive.ObjectID `bson:"_id"`
	Title           string             `bson:"title"`
	TitleEn         string             `bson:"titleEn"`
	CoverImage      string             `bson:"coverImage"`
	TotalEpisodes   *int               `bson:"totalEpisodes"`
	CurrentEpisodes int                `bson:"currentEpisodes"`
	AverageRating   float64            `bson:"averageRating"`
	RatingCount     int                `bson:"ratingCount"`
	Views           int64              `bson:"views"`
	Tags            []string           `bson:"tags"`
	Category        []string           `bson:"category"`
}

// StatsRelatedEpisode 是 /recommendations/:episodeId 相关推荐剧集行（含 __v，
// 因 Express 输出 {...ep.toObject()} 含版本键）。
type StatsRelatedEpisode struct {
	ID              primitive.ObjectID `bson:"_id"`
	Title           string             `bson:"title"`
	CoverImage      string             `bson:"coverImage"`
	CurrentEpisodes int                `bson:"currentEpisodes"`
	TotalEpisodes   *int               `bson:"totalEpisodes"`
	Status          string             `bson:"status"`
	AverageRating   float64            `bson:"averageRating"`
	Views           int64              `bson:"views"`
	Category        []string           `bson:"category"`
	Tags            []string           `bson:"tags"`
	VersionKey      int                `bson:"__v"`
}

// StatsCalendarEpisode 是日历接口 populate('episodeId','title titleEn coverImage
// currentEpisodes totalEpisodes status') 输出的剧集视图（premiereDate 仅供
// StatsFindPremieres 计算日期键）。
type StatsCalendarEpisode struct {
	ID              primitive.ObjectID `bson:"_id"`
	Title           string             `bson:"title"`
	TitleEn         string             `bson:"titleEn"`
	CoverImage      string             `bson:"coverImage"`
	CurrentEpisodes int                `bson:"currentEpisodes"`
	TotalEpisodes   *int               `bson:"totalEpisodes"`
	Status          string             `bson:"status"`
	PremiereDate    *time.Time         `bson:"premiereDate"`
}

// normalize 把 status 缺省补为 ongoing（对齐 mongoose schema default）。
func (e *StatsCalendarEpisode) normalize() {
	if e.Status == "" {
		e.Status = "ongoing"
	}
}

// StatsLifecycleRow 是剧集生命周期行（title/views/createdAt）。
type StatsLifecycleRow struct {
	ID        primitive.ObjectID `bson:"_id"`
	Title     string             `bson:"title"`
	Views     int64              `bson:"views"`
	CreatedAt time.Time          `bson:"createdAt"`
}

// statsAggregateDateCount 按 UTC 日期聚合计数（对齐 $group by $dateToString + $sum:1）。
func statsAggregateDateCount(ctx context.Context, coll *mongo.Collection, match bson.M, dateField string) ([]StatsDateCountRow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$group", Value: bson.M{
			"_id":   bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$" + dateField}},
			"count": bson.M{"$sum": 1},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}
	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []StatsDateCountRow
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// statsAggregateActiveUsers 按 UTC 日期聚合去重用户数（对齐 $addToSet + $size）。
func statsAggregateActiveUsers(ctx context.Context, coll *mongo.Collection, match bson.M, dateField string) ([]StatsDateCountRow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$group", Value: bson.M{
			"_id":   bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$" + dateField}},
			"users": bson.M{"$addToSet": "$userId"},
		}}},
		{{Key: "$project", Value: bson.M{"_id": 1, "count": bson.M{"$size": "$users"}}}},
		{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}
	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []StatsDateCountRow
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// statsFind 通用查询（对齐 Model.find(filter).sort().skip().limit() + 投影）。
// limit<=0 表示不限条数。
func statsFind(ctx context.Context, coll *mongo.Collection, filter bson.M, projection bson.M, sort bson.D, skip, limit int64, out any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	opts := options.Find()
	if projection != nil {
		opts.SetProjection(projection)
	}
	if sort != nil {
		opts.SetSort(sort)
	}
	if skip > 0 {
		opts.SetSkip(skip)
	}
	if limit > 0 {
		opts.SetLimit(limit)
	}
	cur, err := coll.Find(ctx, filter, opts)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	return cur.All(ctx, out)
}

// StatsUserCount 统计用户数（对齐 User.countDocuments(filter)）。
func (r *UserRepo) StatsUserCount(ctx context.Context, filter bson.M) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, filter)
}

// StatsRatingCount 统计评分数（对齐 Rating.countDocuments(filter)）。
func (r *RatingRepo) StatsRatingCount(ctx context.Context, filter bson.M) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, filter)
}

// StatsTotalViews 统计全部剧集浏览量（对齐 Episode.aggregate $sum '$views'）。
func (r *EpisodeRepo) StatsTotalViews(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.M{"_id": nil, "total": bson.M{"$sum": "$views"}}}},
	}
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, err
	}
	defer cur.Close(ctx)
	var out []struct {
		Total int64 `bson:"total"`
	}
	if err := cur.All(ctx, &out); err != nil {
		return 0, err
	}
	if len(out) == 0 {
		return 0, nil
	}
	return out[0].Total, nil
}

// StatsRatingDistribution 评分分布（对齐 Rating.aggregate group by score + sort _id asc）。
func (r *RatingRepo) StatsRatingDistribution(ctx context.Context) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.M{"_id": "$score", "count": bson.M{"$sum": 1}}}},
		{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []bson.M
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StatsEpisodeStatusDist 剧集状态分布（对齐 Episode.aggregate group by status）。
func (r *EpisodeRepo) StatsEpisodeStatusDist(ctx context.Context) ([]bson.M, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.M{"_id": "$status", "count": bson.M{"$sum": 1}}}},
	}
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []bson.M
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StatsTopRated 高分剧集 Top N（对齐 sort({averageRating:-1, ratingCount:-1})）。
func (r *EpisodeRepo) StatsTopRated(ctx context.Context, limit int64) ([]StatsEpisodeRank, error) {
	var out []StatsEpisodeRank
	err := statsFind(ctx, r.coll,
		bson.M{"reviewStatus": "approved", "ratingCount": bson.M{"$gt": 0}},
		bson.M{"title": 1, "averageRating": 1, "ratingCount": 1, "views": 1, "__v": 1},
		bson.D{{Key: "averageRating", Value: -1}, {Key: "ratingCount", Value: -1}},
		0, limit, &out)
	return out, err
}

// StatsTopViewed 浏览量 Top N（对齐 sort({views:-1})）。
func (r *EpisodeRepo) StatsTopViewed(ctx context.Context, limit int64) ([]StatsEpisodeRank, error) {
	var out []StatsEpisodeRank
	err := statsFind(ctx, r.coll,
		bson.M{"reviewStatus": "approved"},
		bson.M{"title": 1, "views": 1, "__v": 1},
		bson.D{{Key: "views", Value: -1}},
		0, limit, &out)
	return out, err
}

// StatsTopByRating 高分剧集 Top N（仅 averageRating 排序，对齐
// sort({averageRating:-1}) + select('title averageRating')）。
func (r *EpisodeRepo) StatsTopByRating(ctx context.Context, limit int64) ([]StatsEpisodeRank, error) {
	var out []StatsEpisodeRank
	err := statsFind(ctx, r.coll,
		bson.M{"reviewStatus": "approved", "ratingCount": bson.M{"$gt": 0}},
		bson.M{"title": 1, "averageRating": 1, "__v": 1},
		bson.D{{Key: "averageRating", Value: -1}},
		0, limit, &out)
	return out, err
}

// StatsTopFollowed 追番最多剧集 Top N（对齐 Follow.aggregate group by episodeId）。
func (r *FollowRepo) StatsTopFollowed(ctx context.Context, limit int) ([]StatsFollowCountRow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	pipeline := mongo.Pipeline{
		{{Key: "$group", Value: bson.M{"_id": "$episodeId", "count": bson.M{"$sum": 1}}}},
		{{Key: "$sort", Value: bson.D{{Key: "count", Value: -1}}}},
		{{Key: "$limit", Value: limit}},
	}
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []StatsFollowCountRow
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StatsFindEpisodeTitles 批量取剧集标题，返回 id→title（对齐 Episode.find({_id:{$in}}).select('title')）。
func (r *EpisodeRepo) StatsFindEpisodeTitles(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]string, error) {
	out := map[primitive.ObjectID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID    primitive.ObjectID `bson:"_id"`
		Title string             `bson:"title"`
	}
	err := statsFind(ctx, r.coll, bson.M{"_id": bson.M{"$in": ids}}, bson.M{"title": 1}, nil, 0, 0, &rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row.Title
	}
	return out, nil
}

// StatsActivityTrend 活跃度趋势（对齐 History.aggregate group by watchedAt 去重用户）。
func (r *HistoryRepo) StatsActivityTrend(ctx context.Context, start time.Time) ([]StatsDateCountRow, error) {
	return statsAggregateActiveUsers(ctx, r.coll, bson.M{"watchedAt": bson.M{"$gte": start}}, "watchedAt")
}

// StatsDAU 每日活跃用户（对齐 History.aggregate group by watchedAt 去重用户）。
func (r *HistoryRepo) StatsDAU(ctx context.Context, start time.Time) ([]StatsDateCountRow, error) {
	return statsAggregateActiveUsers(ctx, r.coll, bson.M{"watchedAt": bson.M{"$gte": start}}, "watchedAt")
}

// StatsRetention 用户留存（对齐 History.aggregate group by lastWatched 去重用户）。
func (r *HistoryRepo) StatsRetention(ctx context.Context, since time.Time) ([]StatsDateCountRow, error) {
	return statsAggregateActiveUsers(ctx, r.coll, bson.M{"lastWatched": bson.M{"$gte": since}}, "lastWatched")
}

// StatsDistinctActiveUsers 统计某时间后活跃去重用户数（对齐 History.distinct('userId', ...)）。
func (r *HistoryRepo) StatsDistinctActiveUsers(ctx context.Context, since time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	vals, err := r.coll.Distinct(ctx, "userId", bson.M{"watchedAt": bson.M{"$gte": since}})
	if err != nil {
		return 0, err
	}
	return int64(len(vals)), nil
}

// StatsUserTrend 用户注册趋势（对齐 User.aggregate group by createdAt）。
func (r *UserRepo) StatsUserTrend(ctx context.Context, start time.Time) ([]StatsDateCountRow, error) {
	return statsAggregateDateCount(ctx, r.coll, bson.M{"createdAt": bson.M{"$gte": start}}, "createdAt")
}

// StatsHeatmapByFollow 追番热度（对齐 Follow.aggregate group by createdAt）。
func (r *FollowRepo) StatsHeatmapByFollow(ctx context.Context, start time.Time) ([]StatsDateCountRow, error) {
	return statsAggregateDateCount(ctx, r.coll, bson.M{"createdAt": bson.M{"$gte": start}}, "createdAt")
}

// StatsHeatmapByRating 评分热度（对齐 Rating.aggregate group by createdAt）。
func (r *RatingRepo) StatsHeatmapByRating(ctx context.Context, start time.Time) ([]StatsDateCountRow, error) {
	return statsAggregateDateCount(ctx, r.coll, bson.M{"createdAt": bson.M{"$gte": start}}, "createdAt")
}

// StatsHeatmapByEpisode 剧集热度（对齐 Episode.aggregate group by createdAt）。
func (r *EpisodeRepo) StatsHeatmapByEpisode(ctx context.Context, start time.Time) ([]StatsDateCountRow, error) {
	return statsAggregateDateCount(ctx, r.coll, bson.M{"createdAt": bson.M{"$gte": start}}, "createdAt")
}

// StatsLifecycleEpisodes 生命周期 Top N（对齐 sort({views:-1}).limit(20).select('title views createdAt')）。
func (r *EpisodeRepo) StatsLifecycleEpisodes(ctx context.Context, limit int64) ([]StatsLifecycleRow, error) {
	var out []StatsLifecycleRow
	err := statsFind(ctx, r.coll,
		bson.M{"reviewStatus": "approved"},
		bson.M{"title": 1, "views": 1, "createdAt": 1},
		bson.D{{Key: "views", Value: -1}},
		0, limit, &out)
	return out, err
}

// StatsSessionCountActiveSince 统计 isActive 且 lastActiveAt 在 since 后的会话数
// （对齐 UserSession.countDocuments({isActive:true, lastActiveAt:{$gte}})）。
func (r *SessionRepo) StatsSessionCountActiveSince(ctx context.Context, since time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"isActive": true, "lastActiveAt": bson.M{"$gte": since}})
}

// ---- 日历 ----

// StatsCalendarReleased 已发布单集（对齐 SingleEpisode.find({releaseDate:{$gte,$lt}}).sort({releaseDate:1})）。
func (r *SingleEpisodeRepo) StatsCalendarReleased(ctx context.Context, start, end time.Time) ([]model.SingleEpisode, error) {
	var out []model.SingleEpisode
	err := statsFind(ctx, r.coll,
		bson.M{"releaseDate": bson.M{"$gte": start, "$lt": end}},
		nil, bson.D{{Key: "releaseDate", Value: 1}}, 0, 0, &out)
	return out, err
}

// StatsCalendarScheduled 已排期单集（对齐 scheduledDate 范围 + isScheduled:true）。
func (r *SingleEpisodeRepo) StatsCalendarScheduled(ctx context.Context, start, end time.Time) ([]model.SingleEpisode, error) {
	var out []model.SingleEpisode
	err := statsFind(ctx, r.coll,
		bson.M{"scheduledDate": bson.M{"$gte": start, "$lt": end}, "isScheduled": true},
		nil, bson.D{{Key: "scheduledDate", Value: 1}}, 0, 0, &out)
	return out, err
}

// StatsCalendarUpcoming 预告单集（对齐 isUpcoming:true + premiereDate 范围）。
func (r *SingleEpisodeRepo) StatsCalendarUpcoming(ctx context.Context, start, end time.Time) ([]model.SingleEpisode, error) {
	var out []model.SingleEpisode
	err := statsFind(ctx, r.coll,
		bson.M{"isUpcoming": true, "premiereDate": bson.M{"$gte": start, "$lt": end}},
		nil, bson.D{{Key: "premiereDate", Value: 1}}, 0, 0, &out)
	return out, err
}

// StatsCalendarEpisodeDocs 批量取日历 populate 的剧集视图，返回 id→视图。
// 仅返回已过审剧集（reviewStatus:'approved'）：未过审剧集无视图 → 关联单集被跳过，
// 日历不展示待审核/未通过内容。
func (r *EpisodeRepo) StatsCalendarEpisodeDocs(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]StatsCalendarEpisode, error) {
	out := map[primitive.ObjectID]StatsCalendarEpisode{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []StatsCalendarEpisode
	err := statsFind(ctx, r.coll,
		bson.M{"_id": bson.M{"$in": ids}, "reviewStatus": "approved"},
		bson.M{"title": 1, "titleEn": 1, "coverImage": 1, "currentEpisodes": 1, "totalEpisodes": 1, "status": 1},
		nil, 0, 0, &rows)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		row.normalize()
		out[row.ID] = row
	}
	return out, nil
}

// StatsFindPremieres 已排期首播剧集（对齐 Episode.find({status:'upcoming', premiereDate:{$gte,$lt}}).sort({premiereDate:1})）。
// 仅已过审剧集：首播排期对公众可见，待审核/未通过不上日历。
func (r *EpisodeRepo) StatsFindPremieres(ctx context.Context, start, end time.Time) ([]StatsCalendarEpisode, error) {
	var out []StatsCalendarEpisode
	err := statsFind(ctx, r.coll,
		bson.M{"status": "upcoming", "premiereDate": bson.M{"$gte": start, "$lt": end}, "reviewStatus": "approved"},
		bson.M{"title": 1, "titleEn": 1, "coverImage": 1, "totalEpisodes": 1, "status": 1, "premiereDate": 1},
		bson.D{{Key: "premiereDate", Value: 1}}, 0, 0, &out)
	for i := range out {
		out[i].normalize()
	}
	return out, err
}

// ---- 协作过滤推荐 ----

// StatsMyHighRated 用户高分剧集 ID（对齐 Rating.find({userId, score:{$gte:4}}).select('episodeId')）。
func (r *RatingRepo) StatsMyHighRated(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	var rows []struct {
		EpisodeID primitive.ObjectID `bson:"episodeId"`
	}
	err := statsFind(ctx, r.coll,
		bson.M{"userId": userID, "score": bson.M{"$gte": 4}},
		bson.M{"episodeId": 1}, nil, 0, 0, &rows)
	if err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EpisodeID)
	}
	return ids, nil
}

// StatsSimilarUserRatings 相似用户的高分评分（对齐
// Rating.find({episodeId:{$in}, score:{$gte:4}, userId:{$ne:userId}}).select('userId episodeId')）。
func (r *RatingRepo) StatsSimilarUserRatings(ctx context.Context, episodeIDs []primitive.ObjectID, excludeUserID primitive.ObjectID) ([]StatsSimilarRating, error) {
	var out []StatsSimilarRating
	err := statsFind(ctx, r.coll,
		bson.M{"episodeId": bson.M{"$in": episodeIDs}, "score": bson.M{"$gte": 4}, "userId": bson.M{"$ne": excludeUserID}},
		bson.M{"userId": 1, "episodeId": 1}, nil, 0, 0, &out)
	return out, err
}

// StatsMyRatedEpisodeIDs 用户全部评分剧集 ID（对齐 Rating.find({userId}).select('episodeId')）。
func (r *RatingRepo) StatsMyRatedEpisodeIDs(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	var rows []struct {
		EpisodeID primitive.ObjectID `bson:"episodeId"`
	}
	err := statsFind(ctx, r.coll, bson.M{"userId": userID}, bson.M{"episodeId": 1}, nil, 0, 0, &rows)
	if err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EpisodeID)
	}
	return ids, nil
}

// StatsMyFollowedEpisodeIDs 用户全部追番剧集 ID（对齐 Follow.find({userId}).select('episodeId')）。
func (r *FollowRepo) StatsMyFollowedEpisodeIDs(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	var rows []struct {
		EpisodeID primitive.ObjectID `bson:"episodeId"`
	}
	err := statsFind(ctx, r.coll, bson.M{"userId": userID}, bson.M{"episodeId": 1}, nil, 0, 0, &rows)
	if err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EpisodeID)
	}
	return ids, nil
}

// StatsCandidateRatings 候选评分（对齐 Rating.find({userId:{$in}, score:{$gte:4},
// episodeId:{$nin}}).select('episodeId userId score')）。
func (r *RatingRepo) StatsCandidateRatings(ctx context.Context, similarUserIDs, excludeEpisodeIDs []primitive.ObjectID) ([]StatsSimilarRating, error) {
	var out []StatsSimilarRating
	err := statsFind(ctx, r.coll,
		bson.M{"userId": bson.M{"$in": similarUserIDs}, "score": bson.M{"$gte": 4}, "episodeId": bson.M{"$nin": excludeEpisodeIDs}},
		bson.M{"episodeId": 1, "userId": 1, "score": 1}, nil, 0, 0, &out)
	return out, err
}

// StatsFindRecEpisodesByIDs 协作过滤结果的剧集详情（对齐
// Episode.find({_id:{$in}, reviewStatus:'approved'}).select(...)）。
func (r *EpisodeRepo) StatsFindRecEpisodesByIDs(ctx context.Context, ids []primitive.ObjectID) ([]StatsRecEpisode, error) {
	var out []StatsRecEpisode
	if len(ids) == 0 {
		return out, nil
	}
	err := statsFind(ctx, r.coll,
		bson.M{"_id": bson.M{"$in": ids}, "reviewStatus": "approved"},
		bson.M{"title": 1, "titleEn": 1, "coverImage": 1, "totalEpisodes": 1,
			"currentEpisodes": 1, "averageRating": 1, "ratingCount": 1},
		nil, 0, 0, &out)
	return out, err
}

// ---- 个性化推荐 ----

// StatsInteractedEpisodes 用户交互过的剧集（对齐 Episode.find({_id:{$in}}).select('tags category title')）。
func (r *EpisodeRepo) StatsInteractedEpisodes(ctx context.Context, ids []primitive.ObjectID) ([]StatsInteractedEpisode, error) {
	var out []StatsInteractedEpisode
	if len(ids) == 0 {
		return out, nil
	}
	err := statsFind(ctx, r.coll,
		bson.M{"_id": bson.M{"$in": ids}},
		bson.M{"tags": 1, "category": 1, "title": 1}, nil, 0, 0, &out)
	return out, err
}

// StatsRecByTags 标签匹配候选（对齐 Episode.find({tags:{$in}, _id:{$nin}, reviewStatus:'approved'})
// .select 全部推荐字段）。
func (r *EpisodeRepo) StatsRecByTags(ctx context.Context, tags []string, exclude []primitive.ObjectID) ([]StatsRecEpisode, error) {
	return r.statsRecCandidates(ctx, bson.M{"tags": bson.M{"$in": tags}, "_id": bson.M{"$nin": exclude}})
}

// StatsRecByCategory 分类匹配候选（对齐 Episode.find({category:{$in}, _id:{$nin}, reviewStatus:'approved'})）。
func (r *EpisodeRepo) StatsRecByCategory(ctx context.Context, categories []string, exclude []primitive.ObjectID) ([]StatsRecEpisode, error) {
	return r.statsRecCandidates(ctx, bson.M{"category": bson.M{"$in": categories}, "_id": bson.M{"$nin": exclude}})
}

// StatsRecPopular 高分热门候选（对齐 sort({averageRating:-1, views:-1}).limit(20)）。
func (r *EpisodeRepo) StatsRecPopular(ctx context.Context, exclude []primitive.ObjectID) ([]StatsRecEpisode, error) {
	return r.statsRecCandidates(ctx, bson.M{"_id": bson.M{"$nin": exclude}, "ratingCount": bson.M{"$gt": 0}})
}

// statsRecCandidates 个性化推荐候选查询公共实现（filter 恒叠加 reviewStatus:'approved'）。
func (r *EpisodeRepo) statsRecCandidates(ctx context.Context, filter bson.M) ([]StatsRecEpisode, error) {
	filter["reviewStatus"] = "approved"
	var out []StatsRecEpisode
	err := statsFind(ctx, r.coll, filter,
		bson.M{"title": 1, "titleEn": 1, "coverImage": 1, "totalEpisodes": 1,
			"currentEpisodes": 1, "averageRating": 1, "ratingCount": 1,
			"views": 1, "tags": 1, "category": 1},
		nil, 0, 0, &out)
	return out, err
}

// StatsRelatedEpisodes 相关推荐样本（对齐 Episode.find({_id:{$ne}, reviewStatus:'approved'})
// .select(...).skip(skip).limit(200)）。
func (r *EpisodeRepo) StatsRelatedEpisodes(ctx context.Context, excludeID primitive.ObjectID, skip int64) ([]StatsRelatedEpisode, error) {
	var out []StatsRelatedEpisode
	err := statsFind(ctx, r.coll,
		bson.M{"_id": bson.M{"$ne": excludeID}, "reviewStatus": "approved"},
		bson.M{"title": 1, "coverImage": 1, "currentEpisodes": 1, "totalEpisodes": 1,
			"status": 1, "averageRating": 1, "views": 1, "category": 1, "tags": 1, "__v": 1},
		nil, skip, 200, &out)
	for i := range out {
		if out[i].Status == "" {
			out[i].Status = "ongoing"
		}
	}
	return out, err
}

// StatsCountRelated 相关推荐样本总数（对齐 Episode.countDocuments({_id:{$ne}, reviewStatus:'approved'})）。
func (r *EpisodeRepo) StatsCountRelated(ctx context.Context, excludeID primitive.ObjectID) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"_id": bson.M{"$ne": excludeID}, "reviewStatus": "approved"})
}
