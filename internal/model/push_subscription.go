package model

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// PushSubscription 对应浏览器 Web Push 订阅文档（models/PushSubscription.js，
// collection pushsubscriptions，userId+endpoint 唯一索引）。
type PushSubscription struct {
	ID        primitive.ObjectID   `bson:"_id,omitempty" json:"_id"`
	UserID    primitive.ObjectID   `bson:"userId" json:"userId"`
	Endpoint  string               `bson:"endpoint" json:"endpoint"`
	Keys      PushSubscriptionKeys `bson:"keys" json:"keys"`
	UserAgent string               `bson:"userAgent" json:"userAgent"`
	CreatedAt time.Time            `bson:"createdAt" json:"createdAt"`
}

// PushSubscriptionKeys 是推送订阅的 VAPID 密钥子文档（对齐 schema keys.p256dh/auth）。
type PushSubscriptionKeys struct {
	P256DH string `bson:"p256dh" json:"p256dh"`
	Auth   string `bson:"auth" json:"auth"`
}
