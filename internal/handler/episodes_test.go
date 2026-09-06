package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/handler"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/upload"
)

// episodesTestApp 组装仅含剧集路由的测试服务器（真实 mongod 127.0.0.1:27017）。
func episodesTestApp(t *testing.T) (*repository.Repos, *auth.Signer, *httptest.Server) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.IsDev = true
	cfg.Server.NodeEnv = "development"
	cfg.Server.Listen = "tcp:127.0.0.1:0"
	cfg.JWT.Secret = strings.Repeat("s", 40)
	cfg.JWT.AccessTTL = 15 * time.Minute
	cfg.JWT.RefreshTTL = 7 * 24 * time.Hour
	cfg.Security.LoginMaxAttempts = 5
	cfg.Security.LoginLockMinutes = 30
	cfg.Server.AllowOrigins = []string{"http://localhost:3000"}

	db, err := repository.Connect(t.Context(), "mongodb://127.0.0.1:27017/neo_episodes_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	// 清库（含上次运行残留），t.Context() 在 cleanup 时已取消，故用 Background。
	if err := db.Drop(context.Background()); err != nil {
		t.Fatalf("drop db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	amw := middleware.NewAuth(repos, signer)
	ep := handler.NewEpisodes(repos, cfg, amw, func(spec ratelimit.Spec) gin.HandlerFunc {
		return middleware.RateLimit(spec, middleware.RateLimitOpts{IsDev: true, SkipRateLimit: true})
	}, nil)

	// 清全局缓存，避免跨测试污染。
	middleware.EpisodeCache.DeleteByPrefix("")

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.BodyParse())
	r.Use(middleware.SanitizeInput())
	r.GET("/api/csrf-token", handler.CSRF(cfg))
	r.Use(middleware.CSRF())
	api := r.Group("/api")
	ep.Register(api.Group("/episodes"))

	upload.SetDir(t.TempDir())
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return repos, signer, ts
}

// epClient 是带 cookie jar + CSRF 自动回填 + Bearer token 的测试客户端。
type epClient struct {
	http  *http.Client
	base  string
	token string
	csrf  string
}

func newEPClient(t *testing.T, base string, accessToken string) *epClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &epClient{http: &http.Client{Jar: jar}, base: base, token: accessToken}
	resp, err := c.http.Get(base + "/api/csrf-token")
	if err != nil {
		t.Fatalf("csrf: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	c.csrf = body.CSRF
	return c
}

func (c *epClient) do(method, path string, body any) (*http.Response, []byte) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.csrf != "" {
		req.Header.Set("X-XSRF-TOKEN", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return resp, nil
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func (c *epClient) json(method, path string, body any, out any) *http.Response {
	resp, data := c.do(method, path, body)
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	return resp
}

// mustToken 签发 access token（id 为 hex 字符串）。
func mustToken(t *testing.T, s *auth.Signer, idHex string) string {
	t.Helper()
	tok, err := s.Sign(idHex, "access", 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

// insertUser 直接插入测试用户。
func insertUser(t *testing.T, repos *repository.Repos, id string, role string) primitive.ObjectID {
	t.Helper()
	oid, _ := primitive.ObjectIDFromHex(id)
	u := &model.User{
		ID:                     oid,
		AccountID:              "acc_" + id[:8],
		Username:               "u_" + id[:8],
		Email:                  id[:8] + "@test.com",
		Role:                   role,
		IsEmailVerified:        true,
		EmailNotificationPrefs: model.DefaultEmailNotificationPrefs(),
	}
	if err := repos.Users.Create(t.Context(), u); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return oid
}

// TestEpisodesCreateListDetail 创建 → 列表 → 详情主流程。
func TestEpisodesCreateListDetail(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	creatorID := primitive.NewObjectID().Hex()
	insertUser(t, repos, creatorID, "creator")
	creator := newEPClient(t, ts.URL, mustToken(t, signer, creatorID))

	var created map[string]any
	resp := creator.json("POST", "/api/episodes", map[string]any{
		"title": "测试剧集", "description": "desc", "coverImage": "/uploads/c.png",
		"status": "ongoing", "tags": []string{"兽剧", "奇幻"}, "category": []string{"动画"},
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d body=%v", resp.StatusCode, created)
	}
	if created["_id"] == "" || created["_id"] == "000000000000000000000000" {
		t.Fatalf("create: bad _id %v", created["_id"])
	}
	if created["reviewStatus"] != "pending" {
		t.Fatalf("create: reviewStatus=%v", created["reviewStatus"])
	}
	if created["__v"] != float64(0) {
		t.Fatalf("create: __v=%v", created["__v"])
	}
	if created["tags"] == nil || len(created["tags"].([]any)) != 2 {
		t.Fatalf("create: tags=%v", created["tags"])
	}
	episodeID := created["_id"].(string)

	// 列表：pending 剧集不可见。
	var list map[string]any
	resp = creator.json("GET", "/api/episodes?page=1&limit=10", nil, &list)
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	if len(list["episodes"].([]any)) != 0 {
		t.Fatalf("list: pending should be hidden, got %v", list["episodes"])
	}
	if list["total"] != float64(0) {
		t.Fatalf("list: total=%v", list["total"])
	}

	// 置为 approved 后可见。
	if _, err := repos.Episodes.FindOneAndUpdate(t.Context(), episodeID,
		bson.M{"$set": bson.M{"reviewStatus": "approved"}}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	middleware.EpisodeCache.DeleteByPrefix("episodes_")
	resp = creator.json("GET", "/api/episodes?page=1&limit=10", nil, &list)
	if resp.StatusCode != 200 || len(list["episodes"].([]any)) != 1 {
		t.Fatalf("list after approve: %d %v", resp.StatusCode, list)
	}
	if list["totalPages"] != float64(1) || list["page"] != float64(1) || list["limit"] != float64(10) {
		t.Fatalf("list pagination: %v", list)
	}

	// 详情：无 token 可见 approved 剧集，createdBy 已 populate。
	anon := newEPClient(t, ts.URL, "")
	var detail map[string]any
	resp = anon.json("GET", "/api/episodes/"+episodeID, nil, &detail)
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %d body=%v", resp.StatusCode, detail)
	}
	if detail["title"] != "测试剧集" {
		t.Fatalf("detail title: %v", detail["title"])
	}
	if detail["createdBy"] == nil || detail["createdBy"].(map[string]any)["username"] == "" {
		t.Fatalf("detail createdBy not populated: %v", detail["createdBy"])
	}
	if detail["episodes"] == nil {
		t.Fatalf("detail episodes missing")
	}
}

// TestEpisodesDetailVisibility 未审核剧集仅创作者/管理员可见。
func TestEpisodesDetailVisibility(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	creatorID := primitive.NewObjectID().Hex()
	insertUser(t, repos, creatorID, "creator")
	adminID := primitive.NewObjectID().Hex()
	insertUser(t, repos, adminID, "admin")
	otherID := primitive.NewObjectID().Hex()
	insertUser(t, repos, otherID, "user")

	creator := newEPClient(t, ts.URL, mustToken(t, signer, creatorID))
	admin := newEPClient(t, ts.URL, mustToken(t, signer, adminID))
	other := newEPClient(t, ts.URL, mustToken(t, signer, otherID))
	anon := newEPClient(t, ts.URL, "")

	var created map[string]any
	resp := creator.json("POST", "/api/episodes", map[string]any{
		"title": "待审核", "description": "d", "coverImage": "/uploads/c.png",
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	episodeID := created["_id"].(string)

	var out map[string]any
	if resp = anon.json("GET", "/api/episodes/"+episodeID, nil, &out); resp.StatusCode != 404 {
		t.Fatalf("anon pending detail: %d", resp.StatusCode)
	}
	if resp = other.json("GET", "/api/episodes/"+episodeID, nil, &out); resp.StatusCode != 404 {
		t.Fatalf("user pending detail: %d", resp.StatusCode)
	}
	if resp = creator.json("GET", "/api/episodes/"+episodeID, nil, &out); resp.StatusCode != 200 {
		t.Fatalf("creator pending detail: %d", resp.StatusCode)
	}
	if resp = admin.json("GET", "/api/episodes/"+episodeID, nil, &out); resp.StatusCode != 200 {
		t.Fatalf("admin pending detail: %d", resp.StatusCode)
	}
}

// TestEpisodesViewCount 观看计数 +1，冷却期内不重复计数。
func TestEpisodesViewCount(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	adminID := primitive.NewObjectID().Hex()
	insertUser(t, repos, adminID, "admin")
	admin := newEPClient(t, ts.URL, mustToken(t, signer, adminID))

	var created map[string]any
	resp := admin.json("POST", "/api/episodes", map[string]any{
		"title": "计数", "description": "d", "coverImage": "/uploads/c.png",
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	episodeID := created["_id"].(string)

	var v1 map[string]any
	resp = admin.json("PUT", "/api/episodes/"+episodeID+"/view", map[string]any{}, &v1)
	if resp.StatusCode != 200 || v1["views"] != float64(1) {
		t.Fatalf("view: %d %v", resp.StatusCode, v1)
	}
	// 10 分钟冷却内：第二次不计数。
	var v2 map[string]any
	resp = admin.json("PUT", "/api/episodes/"+episodeID+"/view", map[string]any{}, &v2)
	if resp.StatusCode != 200 || v2["views"] != float64(1) {
		t.Fatalf("view cooldown: %d %v", resp.StatusCode, v2)
	}
}

// TestEpisodesCreateSingleAndNotification 添加可观看单集 → currentEpisodes+1 且追番用户收到通知。
func TestEpisodesCreateSingleAndNotification(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	adminID := primitive.NewObjectID().Hex()
	insertUser(t, repos, adminID, "admin")
	followerID := primitive.NewObjectID().Hex()
	insertUser(t, repos, followerID, "user")
	admin := newEPClient(t, ts.URL, mustToken(t, signer, adminID))

	var created map[string]any
	resp := admin.json("POST", "/api/episodes", map[string]any{
		"title": "单集", "description": "d", "coverImage": "/uploads/c.png",
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	episodeID := created["_id"].(string)
	episodeOID, _ := primitive.ObjectIDFromHex(episodeID)
	followerOID, _ := primitive.ObjectIDFromHex(followerID)
	if err := repos.Follows.FollowInsert(t.Context(), &model.Follow{
		UserID: followerOID, EpisodeID: episodeOID, FollowedAtEpisodes: 0,
	}); err != nil {
		t.Fatalf("follow: %v", err)
	}

	var single map[string]any
	resp = admin.json("POST", "/api/episodes/"+episodeID+"/episodes", map[string]any{
		"episodeNumber": 1, "title": "第一集",
	}, &single)
	if resp.StatusCode != 201 {
		t.Fatalf("create single: %d body=%v", resp.StatusCode, single)
	}
	if single["episodeId"] != episodeID {
		t.Fatalf("single episodeId: %v", single["episodeId"])
	}

	var ep map[string]any
	resp = admin.json("GET", "/api/episodes/"+episodeID, nil, &ep)
	if resp.StatusCode != 200 || ep["currentEpisodes"] != float64(1) {
		t.Fatalf("currentEpisodes: %d %v", resp.StatusCode, ep["currentEpisodes"])
	}
	if len(ep["episodes"].([]any)) != 1 {
		t.Fatalf("episodes list: %v", ep["episodes"])
	}

	// 追番用户收到 new_episode 通知。
	notifs, err := repos.Notifications.FindByUser(t.Context(), followerOID, 1, 20)
	if err != nil || len(notifs) != 1 {
		t.Fatalf("notifications: %v %d", err, len(notifs))
	}
	if notifs[0].Type != "new_episode" || notifs[0].Message != "《单集》更新了第1集" {
		t.Fatalf("notification content: %+v", notifs[0])
	}
}

// TestEpisodesUpdateSingleBecameAvailable 预告转可观看 → currentEpisodes+1 并通知。
func TestEpisodesUpdateSingleBecameAvailable(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	adminID := primitive.NewObjectID().Hex()
	insertUser(t, repos, adminID, "admin")
	followerID := primitive.NewObjectID().Hex()
	insertUser(t, repos, followerID, "user")
	admin := newEPClient(t, ts.URL, mustToken(t, signer, adminID))

	var created map[string]any
	resp := admin.json("POST", "/api/episodes", map[string]any{
		"title": "预告剧", "description": "d", "coverImage": "/uploads/c.png",
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	episodeID := created["_id"].(string)
	episodeOID, _ := primitive.ObjectIDFromHex(episodeID)
	followerOID, _ := primitive.ObjectIDFromHex(followerID)

	// 创建预告集（isScheduled=true → isUpcoming=true，不计入 currentEpisodes）。
	var single map[string]any
	resp = admin.json("POST", "/api/episodes/"+episodeID+"/episodes", map[string]any{
		"episodeNumber": 1, "title": "预告", "isScheduled": true,
	}, &single)
	if resp.StatusCode != 201 {
		t.Fatalf("create scheduled: %d %v", resp.StatusCode, single)
	}
	singleID := single["_id"].(string)
	var ep map[string]any
	resp = admin.json("GET", "/api/episodes/"+episodeID, nil, &ep)
	if resp.StatusCode != 200 || ep["currentEpisodes"] != float64(0) {
		t.Fatalf("scheduled should not count: %v", ep["currentEpisodes"])
	}

	// 此时再追番，确保只有"转可观看"触发通知。
	if err := repos.Follows.FollowInsert(t.Context(), &model.Follow{
		UserID: followerOID, EpisodeID: episodeOID, FollowedAtEpisodes: 0,
	}); err != nil {
		t.Fatalf("follow: %v", err)
	}

	// 转可观看：isScheduled=false → becameAvailable。
	resp = admin.json("PUT", "/api/episodes/single/"+singleID, map[string]any{"isScheduled": false}, &single)
	if resp.StatusCode != 200 {
		t.Fatalf("update single: %d %v", resp.StatusCode, single)
	}
	if single["isUpcoming"] != false {
		t.Fatalf("isUpcoming should sync to false: %v", single["isUpcoming"])
	}
	resp = admin.json("GET", "/api/episodes/"+episodeID, nil, &ep)
	if resp.StatusCode != 200 || ep["currentEpisodes"] != float64(1) {
		t.Fatalf("became available: currentEpisodes=%v", ep["currentEpisodes"])
	}
	notifs, err := repos.Notifications.FindByUser(t.Context(), followerOID, 1, 20)
	if err != nil || len(notifs) != 1 {
		t.Fatalf("notifications: %v %d", err, len(notifs))
	}
	if notifs[0].Message != "《预告剧》更新了第1集" {
		t.Fatalf("notification message: %q", notifs[0].Message)
	}
}

// TestEpisodesResubmitAndDelete 重新提交审核 + 删除清理。
func TestEpisodesResubmitAndDelete(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	creatorID := primitive.NewObjectID().Hex()
	insertUser(t, repos, creatorID, "creator")
	adminID := primitive.NewObjectID().Hex()
	insertUser(t, repos, adminID, "admin")
	creator := newEPClient(t, ts.URL, mustToken(t, signer, creatorID))
	admin := newEPClient(t, ts.URL, mustToken(t, signer, adminID))

	var created map[string]any
	resp := creator.json("POST", "/api/episodes", map[string]any{
		"title": "审核", "description": "d", "coverImage": "/uploads/c.png",
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	episodeID := created["_id"].(string)
	episodeOID, _ := primitive.ObjectIDFromHex(episodeID)

	// 置为 rejected。
	if _, err := repos.Episodes.FindOneAndUpdate(t.Context(), episodeID,
		bson.M{"$set": bson.M{"reviewStatus": "rejected"}}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	// 重新提交。
	var resub map[string]any
	resp = creator.json("POST", "/api/episodes/"+episodeID+"/resubmit", nil, &resub)
	if resp.StatusCode != 200 || resub["reviewStatus"] != "pending" {
		t.Fatalf("resubmit: %d %v", resp.StatusCode, resub)
	}
	// 非 rejected 状态再提交 → 400。
	resp = creator.json("POST", "/api/episodes/"+episodeID+"/resubmit", nil, &resub)
	if resp.StatusCode != 400 {
		t.Fatalf("resubmit non-rejected: %d", resp.StatusCode)
	}

	// 管理员删除 → 整体移入回收站（前台即刻不可见；关联数据保留，
	// 供回收站恢复；彻底删除时才清理）。
	if err := repos.SingleEpisodes.Create(t.Context(), &model.SingleEpisode{
		EpisodeID: episodeOID, EpisodeNumber: 1, Title: "s",
	}); err != nil {
		t.Fatalf("single create: %v", err)
	}
	followerID := primitive.NewObjectID().Hex()
	followerOID, _ := primitive.ObjectIDFromHex(followerID)
	if err := repos.Follows.FollowInsert(t.Context(), &model.Follow{
		UserID: followerOID, EpisodeID: episodeOID,
	}); err != nil {
		t.Fatalf("follow: %v", err)
	}
	var del map[string]any
	resp = admin.json("DELETE", "/api/episodes/"+episodeID, nil, &del)
	if resp.StatusCode != 200 || del["message"] != "Episode deleted" {
		t.Fatalf("delete: %d %v", resp.StatusCode, del)
	}
	// episodes 集合中已移除 → 详情 404。
	if _, err := repos.Episodes.FindByID(t.Context(), episodeOID); !repository.IsNotFound(err) {
		t.Fatalf("episode still in episodes: %v", err)
	}
	// 回收站中可查到（原因 deleted）。
	trashed, err := repos.EpisodeTrash.FindByID(t.Context(), episodeOID)
	if err != nil {
		t.Fatalf("not in trash: %v", err)
	}
	if trashed.TrashReason != "deleted" {
		t.Fatalf("trash reason: %s", trashed.TrashReason)
	}
	// 关联数据保留（回收站恢复语义）。
	singles, _ := repos.SingleEpisodes.FindByEpisode(t.Context(), episodeOID)
	if len(singles) != 1 {
		t.Fatalf("singles not preserved: %d", len(singles))
	}
	follows, _ := repos.Follows.EpisodesFindByEpisode(t.Context(), episodeOID)
	if len(follows) != 1 {
		t.Fatalf("follows not preserved: %d", len(follows))
	}
	// 不存在再删 → 404。
	resp = admin.json("DELETE", "/api/episodes/"+episodeID, nil, &del)
	if resp.StatusCode != 404 {
		t.Fatalf("delete missing: %d", resp.StatusCode)
	}
	// 彻底删除回收站条目 → 关联数据被清理。
	if _, err := repos.EpisodeTrash.Purge(t.Context(), episodeOID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	singles, _ = repos.SingleEpisodes.FindByEpisode(t.Context(), episodeOID)
	if len(singles) != 0 {
		t.Fatalf("singles not purged: %d", len(singles))
	}
	follows, _ = repos.Follows.EpisodesFindByEpisode(t.Context(), episodeOID)
	if len(follows) != 0 {
		t.Fatalf("follows not purged: %d", len(follows))
	}
}

// TestEpisodesUserStatus 用户状态端点。
func TestEpisodesUserStatus(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	adminID := primitive.NewObjectID().Hex()
	insertUser(t, repos, adminID, "admin")
	admin := newEPClient(t, ts.URL, mustToken(t, signer, adminID))

	var created map[string]any
	resp := admin.json("POST", "/api/episodes", map[string]any{
		"title": "状态", "description": "d", "coverImage": "/uploads/c.png",
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	episodeID := created["_id"].(string)
	adminOID, _ := primitive.ObjectIDFromHex(adminID)
	episodeOID, _ := primitive.ObjectIDFromHex(episodeID)

	var st map[string]any
	resp = admin.json("GET", "/api/episodes/"+episodeID+"/user-status", nil, &st)
	if resp.StatusCode != 200 {
		t.Fatalf("user-status: %d", resp.StatusCode)
	}
	if st["isFollowing"] != false || st["isFavorite"] != false || st["score"] != float64(0) {
		t.Fatalf("empty status: %v", st)
	}
	if st["watchedEpisodes"] == nil {
		t.Fatalf("watchedEpisodes should be [] not null: %v", st["watchedEpisodes"])
	}
	if st["followedAtEpisodes"] != nil {
		t.Fatalf("followedAtEpisodes should be null: %v", st["followedAtEpisodes"])
	}

	// 追番后 isFollowing=true。
	if err := repos.Follows.FollowInsert(t.Context(), &model.Follow{
		UserID: adminOID, EpisodeID: episodeOID, FollowedAtEpisodes: 3,
	}); err != nil {
		t.Fatalf("follow: %v", err)
	}
	resp = admin.json("GET", "/api/episodes/"+episodeID+"/user-status", nil, &st)
	if resp.StatusCode != 200 || st["isFollowing"] != true || st["followedAtEpisodes"] != float64(3) {
		t.Fatalf("following status: %v", st)
	}
}

// TestEpisodesSearchSuggestionsPopularTags 搜索建议 / 热门标签 / 搜索。
func TestEpisodesSearchSuggestionsPopularTags(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	adminID := primitive.NewObjectID().Hex()
	insertUser(t, repos, adminID, "admin")
	admin := newEPClient(t, ts.URL, mustToken(t, signer, adminID))

	var created map[string]any
	resp := admin.json("POST", "/api/episodes", map[string]any{
		"title": "奇幻之森", "titleEn": "Forest Fantasy", "description": "一段奇幻旅程",
		"coverImage": "/uploads/c.png", "tags": []string{"奇幻", "冒险"},
	}, &created)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	// admin 创建的剧集 reviewStatus=approved（非 creator 角色），公开可见。

	var sug map[string]any
	resp = admin.json("GET", "/api/episodes/search-suggestions?q=奇幻", nil, &sug)
	if resp.StatusCode != 200 || len(sug["titles"].([]any)) == 0 {
		t.Fatalf("suggestions titles: %d %v", resp.StatusCode, sug)
	}
	if len(sug["tags"].([]any)) == 0 {
		t.Fatalf("suggestions tags: %v", sug)
	}

	var ptags []any
	resp = admin.json("GET", "/api/episodes/popular-tags", nil, &ptags)
	if resp.StatusCode != 200 || len(ptags) == 0 {
		t.Fatalf("popular-tags: %d %v", resp.StatusCode, ptags)
	}
	// 两标签 count 并列（各 1），排序兜底为名称二进制序：冒险(U+5192) < 奇幻(U+5947)。
	if ptags[0].(map[string]any)["name"] != "冒险" {
		t.Fatalf("popular-tags first: %v", ptags[0])
	}

	var sres []any
	resp = admin.json("GET", "/api/episodes/search?q=奇幻&limit=10", nil, &sres)
	if resp.StatusCode != 200 || len(sres) != 1 {
		t.Fatalf("search: %d %v", resp.StatusCode, sres)
	}
	item := sres[0].(map[string]any)
	if item["title"] != "奇幻之森" || item["averageRating"] != float64(0) {
		t.Fatalf("search item: %v", item)
	}
	// 空 q → []。
	resp = admin.json("GET", "/api/episodes/search?q=", nil, &sres)
	if resp.StatusCode != 200 || len(sres) != 0 {
		t.Fatalf("search empty: %d", resp.StatusCode)
	}
}

// TestEpisodesPermission 权限：普通用户不能创建；未登录 user-status 401；非法 id 详情 500。
func TestEpisodesPermission(t *testing.T) {
	repos, signer, ts := episodesTestApp(t)
	userID := primitive.NewObjectID().Hex()
	insertUser(t, repos, userID, "user")
	u := newEPClient(t, ts.URL, mustToken(t, signer, userID))

	var out map[string]any
	resp := u.json("POST", "/api/episodes", map[string]any{
		"title": "x", "description": "d", "coverImage": "/uploads/c.png",
	}, &out)
	if resp.StatusCode != 403 {
		t.Fatalf("user create should 403: %d %v", resp.StatusCode, out)
	}
	anon := newEPClient(t, ts.URL, "")
	resp = anon.json("GET", "/api/episodes/abc/user-status", nil, &out)
	if resp.StatusCode != 401 {
		t.Fatalf("anon user-status should 401: %d", resp.StatusCode)
	}
	// 非法 id 详情 → 500（mongoose CastError 语义）。
	resp = anon.json("GET", "/api/episodes/not-a-valid-id", nil, &out)
	if resp.StatusCode != 500 {
		t.Fatalf("bad id detail should 500: %d %v", resp.StatusCode, out)
	}
}
