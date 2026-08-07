package repository

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// SessionRepo 用户会话仓储（usersessions）。
type SessionRepo struct {
	coll *mongo.Collection
}

// NewSessionRepo 构造会话仓储。
func NewSessionRepo(coll *mongo.Collection) *SessionRepo {
	return &SessionRepo{coll: coll}
}

func (r *SessionRepo) newCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, contextTimeout)
}

// Create 新建会话（insert refreshTokenHash 等）。
func (r *SessionRepo) Create(ctx context.Context, s *model.UserSession) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.InsertOne(ctx, s)
	return err
}

// FindByRefreshTokenHash 按 refresh 哈希查找（含已吊销的）。
func (r *SessionRepo) FindByRefreshTokenHash(ctx context.Context, hash string) (*model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	s := &model.UserSession{}
	err := r.coll.FindOne(ctx, bson.M{"refreshTokenHash": hash}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// FindAndDeactivateRefresh 原子"取用并作废"：把匹配的 active 会话置为 inactive。
// 返回更新后的文档（new:true）；无匹配返回 ErrNotFound。
// 这是 refresh 轮换 + 重用检测的核心（对齐 authFactory.js:97-101）。
func (r *SessionRepo) FindAndDeactivateRefresh(ctx context.Context, hash string) (*model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	s := &model.UserSession{}
	now := time.Now()
	err := r.coll.FindOneAndUpdate(
		ctx,
		bson.M{"refreshTokenHash": hash, "isActive": true},
		bson.M{"$set": bson.M{"isActive": false, "logoutAt": now}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// FindByTokenHash 按旧 access 哈希查找（tokenHash 兼容字段）。
func (r *SessionRepo) FindByTokenHash(ctx context.Context, hash string) (*model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	s := &model.UserSession{}
	err := r.coll.FindOne(ctx, bson.M{"tokenHash": hash}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// FindActiveByUser 返回用户全部 active 会话（登录设备检测用）。
func (r *SessionRepo) FindActiveByUser(ctx context.Context, userID any) ([]model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"userId": userID, "isActive": true})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.UserSession
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// UpdateLastActiveByRefresh 更新 lastActiveAt（鉴权中间件 fire-and-forget）。
func (r *SessionRepo) UpdateLastActiveByRefresh(ctx context.Context, userID any, t time.Time) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"refreshTokenHash": bson.M{"$exists": true}, "userId": userID, "isActive": true},
		bson.M{"$set": bson.M{"lastActiveAt": t}})
	return err
}

// UpdateLastActiveByTokenHash 更新 lastActiveAt（心跳用）。
func (r *SessionRepo) UpdateLastActiveByTokenHash(ctx context.Context, hash string, t time.Time) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"tokenHash": hash, "isActive": true},
		bson.M{"$set": bson.M{"lastActiveAt": t}})
	return err
}

// DeactivateAllByUser 吊销用户全部 active 会话（对齐 updateMany 吊销逻辑）。
func (r *SessionRepo) DeactivateAllByUser(ctx context.Context, userID any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"userId": userID, "isActive": true},
		bson.M{"$set": bson.M{"isActive": false, "logoutAt": time.Now()}})
	return err
}

// DeactivateByTokenHash 按旧 access 哈希置废（登出用）。
func (r *SessionRepo) DeactivateByTokenHash(ctx context.Context, hash string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"tokenHash": hash, "isActive": true},
		bson.M{"$set": bson.M{"isActive": false, "logoutAt": time.Now()}})
	return err
}

// DeactivateByID 按 ID 置废（userSessions 管理用，不能下线当前设备由 handler 判断）。
func (r *SessionRepo) DeactivateByID(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": ToObjectID(id), "isActive": true},
		bson.M{"$set": bson.M{"isActive": false, "logoutAt": time.Now()}})
	return err
}

// DeleteByUser 删除用户全部会话（注销清理）。
func (r *SessionRepo) DeleteByUser(ctx context.Context, userID any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"userId": userID})
	return err
}

// FindByID 按 ID 查找会话（userSessions 管理用）。
func (r *SessionRepo) FindByID(ctx context.Context, id any) (*model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	s := &model.UserSession{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// UpdateDeviceName 更新会话的设备名称（本人校验由 handler 负责）。
func (r *SessionRepo) UpdateDeviceName(ctx context.Context, id, userID any, name string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id), "userId": userID},
		bson.M{"$set": bson.M{"deviceInfo.deviceName": name}})
	return err
}

// UpsertByTokenHash 按 tokenHash upsert 会话（对齐 userSessions.js POST /create 的
// findOneAndUpdate + upsert + $setOnInsert loginAt）。返回更新后文档。
func (r *SessionRepo) UpsertByTokenHash(ctx context.Context, tokenHash string, userID any, deviceInfo model.DeviceInfo, ip string, now time.Time) (*model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	s := &model.UserSession{}
	err := r.coll.FindOneAndUpdate(
		ctx,
		bson.M{"tokenHash": tokenHash},
		bson.M{
			"$set":         bson.M{"userId": userID, "deviceInfo": deviceInfo, "ip": ip, "isActive": true, "lastActiveAt": now},
			"$setOnInsert": bson.M{"loginAt": now},
		},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(s)
	return s, err
}

// DeactivateAllOtherByTokenHash 吊销用户除指定 tokenHash 外的全部 active 会话。
func (r *SessionRepo) DeactivateAllOtherByTokenHash(ctx context.Context, userID any, excludeHash string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateMany(ctx,
		bson.M{"userId": userID, "isActive": true, "tokenHash": bson.M{"$ne": excludeHash}},
		bson.M{"$set": bson.M{"isActive": false, "logoutAt": time.Now()}})
	return err
}

// FindAll 返回全部会话（按 loginAt 倒序，最多 limit 条；管理端 GET /all 用）。
func (r *SessionRepo) FindAll(ctx context.Context, limit int) ([]model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	if limit <= 0 {
		limit = 200
	}
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "loginAt", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.UserSession
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByUser 返回某用户最近登录的会话，按 loginAt 倒序，最多 limit 条。
// 对齐 routes/userSessions.js GET /my 的 find({userId}).sort({loginAt:-1}).limit(20)。
func (r *SessionRepo) FindByUser(ctx context.Context, userID any, limit int) ([]model.UserSession, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	if limit <= 0 {
		limit = 20
	}
	cur, err := r.coll.Find(ctx, bson.M{"userId": userID},
		options.Find().SetSort(bson.D{{Key: "loginAt", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.UserSession
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// MarkInactiveOlderThan 把 lastActiveAt 早于 cutoff 的 active 会话批量置为 inactive+logoutAt。
// 对齐 src/index.js 会话清理 cron 的 updateMany({isActive:true, lastActiveAt:{$lt:cutoff}})。
// 返回被修改的文档数。
func (r *SessionRepo) MarkInactiveOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	res, err := r.coll.UpdateMany(ctx,
		bson.M{"isActive": true, "lastActiveAt": bson.M{"$lt": cutoff}},
		bson.M{"$set": bson.M{"isActive": false, "logoutAt": time.Now()}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}
