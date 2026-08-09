package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为收藏夹域（folders.js）跨域访问 Follow/Favorite 集合的查询方法。
// 方法统一以 Folder 前缀命名，避免与其它域新增方法冲突；挂到已有的
// FollowRepo / FavoriteRepo 类型上，不修改其它文件。

// folderClearByFolder 通用：把某收藏夹下所有条目的 folderId 置空。
// 对齐 folders.js DELETE /:id 的 Model.updateMany({ folderId }, { $set: { folderId: null } })。
func folderClearByFolder(ctx context.Context, coll *mongo.Collection, folderID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := coll.UpdateMany(ctx, bson.M{"folderId": folderID}, bson.M{"$set": bson.M{"folderId": nil}})
	return err
}

// folderFindByUserEpisode 通用：按 userId+episodeId 查条目。
// 对齐 folders.js POST /:id/items 的 Model.findOne({ userId, episodeId })；不存在返回 ErrNotFound。
func folderFindByUserEpisode(ctx context.Context, coll *mongo.Collection, userID, episodeID any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	err := coll.FindOne(ctx, bson.M{"userId": userID, "episodeId": episodeID}).Decode(out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	return err
}

// folderRemoveItem 通用：把某用户某剧集的 folderId 从指定收藏夹移除。
// 对齐 folders.js DELETE /:id/items/:episodeId 的
// Model.updateOne({ userId, episodeId, folderId }, { $set: { folderId: null } })。
func folderRemoveItem(ctx context.Context, coll *mongo.Collection, userID, episodeID, folderID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := coll.UpdateOne(ctx, bson.M{"userId": userID, "episodeId": episodeID, "folderId": folderID},
		bson.M{"$set": bson.M{"folderId": nil}})
	return err
}

// folderSave 通用：整文档覆盖保存（对齐 item.save()）。
func folderSave(ctx context.Context, coll *mongo.Collection, id any, doc any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := coll.ReplaceOne(ctx, bson.M{"_id": id}, doc)
	return err
}

// folderFindShared 通用：查分享收藏夹条目（folders.js GET /shared/:shareToken）。
// 对齐 Model.find(filter).sort({ createdAt: -1 })；返回非 nil 空切片。
func folderFindShared(ctx context.Context, coll *mongo.Collection, filter bson.M, decode func(bson.Raw) error) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		if err := decode(cur.Current); err != nil {
			return err
		}
	}
	return cur.Err()
}

// FolderClearByFolderID 清空某收藏夹下全部追番条目的 folderId（folders.js DELETE /:id）。
func (r *FollowRepo) FolderClearByFolderID(ctx context.Context, folderID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return folderClearByFolder(ctx, r.coll, folderID)
}

// FolderFindByUserEpisode 查用户的某条追番（folders.js POST /:id/items）；不存在返回 ErrNotFound。
func (r *FollowRepo) FolderFindByUserEpisode(ctx context.Context, userID, episodeID any) (*model.Follow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	item := &model.Follow{}
	if err := folderFindByUserEpisode(ctx, r.coll, userID, episodeID, item); err != nil {
		return nil, err
	}
	return item, nil
}

// FolderRemoveItem 把某追番移出收藏夹（folders.js DELETE /:id/items/:episodeId）。
func (r *FollowRepo) FolderRemoveItem(ctx context.Context, userID, episodeID, folderID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return folderRemoveItem(ctx, r.coll, userID, episodeID, folderID)
}

// FolderSave 保存追番条目（对齐 item.save()；folders.js POST /:id/items）。
func (r *FollowRepo) FolderSave(ctx context.Context, item *model.Follow) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return folderSave(ctx, r.coll, item.ID, item)
}

// FolderFindShared 查分享收藏夹的追番条目（folders.js GET /shared/:shareToken）。
// 按 createdAt 倒序返回；items 为空返回非 nil 空切片。
func (r *FollowRepo) FolderFindShared(ctx context.Context, filter bson.M) ([]model.Follow, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	items := make([]model.Follow, 0)
	if err := folderFindShared(ctx, r.coll, filter, func(raw bson.Raw) error {
		var it model.Follow
		if err := bson.Unmarshal(raw, &it); err != nil {
			return err
		}
		items = append(items, it)
		return nil
	}); err != nil {
		return nil, err
	}
	return items, nil
}

// FolderClearByFolderID 清空某收藏夹下全部收藏条目的 folderId（folders.js DELETE /:id）。
func (r *FavoriteRepo) FolderClearByFolderID(ctx context.Context, folderID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return folderClearByFolder(ctx, r.coll, folderID)
}

// FolderFindByUserEpisode 查用户的某条收藏（folders.js POST /:id/items）；不存在返回 ErrNotFound。
func (r *FavoriteRepo) FolderFindByUserEpisode(ctx context.Context, userID, episodeID any) (*model.Favorite, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	item := &model.Favorite{}
	if err := folderFindByUserEpisode(ctx, r.coll, userID, episodeID, item); err != nil {
		return nil, err
	}
	return item, nil
}

// FolderRemoveItem 把某收藏移出收藏夹（folders.js DELETE /:id/items/:episodeId）。
func (r *FavoriteRepo) FolderRemoveItem(ctx context.Context, userID, episodeID, folderID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return folderRemoveItem(ctx, r.coll, userID, episodeID, folderID)
}

// FolderSave 保存收藏条目（对齐 item.save()；folders.js POST /:id/items）。
func (r *FavoriteRepo) FolderSave(ctx context.Context, item *model.Favorite) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return folderSave(ctx, r.coll, item.ID, item)
}

// FolderFindShared 查分享收藏夹的收藏条目（folders.js GET /shared/:shareToken）。
// 按 createdAt 倒序返回；items 为空返回非 nil 空切片。
func (r *FavoriteRepo) FolderFindShared(ctx context.Context, filter bson.M) ([]model.Favorite, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	items := make([]model.Favorite, 0)
	if err := folderFindShared(ctx, r.coll, filter, func(raw bson.Raw) error {
		var it model.Favorite
		if err := bson.Unmarshal(raw, &it); err != nil {
			return err
		}
		items = append(items, it)
		return nil
	}); err != nil {
		return nil, err
	}
	return items, nil
}
