package server_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/server"
)

// seriesApp 构造带 series 路由的完整测试应用。register_routes.go 已通过
// server.NewApp 挂载 series 域（双版本 + 全局中间件），测试直接复用生产装配。
func seriesApp(t *testing.T) (*httptest.Server, *mongo.Database) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.IsDev = true
	cfg.Server.NodeEnv = "development"
	cfg.Server.Listen = "tcp:127.0.0.1:0"
	cfg.JWT.Secret = strings.Repeat("s", 40)
	cfg.JWT.EncryptionKey = ""
	cfg.JWT.DevAPIToken = "test-dev-token"
	cfg.JWT.DemoEmails = []string{"demo@furry09.com"}
	cfg.JWT.AccessTTL = 15 * time.Minute
	cfg.JWT.RefreshTTL = 7 * 24 * time.Hour
	cfg.Security.LoginMaxAttempts = 5
	cfg.Security.LoginLockMinutes = 30
	cfg.Server.AllowOrigins = []string{"http://localhost:3000"}

	dbName := fmt.Sprintf("neo_series_test_%d", time.Now().UnixNano())
	db, err := repository.Connect(t.Context(), "mongodb://127.0.0.1:27017/"+dbName, "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	// t.Context() 在 Cleanup 时已取消，drop 必须用独立 context。
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	app := server.NewApp(server.Deps{Config: cfg, DB: db, Repos: repos, Signer: signer})

	ts := httptest.NewServer(app)
	t.Cleanup(ts.Close)
	return ts, db
}

// newSeriesUser 注册并登录一个新用户并提升到指定角色，返回其 _id 与专属 client
// （每个用户独立 cookie jar，避免相互覆盖 accessToken）。
func newSeriesUser(t *testing.T, base string, db *mongo.Database, role string) (primitive.ObjectID, *client) {
	t.Helper()
	c := newClient(t, base)
	email := "su_" + fmt.Sprintf("%d", time.Now().UnixNano()%1e7) + "@test.com"
	c.register(email)
	var login map[string]any
	if resp := c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, &login); resp.StatusCode != 200 {
		t.Fatalf("login: %d body=%v", resp.StatusCode, login)
	}
	uid, err := primitive.ObjectIDFromHex(login["_id"].(string))
	if err != nil {
		t.Fatalf("parse login _id: %v", err)
	}
	if role != "" && role != "user" {
		if _, err := db.Collection("users").UpdateOne(t.Context(), bson.M{"_id": uid}, bson.M{"$set": bson.M{"role": role}}); err != nil {
			t.Fatalf("promote role %s: %v", role, err)
		}
	}
	return uid, c
}

// apiCreateSeries 以指定客户端创建系列，返回响应与解析后的 body。
func apiCreateSeries(c *client, body any) (*http.Response, map[string]any) {
	var out map[string]any
	resp := c.json("POST", "/api/series", body, &out)
	return resp, out
}

// seedSeriesEpisode 直接向 episodes 集合写入一条 mongoose 形态的剧集。
func seedSeriesEpisode(t *testing.T, db *mongo.Database) primitive.ObjectID {
	t.Helper()
	res, err := db.Collection("episodes").InsertOne(t.Context(), bson.M{
		"title": "ep1", "coverImage": "/uploads/ep1.jpg", "currentEpisodes": 3,
		"totalEpisodes": nil, "status": "ongoing", "averageRating": 4.5,
		"description": "ep desc", "category": []string{"furry"}, "tags": []string{"t1"},
		"views": 10, "createdAt": time.Now(), "updatedAt": time.Now(),
	})
	if err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	return res.InsertedID.(primitive.ObjectID)
}

// TestSeriesListEmptyAndPagination GET /api/series 空列表与分页。
func TestSeriesListEmptyAndPagination(t *testing.T) {
	ts, db := seriesApp(t)
	c := newClient(t, ts.URL)

	var out map[string]any
	resp := c.json("GET", "/api/series", nil, &out)
	if resp.StatusCode != 200 {
		t.Fatalf("list empty: %d body=%v", resp.StatusCode, out)
	}
	if out["page"] != float64(1) || out["limit"] != float64(50) || out["total"] != float64(0) || out["totalPages"] != float64(0) {
		t.Fatalf("list empty pagination: %v", out)
	}
	if list, ok := out["list"].([]any); !ok || len(list) != 0 {
		t.Fatalf("list should be empty array, got %v", out["list"])
	}

	// 创建 3 个系列后验证分页（默认 limit 50，显式 limit=2）。
	_, creatorC := newSeriesUser(t, ts.URL, db, "creator")
	for i := 1; i <= 3; i++ {
		resp, body := apiCreateSeries(creatorC, map[string]any{"name": fmt.Sprintf("s%d", i), "description": "d"})
		if resp.StatusCode != 201 {
			t.Fatalf("create s%d: %d body=%v", i, resp.StatusCode, body)
		}
	}
	var page1 map[string]any
	resp = c.json("GET", "/api/series?page=1&limit=2", nil, &page1)
	if resp.StatusCode != 200 || len(page1["list"].([]any)) != 2 || page1["total"] != float64(3) || page1["totalPages"] != float64(2) {
		t.Fatalf("page1: %d %v", resp.StatusCode, page1)
	}
	var page2 map[string]any
	resp = c.json("GET", "/api/series?page=2&limit=2", nil, &page2)
	if resp.StatusCode != 200 || len(page2["list"].([]any)) != 1 || page2["total"] != float64(3) {
		t.Fatalf("page2: %d %v", resp.StatusCode, page2)
	}
	// 非法/越界分页：page=0 → 1，limit=999 → 100，limit=0 → 1（对齐 series.js）。
	var clamped map[string]any
	resp = c.json("GET", "/api/series?page=0&limit=999", nil, &clamped)
	if resp.StatusCode != 200 || clamped["page"] != float64(1) || clamped["limit"] != float64(100) {
		t.Fatalf("clamped: %d %v", resp.StatusCode, clamped)
	}
	var zeroLimit map[string]any
	resp = c.json("GET", "/api/series?limit=0", nil, &zeroLimit)
	if resp.StatusCode != 200 || zeroLimit["limit"] != float64(1) {
		t.Fatalf("limit=0: %d %v", resp.StatusCode, zeroLimit)
	}
}

// TestSeriesCreateValidation POST 权限与校验。
func TestSeriesCreateValidation(t *testing.T) {
	ts, db := seriesApp(t)
	// 普通 user 角色 POST → 403。
	regular, c := newSeriesUser(t, ts.URL, db, "user")
	_ = regular
	resp, body := apiCreateSeries(c, map[string]any{"name": "x"})
	if resp.StatusCode != 403 {
		t.Fatalf("user create: %d body=%v", resp.StatusCode, body)
	}
	// creator 缺 name / name 为空 → 400 名称必填。
	_, creatorC := newSeriesUser(t, ts.URL, db, "creator")
	resp, body = apiCreateSeries(creatorC, map[string]any{"description": "no name"})
	if resp.StatusCode != 400 || body["message"] != "名称必填" {
		t.Fatalf("no name: %d body=%v", resp.StatusCode, body)
	}
	resp, body = apiCreateSeries(creatorC, map[string]any{"name": ""})
	if resp.StatusCode != 400 || body["message"] != "名称必填" {
		t.Fatalf("empty name: %d body=%v", resp.StatusCode, body)
	}
	// 合法创建 → 201，字段对齐。
	resp, body = apiCreateSeries(creatorC, map[string]any{"name": "系列A", "description": "描述"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d body=%v", resp.StatusCode, body)
	}
	if body["name"] != "系列A" || body["description"] != "描述" || body["nameEn"] != "" || body["descriptionEn"] != "" {
		t.Fatalf("create fields: %v", body)
	}
	if eps, ok := body["episodes"].([]any); !ok || len(eps) != 0 {
		t.Fatalf("create episodes should be []: %v", body["episodes"])
	}
	// createdBy 为创作者 hex。
	// （从创建者响应反查由 DB 校验，见 TestSeriesDetailAndPopulate。）
	if body["createdBy"] == "" {
		t.Fatalf("createdBy missing: %v", body)
	}
	if body["createdAt"] == "" || body["updatedAt"] == "" {
		t.Fatalf("timestamps missing: %v", body)
	}
	// 显式 null episodes → []。
	resp, body = apiCreateSeries(creatorC, map[string]any{"name": "系列B", "episodes": nil})
	if resp.StatusCode != 201 {
		t.Fatalf("create null eps: %d body=%v", resp.StatusCode, body)
	}
	if eps, ok := body["episodes"].([]any); !ok || len(eps) != 0 {
		t.Fatalf("null episodes should be []: %v", body["episodes"])
	}
	// episodes 含非法 ID → 500（对齐 CastError）。
	resp, body = apiCreateSeries(creatorC, map[string]any{"name": "系列C", "episodes": []string{"not-a-hex"}})
	if resp.StatusCode != 500 || body["message"] != "Server error" {
		t.Fatalf("bad episodes: %d body=%v", resp.StatusCode, body)
	}
}

// TestSeriesDetailAndPopulate GET /:id 详情、populate 与 404/500。
func TestSeriesDetailAndPopulate(t *testing.T) {
	ts, db := seriesApp(t)
	_, creatorC := newSeriesUser(t, ts.URL, db, "creator")
	epID := seedSeriesEpisode(t, db)
	missing := primitive.NewObjectID()
	resp, body := apiCreateSeries(creatorC, map[string]any{"name": "系列", "episodes": []string{epID.Hex(), missing.Hex()}})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d body=%v", resp.StatusCode, body)
	}
	seriesID := body["_id"].(string)

	// 详情：episodes 仅保留存在的剧集，缺失的跳过（对齐 populate）。
	var detail map[string]any
	resp = creatorC.json("GET", "/api/series/"+seriesID, nil, &detail)
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %d body=%v", resp.StatusCode, detail)
	}
	eps := detail["episodes"].([]any)
	if len(eps) != 1 {
		t.Fatalf("detail episodes want 1, got %v", detail["episodes"])
	}
	e0 := eps[0].(map[string]any)
	if e0["_id"] != epID.Hex() || e0["title"] != "ep1" || e0["coverImage"] != "/uploads/ep1.jpg" ||
		e0["status"] != "ongoing" || e0["description"] != "ep desc" || e0["views"] != float64(10) {
		t.Fatalf("detail episode fields: %v", e0)
	}
	if e0["totalEpisodes"] != nil || e0["averageRating"] != float64(4.5) || e0["currentEpisodes"] != float64(3) {
		t.Fatalf("detail episode numbers: %v", e0)
	}
	if cat, ok := e0["category"].([]any); !ok || len(cat) != 1 || cat[0] != "furry" {
		t.Fatalf("detail episode category: %v", e0["category"])
	}
	if tg, ok := e0["tags"].([]any); !ok || len(tg) != 1 {
		t.Fatalf("detail episode tags: %v", e0["tags"])
	}
	// createdBy 对齐创建者。
	if detail["createdBy"] == "" {
		t.Fatalf("detail createdBy missing: %v", detail)
	}

	// 列表：episodes 仅含列表字段子集，无 description/category/tags/views。
	var list map[string]any
	resp = creatorC.json("GET", "/api/series", nil, &list)
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	items := list["list"].([]any)
	found := false
	for _, it := range items {
		row := it.(map[string]any)
		if row["_id"] == seriesID {
			found = true
			leps := row["episodes"].([]any)
			if len(leps) != 1 {
				t.Fatalf("list episodes want 1, got %v", row["episodes"])
			}
			le := leps[0].(map[string]any)
			if le["_id"] != epID.Hex() || le["title"] != "ep1" || le["currentEpisodes"] != float64(3) || le["totalEpisodes"] != nil || le["status"] != "ongoing" || le["averageRating"] != float64(4.5) {
				t.Fatalf("list episode fields: %v", le)
			}
			for _, banned := range []string{"description", "category", "tags", "views"} {
				if _, ok := le[banned]; ok {
					t.Fatalf("list episode should not have %s: %v", banned, le)
				}
			}
		}
	}
	if !found {
		t.Fatalf("series not in list: %v", items)
	}

	// 不存在（合法 hex）→ 404 Not found。
	var nf map[string]any
	resp = creatorC.json("GET", "/api/series/"+primitive.NewObjectID().Hex(), nil, &nf)
	if resp.StatusCode != 404 || nf["message"] != "Not found" {
		t.Fatalf("not found: %d body=%v", resp.StatusCode, nf)
	}
	// 非法 hex → 500（对齐 CastError）。
	var bad map[string]any
	resp = creatorC.json("GET", "/api/series/not-a-hex-id", nil, &bad)
	if resp.StatusCode != 500 || bad["message"] != "Server error" {
		t.Fatalf("bad id: %d body=%v", resp.StatusCode, bad)
	}
}

// TestSeriesUpdateAndDelete PUT/DELETE 归属校验、admin 越权与删除。
func TestSeriesUpdateAndDelete(t *testing.T) {
	ts, db := seriesApp(t)
	owner, ownerC := newSeriesUser(t, ts.URL, db, "creator")
	resp, body := apiCreateSeries(ownerC, map[string]any{"name": "owned", "description": "d1"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d body=%v", resp.StatusCode, body)
	}
	seriesID := body["_id"].(string)
	if body["createdBy"] != owner.Hex() {
		t.Fatalf("createdBy want %s got %v", owner.Hex(), body["createdBy"])
	}

	// 非 owner creator → 403 无权修改此系列。
	_, otherC := newSeriesUser(t, ts.URL, db, "creator")
	var otherBody map[string]any
	resp = otherC.json("PUT", "/api/series/"+seriesID, map[string]any{"name": "hijack"}, &otherBody)
	if resp.StatusCode != 403 || otherBody["message"] != "无权修改此系列" {
		t.Fatalf("non-owner put: %d body=%v", resp.StatusCode, otherBody)
	}

	// owner 更新部分字段（未传 description → 保留原值）。
	resp = ownerC.json("PUT", "/api/series/"+seriesID, map[string]any{"name": "renamed"}, &body)
	if resp.StatusCode != 200 || body["name"] != "renamed" || body["description"] != "d1" {
		t.Fatalf("owner put: %d body=%v", resp.StatusCode, body)
	}
	// 更新 episodes 为空数组 → 响应为原始 ID 数组 []。
	resp = ownerC.json("PUT", "/api/series/"+seriesID, map[string]any{"episodes": []string{}}, &body)
	if resp.StatusCode != 200 {
		t.Fatalf("put episodes: %d body=%v", resp.StatusCode, body)
	}
	if eps, ok := body["episodes"].([]any); !ok || len(eps) != 0 {
		t.Fatalf("put episodes should be []: %v", body["episodes"])
	}

	// admin 可越权修改任意系列。
	_, adminC := newSeriesUser(t, ts.URL, db, "admin")
	resp = adminC.json("PUT", "/api/series/"+seriesID, map[string]any{"name": "by-admin"}, &body)
	if resp.StatusCode != 200 || body["name"] != "by-admin" {
		t.Fatalf("admin put: %d body=%v", resp.StatusCode, body)
	}

	// PUT 不存在 → 404。
	var nf map[string]any
	resp = ownerC.json("PUT", "/api/series/"+primitive.NewObjectID().Hex(), map[string]any{"name": "x"}, &nf)
	if resp.StatusCode != 404 || nf["message"] != "Not found" {
		t.Fatalf("put not found: %d body=%v", resp.StatusCode, nf)
	}

	// DELETE：creator 角色也可删除（Express adminProtect 允许 creator/admin/superadmin）。
	resp = otherC.json("DELETE", "/api/series/"+seriesID, nil, &body)
	if resp.StatusCode != 200 || body["message"] != "Deleted" {
		t.Fatalf("delete: %d body=%v", resp.StatusCode, body)
	}
	var gone map[string]any
	resp = otherC.json("GET", "/api/series/"+seriesID, nil, &gone)
	if resp.StatusCode != 404 {
		t.Fatalf("after delete: %d body=%v", resp.StatusCode, gone)
	}
	// DELETE 不存在 → 200（findByIdAndDelete 不报错）。
	resp = otherC.json("DELETE", "/api/series/"+primitive.NewObjectID().Hex(), nil, &body)
	if resp.StatusCode != 200 || body["message"] != "Deleted" {
		t.Fatalf("delete missing: %d body=%v", resp.StatusCode, body)
	}
	// DELETE 非法 hex → 500。
	var bad map[string]any
	resp = otherC.json("DELETE", "/api/series/bad-id", nil, &bad)
	if resp.StatusCode != 500 {
		t.Fatalf("delete bad id: %d body=%v", resp.StatusCode, bad)
	}
}
