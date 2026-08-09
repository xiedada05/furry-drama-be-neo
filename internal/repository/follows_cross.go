package repository

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// 本文件是追番/收藏域（follows/favorites）跨集合访问的独占文件：
// 给 *EpisodeRepo / *HistoryRepo / *FolderRepo 补填充（populate）所需的查询方法。
// 方法名统一 Follow 前缀、类型名统一 Follows 前缀，避免与其它域 agent 重名。

// FollowsEpisodeDoc 是追番/收藏列表 populate episodeId 时输出的剧集文档视图，
// 字段逐项对齐 backend/models/Episode.js 的 schema（mongoose toJSON 含默认字段）。
type FollowsEpisodeDoc struct {
	ID                   primitive.ObjectID   `bson:"_id" json:"_id"`
	Title                string               `bson:"title" json:"title"`
	TitleEn              string               `bson:"titleEn" json:"titleEn"`
	TitleJa              string               `bson:"titleJa" json:"titleJa"`
	Description          string               `bson:"description" json:"description"`
	DescriptionEn        string               `bson:"descriptionEn" json:"descriptionEn"`
	DescriptionJa        string               `bson:"descriptionJa" json:"descriptionJa"`
	CoverImage           string               `bson:"coverImage" json:"coverImage"`
	TotalEpisodes        *int                 `bson:"totalEpisodes" json:"totalEpisodes"`
	CurrentEpisodes      int                  `bson:"currentEpisodes" json:"currentEpisodes"`
	Status               string               `bson:"status" json:"status"`
	Category             []string             `bson:"category" json:"category"`
	Tags                 []string             `bson:"tags" json:"tags"`
	PlatformLinks        map[string]string    `bson:"platformLinks" json:"platformLinks"`
	Views                int64                `bson:"views" json:"views"`
	AverageRating        float64              `bson:"averageRating" json:"averageRating"`
	RatingCount          int64                `bson:"ratingCount" json:"ratingCount"`
	UpdateDay            string               `bson:"updateDay" json:"updateDay"`
	PremiereDate         *time.Time           `bson:"premiereDate" json:"premiereDate"`
	CreatedBy            *primitive.ObjectID  `bson:"createdBy" json:"createdBy"`
	HideCreator          bool                 `bson:"hideCreator" json:"hideCreator"`
	AllowedEditors       []primitive.ObjectID `bson:"allowedEditors" json:"allowedEditors"`
	CustomAuthors        []primitive.ObjectID `bson:"customAuthors" json:"customAuthors"`
	QQGroupLink          string               `bson:"qqGroupLink" json:"qqGroupLink"`
	ReviewStatus         string               `bson:"reviewStatus" json:"reviewStatus"`
	ReviewNote           string               `bson:"reviewNote" json:"reviewNote"`
	PendingChanges       primitive.M          `bson:"pendingChanges" json:"pendingChanges"`
	HasPendingChanges    bool                 `bson:"hasPendingChanges" json:"hasPendingChanges"`
	PendingChangeSummary string               `bson:"pendingChangeSummary" json:"pendingChangeSummary"`
	ReviewedBy           *primitive.ObjectID  `bson:"reviewedBy" json:"reviewedBy"`
	ReviewedAt           *time.Time           `bson:"reviewedAt" json:"reviewedAt"`
	CreatedAt            time.Time            `bson:"createdAt" json:"createdAt"`
	UpdatedAt            time.Time            `bson:"updatedAt" json:"updatedAt"`
}

// normalize 把缺失字段补齐为 mongoose 默认值：空数组输出 [] 而非 null，空 Map 输出 {}。
func (d *FollowsEpisodeDoc) normalize() {
	if d.Category == nil {
		d.Category = []string{}
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if d.PlatformLinks == nil {
		d.PlatformLinks = map[string]string{}
	}
	if d.AllowedEditors == nil {
		d.AllowedEditors = []primitive.ObjectID{}
	}
	if d.CustomAuthors == nil {
		d.CustomAuthors = []primitive.ObjectID{}
	}
}

// FollowsFolderDoc 是追番/收藏列表 populate folderId 时输出的收藏夹文档视图，
// 字段逐项对齐 backend/models/Folder.js。
type FollowsFolderDoc struct {
	ID          primitive.ObjectID `bson:"_id" json:"_id"`
	UserID      primitive.ObjectID `bson:"userId" json:"userId"`
	Name        string             `bson:"name" json:"name"`
	Type        string             `bson:"type" json:"type"`
	Description string             `bson:"description" json:"description"`
	SortOrder   int                `bson:"sortOrder" json:"sortOrder"`
	ShareToken  *string            `bson:"shareToken" json:"shareToken"`
	CreatedAt   time.Time          `bson:"createdAt" json:"createdAt"`
}

// FollowEpisodeByID 按 ID 查完整剧集文档（对齐 Episode.findById）。
//   - id 为空 → ErrNotFound（mongoose findById(undefined) 返回 null → 404）
//   - id 非法 hex → 非 ErrNotFound 错误（mongoose CastError → 500）
//   - 不存在 → ErrNotFound
func (r *EpisodeRepo) FollowEpisodeByID(ctx context.Context, id string) (*FollowsEpisodeDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if id == "" {
		return nil, ErrNotFound
	}
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, err
	}
	doc := &FollowsEpisodeDoc{}
	err = r.coll.FindOne(ctx, bson.M{"_id": oid}).Decode(doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	doc.normalize()
	return doc, nil
}

// FollowEpisodesByIDs 批量取剧集文档，返回 id → 文档 的 map。
// 空 ids 返回空 map（对齐 populate 空列表不查询）。
func (r *EpisodeRepo) FollowEpisodesByIDs(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]FollowsEpisodeDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	result := map[primitive.ObjectID]FollowsEpisodeDoc{}
	if len(ids) == 0 {
		return result, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var docs []FollowsEpisodeDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for i := range docs {
		docs[i].normalize()
		result[docs[i].ID] = docs[i]
	}
	return result, nil
}

// FollowLastWatchedMap 批量取用户对若干剧集的 lastWatched（对齐 History.find
// {userId, episodeId:{$in}}），返回 episodeId → lastWatched 的 map。
func (r *HistoryRepo) FollowLastWatchedMap(ctx context.Context, userID any, episodeIDs []primitive.ObjectID) (map[primitive.ObjectID]time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	result := map[primitive.ObjectID]time.Time{}
	if len(episodeIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EpisodeID   primitive.ObjectID `bson:"episodeId"`
		LastWatched time.Time          `bson:"lastWatched"`
	}
	cur, err := r.coll.Find(ctx, bson.M{"userId": userID, "episodeId": bson.M{"$in": episodeIDs}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.EpisodeID] = row.LastWatched
	}
	return result, nil
}

// FollowFolderByIDAndUser 查用户拥有的收藏夹（对齐 Folder.findOne({_id, userId}),
// favorites.js add 的归属校验）；不存在返回 ErrNotFound。
func (r *FolderRepo) FollowFolderByIDAndUser(ctx context.Context, id, userID any) (*FollowsFolderDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	doc := &FollowsFolderDoc{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id, "userId": userID}).Decode(doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return doc, err
}

// FollowFoldersByIDs 批量取收藏夹文档，返回 id → 文档 的 map（populate folderId 用）。
func (r *FolderRepo) FollowFoldersByIDs(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]FollowsFolderDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	result := map[primitive.ObjectID]FollowsFolderDoc{}
	if len(ids) == 0 {
		return result, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var docs []FollowsFolderDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		result[d.ID] = d
	}
	return result, nil
}

// FollowFavoriteFolderIDs 取用户全部 type=favorite 且 name!='__unclassified__' 的
// 收藏夹 ID（对齐 favorites.js GET /counts 的 Folder.find 投影 _id）。
func (r *FolderRepo) FollowFavoriteFolderIDs(ctx context.Context, userID any) ([]primitive.ObjectID, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx,
		bson.M{"userId": userID, "type": "favorite", "name": bson.M{"$ne": "__unclassified__"}},
		options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var docs []FollowsFolderDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	return ids, nil
}
