package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// UsedTokenRepo 一次性令牌去重仓储（usedtokens，expiresAt 建 TTL 索引自动删除）。
type UsedTokenRepo struct {
	coll *mongo.Collection
}

// NewUsedTokenRepo 构造令牌仓储。
func NewUsedTokenRepo(coll *mongo.Collection) *UsedTokenRepo {
	return &UsedTokenRepo{coll: coll}
}

func (r *UsedTokenRepo) newCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, contextTimeout)
}

// MarkUsed 标记令牌已用：插入 {tokenHash, purpose, expiresAt: now+ttl}。
// 重复键（已标记过）静默忽略——对齐 utils/UsedToken.js markTokenUsed 的吞错语义。
func (r *UsedTokenRepo) MarkUsed(ctx context.Context, tokenHash, purpose string, ttl time.Duration) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	doc := model.UsedToken{
		TokenHash: tokenHash,
		Purpose:   purpose,
		ExpiresAt: time.Now().Add(ttl),
	}
	_, err := r.coll.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return nil
	}
	return err
}

// IsUsed 判断令牌是否已用（存在即 true）。
func (r *UsedTokenRepo) IsUsed(ctx context.Context, tokenHash string) (bool, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	err := r.coll.FindOne(ctx, bson.M{"tokenHash": tokenHash},
		options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
	if err == mongo.ErrNoDocuments {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
