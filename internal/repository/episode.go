package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EpisodeBasic 是剧集基础字段（导出/列表 populate 用；完整 Episode 模型在内容域实现）。
type EpisodeBasic struct {
	ID         primitive.ObjectID `bson:"_id" json:"_id"`
	Title      string             `bson:"title" json:"title"`
	CoverImage string             `bson:"coverImage" json:"coverImage"`
	Status     string             `bson:"status" json:"status"`
}

// EpisodeRepo 剧集仓储（第一段仅基础查询；内容域补齐）。
type EpisodeRepo struct{ coll *mongo.Collection }

// NewEpisodeRepo 构造剧集仓储。
func NewEpisodeRepo(coll *mongo.Collection) *EpisodeRepo { return &EpisodeRepo{coll: coll} }

// FindBasicByID 按 ID 查基础字段。
func (r *EpisodeRepo) FindBasicByID(ctx context.Context, id any) (*EpisodeBasic, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	e := &EpisodeBasic{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(bson.M{"title": 1, "coverImage": 1, "status": 1})).Decode(e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return e, err
}
