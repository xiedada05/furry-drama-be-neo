package server_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/server"
)

// notificationsTestServer 组装通知域测试专用服务器（独立库 neo_notifications_test）。
// Drop 用 context.Background() 清理，避免 t.Context() 已 cancel 导致残留。
func notificationsTestServer(t *testing.T) (*httptest.Server, *repository.Repos) {
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

	db, err := repository.Connect(context.Background(), "mongodb://127.0.0.1:27017/neo_notifications_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	app := server.NewApp(server.Deps{Config: cfg, DB: db, Repos: repos, Signer: signer})
	ts := httptest.NewServer(app)
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	return ts, repos
}

// notificationTestLogin 注册并登录，返回 userID（hex 字符串）。
func notificationTestLogin(t *testing.T, c *client) string {
	t.Helper()
	email := fmt.Sprintf("ntf_%d@test.com", time.Now().UnixNano())
	c.register(email)
	var login map[string]any
	resp := c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, &login)
	if resp.StatusCode != 200 || login["_id"] == nil {
		t.Fatalf("login: %d body=%v", resp.StatusCode, login)
	}
	return login["_id"].(string)
}

// TestNotificationsStreamTicketAuth SSE ticket 校验的 401 分支。
func TestNotificationsStreamTicketAuth(t *testing.T) {
	ts, _ := notificationsTestServer(t)
	signer := auth.NewSigner(strings.Repeat("s", 40))

	// 1. 无 ticket → 401 需要认证
	resp, err := http.Get(ts.URL + "/api/notifications/stream")
	if err != nil {
		t.Fatalf("no ticket req: %v", err)
	}
	body := readRespBody(resp)
	if resp.StatusCode != 401 || !strings.Contains(string(body), "需要认证") {
		t.Fatalf("no ticket: %d body=%s want 401 需要认证", resp.StatusCode, body)
	}

	// 2. 非法 ticket → 401 认证信息无效
	resp, err = http.Get(ts.URL + "/api/notifications/stream?ticket=not-a-jwt")
	if err != nil {
		t.Fatalf("bad ticket req: %v", err)
	}
	body = readRespBody(resp)
	if resp.StatusCode != 401 || !strings.Contains(string(body), "认证信息无效") {
		t.Fatalf("bad ticket: %d body=%s want 401 认证信息无效", resp.StatusCode, body)
	}

	// 3. purpose 非 sse-ticket（access token）→ 401 无效的ticket
	accessTok, err := signer.Sign(primitive.NewObjectID().Hex(), "access", time.Minute, nil)
	if err != nil {
		t.Fatalf("sign access: %v", err)
	}
	resp, err = http.Get(ts.URL + "/api/notifications/stream?ticket=" + url.QueryEscape(accessTok))
	if err != nil {
		t.Fatalf("wrong purpose req: %v", err)
	}
	body = readRespBody(resp)
	if resp.StatusCode != 401 || !strings.Contains(string(body), "无效的ticket") {
		t.Fatalf("wrong purpose: %d body=%s want 401 无效的ticket", resp.StatusCode, body)
	}
}

// TestNotificationsStreamConnected 合法 sse-ticket → 200 + connected 事件 + SSE 头。
func TestNotificationsStreamConnected(t *testing.T) {
	ts, _ := notificationsTestServer(t)

	// 走真实链路：注册登录 → GET /api/auth/sse-ticket 拿票据。
	c := newClient(t, ts.URL)
	notificationTestLogin(t, c)
	var out map[string]any
	resp := c.json("GET", "/api/auth/sse-ticket", nil, &out)
	if resp.StatusCode != 200 || out["ticket"] == nil {
		t.Fatalf("sse-ticket: %d body=%v", resp.StatusCode, out)
	}
	ticket := out["ticket"].(string)

	req, _ := http.NewRequest("GET", ts.URL+"/api/notifications/stream?ticket="+url.QueryEscape(ticket), nil)
	sresp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("stream req: %v", err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != 200 {
		t.Fatalf("stream status: %d", sresp.StatusCode)
	}
	if ct := sresp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream content-type: %q want text/event-stream", ct)
	}
	br := bufio.NewReader(sresp.Body)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read connected event: %v", err)
	}
	if strings.TrimSpace(line) != `data: {"type":"connected"}` {
		t.Fatalf("first event: %q want connected", line)
	}
	// 关闭连接 → 服务端清理（CloseClientConnections 触发 closeNotify）。
	sresp.Body.Close()
	ts.CloseClientConnections()
}

// TestNotificationsReadFlow 未读数/列表分页/全部已读/单条已读/清理/删除。
func TestNotificationsReadFlow(t *testing.T) {
	ts, repos := notificationsTestServer(t)
	c := newClient(t, ts.URL)
	userIDHex := notificationTestLogin(t, c)
	userID, _ := primitive.ObjectIDFromHex(userIDHex)
	ctx := context.Background()

	var out map[string]any
	resp := c.json("GET", "/api/notifications/unread-count", nil, &out)
	if resp.StatusCode != 200 || out["count"].(float64) != 0 {
		t.Fatalf("unread-count init: %d body=%v", resp.StatusCode, out)
	}

	// 插入 3 条通知（2 未读 1 已读）。
	now := time.Now().UTC().Truncate(time.Millisecond)
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		n := &model.Notification{
			ID:        primitive.NewObjectID(),
			UserID:    userID,
			Type:      "announcement",
			Message:   fmt.Sprintf("msg%d", i),
			IsRead:    i == 2,
			CreatedAt: now,
		}
		if err := repos.Notifications.Create(ctx, n); err != nil {
			t.Fatalf("create notif: %v", err)
		}
		ids = append(ids, n.ID.Hex())
	}

	resp = c.json("GET", "/api/notifications/unread-count", nil, &out)
	if resp.StatusCode != 200 || out["count"].(float64) != 2 {
		t.Fatalf("unread-count: %d body=%v want 2", resp.StatusCode, out)
	}

	// 分页：page=1&limit=2 → list 2 条、total 3、totalPages 2。
	resp = c.json("GET", "/api/notifications/list?page=1&limit=2", nil, &out)
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	if out["total"].(float64) != 3 || out["totalPages"].(float64) != 2 ||
		out["page"].(float64) != 1 || out["limit"].(float64) != 2 {
		t.Fatalf("list paging: %v", out)
	}
	list, _ := out["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("list len: %d want 2", len(list))
	}
	first := list[0].(map[string]any)
	if first["_id"] == nil || first["userId"] != userIDHex || first["episodeId"] != nil {
		t.Fatalf("list row shape: %v", first)
	}

	// PUT /read/:id 标记一条 → 未读变 1。
	resp = c.json("PUT", "/api/notifications/read/"+ids[0], nil, &out)
	if resp.StatusCode != 200 || out["message"] != "Marked as read" {
		t.Fatalf("read: %d body=%v", resp.StatusCode, out)
	}
	resp = c.json("GET", "/api/notifications/unread-count", nil, &out)
	if out["count"].(float64) != 1 {
		t.Fatalf("after read: %v want 1", out)
	}

	// PUT /read-episode/:episodeId 不存在剧集 → 无匹配仍 200（updateMany 幂等）。
	resp = c.json("PUT", "/api/notifications/read-episode/"+primitive.NewObjectID().Hex(), nil, &out)
	if resp.StatusCode != 200 || out["message"] != "Episode notifications marked as read" {
		t.Fatalf("read-episode: %d body=%v", resp.StatusCode, out)
	}

	// PUT /read-all → 全部已读。
	resp = c.json("PUT", "/api/notifications/read-all", nil, &out)
	if resp.StatusCode != 200 || out["message"] != "All marked as read" {
		t.Fatalf("read-all: %d body=%v", resp.StatusCode, out)
	}
	resp = c.json("GET", "/api/notifications/unread-count", nil, &out)
	if out["count"].(float64) != 0 {
		t.Fatalf("after read-all: %v want 0", out)
	}

	// DELETE /:id 删除一条 → total 变 2。
	resp = c.json("DELETE", "/api/notifications/"+ids[0], nil, &out)
	if resp.StatusCode != 200 || out["message"] != "Notification deleted" {
		t.Fatalf("delete: %d body=%v", resp.StatusCode, out)
	}
	resp = c.json("GET", "/api/notifications/list", nil, &out)
	if out["total"].(float64) != 2 {
		t.Fatalf("after delete: %v want total 2", out)
	}

	// DELETE /clear-read → 删除全部已读（剩 1 未读…已全部读过 → 0）。
	resp = c.json("DELETE", "/api/notifications/clear-read", nil, &out)
	if resp.StatusCode != 200 || out["message"] != "Read notifications cleared" {
		t.Fatalf("clear-read: %d body=%v", resp.StatusCode, out)
	}
	resp = c.json("GET", "/api/notifications/list", nil, &out)
	if out["total"].(float64) != 0 {
		t.Fatalf("after clear-read: %v want 0", out)
	}
}

// TestNotificationsSubscribeReminder 订阅提醒：校验/404/创建追番/幂等。
func TestNotificationsSubscribeReminder(t *testing.T) {
	ts, repos := notificationsTestServer(t)
	c := newClient(t, ts.URL)
	userIDHex := notificationTestLogin(t, c)
	userID, _ := primitive.ObjectIDFromHex(userIDHex)
	ctx := context.Background()

	var out map[string]any
	// 缺 episodeId → 400
	resp := c.json("POST", "/api/notifications/subscribe-reminder", map[string]any{}, &out)
	if resp.StatusCode != 400 || out["message"] != "缺少剧集ID" {
		t.Fatalf("missing id: %d body=%v", resp.StatusCode, out)
	}
	// 非法 hex → 500 Server error（对齐 mongoose CastError → catch）
	resp = c.json("POST", "/api/notifications/subscribe-reminder", map[string]any{"episodeId": "xyz"}, &out)
	if resp.StatusCode != 500 || out["message"] != "Server error" {
		t.Fatalf("bad hex: %d body=%v", resp.StatusCode, out)
	}
	// 剧集不存在 → 404
	resp = c.json("POST", "/api/notifications/subscribe-reminder",
		map[string]any{"episodeId": primitive.NewObjectID().Hex()}, &out)
	if resp.StatusCode != 404 || out["message"] != "剧集不存在" {
		t.Fatalf("no episode: %d body=%v", resp.StatusCode, out)
	}

	// 建剧集并订阅 → 200 subscribed + 追番 createdAt/followedAtEpisodes。
	ep := &model.Episode{Title: "T", Description: "D", Status: "ongoing", CurrentEpisodes: 3, ReviewStatus: "approved"}
	if err := repos.Episodes.Create(ctx, ep); err != nil {
		t.Fatalf("create episode: %v", err)
	}
	resp = c.json("POST", "/api/notifications/subscribe-reminder",
		map[string]any{"episodeId": ep.ID.Hex()}, &out)
	if resp.StatusCode != 200 || out["message"] != "订阅提醒成功" || out["subscribed"] != true {
		t.Fatalf("subscribe: %d body=%v", resp.StatusCode, out)
	}
	follow, err := repos.Follows.FollowFindByUserEpisode(ctx, userID, ep.ID)
	if err != nil {
		t.Fatalf("follow not created: %v", err)
	}
	if follow.FollowedAtEpisodes != 3 {
		t.Fatalf("followedAtEpisodes=%d want 3", follow.FollowedAtEpisodes)
	}
	// 重复订阅 → 幂等 200，不重复创建。
	resp = c.json("POST", "/api/notifications/subscribe-reminder",
		map[string]any{"episodeId": ep.ID.Hex()}, &out)
	if resp.StatusCode != 200 || out["subscribed"] != true {
		t.Fatalf("resubscribe: %d body=%v", resp.StatusCode, out)
	}
	count, err := repos.Follows.FollowCount(ctx, bson.M{"userId": userID, "episodeId": ep.ID})
	if err != nil {
		t.Fatalf("count follow: %v", err)
	}
	if count != 1 {
		t.Fatalf("follow count=%d want 1", count)
	}
}

// TestNotificationsPushSubscribe Web Push 订阅保存/取消。
func TestNotificationsPushSubscribe(t *testing.T) {
	ts, _ := notificationsTestServer(t)
	c := newClient(t, ts.URL)
	notificationTestLogin(t, c)

	var out map[string]any
	// 无 subscription → 400
	resp := c.json("POST", "/api/notifications/push/subscribe", map[string]any{}, &out)
	if resp.StatusCode != 400 || out["message"] != "无效的订阅信息" {
		t.Fatalf("no subscription: %d body=%v", resp.StatusCode, out)
	}
	// subscription 无 endpoint → 400
	resp = c.json("POST", "/api/notifications/push/subscribe",
		map[string]any{"subscription": map[string]any{}}, &out)
	if resp.StatusCode != 400 || out["message"] != "无效的订阅信息" {
		t.Fatalf("empty subscription: %d body=%v", resp.StatusCode, out)
	}
	// 合法 → 200 推送订阅成功
	resp = c.json("POST", "/api/notifications/push/subscribe", map[string]any{
		"subscription": map[string]any{
			"endpoint": "https://push.example.com/ep1",
			"keys":     map[string]any{"p256dh": "aaa", "auth": "bbb"},
		},
	}, &out)
	if resp.StatusCode != 200 || out["message"] != "推送订阅成功" {
		t.Fatalf("subscribe: %d body=%v", resp.StatusCode, out)
	}
	// 重复订阅同一 endpoint → 幂等 200
	resp = c.json("POST", "/api/notifications/push/subscribe", map[string]any{
		"subscription": map[string]any{"endpoint": "https://push.example.com/ep1"},
	}, &out)
	if resp.StatusCode != 200 {
		t.Fatalf("resubscribe: %d body=%v", resp.StatusCode, out)
	}
	// 缺 endpoint → 400
	resp = c.json("POST", "/api/notifications/push/unsubscribe", map[string]any{}, &out)
	if resp.StatusCode != 400 || out["message"] != "缺少endpoint" {
		t.Fatalf("unsub no endpoint: %d body=%v", resp.StatusCode, out)
	}
	// 取消 → 200
	resp = c.json("POST", "/api/notifications/push/unsubscribe",
		map[string]any{"endpoint": "https://push.example.com/ep1"}, &out)
	if resp.StatusCode != 200 || out["message"] != "取消推送订阅成功" {
		t.Fatalf("unsubscribe: %d body=%v", resp.StatusCode, out)
	}
}

// TestNotificationsVapidPublicKey VAPID 公钥端点（未配置输出 null）。
func TestNotificationsVapidPublicKey(t *testing.T) {
	ts, _ := notificationsTestServer(t)
	resp, err := http.Get(ts.URL + "/api/notifications/vapid-public-key")
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	body := readRespBody(resp)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"publicKey":null`) {
		t.Fatalf("vapid: %d body=%s", resp.StatusCode, body)
	}
}

// readRespBody 读取并关闭响应体。
func readRespBody(resp *http.Response) []byte {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}
