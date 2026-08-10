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

// cbvApp 构造 categories/banners/auto-status/versions 四域测试应用（独立库）。
// 复用生产装配（register_routes.go 已挂载全部域，含本四域）。
func cbvApp(t *testing.T) (*httptest.Server, *mongo.Database) {
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

	dbName := fmt.Sprintf("neo_cbv_test_%d", time.Now().UnixNano())
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

// cbvUser 注册并登录一个新用户并提升到指定角色，返回其 _id 与专属 client。
func cbvUser(t *testing.T, base string, db *mongo.Database, role string) (primitive.ObjectID, *client) {
	t.Helper()
	c := newClient(t, base)
	email := "cbv_" + fmt.Sprintf("%d", time.Now().UnixNano()%1e7) + "@test.com"
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

// createEpisodeAs 以指定角色创建一部剧集，返回响应 body。
func createEpisodeAs(c *client, body map[string]any) (*http.Response, map[string]any) {
	var out map[string]any
	resp := c.json("POST", "/api/episodes", body, &out)
	return resp, out
}

// TestCategoriesCRUD 分类：公开读 / 权限 / 创建去重 / 更新 / 删除。
func TestCategoriesCRUD(t *testing.T) {
	ts, db := cbvApp(t)
	c := newClient(t, ts.URL)

	// 空列表 → []。
	var empty []any
	resp := c.json("GET", "/api/categories", nil, &empty)
	if resp.StatusCode != 200 || len(empty) != 0 {
		t.Fatalf("list empty: %d %v", resp.StatusCode, empty)
	}

	// 普通 user POST → 403。
	_, userC := cbvUser(t, ts.URL, db, "user")
	var forb map[string]any
	resp = userC.json("POST", "/api/categories", map[string]any{"name": "x"}, &forb)
	if resp.StatusCode != 403 {
		t.Fatalf("user create: %d body=%v", resp.StatusCode, forb)
	}

	_, adminC := cbvUser(t, ts.URL, db, "admin")
	// 创建 → 200，字段对齐。
	var created map[string]any
	resp = adminC.json("POST", "/api/categories", map[string]any{"name": "科幻", "nameEn": "Sci-Fi", "order": 2}, &created)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d body=%v", resp.StatusCode, created)
	}
	if created["name"] != "科幻" || created["nameEn"] != "Sci-Fi" || created["nameJa"] != "" ||
		created["order"] != float64(2) || created["__v"] != float64(0) {
		t.Fatalf("create fields: %v", created)
	}
	if created["createdAt"] == "" || created["_id"] == "" {
		t.Fatalf("create timestamps/id: %v", created)
	}
	catID := created["_id"].(string)

	// 同名创建 → 400 该分类已存在。
	var dup map[string]any
	resp = adminC.json("POST", "/api/categories", map[string]any{"name": "科幻"}, &dup)
	if resp.StatusCode != 400 || dup["message"] != "该分类已存在" {
		t.Fatalf("duplicate: %d body=%v", resp.StatusCode, dup)
	}

	// 更新 name/order（未传 nameJa → 保留）。
	var updated map[string]any
	resp = adminC.json("PUT", "/api/categories/"+catID, map[string]any{"name": "玄幻", "order": 5}, &updated)
	if resp.StatusCode != 200 || updated["name"] != "玄幻" || updated["order"] != float64(5) || updated["nameJa"] != "" {
		t.Fatalf("update: %d body=%v", resp.StatusCode, updated)
	}

	// 更新不存在 → 404。
	var nf map[string]any
	resp = adminC.json("PUT", "/api/categories/"+primitive.NewObjectID().Hex(), map[string]any{"name": "x"}, &nf)
	if resp.StatusCode != 404 || nf["message"] != "分类不存在" {
		t.Fatalf("update missing: %d body=%v", resp.StatusCode, nf)
	}

	// 排序：order 升序。
	var list []any
	resp = c.json("GET", "/api/categories", nil, &list)
	if resp.StatusCode != 200 || len(list) != 1 {
		t.Fatalf("list: %d %v", resp.StatusCode, list)
	}

	// 删除 → 200 分类已删除；再删 → 404。
	var del map[string]any
	resp = adminC.json("DELETE", "/api/categories/"+catID, nil, &del)
	if resp.StatusCode != 200 || del["message"] != "分类已删除" {
		t.Fatalf("delete: %d body=%v", resp.StatusCode, del)
	}
	resp = adminC.json("DELETE", "/api/categories/"+catID, nil, &del)
	if resp.StatusCode != 404 {
		t.Fatalf("delete again: %d body=%v", resp.StatusCode, del)
	}
}

// TestBanners 轮播图：公开仅 active / /all / 链接校验 / 权限。
func TestBanners(t *testing.T) {
	ts, db := cbvApp(t)
	c := newClient(t, ts.URL)

	var empty []any
	resp := c.json("GET", "/api/banners", nil, &empty)
	if resp.StatusCode != 200 || len(empty) != 0 {
		t.Fatalf("list empty: %d %v", resp.StatusCode, empty)
	}

	// 非法链接 → 400。
	_, adminC := cbvUser(t, ts.URL, db, "admin")
	var bad map[string]any
	resp = adminC.json("POST", "/api/banners", map[string]any{
		"title": "b1", "image": "/uploads/x.jpg", "link": "ftp://x",
	}, &bad)
	if resp.StatusCode != 400 || bad["message"] != "链接格式不合法，仅支持 http/https 协议" {
		t.Fatalf("bad link: %d body=%v", resp.StatusCode, bad)
	}

	// 创建 active 默认 true。
	var created map[string]any
	resp = adminC.json("POST", "/api/banners", map[string]any{
		"title": "b1", "image": "/uploads/x.jpg", "link": "http://example.com", "order": 1,
	}, &created)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d body=%v", resp.StatusCode, created)
	}
	if created["active"] != true || created["order"] != float64(1) || created["link"] != "http://example.com" {
		t.Fatalf("create fields: %v", created)
	}
	bannerID := created["_id"].(string)

	// 创建 active=false。
	var hidden map[string]any
	resp = adminC.json("POST", "/api/banners", map[string]any{
		"title": "b2", "image": "/uploads/y.jpg", "active": false,
	}, &hidden)
	if resp.StatusCode != 200 || hidden["active"] != false {
		t.Fatalf("create hidden: %d body=%v", resp.StatusCode, hidden)
	}

	// 公开列表仅 active。
	var pub []any
	resp = c.json("GET", "/api/banners", nil, &pub)
	if resp.StatusCode != 200 || len(pub) != 1 || pub[0].(map[string]any)["_id"] != bannerID {
		t.Fatalf("public list: %d %v", resp.StatusCode, pub)
	}
	// /all 含全部。
	var all []any
	resp = adminC.json("GET", "/api/banners/all", nil, &all)
	if resp.StatusCode != 200 || len(all) != 2 {
		t.Fatalf("all list: %d %v", resp.StatusCode, all)
	}
	// 普通 user 访问 /all → 403。
	_, userC := cbvUser(t, ts.URL, db, "user")
	var forb map[string]any
	resp = userC.json("GET", "/api/banners/all", nil, &forb)
	if resp.StatusCode != 403 {
		t.Fatalf("user /all: %d", resp.StatusCode)
	}

	// PUT 切换 active。
	var toggled map[string]any
	resp = adminC.json("PUT", "/api/banners/"+bannerID, map[string]any{"active": false}, &toggled)
	if resp.StatusCode != 200 || toggled["active"] != false || toggled["title"] != "b1" {
		t.Fatalf("toggle: %d body=%v", resp.StatusCode, toggled)
	}
	resp = c.json("GET", "/api/banners", nil, &pub)
	if resp.StatusCode != 200 || len(pub) != 0 {
		t.Fatalf("public after toggle: %d %v", resp.StatusCode, pub)
	}

	// 删除。
	var del map[string]any
	resp = adminC.json("DELETE", "/api/banners/"+bannerID, nil, &del)
	if resp.StatusCode != 200 || del["message"] != "轮播图已删除" {
		t.Fatalf("delete: %d body=%v", resp.StatusCode, del)
	}
	resp = adminC.json("DELETE", "/api/banners/"+bannerID, nil, &del)
	if resp.StatusCode != 404 {
		t.Fatalf("delete missing: %d body=%v", resp.StatusCode, del)
	}
}

// TestAutoStatus 后台任务：自动完结 + 自动发布预告单集 + 权限。
func TestAutoStatus(t *testing.T) {
	ts, db := cbvApp(t)
	_, creatorC := cbvUser(t, ts.URL, db, "creator")

	// 创建两部 ongoing：一部 current>=total（应完结），一部未到（不变）。
	resp, ep1 := createEpisodeAs(creatorC, map[string]any{
		"title": "epA", "description": "d", "coverImage": "/uploads/a.jpg",
		"totalEpisodes": 3, "currentEpisodes": 3, "status": "ongoing",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create ep1: %d body=%v", resp.StatusCode, ep1)
	}
	resp, ep2 := createEpisodeAs(creatorC, map[string]any{
		"title": "epB", "description": "d", "coverImage": "/uploads/b.jpg",
		"totalEpisodes": 5, "currentEpisodes": 2, "status": "ongoing",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create ep2: %d body=%v", resp.StatusCode, ep2)
	}

	// admin（非 superadmin）调用 → 403。
	_, adminC := cbvUser(t, ts.URL, db, "admin")
	var forb map[string]any
	resp = adminC.json("POST", "/api/auto-status/auto-complete", map[string]any{}, &forb)
	if resp.StatusCode != 403 {
		t.Fatalf("admin auto-complete: %d body=%v", resp.StatusCode, forb)
	}

	_, superC := cbvUser(t, ts.URL, db, "superadmin")
	var done map[string]any
	resp = superC.json("POST", "/api/auto-status/auto-complete", map[string]any{}, &done)
	if resp.StatusCode != 200 {
		t.Fatalf("auto-complete: %d body=%v", resp.StatusCode, done)
	}
	if done["message"] != "已自动将 1 部剧集标记为已完结" || done["updated"] != float64(1) {
		t.Fatalf("auto-complete result: %v", done)
	}
	// ep1 已完结，ep2 仍 ongoing。
	var ep1Doc map[string]any
	resp = creatorC.json("GET", "/api/episodes/"+ep1["_id"].(string), nil, &ep1Doc)
	if resp.StatusCode != 200 || ep1Doc["status"] != "completed" {
		t.Fatalf("ep1 status: %d body=%v", resp.StatusCode, ep1Doc)
	}
	var ep2Doc map[string]any
	resp = creatorC.json("GET", "/api/episodes/"+ep2["_id"].(string), nil, &ep2Doc)
	if resp.StatusCode != 200 || ep2Doc["status"] != "ongoing" {
		t.Fatalf("ep2 status: %d body=%v", resp.StatusCode, ep2Doc)
	}

	// 创建预告单集（premiereDate 在过去）。
	var single map[string]any
	resp = creatorC.json("POST", "/api/episodes/"+ep1["_id"].(string)+"/episodes", map[string]any{
		"episodeNumber": 1, "title": "preview", "isScheduled": true,
		"premiereDate": "2020-01-01T00:00:00Z",
	}, &single)
	if resp.StatusCode != 201 {
		t.Fatalf("create preview single: %d body=%v", resp.StatusCode, single)
	}

	var released map[string]any
	resp = superC.json("POST", "/api/auto-status/check-premieres", map[string]any{}, &released)
	if resp.StatusCode != 200 {
		t.Fatalf("check-premieres: %d body=%v", resp.StatusCode, released)
	}
	if released["message"] != "已自动发布 1 个预告单集" || released["released"] != float64(1) {
		t.Fatalf("check-premieres result: %v", released)
	}
}

// TestVersions 版本历史：创建快照 / 列表分页 / 单版本 / diff / 回滚。
func TestVersions(t *testing.T) {
	ts, db := cbvApp(t)
	_, adminC := cbvUser(t, ts.URL, db, "admin")

	resp, ep := createEpisodeAs(adminC, map[string]any{
		"title": "ver", "description": "d1", "coverImage": "/uploads/v.jpg", "status": "ongoing",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create ep: %d body=%v", resp.StatusCode, ep)
	}
	epID := ep["_id"].(string)

	// 两次更新生成版本 1、2。
	var u1 map[string]any
	resp = adminC.json("PUT", "/api/episodes/"+epID, map[string]any{"description": "d2"}, &u1)
	if resp.StatusCode != 200 {
		t.Fatalf("update1: %d body=%v", resp.StatusCode, u1)
	}
	var u2 map[string]any
	resp = adminC.json("PUT", "/api/episodes/"+epID, map[string]any{"description": "d3"}, &u2)
	if resp.StatusCode != 200 {
		t.Fatalf("update2: %d body=%v", resp.StatusCode, u2)
	}

	// 列表：分页 + 排序（version 倒序）+ changedBy populate。
	var list map[string]any
	resp = adminC.json("GET", "/api/versions/"+epID, nil, &list)
	if resp.StatusCode != 200 {
		t.Fatalf("versions list: %d body=%v", resp.StatusCode, list)
	}
	if list["page"] != float64(1) || list["limit"] != float64(20) || list["total"] != float64(2) || list["totalPages"] != float64(1) {
		t.Fatalf("versions pagination: %v", list)
	}
	vs := list["versions"].([]any)
	if len(vs) != 2 {
		t.Fatalf("versions count: %v", list["versions"])
	}
	v0 := vs[0].(map[string]any)
	v1 := vs[1].(map[string]any)
	if v0["version"] != float64(2) || v1["version"] != float64(1) {
		t.Fatalf("versions order: %v", vs)
	}
	cb := v0["changedBy"].(map[string]any)
	if cb["accountId"] == "" || cb["username"] == "" || cb["_id"] == "" {
		t.Fatalf("changedBy populate: %v", v0["changedBy"])
	}
	data := v0["data"].(map[string]any)
	if data["_id"] != epID || data["description"] != "d2" {
		t.Fatalf("version data: %v", data)
	}

	// 单版本。
	var single map[string]any
	resp = adminC.json("GET", "/api/versions/"+epID+"/1", nil, &single)
	if resp.StatusCode != 200 {
		t.Fatalf("get version1: %d body=%v", resp.StatusCode, single)
	}
	if single["version"] != float64(1) || single["episodeId"] != epID {
		t.Fatalf("version1 doc: %v", single)
	}
	if single["data"].(map[string]any)["description"] != "d1" {
		t.Fatalf("version1 data: %v", single["data"])
	}
	var nf map[string]any
	resp = adminC.json("GET", "/api/versions/"+epID+"/99", nil, &nf)
	if resp.StatusCode != 404 || nf["message"] != "Version not found" {
		t.Fatalf("version missing: %d body=%v", resp.StatusCode, nf)
	}

	// diff：v1 data.d1 vs v2 data.d2 → 含 description 变更。
	var diff []any
	resp = adminC.json("GET", "/api/versions/"+epID+"/diff/1/2", nil, &diff)
	if resp.StatusCode != 200 {
		t.Fatalf("diff: %d body=%v", resp.StatusCode, diff)
	}
	foundDesc := false
	for _, it := range diff {
		row := it.(map[string]any)
		if row["field"] == "description" {
			foundDesc = true
			if row["oldValue"] != "d1" || row["newValue"] != "d2" {
				t.Fatalf("diff description: %v", row)
			}
		}
	}
	if !foundDesc {
		t.Fatalf("diff missing description: %v", diff)
	}

	// 回滚到 v1 → description 恢复为 d1。
	var rolled map[string]any
	resp = adminC.json("POST", "/api/versions/"+epID+"/rollback/1", map[string]any{}, &rolled)
	if resp.StatusCode != 200 {
		t.Fatalf("rollback: %d body=%v", resp.StatusCode, rolled)
	}
	if rolled["description"] != "d1" {
		t.Fatalf("rollback description: %v", rolled)
	}
	var detail map[string]any
	resp = adminC.json("GET", "/api/episodes/"+epID, nil, &detail)
	if resp.StatusCode != 200 || detail["description"] != "d1" {
		t.Fatalf("after rollback detail: %d body=%v", resp.StatusCode, detail)
	}

	// 回滚生成新版本（version 3）。
	var list2 map[string]any
	resp = adminC.json("GET", "/api/versions/"+epID, nil, &list2)
	if resp.StatusCode != 200 || list2["total"] != float64(3) {
		t.Fatalf("versions after rollback: %d body=%v", resp.StatusCode, list2)
	}
}
