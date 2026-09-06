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

// EpisodeTrashRepo 剧集回收站仓储（collection: episodetrash）。
// 被删除/审核拒绝的剧集整体移入回收站集合，从 episodes 集合移除；
// 支持恢复（移回 episodes，保留 _id 与版本记录）与彻底删除。
type EpisodeTrashRepo struct {
	coll      *mongo.Collection
	episodes  *mongo.Collection
	versions  *mongo.Collection
	singles   *mongo.Collection
	follows   *mongo.Collection
	favorites *mongo.Collection
	histories *mongo.Collection
	ratings   *mongo.Collection
	notifs    *mongo.Collection
}

// NewEpisodeTrashRepo 构造回收站仓储；episodes/versions/singles 等集合用于
// 恢复与彻底删除时的跨集合搬移/清理。
func NewEpisodeTrashRepo(db *mongo.Database, episodes, versions, singles,
	follows, favorites, histories, ratings, notifs *mongo.Collection) *EpisodeTrashRepo {
	return &EpisodeTrashRepo{
		coll:      db.Collection("episodetrash"),
		episodes:  episodes,
		versions:  versions,
		singles:   singles,
		follows:   follows,
		favorites: favorites,
		histories: histories,
		ratings:   ratings,
		notifs:    notifs,
	}
}

// MoveToTrash 把剧集移入回收站（同一事务语义：先写回收站再删原集合；
// 存在唯一 _id 约束，重复进入返回 ErrNotFound 之外错误原样上抛）。
// reason: rejected | deleted；note 为审核备注/删除说明；by 为操作人。
func (r *EpisodeTrashRepo) MoveToTrash(ctx context.Context, e *model.Episode,
	reason, note string, by *primitive.ObjectID) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	trash := model.EpisodeTrash{
		Episode:     *e,
		TrashReason: reason,
		TrashNote:   note,
		TrashBy:     by,
		TrashAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	if _, err := r.coll.InsertOne(ctx, &trash); err != nil {
		return err
	}
	_, err := r.episodes.DeleteOne(ctx, bson.M{"_id": e.ID})
	return err
}

// Count 统计回收站剧集数。
func (r *EpisodeTrashRepo) Count(ctx context.Context, filter any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, filter)
}

// FindList 分页查询回收站剧集（按进入回收站时间倒序）。
func (r *EpisodeTrashRepo) FindList(ctx context.Context, filter any, skip, limit int64) ([]model.EpisodeTrash, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	opts := options.Find().SetSort(bson.D{{Key: "trashAt", Value: -1}}).
		SetSkip(skip).SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.EpisodeTrash
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByID 按 ID 查回收站剧集。
func (r *EpisodeTrashRepo) FindByID(ctx context.Context, id primitive.ObjectID) (*model.EpisodeTrash, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	t := &model.EpisodeTrash{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return t, err
}

// Restore 把回收站剧集移回 episodes 集合（保留原 _id 与版本记录）。
func (r *EpisodeTrashRepo) Restore(ctx context.Context, id primitive.ObjectID) (*model.Episode, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	t, err := r.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := r.episodes.InsertOne(ctx, &t.Episode); err != nil {
		return nil, err
	}
	if _, err := r.coll.DeleteOne(ctx, bson.M{"_id": id}); err != nil {
		// 回写失败则回滚插入，避免两边同时存在。
		_, _ = r.episodes.DeleteOne(ctx, bson.M{"_id": id})
		return nil, err
	}
	return &t.Episode, nil
}

// Purge 彻底删除回收站剧集及其全部关联数据（单集/版本/追番/收藏/历史/评分/通知），
// 释放服务器资源。返回是否确实删除了文档。
func (r *EpisodeTrashRepo) Purge(ctx context.Context, id primitive.ObjectID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil || res.DeletedCount == 0 {
		return false, err
	}
	epID := bson.M{"episodeId": id}
	_, _ = r.singles.DeleteMany(ctx, epID)
	_, _ = r.versions.DeleteMany(ctx, epID)
	_, _ = r.follows.DeleteMany(ctx, epID)
	_, _ = r.favorites.DeleteMany(ctx, epID)
	_, _ = r.histories.DeleteMany(ctx, epID)
	_, _ = r.ratings.DeleteMany(ctx, epID)
	_, _ = r.notifs.DeleteMany(ctx, epID)
	return true, nil
}
