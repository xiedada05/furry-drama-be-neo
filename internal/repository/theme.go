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
	if t.Icons == nil {
		t.Icons = map[string]string{}
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

// CountPending 统计待审核主题数（仪表盘待办徽章用）。
func (r *ThemeRepo) CountPending(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"status": model.ThemeStatusPending})
}

// CountURLReferences 统计引用某资源 URL 的其他主题数（排除 excludeID 自身）。
// 壁纸（wallpaperUrl/wallpaperThumb）与图标（icons.<key>）任一字段命中即计入。
// 删除主题时用于判断文件是否已成为孤儿（无引用才可安全删除磁盘文件）。
func (r *ThemeRepo) CountURLReferences(ctx context.Context, url string, excludeID primitive.ObjectID) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{
		"_id": bson.M{"$ne": excludeID},
		"$or": []bson.M{
			{"wallpaperUrl": url},
			{"wallpaperThumb": url},
			{"icons": bson.M{"$in": []string{url}}}, // icons 是 map，值命中即匹配
		},
	})
}


