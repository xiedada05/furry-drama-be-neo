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

// 本文件为 /api/creator-profile 域（routes/creatorProfiles.js）补充挂在
// 已有 *CreatorProfileRepo 类型上的查询/更新方法。方法统一以 CreatorProfiles
// 前缀命名，避免与 misc_repos.go 中已有的 Create/FindByCreator/DeleteByCreator
// 及其它域 agent 新增方法重名。

// CreatorProfilesFindByID 按 ID 查找创作者资料（对齐 CreatorProfile.findById）；
// 不存在返回 ErrNotFound。
func (r *CreatorProfileRepo) CreatorProfilesFindByID(ctx context.Context, id any) (*model.CreatorProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	p := &model.CreatorProfile{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return p, err
}

// CreatorProfilesUpsertPending 把创作者提交的修改暂存到 pendingChanges 并置
// reviewStatus=pending（对齐 creatorProfiles.js PUT /my-profile 的
// findOneAndUpdate({creatorId}, {$set:{pendingChanges, reviewStatus:'pending',
// reviewNote:'', updatedAt}}, {new:true, upsert:true, runValidators:true})）。
// 返回更新后的文档；upsert 新建文档仅含 $set 字段 + 过滤条件中的 creatorId，
// displayName 兜底由调用方负责（对齐 Express 的 !profile.displayName 分支）。
func (r *CreatorProfileRepo) CreatorProfilesUpsertPending(ctx context.Context, creatorID primitive.ObjectID, pendingChanges primitive.M, updatedAt time.Time) (*model.CreatorProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	p := &model.CreatorProfile{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"creatorId": creatorID},
		bson.M{"$set": bson.M{
			"pendingChanges": pendingChanges,
			"reviewStatus":   "pending",
			"reviewNote":     "",
			"updatedAt":      updatedAt,
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetUpsert(true)).Decode(p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return p, err
}

// CreatorProfilesSave 整文档覆盖保存（对齐 mongoose profile.save()，__v 自增）。
func (r *CreatorProfileRepo) CreatorProfilesSave(ctx context.Context, p *model.CreatorProfile) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	p.VersionKey++
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": p.ID}, p)
	return err
}
