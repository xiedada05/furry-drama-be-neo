package migrate

import (
	"context"
	"testing"
	"time"

	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const testMongoURI = "mongodb://127.0.0.1:27017"

// testDB 连接本地 mongod 并创建/清理独立的测试库。
func testDB(t *testing.T) (*mongo.Database, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI))
	if err != nil {
		t.Fatalf("connect mongod: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(context.Background())
		t.Fatalf("mongod unreachable at %s: %v", testMongoURI, err)
	}
	db := client.Database("neo_migrate_test")
	_ = db.Drop(context.Background())
	cleanup := func() {
		_ = db.Drop(context.Background())
		_ = client.Disconnect(context.Background())
	}
	return db, cleanup
}

// mustInsert 插入单个 bson 文档，失败即终止测试。
func mustInsert(t *testing.T, coll *mongo.Collection, doc any) {
	t.Helper()
	if _, err := coll.InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("insert %v: %v", doc, err)
	}
}

// TestMigrateAccountIDBackfill 验证 accountId 回填：
// 非单词字符转下划线、小写、与已有 accountId 冲突时追加 _N 后缀、已有值保持不变、标记写入。
func TestMigrateAccountIDBackfill(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	users := db.Collection("users")

	// 已有 accountId 的旧用户（制造 "bob" 冲突）
	mustInsert(t, users, bson.M{"username": "Bob Old", "email": "bobold@example.com", "accountId": "bob"})

	// 缺 accountId 的用户
	cases := []struct {
		username string
		email    string
		want     string
	}{
		{"Alice Smith", "alice@example.com", "alice_smith"},
		{"Bob", "bob@example.com", "bob_1"},        // 与已有 "bob" 冲突 → _1
		{"User_Two", "ut@example.com", "user_two"}, // 下划线是单词字符，原样保留
		{"王小明", "wang@example.com", "___"},         // CJK 均非单词字符 → 逐个转 _
	}
	for _, c := range cases {
		mustInsert(t, users, bson.M{"username": c.username, "email": c.email})
	}

	repos := repository.NewRepos(db, 5, 30)
	if err := migrateAccountID(ctx, repos); err != nil {
		t.Fatalf("migrateAccountID: %v", err)
	}

	for _, c := range cases {
		var doc bson.M
		if err := users.FindOne(ctx, bson.M{"email": c.email}).Decode(&doc); err != nil {
			t.Fatalf("find %s: %v", c.email, err)
		}
		if got, _ := doc["accountId"].(string); got != c.want {
			t.Errorf("user %s: accountId = %q, want %q", c.email, got, c.want)
		}
	}

	// 已有 accountId 的用户不变，且未被打扰
	var old bson.M
	if err := users.FindOne(ctx, bson.M{"email": "bobold@example.com"}).Decode(&old); err != nil {
		t.Fatalf("find bobold: %v", err)
	}
	if got, _ := old["accountId"].(string); got != "bob" {
		t.Errorf("existing user accountId changed to %q, want bob", got)
	}
	if got, _ := old["username"].(string); got != "Bob Old" {
		t.Errorf("existing user username changed to %q, want Bob Old", got)
	}

	// 迁移标记已写入且为 {done:true}
	done, err := repos.Settings.GetFlag(ctx, keyAccountIDMigration)
	if err != nil {
		t.Fatalf("GetFlag: %v", err)
	}
	if !done {
		t.Fatal("accountId migration flag not set")
	}

	// 幂等：再次运行不报错、不改动
	if err := migrateAccountID(ctx, repos); err != nil {
		t.Fatalf("migrateAccountID (2nd run): %v", err)
	}
	var again bson.M
	if err := users.FindOne(ctx, bson.M{"email": "bob@example.com"}).Decode(&again); err != nil {
		t.Fatalf("find bob after 2nd run: %v", err)
	}
	if got, _ := again["accountId"].(string); got != "bob_1" {
		t.Errorf("2nd run changed accountId to %q, want bob_1", got)
	}
}

// TestMigrateRunRoleAndCreatorProfile 验证 Run 的 role 迁移与 CreatorProfile 迁移：
// adminAccess→role、字段 $rename、角色用户补建主页、settings 三标记、全程幂等。
func TestMigrateRunRoleAndCreatorProfile(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	users := db.Collection("users")

	u1 := primitive.NewObjectID() // user + adminAccess → admin
	u2 := primitive.NewObjectID() // creator + adminAccess → 保持 creator
	u3 := primitive.NewObjectID() // superadmin → 保持
	u4 := primitive.NewObjectID() // 普通 user → 不变
	for _, doc := range []any{
		bson.M{"_id": u1, "accountId": "alice", "username": "alice", "email": "alice@example.com", "role": "user", "adminAccess": true},
		bson.M{"_id": u2, "accountId": "creator", "username": "creator", "email": "creator@example.com", "role": "creator", "adminAccess": true},
		bson.M{"_id": u3, "accountId": "sa", "username": "sa", "email": "sa@example.com", "role": "superadmin"},
		bson.M{"_id": u4, "accountId": "user", "username": "user", "email": "user@example.com", "role": "user"},
	} {
		mustInsert(t, users, doc)
	}

	// 旧 CreatorProfile：adminId 字段 → 迁移后应为 creatorId
	oldProfileID := primitive.NewObjectID()
	oldCreator := primitive.NewObjectID()
	profiles := db.Collection("creatorprofiles")
	mustInsert(t, profiles, bson.M{"_id": oldProfileID, "adminId": oldCreator, "displayName": "Old"})

	repos := repository.NewRepos(db, 5, 30)
	if err := Run(ctx, db, repos); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// role 迁移断言
	roleOf := func(id primitive.ObjectID) string {
		var doc bson.M
		if err := users.FindOne(ctx, bson.M{"_id": id}).Decode(&doc); err != nil {
			t.Fatalf("find user: %v", err)
		}
		return doc["role"].(string)
	}
	if got := roleOf(u1); got != "admin" {
		t.Errorf("u1 role = %q, want admin", got)
	}
	if got := roleOf(u2); got != "creator" {
		t.Errorf("u2 role = %q, want creator", got)
	}
	if got := roleOf(u3); got != "superadmin" {
		t.Errorf("u3 role = %q, want superadmin", got)
	}
	if got := roleOf(u4); got != "user" {
		t.Errorf("u4 role = %q, want user", got)
	}

	// adminAccess 字段应全部清除
	var cnt int64
	cnt, _ = users.CountDocuments(ctx, bson.M{"adminAccess": bson.M{"$exists": true}})
	if cnt != 0 {
		t.Errorf("adminAccess field still present on %d users", cnt)
	}

	// $rename 断言：旧 adminId 文档 → creatorId
	var renamed bson.M
	if err := profiles.FindOne(ctx, bson.M{"_id": oldProfileID}).Decode(&renamed); err != nil {
		t.Fatalf("find renamed profile: %v", err)
	}
	if _, has := renamed["adminId"]; has {
		t.Error("adminId field still present after rename")
	}
	if got, _ := renamed["creatorId"].(primitive.ObjectID); got != oldCreator {
		t.Errorf("renamed creatorId = %v, want %v", got, oldCreator)
	}

	// 角色用户补建主页：u1(admin)/u2(creator)/u3(superadmin) 有，u4(user) 无
	profileFor := func(creatorID primitive.ObjectID) *bson.M {
		var doc bson.M
		err := profiles.FindOne(ctx, bson.M{"creatorId": creatorID}).Decode(&doc)
		if err == mongo.ErrNoDocuments {
			return nil
		}
		if err != nil {
			t.Fatalf("find profile for %v: %v", creatorID, err)
		}
		return &doc
	}
	for _, id := range []primitive.ObjectID{u1, u2, u3} {
		if p := profileFor(id); p == nil {
			t.Errorf("expected CreatorProfile for role user %v", id)
		}
	}
	if p := profileFor(u4); p != nil {
		t.Error("unexpected CreatorProfile for plain user")
	}
	if p := profileFor(u3); p != nil {
		if bio, _ := (*p)["bio"].(string); bio != "站点管理员，负责内容审核与平台运营。" {
			t.Errorf("superadmin bio = %q", bio)
		}
	}
	if p := profileFor(u1); p != nil {
		if bio, _ := (*p)["bio"].(string); bio != "这位创作者还没有填写个人简介。" {
			t.Errorf("admin bio = %q", bio)
		}
	}

	// settings 三个标记全部写入
	for _, key := range []string{keyAccountIDMigration, keyRoleMigration, keyCreatorProfileMigration} {
		done, err := repos.Settings.GetFlag(ctx, key)
		if err != nil {
			t.Fatalf("GetFlag(%s): %v", key, err)
		}
		if !done {
			t.Errorf("migration flag %s not set", key)
		}
	}

	// 幂等：再次 Run 不报错、不产生重复主页
	if err := Run(ctx, db, repos); err != nil {
		t.Fatalf("Run (2nd): %v", err)
	}
	profCount, _ := profiles.CountDocuments(ctx, bson.M{})
	if profCount != 4 { // 1 旧文档(已重命名) + 3 补建
		t.Errorf("profile count after 2nd run = %d, want 4", profCount)
	}
}

// TestGenerateAccountID 纯函数单测：JS /[^\w]/g 替换与 toLowerCase。
func TestGenerateAccountID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Alice Smith", "alice_smith"},
		{"Bob", "bob"},
		{"User_Two", "user_two"},
		{"王小明", "___"},
		{"  Mixed--Chars  ", "__mixed__chars__"},
	}
	for _, c := range cases {
		if got := generateAccountID(c.in); got != c.want {
			t.Errorf("generateAccountID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
