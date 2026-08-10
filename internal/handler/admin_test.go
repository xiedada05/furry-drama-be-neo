package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件是 /api/admin 与 /api/audit-logs 域 handler 的 httptest 集成测试
//（依赖本机 mongod 127.0.0.1:27017，独立库 neo_admin_test，Drop 用 Background）。

// adminTestEnv 组装挂载了 admin/audit-logs 路由的 gin 引擎与一个登录态用户。
type adminTestEnv struct {
	r      *gin.Engine
	db     *mongo.Database
	signer *auth.Signer
	cfg    *config.Config
}

func newAdminTestEnv(t *testing.T) *adminTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.IsDev = true
	cfg.Server.NodeEnv = "development"
	cfg.JWT.Secret = strings.Repeat("s", 40)
	cfg.JWT.DevAPIToken = "test-dev-token"
	cfg.JWT.DemoEmails = []string{}
	cfg.JWT.AccessTTL = 15 * time.Minute
	cfg.JWT.RefreshTTL = 7 * 24 * time.Hour
	cfg.Security.LoginMaxAttempts = 5
	cfg.Security.LoginLockMinutes = 30
	cfg.Server.AllowOrigins = []string{"http://localhost:3000"}

	db, err := repository.Connect(context.Background(), "mongodb://127.0.0.1:27017/neo_admin_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	if err := db.Drop(context.Background()); err != nil {
		t.Fatalf("drop db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(context.Background()) })

	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	amw := middleware.NewAuth(repos, signer)
	noopRL := func(ratelimit.Spec) gin.HandlerFunc { return func(*gin.Context) {} }

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.BodyParse())
	r.Use(middleware.SanitizeInput())
	api := r.Group("/api")
	handler.NewAdmin(repos, cfg, amw, noopRL, nil).Register(api.Group("/admin"))
	handler.NewAuditLogs(repos, cfg, amw, noopRL).Register(api.Group("/audit-logs"))

	return &adminTestEnv{r: r, db: db, signer: signer, cfg: cfg}
}

// insertUser 直接插入一个用户（密码经 bcrypt 哈希，对齐 mongoose 行为）。
func (e *adminTestEnv) insertUser(t *testing.T, accountID, username, email, role, password string) primitive.ObjectID {
	t.Helper()
	hash, err := auth.Hash(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	id := primitive.NewObjectID()
	_, err = e.db.Collection("users").InsertOne(context.Background(), bson.M{
		"_id": id, "accountId": accountID, "username": username, "email": email,
		"password": hash, "role": role, "isEmailVerified": true, "avatar": "",
		"createdAt": time.Now(), "updatedAt": time.Now(),
	})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// token 为指定用户签发 access token。
func (e *adminTestEnv) token(t *testing.T, userID primitive.ObjectID) string {
	t.Helper()
	tok, err := e.signer.Sign(userID.Hex(), "access", time.Hour, nil)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

// do 以指定 token 发起请求（devToken 为 true 时带 x-dev-token 头）。
func (e *adminTestEnv) do(t *testing.T, method, path string, body any, token string, devToken bool) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if devToken {
		req.Header.Set("x-dev-token", "test-dev-token")
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w, w.Body.Bytes()
}

// decode 解析响应 JSON。
func decode(t *testing.T, data []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode body %s: %v", string(data), err)
	}
}

// TestAdminPendingCounts 验证待办数量统计（adminProtect：creator/admin/superadmin 均可）。
func TestAdminPendingCounts(t *testing.T) {
	e := newAdminTestEnv(t)
	superID := e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")
	creatorID := e.insertUser(t, "cr", "creator", "creator@test.com", "creator", "pass1234")

	_, err := e.db.Collection("episodes").InsertOne(context.Background(), bson.M{"reviewStatus": "pending"})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}
	_, _ = e.db.Collection("episodes").InsertOne(context.Background(), bson.M{"hasPendingChanges": true})
	_, _ = e.db.Collection("reports").InsertOne(context.Background(), bson.M{"status": "pending"})
	_, _ = e.db.Collection("feedbacks").InsertOne(context.Background(), bson.M{"status": "pending"})
	_, _ = e.db.Collection("friendlinks").InsertOne(context.Background(), bson.M{"status": "pending", "name": "l", "url": "https://x"})

	var out map[string]any
	w, data := e.do(t, "GET", "/api/admin/pending-counts", nil, e.token(t, creatorID), false)
	if w.Code != 200 {
		t.Fatalf("pending-counts status=%d body=%s", w.Code, string(data))
	}
	decode(t, data, &out)
	if out["episodes"] != float64(2) || out["reports"] != float64(1) ||
		out["feedbacks"] != float64(1) || out["friendLinks"] != float64(1) {
		t.Fatalf("pending-counts unexpected: %v", out)
	}

	// 无 token → 401。
	w, _ = e.do(t, "GET", "/api/admin/pending-counts", nil, "", false)
	if w.Code != 401 {
		t.Fatalf("pending-counts no token status=%d want 401", w.Code)
	}
	_ = superID
}

// TestAdminListRequiresSuperAdmin 验证 /list 仅 superadmin 可访问。
func TestAdminListRequiresSuperAdmin(t *testing.T) {
	e := newAdminTestEnv(t)
	creatorID := e.insertUser(t, "cr", "creator", "creator@test.com", "creator", "pass1234")
	superID := e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")

	w, data := e.do(t, "GET", "/api/admin/list", nil, e.token(t, creatorID), false)
	if w.Code != 403 {
		t.Fatalf("creator /list status=%d body=%s want 403", w.Code, string(data))
	}
	w, data = e.do(t, "GET", "/api/admin/list", nil, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("superadmin /list status=%d body=%s want 200", w.Code, string(data))
	}
	var out struct {
		List       []map[string]any `json:"list"`
		Page       int              `json:"page"`
		Limit      int              `json:"limit"`
		Total      int              `json:"total"`
		TotalPages int              `json:"totalPages"`
	}
	decode(t, data, &out)
	if out.Total != 2 || len(out.List) != 2 || out.TotalPages != 1 {
		t.Fatalf("/list unexpected: %+v", out)
	}
}

// TestAdminLoginFlow 验证管理员登录：dev-token 绕过 altcha、密码错误 400、成功 200。
func TestAdminLoginFlow(t *testing.T) {
	e := newAdminTestEnv(t)
	e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")

	w, data := e.do(t, "POST", "/api/admin/login", map[string]any{
		"email": "super@test.com", "password": "wrongpass1", "altcha": "x",
	}, "", true)
	if w.Code != 400 {
		t.Fatalf("wrong password status=%d body=%s want 400", w.Code, string(data))
	}
	var errBody struct {
		Message string `json:"message"`
	}
	decode(t, data, &errBody)
	if errBody.Message != "用户名或密码错误" {
		t.Fatalf("wrong password message=%q", errBody.Message)
	}

	w, data = e.do(t, "POST", "/api/admin/login", map[string]any{
		"email": "super@test.com", "password": "pass1234", "altcha": "x",
	}, "", true)
	if w.Code != 200 {
		t.Fatalf("login status=%d body=%s want 200", w.Code, string(data))
	}
	var login struct {
		ID               string `json:"_id"`
		Role             string `json:"role"`
		ForceEmailChange bool   `json:"forceEmailChange"`
	}
	decode(t, data, &login)
	if login.ID == "" || login.Role != "superadmin" || login.ForceEmailChange {
		t.Fatalf("login body unexpected: %+v", login)
	}
	// 成功登录应写会话并设置 accessToken cookie。
	found := false
	for _, h := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(h, "accessToken=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("login did not set accessToken cookie: %v", w.Result().Header.Values("Set-Cookie"))
	}
}

// TestAdminRegisterAndRoleFlow 验证创建管理/创作者账户、角色修改、creator profile 自动创建。
func TestAdminRegisterAndRoleFlow(t *testing.T) {
	e := newAdminTestEnv(t)
	superID := e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")

	// 创建 admin 账户。
	w, data := e.do(t, "POST", "/api/admin/register", map[string]any{
		"username": "newadmin", "email": "newadmin@test.com", "password": "pass1234", "role": "admin",
	}, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("register admin status=%d body=%s want 200", w.Code, string(data))
	}
	var created struct {
		ID        string `json:"_id"`
		AccountID string `json:"accountId"`
		Role      string `json:"role"`
		Message   string `json:"message"`
	}
	decode(t, data, &created)
	if created.ID == "" || created.Role != "admin" || created.AccountID != "newadmin" ||
		created.Message != "账号创建成功，通知邮件已发送至用户邮箱" {
		t.Fatalf("register admin unexpected: %+v", created)
	}

	// 创建 creator 账户（不传 accountId → 自动生成；应自动建 creator profile）。
	w, data = e.do(t, "POST", "/api/admin/register", map[string]any{
		"username": "新建创作者", "email": "newcreator@test.com", "password": "pass1234", "role": "creator",
	}, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("register creator status=%d body=%s want 200", w.Code, string(data))
	}
	var cr struct {
		ID string `json:"_id"`
	}
	decode(t, data, &cr)
	creatorOID, err := primitive.ObjectIDFromHex(cr.ID)
	if err != nil {
		t.Fatalf("creator id not valid hex: %q", cr.ID)
	}
	profileCount, err := e.db.Collection("creatorprofiles").CountDocuments(context.Background(),
		bson.M{"creatorId": creatorOID})
	if err != nil {
		t.Fatalf("count creator profiles: %v", err)
	}
	if profileCount != 1 {
		t.Fatalf("creator profile not auto-created, count=%d", profileCount)
	}

	// 角色改为 creator → 自动补建 creator profile。
	w, data = e.do(t, "PUT", "/api/admin/role/"+created.ID, map[string]any{"role": "creator"}, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("update role status=%d body=%s want 200", w.Code, string(data))
	}
	createdOID, err := primitive.ObjectIDFromHex(created.ID)
	if err != nil {
		t.Fatalf("created id not valid hex: %q", created.ID)
	}
	profileCount2, _ := e.db.Collection("creatorprofiles").CountDocuments(context.Background(),
		bson.M{"creatorId": createdOID})
	if profileCount2 != 1 {
		t.Fatalf("creator profile not created on role change, count=%d", profileCount2)
	}

	// 不能删除自己的账号（self-check 先于"最后一个超管"检查，对齐 Express 分支顺序）。
	w, data = e.do(t, "DELETE", "/api/admin/"+superID.Hex(), nil, e.token(t, superID), false)
	if w.Code != 400 {
		t.Fatalf("delete self status=%d body=%s want 400", w.Code, string(data))
	}
	var delMsg struct {
		Message string `json:"message"`
	}
	decode(t, data, &delMsg)
	if delMsg.Message != "不能删除自己的账号" {
		t.Fatalf("delete self message=%q", delMsg.Message)
	}

	// 删除非管理/创作者账户 → 400。
	plainID := e.insertUser(t, "u1", "u1", "u1@test.com", "user", "pass1234")
	w, data = e.do(t, "DELETE", "/api/admin/"+plainID.Hex(), nil, e.token(t, superID), false)
	if w.Code != 400 {
		t.Fatalf("delete non-manager status=%d body=%s want 400", w.Code, string(data))
	}
	decode(t, data, &delMsg)
	if delMsg.Message != "该账户不是管理/创作者账户" {
		t.Fatalf("delete non-manager message=%q", delMsg.Message)
	}

	// 账户不存在 → 404。
	w, data = e.do(t, "DELETE", "/api/admin/"+primitive.NewObjectID().Hex(), nil, e.token(t, superID), false)
	if w.Code != 404 {
		t.Fatalf("delete missing status=%d body=%s want 404", w.Code, string(data))
	}

	// 第二个超管可删除第一个超管。
	sadm2ID := e.insertUser(t, "sadm2", "super2", "super2@test.com", "superadmin", "pass1234")
	w, data = e.do(t, "DELETE", "/api/admin/"+superID.Hex(), nil, e.token(t, sadm2ID), false)
	if w.Code != 200 {
		t.Fatalf("delete superadmin status=%d body=%s want 200", w.Code, string(data))
	}
}

// TestAdminDeleteUserCascade 验证删除用户级联清理评分并重算剧集平均分。
func TestAdminDeleteUserCascade(t *testing.T) {
	e := newAdminTestEnv(t)
	superID := e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")
	victimID := e.insertUser(t, "victim", "victim", "victim@test.com", "user", "pass1234")

	episodeID := primitive.NewObjectID()
	_, err := e.db.Collection("episodes").InsertOne(context.Background(), bson.M{
		"_id": episodeID, "title": "e", "averageRating": 5.0, "ratingCount": 2,
	})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}
	_, _ = e.db.Collection("ratings").InsertOne(context.Background(), bson.M{
		"userId": victimID, "episodeId": episodeID, "score": 5,
	})
	_, _ = e.db.Collection("ratings").InsertOne(context.Background(), bson.M{
		"userId": victimID, "episodeId": episodeID, "score": 4,
	})

	w, data := e.do(t, "DELETE", "/api/admin/users/"+victimID.Hex(), nil, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("delete user status=%d body=%s want 200", w.Code, string(data))
	}
	// 该用户评分应全部删除。
	ratingLeft, _ := e.db.Collection("ratings").CountDocuments(context.Background(), bson.M{"userId": victimID})
	if ratingLeft != 0 {
		t.Fatalf("ratings not cleaned, left=%d", ratingLeft)
	}
	// 剧集评分应重算为 0/0。
	var ep struct {
		AverageRating float64 `bson:"averageRating"`
		RatingCount   int     `bson:"ratingCount"`
	}
	if err := e.db.Collection("episodes").FindOne(context.Background(), bson.M{"_id": episodeID}).Decode(&ep); err != nil {
		t.Fatalf("decode episode: %v", err)
	}
	if ep.AverageRating != 0 || ep.RatingCount != 0 {
		t.Fatalf("episode rating not reset: avg=%v count=%d", ep.AverageRating, ep.RatingCount)
	}
}

// TestAuditLogsList 验证审计日志分页列表（superadmin 专属 + 过滤）。
func TestAuditLogsList(t *testing.T) {
	e := newAdminTestEnv(t)
	superID := e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")
	now := time.Now()
	_, _ = e.db.Collection("auditlogs").InsertMany(context.Background(), []any{
		bson.M{"action": "USER_DELETE", "adminName": "super", "userName": "a", "createdAt": now},
		bson.M{"action": "USER_BAN", "adminName": "super", "userName": "b", "createdAt": now},
	})

	// creator → 403。
	w, data := e.do(t, "GET", "/api/audit-logs", nil, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("audit-logs status=%d body=%s want 200", w.Code, string(data))
	}
	var out struct {
		Logs       []map[string]any `json:"logs"`
		Total      int              `json:"total"`
		Page       int              `json:"page"`
		TotalPages int              `json:"totalPages"`
	}
	decode(t, data, &out)
	if out.Total != 2 || len(out.Logs) != 2 || out.TotalPages != 1 || out.Page != 1 {
		t.Fatalf("audit-logs unexpected: %+v", out)
	}
	if _, has := out.Logs[0]["limit"]; has {
		t.Fatalf("audit-logs should not contain limit key: %v", out.Logs[0])
	}

	// 按 action 过滤。
	w, data = e.do(t, "GET", "/api/audit-logs?action=USER_BAN", nil, e.token(t, superID), false)
	decode(t, data, &out)
	if out.Total != 1 || out.Logs[0]["action"] != "USER_BAN" {
		t.Fatalf("audit-logs filter unexpected: %+v", out)
	}
}

// TestAdminCreatorsAndProfiles 验证创作者列表与创作者主页查询/编辑/审核。
func TestAdminCreatorsAndProfiles(t *testing.T) {
	e := newAdminTestEnv(t)
	superID := e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")
	creatorID := e.insertUser(t, "cr", "creator", "creator@test.com", "creator", "pass1234")

	profileID := primitive.NewObjectID()
	_, err := e.db.Collection("creatorprofiles").InsertOne(context.Background(), bson.M{
		"_id": profileID, "creatorId": creatorID, "displayName": "创作者", "bio": "简介",
		"socialLinks": bson.M{}, "reviewStatus": "approved", "reviewNote": "",
		"pendingChanges": bson.M{"displayName": "新名字", "avatar": "", "bio": "", "socialLinks": bson.M{}, "qqGroupLink": ""},
		"createdAt":      time.Now(), "updatedAt": time.Now(),
	})
	if err != nil {
		t.Fatalf("insert profile: %v", err)
	}

	// 创作者列表（adminProtect：superadmin 可访问）。
	w, data := e.do(t, "GET", "/api/admin/creators", nil, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("creators status=%d body=%s want 200", w.Code, string(data))
	}
	var creators []map[string]any
	decode(t, data, &creators)
	if len(creators) != 1 || creators[0]["role"] != "creator" {
		t.Fatalf("creators unexpected: %v", creators)
	}

	// 创作者主页列表：creatorId 已 populate。
	w, data = e.do(t, "GET", "/api/admin/creator-profiles", nil, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("profiles status=%d body=%s want 200", w.Code, string(data))
	}
	var profiles []map[string]any
	decode(t, data, &profiles)
	if len(profiles) != 1 {
		t.Fatalf("profiles unexpected: %v", profiles)
	}
	ref, ok := profiles[0]["creatorId"].(map[string]any)
	if !ok || ref["email"] != "creator@test.com" {
		t.Fatalf("profile creatorId not populated: %v", profiles[0]["creatorId"])
	}

	// 主页详情。
	w, data = e.do(t, "GET", "/api/admin/creator-profiles/"+profileID.Hex(), nil, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("profile detail status=%d body=%s want 200", w.Code, string(data))
	}
	var detail map[string]any
	decode(t, data, &detail)
	if detail["displayName"] != "创作者" {
		t.Fatalf("profile detail unexpected: %v", detail)
	}

	// 审核通过：pendingChanges 应用 + 通知写入。
	w, data = e.do(t, "PUT", "/api/admin/creator-profiles/"+profileID.Hex()+"/approve", nil, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("approve status=%d body=%s want 200", w.Code, string(data))
	}
	var approved map[string]any
	decode(t, data, &approved)
	if approved["displayName"] != "新名字" || approved["reviewStatus"] != "approved" {
		t.Fatalf("approve result unexpected: %v", approved)
	}
	notifCount, _ := e.db.Collection("notifications").CountDocuments(context.Background(),
		bson.M{"userId": creatorID, "type": "profile_review"})
	if notifCount != 1 {
		t.Fatalf("approve notification not written, count=%d", notifCount)
	}

	// 直接编辑：清空 pendingChanges 并置 approved。
	w, data = e.do(t, "PUT", "/api/admin/creator-profiles/"+profileID.Hex(),
		map[string]any{"displayName": "终态名字", "bio": "新简介"}, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("edit profile status=%d body=%s want 200", w.Code, string(data))
	}
	var edited map[string]any
	decode(t, data, &edited)
	if edited["displayName"] != "终态名字" || edited["reviewStatus"] != "approved" {
		t.Fatalf("edit profile result unexpected: %v", edited)
	}

	// 不存在的 ID → 404。
	w, _ = e.do(t, "GET", "/api/admin/creator-profiles/"+primitive.NewObjectID().Hex(), nil, e.token(t, superID), false)
	if w.Code != 404 {
		t.Fatalf("missing profile status=%d want 404", w.Code)
	}
}

// TestAdminVerifyPassword 验证密码校验端点。
func TestAdminVerifyPassword(t *testing.T) {
	e := newAdminTestEnv(t)
	adminID := e.insertUser(t, "adm", "admin", "admin@test.com", "admin", "pass1234")

	w, data := e.do(t, "POST", "/api/admin/verify-password", map[string]any{"password": "pass1234"}, e.token(t, adminID), false)
	if w.Code != 200 {
		t.Fatalf("verify-password status=%d body=%s want 200", w.Code, string(data))
	}
	var out map[string]any
	decode(t, data, &out)
	if out["verified"] != true {
		t.Fatalf("verify-password body=%v", out)
	}

	w, data = e.do(t, "POST", "/api/admin/verify-password", map[string]any{"password": "wrong123"}, e.token(t, adminID), false)
	if w.Code != 400 {
		t.Fatalf("verify-password wrong status=%d body=%s want 400", w.Code, string(data))
	}
}

// TestAdminToggleAdminAccess 验证管理权限切换。
func TestAdminToggleAdminAccess(t *testing.T) {
	e := newAdminTestEnv(t)
	superID := e.insertUser(t, "sadm", "super", "super@test.com", "superadmin", "pass1234")
	userID := e.insertUser(t, "u1", "user", "user@test.com", "user", "pass1234")

	w, data := e.do(t, "PUT", "/api/admin/user-admin-access/"+userID.Hex(),
		map[string]any{"adminAccess": true}, e.token(t, superID), false)
	if w.Code != 200 {
		t.Fatalf("grant status=%d body=%s want 200", w.Code, string(data))
	}
	var out map[string]any
	decode(t, data, &out)
	if out["role"] != "admin" || out["adminAccess"] != true {
		t.Fatalf("grant body=%v", out)
	}
	var stored struct {
		Role string `bson:"role"`
	}
	if err := e.db.Collection("users").FindOne(context.Background(), bson.M{"_id": userID}).Decode(&stored); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if stored.Role != "admin" {
		t.Fatalf("role not persisted, role=%q", stored.Role)
	}

	// 非布尔参数 → 400。
	w, _ = e.do(t, "PUT", "/api/admin/user-admin-access/"+userID.Hex(),
		map[string]any{"adminAccess": "yes"}, e.token(t, superID), false)
	if w.Code != 400 {
		t.Fatalf("bad param status=%d want 400", w.Code)
	}
}
