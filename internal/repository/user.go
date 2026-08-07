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

// errDuplicateKey 标记唯一键冲突（对应 mongoose 11000）。
var errDuplicateKey = errors.New("duplicate key")

// IsDuplicateKey 判断错误是否为唯一键冲突。
func IsDuplicateKey(err error) bool { return errors.Is(err, errDuplicateKey) }

// ErrNotFound 是仓储层"文档不存在"哨兵错误。
var ErrNotFound = errors.New("document not found")

// IsNotFound 判断错误是否为文档不存在。
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// UserRepo 用户仓储。
type UserRepo struct {
	coll          *mongo.Collection
	maxAttempts   int
	lockMinutes   int
}

// NewUserRepo 构造用户仓储；maxAttempts/lockMinutes 为账号锁定策略
// （对齐 accountLockout.js 默认 5 次 / 30 分钟）。
func NewUserRepo(coll *mongo.Collection, maxAttempts, lockMinutes int) *UserRepo {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if lockMinutes <= 0 {
		lockMinutes = 30
	}
	return &UserRepo{coll: coll, maxAttempts: maxAttempts, lockMinutes: lockMinutes}
}

func (r *UserRepo) newCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, contextTimeout)
}

// publicProjection 排除敏感字段（等价 mongoose select:false：password/2FA/锁定计数）。
var publicProjection = bson.M{
	"password":               0,
	"twoFactorSecret":        0,
	"twoFactorBackupCodes":   0,
	"loginAttempts":          0,
	"lockUntil":              0,
}

// FindByEmail 按邮箱查找（不含敏感字段）。
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"email": email}, publicProjection)
}

// FindByEmailWithAuth 按邮箱查找，附带账号锁定字段（login 用）。
func (r *UserRepo) FindByEmailWithAuth(ctx context.Context, email string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"email": email}, bson.M{"password": 0, "twoFactorSecret": 0, "twoFactorBackupCodes": 0})
}

// FindByEmailWith2FA 按邮箱查找，附带 2FA 敏感字段与锁定字段（login-2fa 用）。
func (r *UserRepo) FindByEmailWith2FA(ctx context.Context, email string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"email": email}, bson.M{})
}

// FindByID 按 ID 查找（不含敏感字段）。
func (r *UserRepo) FindByID(ctx context.Context, id any) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"_id": id}, publicProjection)
}

// FindByAccountID 按 accountId 查找（不含敏感字段）。
func (r *UserRepo) FindByAccountID(ctx context.Context, accountID string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"accountId": accountID}, publicProjection)
}

// ExistsByEmail 判断邮箱是否已被占用。
func (r *UserRepo) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.exists(ctx, bson.M{"email": email})
}

// ExistsByAccountID 判断 accountId 是否已被占用。
func (r *UserRepo) ExistsByAccountID(ctx context.Context, accountID string) (bool, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.exists(ctx, bson.M{"accountId": accountID})
}

// Create 插入用户；唯一键冲突返回 IsDuplicateKey(err)。
func (r *UserRepo) Create(ctx context.Context, u *model.User) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.InsertOne(ctx, u)
	if mongo.IsDuplicateKeyError(err) {
		return errDuplicateKey
	}
	return err
}

// Save 整文档覆盖保存（对齐 user.save()）。
func (r *UserRepo) Save(ctx context.Context, u *model.User) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.ReplaceOne(ctx, bson.M{"_id": u.ID}, u)
	if err != nil {
		return err
	}
	return nil
}

// UpdateVerified 置邮箱已验证（verify-email 用）。
func (r *UserRepo) UpdateVerified(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"isEmailVerified": true}})
	return err
}

// SetEmailVerifiedAndEmail 置邮箱与验证状态（verify-email-change 用）。
func (r *UserRepo) SetEmailVerifiedAndEmail(ctx context.Context, id any, email string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"email": email, "isEmailVerified": true}})
	return err
}

// UpdatePassword 更新密码哈希与 passwordChangedAt。
func (r *UserRepo) UpdatePassword(ctx context.Context, id any, hash string, changedAt time.Time) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"password": hash, "passwordChangedAt": changedAt}})
	return err
}

// IsLocked 判断账号是否处于锁定（lockUntil > now）。由调用方对 user.LockUntil 使用。
func IsLocked(u *model.User) bool {
	return u.LockUntil > 0 && u.LockUntil > time.Now().UnixMilli()
}

// IncLoginAttempts 增加登录失败次数；达到阈值后锁定 lockMinutes 分钟。
// 对齐 accountLockout.js：若当前已锁定则重置为 1 次并清除 lockUntil（防御分支）。
func (r *UserRepo) IncLoginAttempts(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()

	// 先读当前锁定状态
	var lock struct {
		LockUntil   int64 `bson:"lockUntil"`
		LoginAttempts int `bson:"loginAttempts"`
	}
	err := r.coll.FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(bson.M{"lockUntil": 1, "loginAttempts": 1})).Decode(&lock)
	if err != nil {
		return normalizeErr(err)
	}
	now := time.Now()
	if lock.LockUntil > 0 && lock.LockUntil > now.UnixMilli() {
		// 已锁定：重置并清除锁
		_, err = r.coll.UpdateOne(ctx, bson.M{"_id": id},
			bson.M{"$set": bson.M{"loginAttempts": 1}, "$unset": bson.M{"lockUntil": ""}})
		return err
	}
	next := lock.LoginAttempts + 1
	update := bson.M{"$set": bson.M{"loginAttempts": next}}
	if next >= r.maxAttempts {
		update["$set"].(bson.M)["lockUntil"] = now.Add(time.Duration(r.lockMinutes) * time.Minute).UnixMilli()
	}
	_, err = r.coll.UpdateOne(ctx, bson.M{"_id": id}, update)
	return err
}

// ResetLoginAttempts 清零登录失败次数并清除锁定。
func (r *UserRepo) ResetLoginAttempts(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"loginAttempts": 0}, "$unset": bson.M{"lockUntil": ""}})
	return err
}

// DeleteByID 物理删除用户。
func (r *UserRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (r *UserRepo) findOne(ctx context.Context, filter, projection bson.M) (*model.User, error) {
	u := &model.User{}
	opts := options.FindOne()
	if projection != nil {
		opts.SetProjection(projection)
	}
	err := r.coll.FindOne(ctx, filter, opts).Decode(u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (r *UserRepo) exists(ctx context.Context, filter bson.M) (bool, error) {
	err := r.coll.FindOne(ctx, filter, options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func normalizeErr(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	return err
}
