package repository

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 /api/admin 与 /api/audit-logs 域（backend/routes/admin.js、
// routes/auditLogs.js）跨域补充的管理方法。所有方法统一以 Admin 前缀命名，
// 避免与其它域 agent 在各自 repo 文件里新增的方法重名。绝不修改 repos.go。

// ---- UserRepo 扩展 ----

// AdminFindByIdentifierWithAuth 按 邮箱或 accountId 二选一查找，附带密码哈希与
// 锁定字段（对齐 admin.js POST /login 的 findOne({$or:[{email},{accountId}]})
// .select('+loginAttempts +lockUntil +password')）。排除 2FA 密文。
func (r *UserRepo) AdminFindByIdentifierWithAuth(ctx context.Context, identifier string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"$or": bson.A{
		bson.M{"email": identifier},
		bson.M{"accountId": identifier},
	}}, bson.M{"twoFactorSecret": 0, "twoFactorBackupCodes": 0})
}

// AdminCountRoles 统计角色属于 roles 集合的用户数（对齐 /list 的
// countDocuments({role:{$in:[admin,superadmin,creator]}})）。
func (r *UserRepo) AdminCountRoles(ctx context.Context, roles []string) (int64, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"role": bson.M{"$in": roles}})
}

// AdminFindRoles 分页查询指定角色的用户，createdAt 倒序（对齐 /list 的
// find({role:{$in}}).select('-password').sort({createdAt:-1}).skip().limit()）。
// skip 恒设置（含负值）：负 skip 由 mongo 拒绝 → 500（对齐 Express 行为）。
func (r *UserRepo) AdminFindRoles(ctx context.Context, roles []string, page, limit int) ([]model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"role": bson.M{"$in": roles}},
		options.Find().
			SetProjection(publicProjection).
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(int64((page-1)*limit)).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.User
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// adminUserSearchFilter 组装 /users 的搜索条件（对齐 admin.js GET /users：
// accountId/username/email 三字段正则，escapeRegex 后 $options i）。
func adminUserSearchFilter(search string) bson.M {
	if search == "" {
		return bson.M{}
	}
	escaped := adminEscapeRegex(search)
	return bson.M{"$or": bson.A{
		bson.M{"accountId": bson.M{"$regex": escaped, "$options": "i"}},
		bson.M{"username": bson.M{"$regex": escaped, "$options": "i"}},
		bson.M{"email": bson.M{"$regex": escaped, "$options": "i"}},
	}}
}

// AdminCountSearch 统计匹配搜索条件的用户总数（/users 的 total）。
func (r *UserRepo) AdminCountSearch(ctx context.Context, search string) (int64, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.coll.CountDocuments(ctx, adminUserSearchFilter(search))
}

// AdminFindSearch 分页查询用户（/users：.select('-password').sort({createdAt:-1})）。
func (r *UserRepo) AdminFindSearch(ctx context.Context, search string, page, limit int) ([]model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, adminUserSearchFilter(search),
		options.Find().
			SetProjection(publicProjection).
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(int64((page-1)*limit)).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.User
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// AdminCountRole 统计某角色的用户数（"不能删除/降级最后一个超管"检查）。
func (r *UserRepo) AdminCountRole(ctx context.Context, role string) (int64, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"role": role})
}

// AdminFindByIDAndSetRole 按 ID 更新角色并返回新文档（对齐 /role/:id 的
// findByIdAndUpdate(id,{role},{new:true}).select('-password')）。不存在返回 ErrNotFound。
func (r *UserRepo) AdminFindByIDAndSetRole(ctx context.Context, id, role string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	u := &model.User{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)},
		bson.M{"$set": bson.M{"role": role}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(publicProjection)).Decode(u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return u, err
}

// AdminSetRole 定点更新角色（对齐 /user-admin-access/:id 的 user.role = x; user.save()
// 语义——仅改 role，不动其它字段）。
func (r *UserRepo) AdminSetRole(ctx context.Context, id any, role string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{"role": role}})
	return err
}

// AdminUpdateLastLogin 更新最近登录时间与 IP（对齐 admin.js POST /login 的
// user.lastLoginAt = new Date(); user.lastLoginIp = ip; user.save()）。
func (r *UserRepo) AdminUpdateLastLogin(ctx context.Context, id any, lastLoginAt time.Time, ip string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)},
		bson.M{"$set": bson.M{"lastLoginAt": lastLoginAt, "lastLoginIp": ip}})
	return err
}

// AdminUserRef 是 creatorId populate 需要的用户摘要（{_id, accountId, username, email}）。
type AdminUserRef struct {
	ID        primitive.ObjectID `bson:"_id"`
	AccountID string             `bson:"accountId"`
	Username  string             `bson:"username"`
	Email     string             `bson:"email"`
}

// AdminFindUserRefsByIDs 批量查用户摘要（对齐 CreatorProfile.find().populate(
// 'creatorId','accountId username email') 的 join 语义；不存在的用户不进映射）。
func (r *UserRepo) AdminFindUserRefsByIDs(ctx context.Context, ids []primitive.ObjectID) (map[string]AdminUserRef, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	out := make(map[string]AdminUserRef, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	cur, err := r.coll.Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"accountId": 1, "username": 1, "email": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var u AdminUserRef
		if err := cur.Decode(&u); err != nil {
			return nil, err
		}
		out[u.ID.Hex()] = u
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AdminDeletePushSubscriptionsByUser 删除用户全部 Web Push 订阅（注销级联；
// pushsubscriptions 集合无独立 repo，通过同库集合句柄操作）。
func (r *UserRepo) AdminDeletePushSubscriptionsByUser(ctx context.Context, userID any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.Database().Collection("pushsubscriptions").DeleteMany(ctx, bson.M{"userId": userID})
	return err
}

// ---- FolderRepo 扩展 ----

// AdminDeleteManyByUser 删除用户全部收藏夹（注销级联的 Folder.deleteMany({userId})）。
func (r *FolderRepo) AdminDeleteManyByUser(ctx context.Context, userID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"userId": userID})
	return err
}

// ---- EpisodeRepo 扩展 ----

// AdminCountPendingReview 统计待审核剧集（reviewStatus=pending 或 hasPendingChanges=true；
// 对齐 /pending-counts 的 Episode.countDocuments 条件）。
func (r *EpisodeRepo) AdminCountPendingReview(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"$or": bson.A{
		bson.M{"reviewStatus": "pending"},
		bson.M{"hasPendingChanges": true},
	}})
}

// AdminRebuildEpisodeRatings 删除用户评分后重算受影响剧集的平均分/评分人数，
// 并批量写回（对齐 admin.js DELETE /users/:id 的 Rating.aggregate + Episode.bulkWrite）。
// 对每部受影响剧集：若无剩余评分 → averageRating 0 / ratingCount 0；否则取
// $avg 四舍五入到 1 位小数（Math.round(avg*10)/10）。
func (r *EpisodeRepo) AdminRebuildEpisodeRatings(ctx context.Context, episodeIDs []primitive.ObjectID) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	if len(episodeIDs) == 0 {
		return nil
	}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"episodeId": bson.M{"$in": episodeIDs}}}},
		{{Key: "$group", Value: bson.M{"_id": "$episodeId", "avg": bson.M{"$avg": "$score"}, "count": bson.M{"$sum": 1}}}},
	}
	cur, err := r.coll.Database().Collection("ratings").Aggregate(ctx, pipeline)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	stats := map[string]struct {
		Avg   float64
		Count int
	}{}
	for cur.Next(ctx) {
		var row struct {
			ID    primitive.ObjectID `bson:"_id"`
			Avg   float64            `bson:"avg"`
			Count int                `bson:"count"`
		}
		if err := cur.Decode(&row); err != nil {
			return err
		}
		stats[row.ID.Hex()] = struct {
			Avg   float64
			Count int
		}{row.Avg, row.Count}
	}
	if err := cur.Err(); err != nil {
		return err
	}
	writeModels := make([]mongo.WriteModel, 0, len(episodeIDs))
	for _, epID := range episodeIDs {
		var avg float64
		var count int
		if s, ok := stats[epID.Hex()]; ok {
			avg = math.Round(s.Avg*10) / 10
			count = s.Count
		}
		writeModels = append(writeModels, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": epID}).
			SetUpdate(bson.M{"$set": bson.M{"averageRating": avg, "ratingCount": count}}))
	}
	_, err = r.coll.BulkWrite(ctx, writeModels)
	return err
}

// ---- ReportRepo / FeedbackRepo / FriendLinkRepo 扩展（/pending-counts）----

// AdminCountStatus 统计指定状态的举报数（Report.countDocuments({status})）。
func (r *ReportRepo) AdminCountStatus(ctx context.Context, status string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"status": status})
}

// AdminDeleteReportsByLegacyReporter 按 legacy 的 reporter 字段删除举报
// （对齐 admin.js DELETE /users/:id 的 Report.deleteMany({ reporter: id })——
// 注意 Express 用的是 reporter 而非 schema 的 reporterId，为保持 1:1 行为照抄，
// 对 schema 正确的文档实为无匹配的 no-op）。
func (r *ReportRepo) AdminDeleteReportsByLegacyReporter(ctx context.Context, reporterID any) error {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	_, err := r.coll.DeleteMany(ctx, bson.M{"reporter": reporterID})
	return err
}

// AdminCountStatus 统计指定状态的反馈数（Feedback.countDocuments({status})）。
func (r *FeedbackRepo) AdminCountStatus(ctx context.Context, status string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"status": status})
}

// AdminCountStatus 统计指定状态的友链数（FriendLink.countDocuments({status})）。
func (r *FriendLinkRepo) AdminCountStatus(ctx context.Context, status string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, bson.M{"status": status})
}

// ---- CreatorProfileRepo 扩展 ----

// AdminFindAllProfiles 查询全部创作者主页，updatedAt 倒序（对齐
// CreatorProfile.find().sort({updatedAt:-1})）。
func (r *CreatorProfileRepo) AdminFindAllProfiles(ctx context.Context) ([]model.CreatorProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.CreatorProfile
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// AdminFindProfileByID 按 ID 查找创作者主页；不存在返回 ErrNotFound。
func (r *CreatorProfileRepo) AdminFindProfileByID(ctx context.Context, id any) (*model.CreatorProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	p := &model.CreatorProfile{}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)}).Decode(p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return p, err
}

// AdminUpdateProfile 按 ID 定点更新并返回新文档（对齐 findByIdAndUpdate(id, update,
// {new:true})）。bumpVersion 为 true 时额外 $inc __v（对齐 approve/reject 的
// profile.save() 版本号自增；PUT /:id 用 findByIdAndUpdate 不增）。
func (r *CreatorProfileRepo) AdminUpdateProfile(ctx context.Context, id any, set bson.M, bumpVersion bool) (*model.CreatorProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	update := bson.M{"$set": set}
	if bumpVersion {
		update["$inc"] = bson.M{"__v": 1}
	}
	p := &model.CreatorProfile{}
	err := r.coll.FindOneAndUpdate(ctx, bson.M{"_id": ToObjectID(id)}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return p, err
}

// ---- AuditLogRepo 扩展 ----

// AdminCount 按过滤条件统计审计日志数（auditLogs.js GET / 的 total）。
func (r *AuditLogRepo) AdminCount(ctx context.Context, filter bson.M) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	return r.coll.CountDocuments(ctx, filter)
}

// AdminFindPaged 分页查询审计日志，createdAt 倒序（对齐 auditLogs.js GET / 的
// find(query).sort({createdAt:-1}).skip().limit()）。
func (r *AuditLogRepo) AdminFindPaged(ctx context.Context, filter bson.M, page, limit int) ([]model.AuditLog, error) {
	ctx, cancel := context.WithTimeout(ctx, contextTimeout)
	defer cancel()
	cur, err := r.coll.Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: -1}}).
			SetSkip(int64((page-1)*limit)).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var list []model.AuditLog
	if err := cur.All(ctx, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// adminEscapeRegex 转义正则特殊字符（对齐 utils/helpers.js escapeRegex）。
func adminEscapeRegex(s string) string {
	var b strings.Builder
	for _, ch := range s {
		if strings.ContainsRune(`.*+?^${}()|[]\`, ch) {
			b.WriteByte('\\')
		}
		b.WriteRune(ch)
	}
	return b.String()
}
