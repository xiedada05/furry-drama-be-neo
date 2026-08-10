package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 /api/site-content 域（routes/siteContent.js）补充挂在已有
// *SiteContentRepo 类型上的查询/更新方法。方法统一以 SiteContents 前缀命名，
// 避免与 misc_repos.go 中已有的 FindByKey/Upsert 及其它域 agent 新增方法重名。

// SiteContentsCreate 插入站点内容；未显式设置时补 _id/updatedAt/__v 默认值
// （对齐 mongoose 自动 _id、default: Date.now 与 __v:0）。
func (r *SiteContentRepo) SiteContentsCreate(ctx context.Context, sc *model.SiteContent) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if sc.ID.IsZero() {
		sc.ID = primitive.NewObjectID()
	}
	if sc.UpdatedAt.IsZero() {
		sc.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	}
	_, err := r.coll.InsertOne(ctx, sc)
	return err
}

// SiteContentsFindAll 查询全部站点内容（对齐 SiteContent.find({})）。
func (r *SiteContentRepo) SiteContentsFindAll(ctx context.Context) ([]model.SiteContent, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.SiteContent
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// SiteContentsFindOneAndUpdate 按 key 更新并返回新文档（对齐
// SiteContent.findOneAndUpdate({key}, {title, content, updatedAt},
// {new:true, upsert:true, runValidators:true})）。update 为已包裹 $set 的对象。
func (r *SiteContentRepo) SiteContentsFindOneAndUpdate(ctx context.Context, key string, update bson.M) (*model.SiteContent, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	sc := &model.SiteContent{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"key": key}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetUpsert(true)).Decode(sc)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	return sc, err
}
