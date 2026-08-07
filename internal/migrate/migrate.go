// Package migrate 实现启动时的一次性数据迁移（对齐 backend/src/index.js L84-177）。
//
// 每个迁移由 settings 集合的标记（value: {done:true}）控制，幂等可重入：
//   - accountId_migration_v1：为缺少 accountId 的旧用户按规则回填
//   - role_migration_v1：adminAccess 布尔字段 → role 字符串角色
//   - creatorProfile_migration_v1：CreatorProfile.adminId 字段重命名为 creatorId，
//     并为 creator/admin/superadmin 角色补建创作者主页
package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// 迁移标记键（settings 集合）。
const (
	keyAccountIDMigration      = "accountId_migration_v1"
	keyRoleMigration           = "role_migration_v1"
	keyCreatorProfileMigration = "creatorProfile_migration_v1"
)

// nonWordRegex 对应 JS 的 /[^\w]/g（\w = [A-Za-z0-9_]）。
var nonWordRegex = regexp.MustCompile(`[^\w]`)

// generateAccountID 按 Express 规则由 username 生成 accountId：
// username.replace(/[^\w]/g, '_').toLowerCase()（models/User.js 回填逻辑）。
func generateAccountID(username string) string {
	return strings.ToLower(nonWordRegex.ReplaceAllString(username, "_"))
}

// Run 执行全部启动迁移（幂等）。任一步失败即返回错误并停止后续迁移，
// 由调用方（cmd/server 启动流程）决定是否中断进程。
func Run(ctx context.Context, db *mongo.Database, repos *repository.Repos) error {
	if err := migrateAccountID(ctx, repos); err != nil {
		return fmt.Errorf("migrate accountId: %w", err)
	}
	if err := migrateRole(ctx, db, repos); err != nil {
		return fmt.Errorf("migrate role: %w", err)
	}
	if err := migrateCreatorProfile(ctx, db, repos); err != nil {
		return fmt.Errorf("migrate creatorProfile: %w", err)
	}
	return nil
}

// migrateAccountID 回填缺 accountId 的用户。
// 生成规则：baseId = username.replace(/[^\w]/g,'_').toLowerCase()；
// 若与其它用户（排除自身）冲突则追加 _N 后缀（N 从 1 递增）。
func migrateAccountID(ctx context.Context, repos *repository.Repos) error {
	done, err := repos.Settings.GetFlag(ctx, keyAccountIDMigration)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	users, err := repos.Users.FindMissingAccountID(ctx)
	if err != nil {
		return err
	}
	backfilled := 0
	for _, u := range users {
		base := generateAccountID(u.Username)
		accountID := base
		counter := 1
		for {
			exists, err := repos.Users.AccountIDExistsExcluding(ctx, accountID, u.ID)
			if err != nil {
				return err
			}
			if !exists {
				break
			}
			accountID = fmt.Sprintf("%s_%d", base, counter)
			counter++
		}
		if err := repos.Users.UpdateAccountID(ctx, u.ID, accountID); err != nil {
			return err
		}
		backfilled++
	}
	if backfilled > 0 {
		slog.Info("已为旧用户自动生成账号ID", "count", backfilled)
	}
	return repos.Settings.SetFlag(ctx, keyAccountIDMigration, bson.M{"done": true})
}

// migrateRole 迁移 adminAccess 布尔字段 → role 字符串角色。
// 对 adminAccess=true 且 role 为 user/空/缺失（排除 superadmin）的用户置 role='admin'，
// 随后清除所有用户的 adminAccess 字段（对齐 src/index.js 的逐文档判断 + updateMany $unset）。
func migrateRole(ctx context.Context, db *mongo.Database, repos *repository.Repos) error {
	done, err := repos.Settings.GetFlag(ctx, keyRoleMigration)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	users := db.Collection("users")
	// adminAccess:true 且 role 非 superadmin、逐文档 (role==='user' || !role) 成立
	res, err := users.UpdateMany(ctx, bson.M{
		"adminAccess": true,
		"$or": []bson.M{
			{"role": "user"},
			{"role": bson.M{"$in": []any{nil, ""}}},
		},
	}, bson.M{"$set": bson.M{"role": "admin"}})
	if err != nil {
		return err
	}
	if res.ModifiedCount > 0 {
		slog.Info("已将 adminAccess 用户迁移为 admin 角色", "count", res.ModifiedCount)
	}

	// 清除所有用户的 adminAccess 字段
	if _, err := users.UpdateMany(ctx, bson.M{}, bson.M{"$unset": bson.M{"adminAccess": ""}}); err != nil {
		return err
	}
	return repos.Settings.SetFlag(ctx, keyRoleMigration, bson.M{"done": true})
}

// roleUser 创作者主页迁移需要的用户投影。
type roleUser struct {
	ID       primitive.ObjectID `bson:"_id"`
	Username string             `bson:"username"`
	Role     string             `bson:"role"`
}

// migrateCreatorProfile 迁移 CreatorProfile：
//  1. 删除旧的 adminId_1 唯一索引（$rename 前必须移除，避免 null 冲突）；
//  2. 把存在 adminId 字段的文档 $rename 为 creatorId；
//  3. 为 creator/admin/superadmin 角色但无 CreatorProfile 的用户补建初始主页（幂等）。
func migrateCreatorProfile(ctx context.Context, db *mongo.Database, repos *repository.Repos) error {
	done, err := repos.Settings.GetFlag(ctx, keyCreatorProfileMigration)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	profiles := db.Collection("creatorprofiles")

	// 旧索引可能不存在，忽略删除错误
	_, _ = profiles.Indexes().DropOne(ctx, "adminId_1")

	oldCount, err := profiles.CountDocuments(ctx, bson.M{"adminId": bson.M{"$exists": true}})
	if err != nil {
		return err
	}
	if oldCount > 0 {
		if _, err := profiles.UpdateMany(ctx,
			bson.M{"adminId": bson.M{"$exists": true}},
			bson.M{"$rename": bson.M{"adminId": "creatorId"}}); err != nil {
			return err
		}
		slog.Info("已将 CreatorProfile 的 adminId 字段迁移为 creatorId", "count", oldCount)
	}

	var users []roleUser
	cur, err := db.Collection("users").Find(ctx,
		bson.M{"role": bson.M{"$in": []string{"creator", "admin", "superadmin"}}},
		options.Find().SetProjection(bson.M{"_id": 1, "username": 1, "role": 1}))
	if err != nil {
		return err
	}
	if err := cur.All(ctx, &users); err != nil {
		cur.Close(ctx)
		return err
	}
	cur.Close(ctx)

	created := 0
	for _, u := range users {
		existing, err := repos.CreatorProfiles.FindByCreator(ctx, u.ID)
		if err != nil {
			if repository.IsNotFound(err) {
				existing = nil
			} else {
				return err
			}
		}
		if existing != nil {
			continue
		}

		displayName := u.Username
		if displayName == "" {
			displayName = "创作者"
		}
		bio := "这位创作者还没有填写个人简介。"
		if u.Role == "superadmin" {
			bio = "站点管理员，负责内容审核与平台运营。"
		}
		p := &model.CreatorProfile{
			CreatorID:   u.ID,
			DisplayName: displayName,
			Bio:         bio,
			SocialLinks: map[string]string{},
		}
		if err := repos.CreatorProfiles.Create(ctx, p); err != nil {
			// 并发/重复创建则跳过（幂等），对齐 Express 捕获 11000 的语义
			if repository.IsDuplicateKey(err) {
				continue
			}
			return err
		}
		created++
	}
	if created > 0 {
		slog.Info("已为角色用户补建创作者主页", "count", created)
	}
	return repos.Settings.SetFlag(ctx, keyCreatorProfileMigration, bson.M{"done": true})
}
