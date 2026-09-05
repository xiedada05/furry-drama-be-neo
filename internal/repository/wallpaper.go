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

// WallpaperRepo 系统壁纸仓储（models/SystemWallpaper.js → systemwallpapers 集合）。
type WallpaperRepo struct{ coll *mongo.Collection }

// NewWallpaperRepo 构造系统壁纸仓储。
func NewWallpaperRepo(coll *mongo.Collection) *WallpaperRepo { return &WallpaperRepo{coll: coll} }

// FindEnabled 查询启用的系统壁纸，按 sortOrder 升序/createdAt 倒序，仅投影
// name/url/thumbnailUrl/sortOrder（对齐 GET /system 的 find+select）。
func (r *WallpaperRepo) FindEnabled(ctx context.Context) ([]model.SystemWallpaper, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"enabled": true},
		options.Find().
			SetSort(bson.D{{Key: "sortOrder", Value: 1}, {Key: "createdAt", Value: -1}}).
			SetProjection(bson.M{"name": 1, "url": 1, "thumbnailUrl": 1, "sortOrder": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.SystemWallpaper
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ExistsByURL 判断系统壁纸库是否存在引用该 URL 的记录（删除主题时防止误删
// 主题壁纸与系统壁纸共用的文件）。
func (r *WallpaperRepo) ExistsByURL(ctx context.Context, url string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	n, err := r.coll.CountDocuments(ctx, bson.M{"$or": []bson.M{{"url": url}, {"thumbnailUrl": url}}})
	return n > 0, err
}

// FindAll 查询全部系统壁纸（含禁用），按 sortOrder 升序/createdAt 倒序（对齐 GET /system/all）。
func (r *WallpaperRepo) FindAll(ctx context.Context) ([]model.SystemWallpaper, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "sortOrder", Value: 1}, {Key: "createdAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.SystemWallpaper
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Create 插入系统壁纸；未显式设置时补 _id/createdAt/updatedAt。
func (r *WallpaperRepo) Create(ctx context.Context, w *model.SystemWallpaper) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if w.ID.IsZero() {
		w.ID = primitive.NewObjectID()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	if w.UpdatedAt.IsZero() {
		w.UpdatedAt = now
	}
	_, err := r.coll.InsertOne(ctx, w)
	return err
}

// FindOneAndUpdate 按 ID 更新并返回新文档（对齐 findByIdAndUpdate(id, update, {new:true})）。
func (r *WallpaperRepo) FindOneAndUpdate(ctx context.Context, id any, update bson.M) (*model.SystemWallpaper, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	w := &model.SystemWallpaper{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(w)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return w, err
}

// FindOneAndDelete 按 ID 删除并返回被删文档（对齐 SystemWallpaper.findByIdAndDelete）。
func (r *WallpaperRepo) FindOneAndDelete(ctx context.Context, id any) (*model.SystemWallpaper, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	w := &model.SystemWallpaper{}
	err := r.coll.FindOneAndDelete(ctx, bson.M{"_id": ToObjectID(id)}).Decode(w)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return w, err
}
