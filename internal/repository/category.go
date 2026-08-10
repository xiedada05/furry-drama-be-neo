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

// CategoryRepo 分类仓储（models/Category.js → categories 集合）。
type CategoryRepo struct{ coll *mongo.Collection }

// NewCategoryRepo 构造分类仓储。
func NewCategoryRepo(coll *mongo.Collection) *CategoryRepo { return &CategoryRepo{coll: coll} }

// FindAllSorted 查询全部分类，按 order 升序、createdAt 升序
// （对齐 categories.js GET / 的 find({}).sort({order:1, createdAt:1})）。
func (r *CategoryRepo) FindAllSorted(ctx context.Context) ([]model.Category, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "order", Value: 1}, {Key: "createdAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Category
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByName 按 name 精确查分类（唯一名检查用，对齐 Category.findOne({name})）；
// 不存在返回 ErrNotFound。
func (r *CategoryRepo) FindByName(ctx context.Context, name string) (*model.Category, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	c := &model.Category{}
	err := r.coll.FindOne(ctx, bson.M{"name": name}).Decode(c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return c, err
}

// FindByID 按 ID 查分类（对齐 Category.findById）；不存在返回 ErrNotFound。
func (r *CategoryRepo) FindByID(ctx context.Context, id any) (*model.Category, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	c := &model.Category{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return c, err
}

// Create 插入分类；未显式设置时补 _id/createdAt（对齐 mongoose 自动 _id 与
// default: Date.now）。唯一名冲突（E11000）原样返回，由 handler 对齐 Express
// catch-all 的 500 行为。
func (r *CategoryRepo) Create(ctx context.Context, c *model.Category) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if c.ID.IsZero() {
		c.ID = primitive.NewObjectID()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	_, err := r.coll.InsertOne(ctx, c)
	return err
}

// Save 整文档覆盖保存（对齐 category.save()；__v 自增对齐 mongoose versionKey）。
func (r *CategoryRepo) Save(ctx context.Context, c *model.Category) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	c.VersionKey++
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": c.ID}, c)
	return err
}

// DeleteByID 按 ID 删除分类（对齐 Category.findByIdAndDelete）。
func (r *CategoryRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": ToObjectID(id)})
	return err
}
