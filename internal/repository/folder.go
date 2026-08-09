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

// FolderRepo 收藏夹仓储（models/Folder.js / folders.js 全部查询）。
type FolderRepo struct{ coll *mongo.Collection }

// NewFolderRepo 构造收藏夹仓储。
func NewFolderRepo(coll *mongo.Collection) *FolderRepo { return &FolderRepo{coll: coll} }

// Create 插入收藏夹；补 createdAt 默认值并回填生成的 _id（对齐 mongoose Folder.create）。
func (r *FolderRepo) Create(ctx context.Context, f *model.Folder) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now()
	}
	res, err := r.coll.InsertOne(ctx, f)
	if err != nil {
		return err
	}
	if id, ok := res.InsertedID.(primitive.ObjectID); ok {
		f.ID = id
	}
	return nil
}

// Save 整文档覆盖保存（对齐 user.save() 的 ReplaceOne 语义，folder.save() 同构）。
// 仅用于已通过 Find* 取出的完整文档。
func (r *FolderRepo) Save(ctx context.Context, f *model.Folder) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": f.ID}, f)
	return err
}

// FindUserFolders 查询用户收藏夹列表（folders.js GET /）。
// 对齐 find({ userId, name: { $ne: '__unclassified__' }, [type] })
// .sort({ sortOrder: 1, createdAt: 1 })；type 为空时不加 type 过滤。
// 返回非 nil 空切片（空列表 JSON 输出 []）。
func (r *FolderRepo) FindUserFolders(ctx context.Context, userID any, ftype string) ([]model.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	filter := bson.M{"userId": userID, "name": bson.M{"$ne": "__unclassified__"}}
	if ftype != "" {
		filter["type"] = ftype
	}
	cur, err := r.coll.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "sortOrder", Value: 1}, {Key: "createdAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]model.Folder, 0)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindOwnedByID 按 _id+userId 查收藏夹（folders.js 各 :id 端点）；不存在返回 ErrNotFound。
// 非法 ObjectId 由调用方在 handler 层拦截（对齐 mongoose CastError → 500）。
func (r *FolderRepo) FindOwnedByID(ctx context.Context, id, userID any) (*model.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	f := &model.Folder{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id, "userId": userID}).Decode(f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return f, err
}

// FindUnclassified 查用户的「默认收藏夹」虚拟收藏夹（folders.js POST /share-unclassified）。
// 条件 type=favorite 且 name='__unclassified__'；不存在返回 ErrNotFound。
func (r *FolderRepo) FindUnclassified(ctx context.Context, userID any) (*model.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	f := &model.Folder{}
	err := r.coll.FindOne(ctx, bson.M{"userId": userID, "type": "favorite", "name": "__unclassified__"}).Decode(f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return f, err
}

// FindByShareToken 按 shareToken 查收藏夹（GET /shared/:shareToken、saved-folders POST /）。
// 不存在返回 ErrNotFound。
func (r *FolderRepo) FindByShareToken(ctx context.Context, shareToken string) (*model.Folder, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	f := &model.Folder{}
	err := r.coll.FindOne(ctx, bson.M{"shareToken": shareToken}).Decode(f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return f, err
}

// Reorder 批量更新 sortOrder（folders.js PUT /reorder 的 bulkWrite）。
// orderedIDs 为前端传入的顺序；第 i 个 id 的 sortOrder 置为 i。
// 每个 updateOne 的 filter 都限定 userId，防止越权。
func (r *FolderRepo) Reorder(ctx context.Context, userID any, orderedIDs []any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	models := make([]mongo.WriteModel, 0, len(orderedIDs))
	for i, id := range orderedIDs {
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id, "userId": userID}).
			SetUpdate(bson.M{"$set": bson.M{"sortOrder": i}}))
	}
	if len(models) == 0 {
		return nil
	}
	_, err := r.coll.BulkWrite(ctx, models)
	return err
}

// DeleteByID 物理删除收藏夹（folders.js DELETE /:id 的 folder.deleteOne()）。
func (r *FolderRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// SavedFolderRepo 收藏夹收藏项仓储（models/SavedFolder.js / savedFolders.js）。
type SavedFolderRepo struct{ coll *mongo.Collection }

// NewSavedFolderRepo 构造收藏夹收藏项仓储。
func NewSavedFolderRepo(coll *mongo.Collection) *SavedFolderRepo { return &SavedFolderRepo{coll: coll} }

// Create 插入收藏项；补 createdAt 默认值并回填 _id。唯一键（userId+shareToken）冲突
// 返回 IsDuplicateKey(err)（对齐 mongoose E11000）。
func (r *SavedFolderRepo) Create(ctx context.Context, s *model.SavedFolder) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	res, err := r.coll.InsertOne(ctx, s)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return errDuplicateKey
		}
		return err
	}
	if id, ok := res.InsertedID.(primitive.ObjectID); ok {
		s.ID = id
	}
	return nil
}

// FindByUser 查询用户收藏的他人收藏夹（savedFolders.js GET /）。
// 对齐 find({ userId }).sort({ createdAt: -1 })；返回非 nil 空切片。
func (r *SavedFolderRepo) FindByUser(ctx context.Context, userID any) ([]model.SavedFolder, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"userId": userID},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	list := make([]model.SavedFolder, 0)
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// FindByUserAndShareToken 查用户是否已收藏某 shareToken（savedFolders.js POST /）。
// 不存在返回 ErrNotFound。
func (r *SavedFolderRepo) FindByUserAndShareToken(ctx context.Context, userID any, shareToken string) (*model.SavedFolder, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	s := &model.SavedFolder{}
	err := r.coll.FindOne(ctx, bson.M{"userId": userID, "shareToken": shareToken}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// FindOwnedByID 按 _id+userId 查收藏项（savedFolders.js DELETE /:id）；不存在返回 ErrNotFound。
func (r *SavedFolderRepo) FindOwnedByID(ctx context.Context, id, userID any) (*model.SavedFolder, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	s := &model.SavedFolder{}
	err := r.coll.FindOne(ctx, bson.M{"_id": id, "userId": userID}).Decode(s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return s, err
}

// DeleteByID 物理删除收藏项（savedFolders.js DELETE /:id 的 savedFolder.deleteOne()）。
func (r *SavedFolderRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	return err
}
