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

// ThemeRepo 主题仓储（themes 集合）。
type ThemeRepo struct{ coll *mongo.Collection }

// NewThemeRepo 构造主题仓储。
func NewThemeRepo(coll *mongo.Collection) *ThemeRepo { return &ThemeRepo{coll: coll} }

// FindDefault 查询站点默认主题（isDefault=true 且启用）。
func (r *ThemeRepo) FindDefault(ctx context.Context) (*model.Theme, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	t := &model.Theme{}
	err := r.coll.FindOne(ctx, bson.M{"isDefault": true, "enabled": true}).Decode(t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return t, err
}

// FindSystemEnabled 查询所有启用的系统主题（用户可选列表），按 createdAt 倒序。
func (r *ThemeRepo) FindSystemEnabled(ctx context.Context) ([]model.Theme, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"isSystem": true, "enabled": true},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Theme
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByOwner 查询某用户的全部个人主题，按 createdAt 倒序。
func (r *ThemeRepo) FindByOwner(ctx context.Context, owner primitive.ObjectID) ([]model.Theme, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"ownerId": owner},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Theme
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindAllAdmin 查询全部主题（管理后台），按 createdAt 倒序。
func (r *ThemeRepo) FindAllAdmin(ctx context.Context) ([]model.Theme, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Theme
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByID 按 ID 查询单个主题。
func (r *ThemeRepo) FindByID(ctx context.Context, id primitive.ObjectID) (*model.Theme, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	t := &model.Theme{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return t, err
}

// Create 插入主题；补 _id/createdAt/updatedAt。
func (r *ThemeRepo) Create(ctx context.Context, t *model.Theme) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if t.ID.IsZero() {
		t.ID = primitive.NewObjectID()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	if t.Variables == nil {
		t.Variables = map[string]string{}
	}
	_, err := r.coll.InsertOne(ctx, t)
	return err
}

// FindOneAndUpdate 按 ID 更新并返回新文档。
func (r *ThemeRepo) FindOneAndUpdate(ctx context.Context, id primitive.ObjectID, update bson.M) (*model.Theme, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	t := &model.Theme{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": id}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return t, err
}

// FindOneAndDelete 按 ID 删除并返回被删文档。
func (r *ThemeRepo) FindOneAndDelete(ctx context.Context, id primitive.ObjectID) (*model.Theme, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	t := &model.Theme{}
	err := r.coll.FindOneAndDelete(ctx, bson.M{"_id": id}).Decode(t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return t, err
}

// ClearDefaultExcept 取消除 excludeID 外所有主题的默认标记（事务外逐条更新，幂等）。
func (r *ThemeRepo) ClearDefaultExcept(ctx context.Context, excludeID primitive.ObjectID) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"isDefault": true, "_id": bson.M{"$ne": excludeID}},
		bson.M{"$set": bson.M{"isDefault": false, "updatedAt": time.Now().UTC().Truncate(time.Millisecond)}})
	return err
}


