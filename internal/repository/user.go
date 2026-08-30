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
	coll        *mongo.Collection
	maxAttempts int
	lockMinutes int
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
	"password":             0,
	"twoFactorSecret":      0,
	"twoFactorBackupCodes": 0,
	"loginAttempts":        0,
	"lockUntil":            0,
}

// FindByEmail 按邮箱查找（不含敏感字段）。
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"email": email}, publicProjection)
}

// FindByEmailWithAuth 按邮箱查找，附带密码哈希与账号锁定字段（login 用）。
// 对齐 Express 的 User.findOne({ email }).select('+loginAttempts +lockUntil')：
// password 参与 schema 默认 select（非 select:false），故返回中保留 password 供 matchPassword 使用。
func (r *UserRepo) FindByEmailWithAuth(ctx context.Context, email string) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"email": email}, bson.M{"twoFactorSecret": 0, "twoFactorBackupCodes": 0})
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
	return r.findOne(ctx, bson.M{"_id": ToObjectID(id)}, publicProjection)
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
// 对齐 Mongoose save()：落库后把驱动生成的 _id 回填到 struct，
// 使调用方随后可读 u.ID（bson omitempty 会省略零值 _id，驱动不会自动回填）。
func (r *UserRepo) Create(ctx context.Context, u *model.User) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	res, err := r.coll.InsertOne(ctx, u)
	if mongo.IsDuplicateKeyError(err) {
		return errDuplicateKey
	}
	if err != nil {
		return err
	}
	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		u.ID = oid
	}
	return nil
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
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{"isEmailVerified": true}})
	return err
}

// SetEmailVerifiedAndEmail 置邮箱与验证状态（verify-email-change 用）。
func (r *UserRepo) SetEmailVerifiedAndEmail(ctx context.Context, id any, email string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{"email": email, "isEmailVerified": true}})
	return err
}

// UpdatePassword 更新密码哈希与 passwordChangedAt。
func (r *UserRepo) UpdatePassword(ctx context.Context, id any, hash string, changedAt time.Time) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)},
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
		LockUntil     int64 `bson:"lockUntil"`
		LoginAttempts int   `bson:"loginAttempts"`
	}
	err := r.coll.FindOne(ctx, bson.M{"_id": ToObjectID(id)},
		options.FindOne().SetProjection(bson.M{"lockUntil": 1, "loginAttempts": 1})).Decode(&lock)
	if err != nil {
		return normalizeErr(err)
	}
	now := time.Now()
	if lock.LockUntil > 0 && lock.LockUntil > now.UnixMilli() {
		// 已锁定：重置并清除锁
		_, err = r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)},
			bson.M{"$set": bson.M{"loginAttempts": 1}, "$unset": bson.M{"lockUntil": ""}})
		return err
	}
	next := lock.LoginAttempts + 1
	update := bson.M{"$set": bson.M{"loginAttempts": next}}
	if next >= r.maxAttempts {
		update["$set"].(bson.M)["lockUntil"] = now.Add(time.Duration(r.lockMinutes) * time.Minute).UnixMilli()
	}
	_, err = r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, update)
	return err
}

// ResetLoginAttempts 清零登录失败次数并清除锁定。
func (r *UserRepo) ResetLoginAttempts(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)},
		bson.M{"$set": bson.M{"loginAttempts": 0}, "$unset": bson.M{"lockUntil": ""}})
	return err
}

// DeleteByID 物理删除用户。
func (r *UserRepo) DeleteByID(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": ToObjectID(id)})
	return err
}

// FindByIDWithAuth 按 ID 查找，附带密码哈希与账号锁定字段，排除 2FA 密文。
// 对齐 Express 的 User.findById(id).select('+loginAttempts +lockUntil')
// （change-password / verify-device / confirm-device-login / reset-password 使用点）。
func (r *UserRepo) FindByIDWithAuth(ctx context.Context, id any) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"twoFactorSecret": 0, "twoFactorBackupCodes": 0})
}

// FindByIDWithAllSecrets 按 ID 查找，返回全部字段（含 password / 2FA 密文 / 锁定字段）。
// 对齐 Express 的 select('+twoFactorSecret +twoFactorBackupCodes')（twoFactor 路由使用点）。
func (r *UserRepo) FindByIDWithAllSecrets(ctx context.Context, id any) (*model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.findOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{})
}

// UpdateDeviceInfoAndLogin 更新登录后的设备信息与最近登录字段。
// 对齐 Express login 成功 / confirm-device-login / login-2fa 的
// user.deviceInfo = ...; user.lastLoginAt = ...; user.lastLoginIp = ...; user.lastLoginRegion = ...; user.save()。
func (r *UserRepo) UpdateDeviceInfoAndLogin(ctx context.Context, id any, deviceInfo model.DeviceInfo, lastLoginAt time.Time, lastLoginIP, lastLoginRegion string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{
		"deviceInfo":      deviceInfo,
		"lastLoginAt":     lastLoginAt,
		"lastLoginIp":     lastLoginIP,
		"lastLoginRegion": lastLoginRegion,
	}})
	return err
}

// BackgroundPrefsPatch 背景偏好局部更新字段（nil 表示该字段不修改）。
type BackgroundPrefsPatch struct {
	Image   *string
	Enabled *bool
	Opacity *int
	Blur    *int
}

// UpdateBackgroundPrefs 更新背景偏好（$set 局部字段）。
// 对齐 routes/users.js PUT /background-prefs 的可选字段合并语义。
func (r *UserRepo) UpdateBackgroundPrefs(ctx context.Context, id any, p BackgroundPrefsPatch) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	set := bson.M{}
	if p.Image != nil {
		set["backgroundPrefs.image"] = *p.Image
	}
	if p.Enabled != nil {
		set["backgroundPrefs.enabled"] = *p.Enabled
	}
	if p.Opacity != nil {
		set["backgroundPrefs.opacity"] = *p.Opacity
	}
	if p.Blur != nil {
		set["backgroundPrefs.blur"] = *p.Blur
	}
	if len(set) == 0 {
		return nil
	}
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": set})
	return err
}

// UpdateThemeSlots 更新用户背景/图标两槽主题（零值槽为 $unset 表示清空该槽）。
// 写入时同时清除旧版单主题字段（themeId/themeApply*），完成向两槽模型的迁移。
func (r *UserRepo) UpdateThemeSlots(ctx context.Context, id any,
	wallpaperID, iconsID primitive.ObjectID) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	unset := bson.M{"themeId": "", "themeApplyIcons": "", "themeApplyWallpaper": ""}
	set := bson.M{}
	if !wallpaperID.IsZero() {
		set["themeWallpaperId"] = wallpaperID
	} else {
		unset["themeWallpaperId"] = ""
	}
	if !iconsID.IsZero() {
		set["themeIconsId"] = iconsID
	} else {
		unset["themeIconsId"] = ""
	}
	update := bson.M{"$unset": unset}
	if len(set) > 0 {
		update["$set"] = set
	}
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, update)
	return err
}

// ClearThemeReferences 把所有选择了 themeID 的用户重置为未选择（主题删除后回收）。
// 覆盖旧单主题字段与两槽字段；清掉壁纸槽的用户同时回收背景偏好。
func (r *UserRepo) ClearThemeReferences(ctx context.Context, themeID primitive.ObjectID) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	filter := bson.M{"$or": bson.A{
		bson.M{"themeId": themeID},
		bson.M{"themeWallpaperId": themeID},
		bson.M{"themeIconsId": themeID},
	}}
	unset := bson.M{
		"themeId": "", "themeApplyIcons": "", "themeApplyWallpaper": "",
	}
	unsetWallpaper := bson.M{}
	for k, v := range unset {
		unsetWallpaper[k] = v
	}
	unsetWallpaper["themeWallpaperId"] = ""
	unsetIcons := bson.M{}
	for k, v := range unset {
		unsetIcons[k] = v
	}
	unsetIcons["themeIconsId"] = ""
	// 三类更新分开执行（$unset 组合不同）：
	// 1) 仅壁纸槽引用该主题：清壁纸槽 + 背景偏好。
	_, err := r.coll.UpdateMany(ctx, bson.M{"themeWallpaperId": themeID}, bson.M{
		"$unset": unsetWallpaper,
		"$set":   bson.M{"backgroundPrefs.image": "", "backgroundPrefs.enabled": false},
	})
	if err != nil {
		return err
	}
	// 2) 仅图标槽引用该主题：清图标槽。
	_, err = r.coll.UpdateMany(ctx, bson.M{"themeIconsId": themeID}, bson.M{"$unset": unsetIcons})
	if err != nil {
		return err
	}
	// 3) 旧单主题字段或两槽同时命中（前两步已清对应槽，此处兜底清旧字段）。
	_, err = r.coll.UpdateMany(ctx, filter, bson.M{"$unset": unset})
	return err
}

// UpdateEmailNotificationPrefs 更新邮件通知偏好（prefs 为「键→布尔」局部映射，键名对齐 7 个偏好键）。
// 对齐 routes/auth/email.js PUT /email-notification-prefs 的 $set emailNotificationPrefs.<key>。
func (r *UserRepo) UpdateEmailNotificationPrefs(ctx context.Context, id any, prefs map[string]bool) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	if len(prefs) == 0 {
		return nil
	}
	set := bson.M{}
	for k, v := range prefs {
		set["emailNotificationPrefs."+k] = v
	}
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": set})
	return err
}

// FindCreatorsByRole 按角色查找用户（不含敏感字段）。
// 对齐 routes/admin.js GET /creators 的 User.find({ role }).select('-password')。
func (r *UserRepo) FindCreatorsByRole(ctx context.Context, role string) ([]model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"role": role}, options.Find().SetProjection(publicProjection))
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

// UpdateAvatar 更新头像 URL（routes/users.js POST /avatar）。
func (r *UserRepo) UpdateAvatar(ctx context.Context, id any, url string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{"avatar": url}})
	return err
}

// UpdateUsername 更新昵称（routes/users.js PUT /profile）。
func (r *UserRepo) UpdateUsername(ctx context.Context, id any, username string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{"username": username}})
	return err
}

// SetTwoFactorSetup 写入 2FA 初始化状态（routes/twoFactor.js POST /enable：
// 加密密钥 + 备份码 + twoFactorEnabled=false）。
func (r *UserRepo) SetTwoFactorSetup(ctx context.Context, id any, secretEnc string, backupCodes []string, enabled bool) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{
		"twoFactorSecret":      secretEnc,
		"twoFactorBackupCodes": backupCodes,
		"twoFactorEnabled":     enabled,
	}})
	return err
}

// EnableTwoFactor 置 twoFactorEnabled=true（routes/twoFactor.js POST /verify-enable）。
func (r *UserRepo) EnableTwoFactor(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{"twoFactorEnabled": true}})
	return err
}

// DisableTwoFactor 关闭 2FA 并清除密钥与备份码（routes/twoFactor.js POST /disable）。
func (r *UserRepo) DisableTwoFactor(ctx context.Context, id any) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{
		"$set":   bson.M{"twoFactorEnabled": false},
		"$unset": bson.M{"twoFactorSecret": "", "twoFactorBackupCodes": ""},
	})
	return err
}

// FindMissingAccountID 查找缺少 accountId 字段的用户（accountId 回填迁移用）。
func (r *UserRepo) FindMissingAccountID(ctx context.Context) ([]model.User, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	cur, err := r.coll.Find(ctx, bson.M{"accountId": bson.M{"$exists": false}})
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

// UpdateAccountID 仅更新 accountId 字段（$set，保留其余字段）。
// 对齐 Express 回填迁移的 user.accountId = ...; user.save({validateBeforeSave:false})——
// mongoose 只持久化被修改的路径，这里用定点 $set 而非整文档覆盖，避免丢失未知旧字段。
func (r *UserRepo) UpdateAccountID(ctx context.Context, id any, accountID string) error {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	_, err := r.coll.UpdateOne(ctx, bson.M{"_id": ToObjectID(id)}, bson.M{"$set": bson.M{"accountId": accountID}})
	return err
}

// AccountIDExistsExcluding 判断 accountId 是否被除 excludeID 之外的用户占用（回填迁移碰撞检测用）。
func (r *UserRepo) AccountIDExistsExcluding(ctx context.Context, accountID string, excludeID any) (bool, error) {
	ctx, cancel := r.newCtx(ctx)
	defer cancel()
	return r.exists(ctx, bson.M{"accountId": accountID, "_id": bson.M{"$ne": excludeID}})
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
