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

// FriendLinkRepo 友链仓储（models/FriendLink.js → friendlinks 集合）。
type FriendLinkRepo struct{ coll *mongo.Collection }

// NewFriendLinkRepo 构造友链仓储。
func NewFriendLinkRepo(coll *mongo.Collection) *FriendLinkRepo { return &FriendLinkRepo{coll: coll} }

// FindActive 查询对外展示的友链（isActive 且 approved 或 status 缺失），
// 按 order/createdAt 升序（对齐 GET / 的 find().sort({order:1, createdAt:1})）。
func (r *FriendLinkRepo) FindActive(ctx context.Context) ([]model.FriendLink, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{
		"isActive": true,
		"$or": bson.A{
			bson.M{"status": "approved"},
			bson.M{"status": bson.M{"$exists": false}},
		},
	}, options.Find().SetSort(bson.D{{Key: "order", Value: 1}, {Key: "createdAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.FriendLink
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByApplicant 按申请者查友链，createdAt 倒序（对齐 GET /my-applications）。
func (r *FriendLinkRepo) FindByApplicant(ctx context.Context, applicantID any) ([]model.FriendLink, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"applicantId": ToObjectID(applicantID)},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.FriendLink
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindAll 查询全部友链，按 order/createdAt 升序（对齐 GET /all）。
func (r *FriendLinkRepo) FindAll(ctx context.Context) ([]model.FriendLink, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "order", Value: 1}, {Key: "createdAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.FriendLink
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Create 插入友链；未显式设置时补 _id/createdAt/updatedAt。
func (r *FriendLinkRepo) Create(ctx context.Context, l *model.FriendLink) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if l.ID.IsZero() {
		l.ID = primitive.NewObjectID()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if l.CreatedAt.IsZero() {
		l.CreatedAt = now
	}
	if l.UpdatedAt.IsZero() {
		l.UpdatedAt = now
	}
	_, err := r.coll.InsertOne(ctx, l)
	return err
}

// FindOneAndUpdate 按 ID 更新并返回新文档（对齐 findByIdAndUpdate(id, update, {new:true})）。
func (r *FriendLinkRepo) FindOneAndUpdate(ctx context.Context, id any, update bson.M) (*model.FriendLink, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	l := &model.FriendLink{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return l, err
}

// FindOneAndDelete 按 ID 删除并返回被删文档（对齐 FriendLink.findByIdAndDelete）。
func (r *FriendLinkRepo) FindOneAndDelete(ctx context.Context, id any) (*model.FriendLink, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	l := &model.FriendLink{}
	err := r.coll.FindOneAndDelete(ctx, bson.M{"_id": ToObjectID(id)}).Decode(l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return l, err
}
