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

// IconRepo 图标仓储（icons 集合）。
type IconRepo struct{ coll *mongo.Collection }

// NewIconRepo 构造图标仓储。
func NewIconRepo(coll *mongo.Collection) *IconRepo { return &IconRepo{coll: coll} }

// FindEnabled 查询全部启用图标，按 category 升序/createdAt 倒序。
func (r *IconRepo) FindEnabled(ctx context.Context) ([]model.Icon, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"enabled": true},
		options.Find().SetSort(bson.D{{Key: "category", Value: 1}, {Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Icon
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindAll 查询全部图标（管理后台，含禁用）。
func (r *IconRepo) FindAll(ctx context.Context) ([]model.Icon, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "category", Value: 1}, {Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Icon
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByID 按 ID 查询单个图标。
func (r *IconRepo) FindByID(ctx context.Context, id primitive.ObjectID) (*model.Icon, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	i := &model.Icon{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(i)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return i, err
}

// Create 插入图标；补 _id/createdAt/updatedAt。
func (r *IconRepo) Create(ctx context.Context, i *model.Icon) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if i.ID.IsZero() {
		i.ID = primitive.NewObjectID()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if i.CreatedAt.IsZero() {
		i.CreatedAt = now
	}
	if i.UpdatedAt.IsZero() {
		i.UpdatedAt = now
	}
	if i.Mappings == nil {
		i.Mappings = []string{}
	}
	_, err := r.coll.InsertOne(ctx, i)
	return err
}

// FindOneAndUpdate 按 ID 更新并返回新文档。
func (r *IconRepo) FindOneAndUpdate(ctx context.Context, id primitive.ObjectID, update bson.M) (*model.Icon, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	i := &model.Icon{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": id}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(i)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return i, err
}

// FindOneAndDelete 按 ID 删除并返回被删文档。
func (r *IconRepo) FindOneAndDelete(ctx context.Context, id primitive.ObjectID) (*model.Icon, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	i := &model.Icon{}
	err := r.coll.FindOneAndDelete(ctx, bson.M{"_id": id}).Decode(i)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return i, err
}

// UpdateManyByIDs 批量按 ID 更新（图标批量上传后补充分类等）。
func (r *IconRepo) UpdateManyByIDs(ctx context.Context, ids []primitive.ObjectID, update bson.M) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if len(ids) == 0 {
		return nil
	}
	_, err := r.coll.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}}, update)
	return err
}
