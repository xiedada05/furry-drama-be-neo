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

// 本文件为 /api/histories 域（routes/histories.js）的数据访问方法，
// 挂在 *HistoryRepo 与 *EpisodeRepo 上。方法名带 History 前缀，避免与
// episodes/follows 等其它域的跨集合方法（Episodes* / Follows* 前缀）冲突。

// historyDoc 是 histories 集合文档（含 mongoose 版本键 __v，对齐 Express 响应中的
// __v 输出）。内联嵌入 model.History 复用字段。
type historyDoc struct {
	model.History `bson:",inline"`
	Version       int `bson:"__v"`
}

// HistoryRow 是单条历史记录 + mongoose 版本键（供 handler 组装响应）。
type HistoryRow struct {
	History model.History
	Version int
}

// HistoryCheckRow 是 GET /check 的返回数据。时间用指针以保留 null（对齐 mongoose
// 对 lastWatched 缺省 null 的输出）；watchedEpisodes 由 handler 归一化为空数组。
type HistoryCheckRow struct {
	WatchedEpisodes          []int      `bson:"watchedEpisodes"`
	LastWatchedEpisodeNumber *int       `bson:"lastWatchedEpisodeNumber"`
	LastWatched              *time.Time `bson:"lastWatched"`
}

// HistoryEpisodeView 是历史响应中 populate 出的剧集摘要（对齐 routes/histories.js
// populate('episodeId', 'title titleEn titleJa coverImage totalEpisodes
// currentEpisodes status category tags views averageRating') 的字段列表与
// mongoose schema 默认值）。
type HistoryEpisodeView struct {
	ID              primitive.ObjectID `bson:"_id" json:"_id"`
	Title           string             `bson:"title" json:"title"`
	TitleEn         string             `bson:"titleEn" json:"titleEn"`
	TitleJa         string             `bson:"titleJa" json:"titleJa"`
	CoverImage      string             `bson:"coverImage" json:"coverImage"`
	TotalEpisodes   *int               `bson:"totalEpisodes" json:"totalEpisodes"`
	CurrentEpisodes int                `bson:"currentEpisodes" json:"currentEpisodes"`
	Status          string             `bson:"status" json:"status"`
	Category        []string           `bson:"category" json:"category"`
	Tags            []string           `bson:"tags" json:"tags"`
	Views           int64              `bson:"views" json:"views"`
	AverageRating   float64            `bson:"averageRating" json:"averageRating"`
}

// normalize 补齐 mongoose schema 默认值：status 缺省 ongoing，category/tags 缺省 []。
func (v *HistoryEpisodeView) normalize() {
	if v.Status == "" {
		v.Status = "ongoing"
	}
	if v.Category == nil {
		v.Category = []string{}
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
}

// HistoryUpsertRecord 记录/更新一条观看历史（对齐 POST /record 的 findOne → create /
// push + save 分支）：
//   - 不存在 → 插入 {watchedEpisodes:[epNum], lastWatchedEpisodeNumber:epNum, lastWatched:now}；
//   - 已存在 → epNum 不在 watchedEpisodes 则追加，更新 lastWatchedEpisodeNumber /
//     lastWatched，__v 自增（对齐 mongoose save()）。
//
// 返回保存后的历史文档与 __v 值。
func (r *HistoryRepo) HistoryUpsertRecord(ctx context.Context, userID, episodeID primitive.ObjectID, epNum int, now time.Time) (*model.History, int, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	now = now.UTC().Truncate(time.Millisecond) // 对齐 Date.now() 毫秒精度 + mongoose 的 Z 时区

	doc := &historyDoc{}
	err := r.coll.FindOne(ctx, bson.M{"userId": userID, "episodeId": episodeID}).Decode(doc)
	if err == nil {
		found := false
		for _, n := range doc.WatchedEpisodes {
			if n == epNum {
				found = true
				break
			}
		}
		if !found {
			doc.WatchedEpisodes = append(doc.WatchedEpisodes, epNum)
		}
		doc.LastWatchedEpisodeNumber = &epNum
		doc.LastWatched = now
		doc.Version++
		if _, err := r.coll.UpdateOne(ctx, bson.M{"_id": doc.ID}, bson.M{"$set": bson.M{
			"watchedEpisodes":          doc.WatchedEpisodes,
			"lastWatchedEpisodeNumber": epNum,
			"lastWatched":              now,
			"__v":                      doc.Version,
		}}); err != nil {
			return nil, 0, err
		}
		h := doc.History
		return &h, doc.Version, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, 0, err
	}

	h := &model.History{
		UserID:                   userID,
		EpisodeID:                episodeID,
		WatchedEpisodes:          []int{epNum},
		LastWatchedEpisodeNumber: &epNum,
		LastWatched:              now,
		CreatedAt:                now,
	}
	res, err := r.coll.InsertOne(ctx, historyDoc{History: *h, Version: 0})
	if err != nil {
		return nil, 0, err
	}
	h.ID = res.InsertedID.(primitive.ObjectID)
	return h, 0, nil
}

// HistoryCountByUser 统计用户历史总数（GET /list 的 total）。
func (r *HistoryRepo) HistoryCountByUser(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"userId": userID})
}

// HistoryFindContinueWatching 返回用户最近观看的至多 limit 条历史
// （对齐 GET /continue-watching 的 sort({lastWatched:-1}).limit(10)）。
func (r *HistoryRepo) HistoryFindContinueWatching(ctx context.Context, userID primitive.ObjectID, limit int) ([]HistoryRow, error) {
	return r.historyFindRows(ctx, userID, 0, int64(limit))
}

// HistoryFindPage 分页查询用户历史（对齐 GET /list 的
// sort({lastWatched:-1}).skip(...).limit(...)）。
//   - skip 恒设置：负值由 mongo 拒绝 → error → handler 500（对齐 Express
//     skip((pageNum-1)*limit) 在 page<1 时抛错）；
//   - limit==0 表示不限条数（对齐 mongoose limit(0)）；limit<0 由 mongo 拒绝 → 500。
func (r *HistoryRepo) HistoryFindPage(ctx context.Context, userID primitive.ObjectID, skip, limit int64) ([]HistoryRow, error) {
	return r.historyFindRows(ctx, userID, skip, limit)
}

// historyFindRows 按 lastWatched 倒序查询历史行。
func (r *HistoryRepo) historyFindRows(ctx context.Context, userID primitive.ObjectID, skip, limit int64) ([]HistoryRow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	opts := options.Find().
		SetSort(bson.D{{Key: "lastWatched", Value: -1}}).
		SetSkip(skip)
	if limit != 0 {
		opts.SetLimit(limit)
	}
	cur, err := r.coll.Find(ctx, bson.M{"userId": userID}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	rows := make([]HistoryRow, 0)
	for cur.Next(ctx) {
		var d historyDoc
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		rows = append(rows, HistoryRow{History: d.History, Version: d.Version})
	}
	return rows, cur.Err()
}

// HistoryFindCheck 查用户对某剧集的观看历史（GET /check/:episodeId，对齐
// History.findOne({userId, episodeId})）；不存在返回 ErrNotFound。
func (r *HistoryRepo) HistoryFindCheck(ctx context.Context, userID, episodeID primitive.ObjectID) (*HistoryCheckRow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	row := &HistoryCheckRow{}
	err := r.coll.FindOne(ctx, bson.M{"userId": userID, "episodeId": episodeID},
		options.FindOne().SetProjection(bson.M{"watchedEpisodes": 1, "lastWatchedEpisodeNumber": 1, "lastWatched": 1})).Decode(row)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return row, err
}

// HistoryDeleteOneByUserEpisode 删除用户对某剧集的观看历史（DELETE /:episodeId，
// 对齐 History.deleteOne({userId, episodeId})）。
func (r *HistoryRepo) HistoryDeleteOneByUserEpisode(ctx context.Context, userID, episodeID primitive.ObjectID) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"userId": userID, "episodeId": episodeID})
	return err
}

// HistoryFindEpisodePopulate 批量拉取历史 populate 所需的剧集摘要
// （key 为 episode _id；不存在的剧集不在 map 中 → 响应 episodeId: null，
// 对齐 mongoose populate 对已删除剧集置 null）。
func (r *EpisodeRepo) HistoryFindEpisodePopulate(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]*HistoryEpisodeView, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	out := make(map[primitive.ObjectID]*HistoryEpisodeView, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	projection := bson.M{
		"title": 1, "titleEn": 1, "titleJa": 1, "coverImage": 1,
		"totalEpisodes": 1, "currentEpisodes": 1, "status": 1,
		"category": 1, "tags": 1, "views": 1, "averageRating": 1,
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(projection))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var v HistoryEpisodeView
		if err := cur.Decode(&v); err != nil {
			return nil, err
		}
		v.normalize()
		out[v.ID] = &v
	}
	return out, cur.Err()
}
