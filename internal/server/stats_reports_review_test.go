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
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/server"
)

// srrServer 组装 stats/reports/review 域测试服务器
// （独立库 neo_srr_test；Drop 用 context.Background() 清理，避免测试结束 context 失效）。
func srrServer(t *testing.T) (*httptest.Server, *repository.Repos, *mongo.Database) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.IsDev = true
	cfg.Server.NodeEnv = "development"
	cfg.Server.Listen = "tcp:127.0.0.1:0"
	cfg.JWT.Secret = strings.Repeat("s", 40)
	cfg.JWT.DevAPIToken = "test-dev-token"
	cfg.JWT.DemoEmails = []string{"demo@furry09.com"}
	cfg.JWT.AccessTTL = 15 * time.Minute
	cfg.JWT.RefreshTTL = 7 * 24 * time.Hour
	cfg.Security.LoginMaxAttempts = 5
	cfg.Security.LoginLockMinutes = 30
	cfg.Server.AllowOrigins = []string{"http://localhost:3000"}

	db, err := repository.Connect(context.Background(), "mongodb://127.0.0.1:27017/neo_srr_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	app := server.NewApp(server.Deps{Config: cfg, DB: db, Repos: repos, Signer: signer})
	ts := httptest.NewServer(app)
	t.Cleanup(ts.Close)
	return ts, repos, db
}

// TestReportsCreateResolveList 举报域主流程：提交 → 列表(populate 举报者) → 处理。
func TestReportsCreateResolveList(t *testing.T) {
	ts, repos, db := srrServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("srr_%d@test.com", time.Now().UnixNano()%1e9)
	c.registerAndLogin(email)

	targetID := primitive.NewObjectID().Hex()

	// 非法 targetType → 400。
	var out map[string]any
	resp := c.json("POST", "/api/reports", map[string]any{
		"targetType": "bogus", "targetId": targetID, "reason": "spam",
	}, &out)
	if resp.StatusCode != http.StatusBadRequest || out["message"] != "Invalid target type" {
		t.Fatalf("invalid targetType: %d %v", resp.StatusCode, out)
	}
	// 非法 reason → 400。
	resp = c.json("POST", "/api/reports", map[string]any{
		"targetType": "episode", "targetId": targetID, "reason": "nope",
	}, &out)
	if resp.StatusCode != http.StatusBadRequest || out["message"] != "Invalid reason" {
		t.Fatalf("invalid reason: %d %v", resp.StatusCode, out)
	}
	// description 超长（2001 个 UTF-16 码元）→ 400。
	longDesc := strings.Repeat("字", 2001)
	resp = c.json("POST", "/api/reports", map[string]any{
		"targetType": "episode", "targetId": targetID, "reason": "spam", "description": longDesc,
	}, &out)
	if resp.StatusCode != http.StatusBadRequest || out["message"] != "描述不能超过2000个字符" {
		t.Fatalf("long desc: %d %v", resp.StatusCode, out)
	}

	// 正常提交 → 201，status=pending。
	var created map[string]any
	resp = c.json("POST", "/api/reports", map[string]any{
		"targetType": "episode", "targetId": targetID, "reason": "spam", "description": "广告",
	}, &created)
	if resp.StatusCode != http.StatusCreated || created["status"] != "pending" {
		t.Fatalf("create: %d %v", resp.StatusCode, created)
	}
	reportID, _ := created["_id"].(string)
	if reportID == "" || created["reporterId"] == nil {
		t.Fatalf("create incomplete: %v", created)
	}

	// 重复 pending → 400 Already reported。
	resp = c.json("POST", "/api/reports", map[string]any{
		"targetType": "episode", "targetId": targetID, "reason": "other",
	}, &out)
	if resp.StatusCode != http.StatusBadRequest || out["message"] != "Already reported" {
		t.Fatalf("duplicate: %d %v", resp.StatusCode, out)
	}

	// 提升为 admin 后：列表应看到该举报（reporterId 已 populate）。
	u, err := repos.Users.FindByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("find reporter: %v", err)
	}
	promoteUser(t, db, u.ID, "admin")

	var listed struct {
		Reports []map[string]any `json:"reports"`
		Total   int              `json:"total"`
	}
	resp = c.json("GET", "/api/reports/list?page=1&limit=20", nil, &listed)
	if resp.StatusCode != http.StatusOK || listed.Total != 1 || len(listed.Reports) != 1 {
		t.Fatalf("list: %d %v", resp.StatusCode, listed)
	}
	reporter, ok := listed.Reports[0]["reporterId"].(map[string]any)
	if !ok || reporter["username"] == nil || reporter["accountId"] == nil {
		t.Fatalf("list reporterId not populated: %v", listed.Reports[0]["reporterId"])
	}

	// 处理举报 → 200 且状态 resolved；举报者收到 report_result 站内通知。
	var resolved map[string]any
	resp = c.json("PUT", "/api/reports/resolve/"+reportID, map[string]any{"status": "resolved", "resolveNote": "采纳"}, &resolved)
	if resp.StatusCode != http.StatusOK || resolved["status"] != "resolved" || resolved["resolveNote"] != "采纳" {
		t.Fatalf("resolve: %d %v", resp.StatusCode, resolved)
	}
	count := countDocs(t, db, "notifications", bson.M{"userId": u.ID, "type": "report_result"})
	if count < 1 {
		t.Fatalf("expected report_result notification, got %d", count)
	}
}

// TestReviewApprovePending 审核域：插入 pending 剧集 → 待审核列表 → 审核通过。
func TestReviewApprovePending(t *testing.T) {
	ts, repos, db := srrServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("srr2_%d@test.com", time.Now().UnixNano()%1e9)
	c.registerAndLogin(email)
	creator, err := repos.Users.FindByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("find creator: %v", err)
	}
	insertPendingEpisode(t, db, creator.ID)
	promoteUser(t, db, creator.ID, "admin")

	var listed struct {
		List  []map[string]any `json:"list"`
		Total int              `json:"total"`
		Page  int              `json:"page"`
		Limit int              `json:"limit"`
	}
	resp := c.json("GET", "/api/review/pending?page=1&limit=20", nil, &listed)
	if resp.StatusCode != http.StatusOK || listed.Total != 1 {
		t.Fatalf("pending list: %d %v", resp.StatusCode, listed)
	}
	epID, _ := listed.List[0]["_id"].(string)
	if epID == "" || listed.List[0]["reviewStatus"] != "pending" {
		t.Fatalf("pending item: %v", listed.List[0])
	}

	var approved map[string]any
	resp = c.json("PUT", "/api/review/approve/"+epID, map[string]any{"note": "通过"}, &approved)
	if resp.StatusCode != http.StatusOK || approved["reviewStatus"] != "approved" || approved["reviewNote"] != "通过" {
		t.Fatalf("approve: %d %v", resp.StatusCode, approved)
	}
	resp = c.json("GET", "/api/review/pending", nil, &listed)
	if resp.StatusCode != http.StatusOK || listed.Total != 0 {
		t.Fatalf("pending after approve: %d %v", resp.StatusCode, listed)
	}
	// 创作者收到 review_result 通知。
	count := countDocs(t, db, "notifications", bson.M{"userId": creator.ID, "type": "review_result"})
	if count < 1 {
		t.Fatalf("expected review_result notification, got %d", count)
	}
}

// TestReviewReject 审核拒绝：rejected 后不再出现在待审核列表。
func TestReviewReject(t *testing.T) {
	ts, repos, db := srrServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("srr3_%d@test.com", time.Now().UnixNano()%1e9)
	c.registerAndLogin(email)
	creator, err := repos.Users.FindByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("find creator: %v", err)
	}
	insertPendingEpisode(t, db, creator.ID)
	promoteUser(t, db, creator.ID, "admin")

	var listed struct {
		List []map[string]any `json:"list"`
	}
	if resp := c.json("GET", "/api/review/pending", nil, &listed); resp.StatusCode != http.StatusOK {
		t.Fatalf("pending: %d", resp.StatusCode)
	}
	epID, _ := listed.List[0]["_id"].(string)

	var rejected map[string]any
	resp := c.json("PUT", "/api/review/reject/"+epID, map[string]any{"note": "驳回"}, &rejected)
	if resp.StatusCode != http.StatusOK || rejected["reviewStatus"] != "rejected" {
		t.Fatalf("reject: %d %v", resp.StatusCode, rejected)
	}
	count := countDocs(t, db, "notifications", bson.M{"userId": creator.ID, "type": "review_result", "message": "您的剧集《测试剧集》未通过审核：驳回"})
	if count < 1 {
		t.Fatalf("expected rejected notification, got %d", count)
	}
}

// TestStatsRealtimeAndOverview 统计域：realtime 与 overview 字段齐备。
func TestStatsRealtimeAndOverview(t *testing.T) {
	ts, repos, db := srrServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("srr4_%d@test.com", time.Now().UnixNano()%1e9)
	c.registerAndLogin(email)
	u, err := repos.Users.FindByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("find user: %v", err)
	}
	promoteUser(t, db, u.ID, "superadmin")

	var rt map[string]any
	resp := c.json("GET", "/api/stats/realtime", nil, &rt)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("realtime: %d %v", resp.StatusCode, rt)
	}
	for _, k := range []string{"onlineUsers", "todayVisits", "todayNewUsers", "todayNewEpisodes"} {
		if _, ok := rt[k]; !ok {
			t.Fatalf("realtime missing %s: %v", k, rt)
		}
	}

	var ov map[string]any
	resp = c.json("GET", "/api/stats/overview", nil, &ov)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview: %d %v", resp.StatusCode, ov)
	}
	for _, k := range []string{"totalEpisodes", "totalUsers", "totalViews", "userTrend", "retention", "activityTrend"} {
		if _, ok := ov[k]; !ok {
			t.Fatalf("overview missing %s: %v", k, ov)
		}
	}
}

// ---- 测试辅助 ----

// promoteUser 直接把用户角色提升为 admin/superadmin（审核/举报/统计管理端点需要）。
func promoteUser(t *testing.T, db *mongo.Database, userID primitive.ObjectID, role string) {
	t.Helper()
	_, err := db.Collection("users").UpdateOne(context.Background(),
		bson.M{"_id": userID}, bson.M{"$set": bson.M{"role": role}})
	if err != nil {
		t.Fatalf("promote user: %v", err)
	}
}

// insertPendingEpisode 直接插入一条 reviewStatus=pending 的剧集。
func insertPendingEpisode(t *testing.T, db *mongo.Database, creatorID primitive.ObjectID) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ep := model.Episode{
		Title:        "测试剧集",
		Description:  "测试描述",
		CoverImage:   "/uploads/test.png",
		Status:       "ongoing",
		CreatedBy:    &creatorID,
		ReviewStatus: "pending",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err := db.Collection("episodes").InsertOne(context.Background(), ep); err != nil {
		t.Fatalf("insert episode: %v", err)
	}
}

// countDocs 统计集合中匹配文档数。
func countDocs(t *testing.T, db *mongo.Database, coll string, filter bson.M) int64 {
	t.Helper()
	n, err := db.Collection(coll).CountDocuments(context.Background(), filter)
	if err != nil {
		t.Fatalf("count %s: %v", coll, err)
	}
	return n
}
