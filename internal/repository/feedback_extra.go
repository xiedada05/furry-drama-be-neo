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

// 本文件为 /api/feedback 域（routes/feedback.js）补充挂在已有 *FeedbackRepo
// 类型上的查询/更新方法。方法统一以 Feedback 前缀命名，避免与 misc_repos.go
// 中已有的 DeleteByUser 及其它域 agent 新增方法重名。

// FeedbackCreate 插入反馈；未显式设置时补 _id/createdAt/__v 默认值
// （对齐 mongoose 自动 _id、default: Date.now 与 __v:0）。
func (r *FeedbackRepo) FeedbackCreate(ctx context.Context, f *model.Feedback) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if f.ID.IsZero() {
		f.ID = primitive.NewObjectID()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	_, err := r.coll.InsertOne(ctx, f)
	return err
}

// FeedbackCount 按过滤条件统计反馈数（对齐 countDocuments(filter)）。
func (r *FeedbackRepo) FeedbackCount(ctx context.Context, filter bson.M) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, filter)
}

// FeedbackFindPaged 分页查询反馈，按 sort 排序后 skip/limit
// （对齐 Feedback.find(filter).sort(sort).skip().limit()）。
func (r *FeedbackRepo) FeedbackFindPaged(ctx context.Context, filter bson.M, sort bson.D, page, limit int) ([]model.Feedback, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	opts := options.Find()
	if sort != nil {
		opts.SetSort(sort)
	}
	// skip 恒设置（含负值）：负 skip 由 mongo 拒绝 → 500（对齐 Express 在
	// page<1 时 skip((page-1)*limit) 的行为）。
	opts.SetSkip(int64((page - 1) * limit))
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.Feedback
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FeedbackFindOneAndUpdate 按 ID 更新并返回新文档（对齐
// Feedback.findByIdAndUpdate(id, update, {new:true})）；不存在返回 ErrNotFound。
// update 为普通字段对象（无 $ 运算符），由调用方按 mongoose 语义包裹 $set。
func (r *FeedbackRepo) FeedbackFindOneAndUpdate(ctx context.Context, id any, update bson.M) (*model.Feedback, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	f := &model.Feedback{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return f, err
}
