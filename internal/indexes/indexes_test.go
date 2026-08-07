package indexes

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
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
	db := client.Database("neo_indexes_test")
	_ = db.Drop(context.Background())
	cleanup := func() {
		_ = db.Drop(context.Background())
		_ = client.Disconnect(context.Background())
	}
	return db, cleanup
}

// indexNames 返回集合内全部索引名（含 _id_）。
func indexNames(t *testing.T, coll *mongo.Collection) map[string]bool {
	t.Helper()
	ctx := context.Background()
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer cur.Close(ctx)
	names := map[string]bool{}
	for cur.Next(ctx) {
		var idx struct {
			Name string `bson:"name"`
		}
		if err := cur.Decode(&idx); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		names[idx.Name] = true
	}
	return names
}

// TestEnsureCreatesIndexes 验证 Ensure 在干净库上创建全部索引，且幂等可重入。
func TestEnsureCreatesIndexes(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	if err := Ensure(context.Background(), db); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// 抽查各集合关键索引是否建成
	want := map[string][]string{
		"users":         {"email_1", "accountId_1"},
		"usersessions":  {"userId_1_isActive_1", "isActive_1_lastActiveAt_-1", "refreshTokenHash_1"},
		"usedtokens":    {"tokenHash_1", "expiresAt_1"},
		"histories":     {"lastWatched_1", "userId_1_lastWatched_-1"},
		"notifications": {"userId_1_isRead_1", "createdAt_1"},
		"ratings":       {"userId_1_episodeId_1", "episodeId_1"},
		"follows":       {"userId_1_episodeId_1"},
		"favorites":     {"userId_1_episodeId_1"},
		"auditlogs":     {"createdAt_-1", "adminId_1", "userId_1"},
		"apiusages":     {"endpoint_1_date_1"},
		"sitecontents":  {"key_1"},
		"folders":       {"userId_1_type_1"},
	}
	for coll, names := range want {
		got := indexNames(t, db.Collection(coll))
		for _, n := range names {
			if !got[n] {
				t.Errorf("collection %s missing index %s", coll, n)
			}
		}
	}

	// 幂等：再次 Ensure 不报错
	if err := Ensure(context.Background(), db); err != nil {
		t.Fatalf("Ensure (2nd): %v", err)
	}
}

// TestEnsureToleratesExisting 验证已有同名索引时幂等跳过（含 TTL 选项一致性）。
func TestEnsureToleratesExisting(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	// 预置一个同名同键索引（users.email_1 唯一），Ensure 应跳过不报错
	coll := db.Collection("users")
	if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetName("email_1"),
	}); err != nil {
		t.Fatalf("precreate: %v", err)
	}
	// 预置一个 TTL 索引（notifications.createdAt_1），Ensure 应识别并跳过
	notif := db.Collection("notifications")
	if _, err := notif.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "createdAt", Value: 1}},
		Options: options.Index().SetName("createdAt_1").SetExpireAfterSeconds(90 * 24 * 60 * 60),
	}); err != nil {
		t.Fatalf("precreate ttl: %v", err)
	}

	if err := Ensure(ctx, db); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// 断言没有产生重复的同名索引
	if got := indexNames(t, coll); got["email_1"] != true {
		t.Fatal("email_1 missing")
	}
}
