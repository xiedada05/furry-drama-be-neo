package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/xiedada05/furry-drama-be-neo/internal/handler"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/router"
)

// rssTranslateBackupApp 构造挂载 rss/translate/backup 三域的测试应用（独立库，t.Cleanup
// 用 context.Background() 的 Drop）。
func rssTranslateBackupApp(t *testing.T) (*httptest.Server, *mongo.Database, *repository.Repos, *auth.Signer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.IsDev = true
	cfg.Server.NodeEnv = "development"
	cfg.Server.Listen = "tcp:127.0.0.1:0"
	cfg.Server.SiteURL = "http://localhost:3000"
	cfg.JWT.Secret = strings.Repeat("s", 40)
	cfg.JWT.DevAPIToken = "test-dev-token"
	cfg.JWT.AccessTTL = 15 * time.Minute
	cfg.JWT.RefreshTTL = 7 * 24 * time.Hour
	cfg.Security.LoginMaxAttempts = 5
	cfg.Security.LoginLockMinutes = 30
	cfg.Server.AllowOrigins = []string{"http://localhost:3000"}

	dbName := fmt.Sprintf("neo_rss_translate_backup_test_%d", time.Now().UnixNano())
	db, err := repository.Connect(context.Background(), "mongodb://127.0.0.1:27017/"+dbName, "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	amw := middleware.NewAuth(repos, signer)
	opts := middleware.RateLimitOpts{TrustXFF: false, IsDev: true}
	rl := func(spec ratelimit.Spec) gin.HandlerFunc { return middleware.RateLimit(spec, opts) }

	r := gin.New()
	r.Use(gin.Recovery())
	api := r.Group("/api")
	router.MountDual(api, "/rss", handler.NewRSS(repos, cfg, amw, rl).Register)
	router.MountDual(api, "/translate", handler.NewTranslate(repos, cfg, amw, rl).Register)
	router.MountDual(api, "/backup", handler.NewBackup(repos, cfg, amw, rl, db).Register)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, db, repos, signer
}

// rtbNewRoleUser 直接写库创建指定角色用户并签发 access token（避免依赖 /api/auth 路由）。
func rtbNewRoleUser(t *testing.T, repos *repository.Repos, signer *auth.Signer, role, prefix string) (primitive.ObjectID, string) {
	t.Helper()
	n := time.Now().UnixNano() % 100000000
	u := &model.User{
		AccountID:       fmt.Sprintf("%s_acc_%d", prefix, n),
		Username:        fmt.Sprintf("%s_user_%d", prefix, n),
		Email:           fmt.Sprintf("%s_%d@test.com", prefix, n),
		Role:            role,
		IsEmailVerified: true,
		CreatedAt:       time.Now(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := repos.Users.Create(ctx, u); err != nil {
		t.Fatalf("create %s user: %v", role, err)
	}
	token, err := signer.Sign(u.ID.Hex(), "access", 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return u.ID, token
}

// rtbDo 发起带 Bearer token（可为空）的 JSON 请求。
func rtbDo(base, method, path, token string, body any) (*http.Response, []byte) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return resp, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

// ---- RSS ----

// TestRSSFeedEmpty RSS feed 空库：默认站点名 + 空 channel。
func TestRSSFeedEmpty(t *testing.T) {
	ts, _, _, _ := rssTranslateBackupApp(t)
	resp, body := rtbDo(ts.URL, "GET", "/api/rss", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("rss status=%d body=%s", resp.StatusCode, string(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Fatalf("rss Content-Type=%q want application/xml", ct)
	}
	s := string(body)
	if !strings.HasPrefix(s, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Fatalf("rss missing xml declaration: %s", s)
	}
	if !strings.Contains(s, `<title>兽剧聚合平台 - 更新订阅</title>`) {
		t.Fatalf("rss missing default site name: %s", s)
	}
	if !strings.Contains(s, `<description>兽剧内容聚合平台</description>`) {
		t.Fatalf("rss missing default site desc: %s", s)
	}
	if strings.Contains(s, "<item>") {
		t.Fatalf("rss should have no items, got %s", s)
	}
}

// TestRSSFeedWithData RSS feed 数据：settings/about 覆盖站点名，剧集/单集条目输出。
func TestRSSFeedWithData(t *testing.T) {
	ts, db, _, _ := rssTranslateBackupApp(t)
	ctx := context.Background()

	// SiteContent settings + about。
	for _, sc := range []bson.M{
		{"key": "settings", "title": "settings", "content": `{"siteName":"测试兽剧站"}`, "updatedAt": time.Now()},
		{"key": "about", "title": "about", "content": `{"description":"这是一个测试站点"}`, "updatedAt": time.Now()},
	} {
		if _, err := db.Collection("sitecontents").InsertOne(ctx, sc); err != nil {
			t.Fatalf("seed sitecontent: %v", err)
		}
	}

	// 已审核 + 近期剧集（应出现）。
	now := time.Now().UTC().Truncate(time.Millisecond)
	ep, err := db.Collection("episodes").InsertOne(ctx, bson.M{
		"title": "我的测试剧", "currentEpisodes": 5, "totalEpisodes": 12,
		"status": "ongoing", "reviewStatus": "approved", "updatedAt": now,
	})
	if err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	epID := ep.InsertedID.(primitive.ObjectID)
	// 待审核剧集（不应出现）。
	if _, err := db.Collection("episodes").InsertOne(ctx, bson.M{
		"title": "未过审", "currentEpisodes": 1, "status": "ongoing",
		"reviewStatus": "pending", "updatedAt": now,
	}); err != nil {
		t.Fatalf("seed pending episode: %v", err)
	}
	// 30 天前的旧剧集（不应出现）。
	if _, err := db.Collection("episodes").InsertOne(ctx, bson.M{
		"title": "旧剧", "currentEpisodes": 1, "status": "completed",
		"reviewStatus": "approved", "updatedAt": now.Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed old episode: %v", err)
	}
	// 单集（应出现）。
	if _, err := db.Collection("singleepisodes").InsertOne(ctx, bson.M{
		"episodeId": epID, "episodeNumber": 3, "title": "第三集", "createdAt": now,
	}); err != nil {
		t.Fatalf("seed single episode: %v", err)
	}

	resp, body := rtbDo(ts.URL, "GET", "/api/rss", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("rss status=%d body=%s", resp.StatusCode, string(body))
	}
	s := string(body)
	if !strings.Contains(s, `<title>测试兽剧站 - 更新订阅</title>`) {
		t.Fatalf("rss missing custom site name: %s", s)
	}
	if !strings.Contains(s, `<description>这是一个测试站点</description>`) {
		t.Fatalf("rss missing custom site desc: %s", s)
	}
	if !strings.Contains(s, "<item><title>我的测试剧 - 更新至第5集</title><link>http://localhost:3000/episode/"+epID.Hex()+"</link><description>状态：连载中，共12集</description>") {
		t.Fatalf("rss missing episode item: %s", s)
	}
	if !strings.Contains(s, "<item><title>我的测试剧 第3集更新</title><link>http://localhost:3000/episode/"+epID.Hex()+"</link><description>第三集</description>") {
		t.Fatalf("rss missing single item: %s", s)
	}
	// 待审核与旧剧不得出现。
	if strings.Contains(s, "未过审") || strings.Contains(s, "旧剧") {
		t.Fatalf("rss should exclude unapproved/old episodes: %s", s)
	}
}

// TestRSSFeedEscapesXML RSS title 中特殊字符被转义。
func TestRSSFeedEscapesXML(t *testing.T) {
	ts, db, _, _ := rssTranslateBackupApp(t)
	_, err := db.Collection("episodes").InsertOne(context.Background(), bson.M{
		"title": "A&B <C>", "currentEpisodes": 1, "status": "ongoing",
		"reviewStatus": "approved", "updatedAt": time.Now(),
	})
	if err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	_, body := rtbDo(ts.URL, "GET", "/api/rss", "", nil)
	s := string(body)
	if !strings.Contains(s, "A&amp;B &lt;C&gt;") {
		t.Fatalf("rss xml escape failed: %s", s)
	}
}

// TestRSSApiUsage api-usage 统计：dailyTotals/topEndpoints/raw。
func TestRSSApiUsage(t *testing.T) {
	ts, db, repos, signer := rssTranslateBackupApp(t)
	// 直接插入 api-usage 文档（对齐 Express ApiUsage 集合形态）。
	docs := []any{
		bson.M{"endpoint": "GET /episodes", "method": "GET", "count": 5, "date": time.Now().UTC().Format("2006-01-02")},
		bson.M{"endpoint": "GET /episodes", "method": "GET", "count": 3, "date": time.Now().UTC().Add(-1 * 24 * time.Hour).Format("2006-01-02")},
		bson.M{"endpoint": "POST /episodes", "method": "POST", "count": 2, "date": time.Now().UTC().Format("2006-01-02")},
		bson.M{"endpoint": "GET /users", "method": "GET", "count": 99, "date": time.Now().UTC().Add(-10 * 24 * time.Hour).Format("2006-01-02")},
	}
	if _, err := db.Collection("apiusages").InsertMany(context.Background(), docs); err != nil {
		t.Fatalf("seed apiusage: %v", err)
	}
	_, token := rtbNewRoleUser(t, repos, signer, "superadmin", "rssa")
	resp, body := rtbDo(ts.URL, "GET", "/api/rss/api-usage", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("api-usage status=%d body=%s", resp.StatusCode, string(body))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("api-usage unmarshal: %v", err)
	}
	dailyTotals, ok := out["dailyTotals"].(map[string]any)
	if !ok || len(dailyTotals) == 0 {
		t.Fatalf("api-usage dailyTotals missing: %v", out)
	}
	top, ok := out["topEndpoints"].([]any)
	if !ok || len(top) == 0 {
		t.Fatalf("api-usage topEndpoints missing: %v", out)
	}
	// 10 天前的记录被 days=7 过滤（top 不应含 GET /users）。
	for _, pair := range top {
		if arr, ok := pair.([]any); ok && len(arr) > 0 && arr[0] == "GET /users" {
			t.Fatalf("api-usage should exclude old days: %v", top)
		}
	}
	raw, ok := out["raw"].([]any)
	if !ok {
		t.Fatalf("api-usage raw missing: %v", out)
	}
	// 校验 raw 文档 JSON 化（_id hex、count number）。
	if len(raw) > 0 {
		if _, ok := raw[0].(map[string]any); !ok {
			t.Fatalf("api-usage raw[0] not object: %v", raw[0])
		}
	}
}

// TestRSSApiUsageForbidden 非 superadmin 访问 api-usage → 403。
func TestRSSApiUsageForbidden(t *testing.T) {
	ts, _, repos, signer := rssTranslateBackupApp(t)
	_, token := rtbNewRoleUser(t, repos, signer, "admin", "rssf")
	resp, body := rtbDo(ts.URL, "GET", "/api/rss/api-usage", token, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("api-usage admin status=%d body=%s want 403", resp.StatusCode, string(body))
	}
}

// ---- Translate ----

// TestTranslateValidation 参数校验分支。
func TestTranslateValidation(t *testing.T) {
	ts, _, _, _ := rssTranslateBackupApp(t)
	// 缺 key。
	resp, body := rtbDo(ts.URL, "POST", "/api/translate", "", map[string]any{"targetLang": "en"})
	if resp.StatusCode != 400 {
		t.Fatalf("missing key status=%d body=%s", resp.StatusCode, string(body))
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["message"] != "Missing key or targetLang" {
		t.Fatalf("missing key message=%v", out["message"])
	}
	// 缺 targetLang。
	resp, _ = rtbDo(ts.URL, "POST", "/api/translate", "", map[string]any{"key": "你好"})
	if resp.StatusCode != 400 {
		t.Fatalf("missing targetLang status=%d", resp.StatusCode)
	}
	// 不支持语言。
	resp, body = rtbDo(ts.URL, "POST", "/api/translate", "", map[string]any{"key": "你好", "targetLang": "fr"})
	if resp.StatusCode != 400 {
		t.Fatalf("unsupported lang status=%d body=%s", resp.StatusCode, string(body))
	}
	_ = json.Unmarshal(body, &out)
	if out["message"] != "Unsupported language" {
		t.Fatalf("unsupported lang message=%v", out["message"])
	}
}

// TestTranslateZHPassthrough targetLang=zh 原样返回 key。
func TestTranslateZHPassthrough(t *testing.T) {
	ts, _, _, _ := rssTranslateBackupApp(t)
	resp, body := rtbDo(ts.URL, "POST", "/api/translate", "", map[string]any{"key": "你好世界", "targetLang": "zh"})
	if resp.StatusCode != 200 {
		t.Fatalf("zh passthrough status=%d body=%s", resp.StatusCode, string(body))
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["translation"] != "你好世界" {
		t.Fatalf("zh passthrough translation=%v", out["translation"])
	}
}

// TestTranslateHardcoded 硬编码表命中。
func TestTranslateHardcoded(t *testing.T) {
	ts, _, _, _ := rssTranslateBackupApp(t)
	resp, body := rtbDo(ts.URL, "POST", "/api/translate", "", map[string]any{"key": "连载中", "targetLang": "en"})
	if resp.StatusCode != 200 {
		t.Fatalf("hardcoded status=%d body=%s", resp.StatusCode, string(body))
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["translation"] != "Ongoing" {
		t.Fatalf("hardcoded translation=%v", out["translation"])
	}
}

// TestTranslateBatch 批量翻译：校验、zh 透传、硬编码命中（不触发机器翻译/网络）。
func TestTranslateBatch(t *testing.T) {
	ts, _, _, _ := rssTranslateBackupApp(t)
	// 缺 texts。
	resp, body := rtbDo(ts.URL, "POST", "/api/translate/batch", "", map[string]any{"targetLang": "en"})
	if resp.StatusCode != 400 {
		t.Fatalf("batch missing texts status=%d body=%s", resp.StatusCode, string(body))
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["message"] != "Missing texts or targetLang" {
		t.Fatalf("batch missing texts message=%v", out["message"])
	}
	// zh 透传。
	resp, body = rtbDo(ts.URL, "POST", "/api/translate/batch", "", map[string]any{"texts": []string{"a", "b"}, "targetLang": "zh"})
	if resp.StatusCode != 200 {
		t.Fatalf("batch zh status=%d body=%s", resp.StatusCode, string(body))
	}
	_ = json.Unmarshal(body, &out)
	if arr, ok := out["translations"].([]any); !ok || len(arr) != 2 {
		t.Fatalf("batch zh translations=%v", out["translations"])
	}
	// 全硬编码命中。
	resp, body = rtbDo(ts.URL, "POST", "/api/translate/batch", "", map[string]any{
		"texts": []string{"评分", "连载中"}, "targetLang": "en",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("batch hardcoded status=%d body=%s", resp.StatusCode, string(body))
	}
	_ = json.Unmarshal(body, &out)
	if arr, ok := out["translations"].([]any); !ok || len(arr) != 2 {
		t.Fatalf("batch hardcoded translations=%v", out["translations"])
	} else if arr[0] != "Rating" || arr[1] != "Ongoing" {
		t.Fatalf("batch hardcoded values=%v", arr)
	}
}

// ---- Backup ----

// TestBackupExport 导出：结构完整、users 剔除敏感字段、非超管被拒。
func TestBackupExport(t *testing.T) {
	ts, db, repos, signer := rssTranslateBackupApp(t)
	ctx := context.Background()
	if _, err := db.Collection("users").InsertOne(ctx, bson.M{
		"accountId": "acc1", "username": "u1", "email": "u1@test.com",
		"role": "user", "password": "HASH_SECRET", "lastLoginIp": "1.2.3.4",
		"deviceInfo": bson.M{"browser": "Chrome"}, "createdAt": time.Now(),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Collection("episodes").InsertOne(ctx, bson.M{
		"title": "ep", "currentEpisodes": 1, "status": "ongoing",
		"reviewStatus": "approved", "createdAt": time.Now(), "updatedAt": time.Now(),
	}); err != nil {
		t.Fatalf("seed episode: %v", err)
	}

	// 非 superadmin（admin）→ 403 需要超级管理员权限。
	_, adminToken := rtbNewRoleUser(t, repos, signer, "admin", "bke")
	resp, body := rtbDo(ts.URL, "GET", "/api/backup/export", adminToken, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("export admin status=%d body=%s want 403", resp.StatusCode, string(body))
	}

	_, token := rtbNewRoleUser(t, repos, signer, "superadmin", "bkx")
	resp, body = rtbDo(ts.URL, "GET", "/api/backup/export", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("export status=%d body=%s", resp.StatusCode, string(body))
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=backup_") {
		t.Fatalf("export Content-Disposition=%q", cd)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("export unmarshal: %v", err)
	}
	users, ok := out["users"].([]any)
	if !ok || len(users) == 0 {
		t.Fatalf("export users missing: %v", out)
	}
	u0 := users[0].(map[string]any)
	if _, has := u0["password"]; has {
		t.Fatalf("export leaked password: %v", u0)
	}
	if _, has := u0["lastLoginIp"]; has {
		t.Fatalf("export leaked lastLoginIp: %v", u0)
	}
	if _, has := u0["deviceInfo"]; has {
		t.Fatalf("export leaked deviceInfo: %v", u0)
	}
	eps, ok := out["episodes"].([]any)
	if !ok || len(eps) == 0 {
		t.Fatalf("export episodes missing: %v", out)
	}
	if id, has := eps[0].(map[string]any)["_id"]; !has {
		t.Fatalf("export episode missing _id: %v", eps[0])
	} else if _, isStr := id.(string); !isStr {
		t.Fatalf("export episode _id not hex string: %v", id)
	}
	// 空集合输出 [] 而非 null。
	if v, has := out["banners"]; !has || v == nil {
		t.Fatalf("export banners should be []: %v", out["banners"])
	}
}

// TestBackupImport 导入：插入成功、字段白名单过滤、审计日志写入、非 superadmin 被拒。
func TestBackupImport(t *testing.T) {
	ts, db, repos, signer := rssTranslateBackupApp(t)
	ctx := context.Background()

	_, adminToken := rtbNewRoleUser(t, repos, signer, "creator", "bki")
	resp, body := rtbDo(ts.URL, "POST", "/api/backup/import", adminToken, map[string]any{
		"data": map[string]any{"episodes": []any{}},
	})
	if resp.StatusCode != 403 {
		t.Fatalf("import creator status=%d body=%s want 403", resp.StatusCode, string(body))
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["message"] != "需要超级管理员权限" {
		t.Fatalf("import creator message=%v want 需要超级管理员权限", out["message"])
	}

	_, token := rtbNewRoleUser(t, repos, signer, "superadmin", "bko")

	// 无 data → 400。
	resp, body = rtbDo(ts.URL, "POST", "/api/backup/import", token, map[string]any{"overwrite": false})
	if resp.StatusCode != 400 {
		t.Fatalf("import no data status=%d body=%s", resp.StatusCode, string(body))
	}

	// 正常导入（overwrite=false，无需事务，独立 mongod 亦可运行）。
	data := map[string]any{
		"episodes": []any{
			map[string]any{
				"_id": primitive.NewObjectID().Hex(), "title": "导入剧",
				"currentEpisodes": 3, "status": "ongoing",
				"reviewStatus": "approved", "secretField": "x", "__v": 0,
			},
		},
		"notallowed": []any{map[string]any{"a": 1}},
	}
	resp, body = rtbDo(ts.URL, "POST", "/api/backup/import", token, map[string]any{"data": data, "overwrite": false})
	if resp.StatusCode != 200 {
		t.Fatalf("import status=%d body=%s", resp.StatusCode, string(body))
	}
	_ = json.Unmarshal(body, &out)
	if out["message"] != "数据恢复完成" {
		t.Fatalf("import message=%v", out["message"])
	}
	results, ok := out["results"].(map[string]any)
	if !ok {
		t.Fatalf("import results missing: %v", out)
	}
	if results["episodes"] != float64(1) {
		t.Fatalf("import episodes count=%v want 1", results["episodes"])
	}
	if results["notallowed"] != "skipped: not allowed" {
		t.Fatalf("import notallowed=%v", results["notallowed"])
	}

	// 校验落库：_id 被剥离（新生成）、secretField 被白名单过滤。
	var ep model.Episode
	if err := db.Collection("episodes").FindOne(ctx, bson.M{"title": "导入剧"}).Decode(&ep); err != nil {
		t.Fatalf("imported episode not found: %v", err)
	}
	if ep.CurrentEpisodes != 3 || ep.Status != "ongoing" {
		t.Fatalf("imported episode fields=%+v", ep)
	}
	var raw map[string]any
	if err := db.Collection("episodes").FindOne(ctx, bson.M{"title": "导入剧"}).Decode(&raw); err != nil {
		t.Fatalf("decode imported raw: %v", err)
	}
	if _, has := raw["secretField"]; has {
		t.Fatalf("import leaked disallowed field: %v", raw)
	}

	// 审计日志已写入。
	var al model.AuditLog
	if err := db.Collection("auditlogs").FindOne(ctx, bson.M{"action": "数据恢复"}).Decode(&al); err != nil {
		t.Fatalf("audit log not written: %v", err)
	}
	if al.Target != "全库" || al.Action != "数据恢复" || al.Details == "" {
		t.Fatalf("audit log fields=%+v", al)
	}
}

// TestBackupImportOverwrite 覆盖导入：200 且 results 含集合计数或错误串
// （独立 mongod 无事务支持时 overwrite 走 error: 导入失败，与 Express 行为一致）。
func TestBackupImportOverwrite(t *testing.T) {
	ts, db, repos, signer := rssTranslateBackupApp(t)
	_, token := rtbNewRoleUser(t, repos, signer, "superadmin", "bkw")
	data := map[string]any{
		"episodes": []any{map[string]any{"title": "覆盖剧", "currentEpisodes": 1, "status": "ongoing"}},
	}
	resp, body := rtbDo(ts.URL, "POST", "/api/backup/import", token, map[string]any{"data": data, "overwrite": true})
	if resp.StatusCode != 200 {
		t.Fatalf("overwrite import status=%d body=%s", resp.StatusCode, string(body))
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["message"] != "数据恢复完成" {
		t.Fatalf("overwrite import message=%v", out["message"])
	}
	results, _ := out["results"].(map[string]any)
	v, has := results["episodes"]
	if !has {
		t.Fatalf("overwrite results missing episodes: %v", out)
	}
	switch tv := v.(type) {
	case float64:
		if tv != 1 {
			t.Fatalf("overwrite count=%v want 1", tv)
		}
	case string:
		if tv != "error: 导入失败" {
			t.Fatalf("overwrite error=%v", tv)
		}
	default:
		t.Fatalf("overwrite episodes result type=%T", v)
	}
	_ = db
}
