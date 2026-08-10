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

// 本文件定义 PushSubscriptionRepo（collection pushsubscriptions）。
// routes/notifications.js 的 push/subscribe 与 push/unsubscribe 使用；
// repos.go 未注册该 repo（推送域后续接入），由通知域 handler 通过
// NewNotifications 的 db 依赖单独构造。

// PushSubscriptionRepo 推送订阅仓储。
type PushSubscriptionRepo struct{ coll *mongo.Collection }

// NewPushSubscriptionRepo 构造推送订阅仓储。
func NewPushSubscriptionRepo(coll *mongo.Collection) *PushSubscriptionRepo {
	return &PushSubscriptionRepo{coll: coll}
}

// PushSubUpsertByEndpoint 按 userId+endpoint upsert 推送订阅（对齐
// routes/notifications.js POST /push/subscribe 的 findOneAndUpdate upsert:true
// 普通对象更新 → $set 语义）。未设置时补 createdAt 默认值（对齐 mongoose default）。
func (r *PushSubscriptionRepo) PushSubUpsertByEndpoint(ctx context.Context,
	userID primitive.ObjectID, endpoint string, keys model.PushSubscriptionKeys, userAgent string) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"userId": userID, "endpoint": endpoint},
		bson.M{
			"$set": bson.M{
				"userId":    userID,
				"endpoint":  endpoint,
				"keys":      keys,
				"userAgent": userAgent,
			},
			"$setOnInsert": bson.M{"createdAt": time.Now()},
		},
		options.Update().SetUpsert(true))
	return err
}

// PushSubDeleteByEndpoint 删除用户指定 endpoint 的推送订阅（对齐 POST
// /push/unsubscribe 的 deleteOne）。不存在也返回成功（幂等语义）。
func (r *PushSubscriptionRepo) PushSubDeleteByEndpoint(ctx context.Context,
	userID primitive.ObjectID, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"userId": userID, "endpoint": endpoint})
	return err
}
