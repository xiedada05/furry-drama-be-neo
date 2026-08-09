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
)

// newHrServer 组装只挂载 /api/histories 与 /api/ratios 的测试服务器
// （真实 mongod，DB 名唯一，测试结束丢弃）。
func newHrServer(t *testing.T) (*httptest.Server, *mongo.Database, *repository.Repos, *auth.Signer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.IsDev = true
	cfg.JWT.Secret = strings.Repeat("s", 40)
	cfg.Security.LoginMaxAttempts = 5
	cfg.Security.LoginLockMinutes = 30

	dbName := fmt.Sprintf("neo_hr_%d", time.Now().UnixNano())
	db, err := repository.Connect(t.Context(), "mongodb://127.0.0.1:27017/"+dbName, "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(t.Context()) })

	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	amw := middleware.NewAuth(repos, signer)
	rl := func(spec ratelimit.Spec) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } }

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.BodyParse())
	api := r.Group("/api")
	handler.NewHistories(repos, cfg, amw, rl).Register(api.Group("/histories"))
	handler.NewRatings(repos, cfg, amw, rl).Register(api.Group("/ratings"))
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, db, repos, signer
}

// createUser 插入测试用户并返回其 ID 与 access token。
func createUser(t *testing.T, repos *repository.Repos, signer *auth.Signer) (primitive.ObjectID, string) {
	t.Helper()
	u := &model.User{
		ID:        primitive.NewObjectID(),
		AccountID: "hr_test",
		Username:  "hruser",
		Email:     "hr_test@test.com",
		Role:      middleware.RoleUser,
	}
	if err := repos.Users.Create(t.Context(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tok, err := signer.Sign(u.ID.Hex(), "access", 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return u.ID, tok
}

// insertEpisode 直接插入剧集（reviewStatus 缺省视为 approved）。
func insertEpisode(t *testing.T, db *mongo.Database, doc bson.M) primitive.ObjectID {
	t.Helper()
	id := primitive.NewObjectID()
	doc["_id"] = id
	if _, err := db.Collection("episodes").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("insert episode: %v", err)
	}
	return id
}

// doReq 发起带 Bearer token 的 JSON 请求，返回响应与 body。
func doReq(t *testing.T, base, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("req %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func decodeObj(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode body %q: %v", data, err)
	}
	return m
}

func decodeArr(t *testing.T, data []byte) []any {
	t.Helper()
	var a []any
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatalf("decode body %q: %v", data, err)
	}
	return a
}

// TestHistoriesRecordListCheckDelete 主流程：record → continue-watching → list →
// check → delete one → clear。
func TestHistoriesRecordListCheckDelete(t *testing.T) {
	ts, db, repos, signer := newHrServer(t)
	userID, tok := createUser(t, repos, signer)
	ep := insertEpisode(t, db, bson.M{
		"title": "hr-approve", "titleEn": "EN", "titleJa": "JA",
		"description": "d", "coverImage": "/uploads/c.png",
		"totalEpisodes": 12, "currentEpisodes": 5, "status": "ongoing",
		"category": []string{"furry"}, "tags": []string{"a"},
		"views": 7, "averageRating": 0, "ratingCount": 0,
		"reviewStatus": "approved",
	})

	// record ep 3
	resp, data := doReq(t, ts.URL, "POST", "/api/histories/record", tok, map[string]any{"episodeId": ep.Hex(), "episodeNumber": 3})
	if resp.StatusCode != 200 {
		t.Fatalf("record: %d %s", resp.StatusCode, data)
	}
	body := decodeObj(t, data)
	if body["userId"] != userID.Hex() || body["episodeId"] != ep.Hex() {
		t.Fatalf("record ids: %v", body)
	}
	watched, _ := body["watchedEpisodes"].([]any)
	if len(watched) != 1 || watched[0].(float64) != 3 {
		t.Fatalf("record watched: %v", body["watchedEpisodes"])
	}
	if body["__v"].(float64) != 0 {
		t.Fatalf("record __v: %v", body["__v"])
	}

	// record ep 5 → __v 1
	resp, data = doReq(t, ts.URL, "POST", "/api/histories/record", tok, map[string]any{"episodeId": ep.Hex(), "episodeNumber": 5})
	if resp.StatusCode != 200 || decodeObj(t, data)["__v"].(float64) != 1 {
		t.Fatalf("record2: %d %s", resp.StatusCode, data)
	}
	// record ep 3 again → 去重，__v 2
	resp, data = doReq(t, ts.URL, "POST", "/api/histories/record", tok, map[string]any{"episodeId": ep.Hex(), "episodeNumber": 3})
	if resp.StatusCode != 200 {
		t.Fatalf("record3: %d %s", resp.StatusCode, data)
	}
	body = decodeObj(t, data)
	if body["__v"].(float64) != 2 {
		t.Fatalf("record3 __v: %v", body["__v"])
	}
	watched, _ = body["watchedEpisodes"].([]any)
	if len(watched) != 2 || watched[1].(float64) != 5 {
		t.Fatalf("record3 watched: %v", body["watchedEpisodes"])
	}

	// continue-watching：populate episodeId 对象
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/continue-watching", tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("cw: %d %s", resp.StatusCode, data)
	}
	arr := decodeArr(t, data)
	if len(arr) != 1 {
		t.Fatalf("cw len: %d", len(arr))
	}
	row := arr[0].(map[string]any)
	epPop := row["episodeId"].(map[string]any)
	if epPop["title"] != "hr-approve" || epPop["titleEn"] != "EN" || epPop["totalEpisodes"].(float64) != 12 {
		t.Fatalf("cw populate: %v", epPop)
	}
	if row["__v"].(float64) != 2 {
		t.Fatalf("cw __v: %v", row["__v"])
	}
	// lastWatched 必须为 UTC（Z 后缀，对齐 mongoose toISOString，差分归一化依赖）
	if lw, _ := row["lastWatched"].(string); !strings.HasSuffix(lw, "Z") {
		t.Fatalf("cw lastWatched not UTC: %v", row["lastWatched"])
	}

	// list：分页响应 + populate
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/list", tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d %s", resp.StatusCode, data)
	}
	body = decodeObj(t, data)
	if body["page"].(float64) != 1 || body["limit"].(float64) != 20 || body["total"].(float64) != 1 || body["totalPages"].(float64) != 1 {
		t.Fatalf("list paging: %v", body)
	}
	lst, _ := body["list"].([]any)
	if len(lst) != 1 {
		t.Fatalf("list len: %d", len(lst))
	}

	// list 空 limit=0 → totalPages null（对齐 Math.ceil(total/0)=NaN）
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/list?limit=0", tok, nil)
	body = decodeObj(t, data)
	if body["limit"].(float64) != 0 || body["totalPages"] != nil {
		t.Fatalf("list limit=0: %v", body)
	}
	// list page=abc&limit=abc → page/limit/totalPages null
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/list?page=abc&limit=abc", tok, nil)
	body = decodeObj(t, data)
	if body["page"] != nil || body["limit"] != nil || body["totalPages"] != nil {
		t.Fatalf("list NaN: %v", body)
	}
	lst, _ = body["list"].([]any)
	if len(lst) != 1 {
		t.Fatalf("list NaN len: %d", len(lst))
	}
	// list limit=200 → 不钳制（对齐 histories.js 自解析）
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/list?limit=200", tok, nil)
	if decodeObj(t, data)["limit"].(float64) != 200 {
		t.Fatalf("list limit=200: %s", data)
	}
	// list page=0&limit=1 → 负 skip → 500（对齐 Express skip(-1) 抛错）
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/list?page=0&limit=1", tok, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("list page=0: %d %s", resp.StatusCode, data)
	}

	// check
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/check/"+ep.Hex(), tok, nil)
	body = decodeObj(t, data)
	if body["lastWatchedEpisodeNumber"].(float64) != 3 {
		t.Fatalf("check: %v", body)
	}
	watched, _ = body["watchedEpisodes"].([]any)
	if len(watched) != 2 {
		t.Fatalf("check watched: %v", body["watchedEpisodes"])
	}

	// check 无历史 → 空默认值
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/check/000000000000000000000000", tok, nil)
	body = decodeObj(t, data)
	if body["lastWatched"] != nil || body["lastWatchedEpisodeNumber"] != nil {
		t.Fatalf("check empty: %v", body)
	}
	if wa, _ := body["watchedEpisodes"].([]any); len(wa) != 0 {
		t.Fatalf("check empty watched: %v", body["watchedEpisodes"])
	}

	// delete one
	resp, data = doReq(t, ts.URL, "DELETE", "/api/histories/"+ep.Hex(), tok, nil)
	if resp.StatusCode != 200 || decodeObj(t, data)["message"] != "History deleted" {
		t.Fatalf("delete one: %d %s", resp.StatusCode, data)
	}
	// clear（无尾斜杠）
	resp, data = doReq(t, ts.URL, "DELETE", "/api/histories", tok, nil)
	if resp.StatusCode != 200 || decodeObj(t, data)["message"] != "All history cleared" {
		t.Fatalf("clear: %d %s", resp.StatusCode, data)
	}
	// clear 后 list → total 0、list []、totalPages 0
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/list", tok, nil)
	body = decodeObj(t, data)
	if body["total"].(float64) != 0 || body["totalPages"].(float64) != 0 {
		t.Fatalf("list after clear: %v", body)
	}
	lst, _ = body["list"].([]any)
	if len(lst) != 0 {
		t.Fatalf("list after clear len: %d", len(lst))
	}
}

// TestHistoriesRecordErrors record 校验分支。
func TestHistoriesRecordErrors(t *testing.T) {
	ts, _, repos, signer := newHrServer(t)
	_, tok := createUser(t, repos, signer)

	resp, data := doReq(t, ts.URL, "POST", "/api/histories/record", tok, map[string]any{"episodeId": "000000000000000000000000", "episodeNumber": "abc"})
	if resp.StatusCode != 400 || decodeObj(t, data)["message"] != "Invalid episode number" {
		t.Fatalf("invalid epnum: %d %s", resp.StatusCode, data)
	}
	resp, data = doReq(t, ts.URL, "POST", "/api/histories/record", tok, map[string]any{"episodeId": "000000000000000000000000", "episodeNumber": 1})
	if resp.StatusCode != 404 || decodeObj(t, data)["message"] != "Episode not found" {
		t.Fatalf("missing ep: %d %s", resp.StatusCode, data)
	}
	resp, data = doReq(t, ts.URL, "POST", "/api/histories/record", tok, map[string]any{"episodeNumber": 1})
	if resp.StatusCode != 404 || decodeObj(t, data)["message"] != "Episode not found" {
		t.Fatalf("no episodeId: %d %s", resp.StatusCode, data)
	}
	resp, data = doReq(t, ts.URL, "POST", "/api/histories/record", tok, map[string]any{"episodeId": "xyz", "episodeNumber": 1})
	if resp.StatusCode != 500 {
		t.Fatalf("bad hex: %d %s", resp.StatusCode, data)
	}
	resp, data = doReq(t, ts.URL, "GET", "/api/histories/check/xyz", tok, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("check bad hex: %d %s", resp.StatusCode, data)
	}
	resp, data = doReq(t, ts.URL, "DELETE", "/api/histories/xyz", tok, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("delete bad hex: %d %s", resp.StatusCode, data)
	}
	// 未登录 → 401
	resp, _ = doReq(t, ts.URL, "GET", "/api/histories/list", "", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("unauth: %d", resp.StatusCode)
	}
}

// TestRatingsSubmitRemoveCheck 评分主流程 + reviewStatus 分支 + 统计回写。
func TestRatingsSubmitRemoveCheck(t *testing.T) {
	ts, db, repos, signer := newHrServer(t)
	_, tok := createUser(t, repos, signer)

	approved := insertEpisode(t, db, bson.M{"title": "a", "coverImage": "/u/a.png", "reviewStatus": "approved"})
	pending := insertEpisode(t, db, bson.M{"title": "p", "coverImage": "/u/p.png", "reviewStatus": "pending"})
	noreview := insertEpisode(t, db, bson.M{"title": "n", "coverImage": "/u/n.png"})

	// 首次评分 → avg=score
	resp, data := doReq(t, ts.URL, "POST", "/api/ratings", tok, map[string]any{"episodeId": approved.Hex(), "score": 4})
	if resp.StatusCode != 200 {
		t.Fatalf("rate: %d %s", resp.StatusCode, data)
	}
	body := decodeObj(t, data)
	if body["score"].(float64) != 4 || body["averageRating"].(float64) != 4 || body["ratingCount"].(float64) != 1 {
		t.Fatalf("rate body: %v", body)
	}
	// 同集改分 → upsert，count 仍 1
	resp, data = doReq(t, ts.URL, "POST", "/api/ratings", tok, map[string]any{"episodeId": approved.Hex(), "score": 2})
	body = decodeObj(t, data)
	if body["averageRating"].(float64) != 2 || body["ratingCount"].(float64) != 1 {
		t.Fatalf("rate2: %v", body)
	}
	// pending → 403
	resp, data = doReq(t, ts.URL, "POST", "/api/ratings", tok, map[string]any{"episodeId": pending.Hex(), "score": 4})
	if resp.StatusCode != 403 || decodeObj(t, data)["message"] != "该剧集暂不可评分" {
		t.Fatalf("pending: %d %s", resp.StatusCode, data)
	}
	// reviewStatus 缺失 → 视为 approved
	resp, data = doReq(t, ts.URL, "POST", "/api/ratings", tok, map[string]any{"episodeId": noreview.Hex(), "score": 5})
	if resp.StatusCode != 200 {
		t.Fatalf("noreview: %d %s", resp.StatusCode, data)
	}
	// 两集不同评分 → avg 取整 1 位小数
	resp, data = doReq(t, ts.URL, "POST", "/api/ratings", tok, map[string]any{"episodeId": approved.Hex(), "score": 4})
	body = decodeObj(t, data)
	if body["averageRating"].(float64) != 4 || body["ratingCount"].(float64) != 1 {
		t.Fatalf("rate3: %v", body)
	}

	// check
	resp, data = doReq(t, ts.URL, "GET", "/api/ratings/check/"+approved.Hex(), tok, nil)
	if decodeObj(t, data)["score"].(float64) != 4 {
		t.Fatalf("check: %s", data)
	}
	resp, data = doReq(t, ts.URL, "GET", "/api/ratings/check/"+pending.Hex(), tok, nil)
	if decodeObj(t, data)["score"].(float64) != 0 {
		t.Fatalf("check empty: %s", data)
	}

	// 删除 → 统计归零
	resp, data = doReq(t, ts.URL, "DELETE", "/api/ratings/"+approved.Hex(), tok, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d %s", resp.StatusCode, data)
	}
	body = decodeObj(t, data)
	if body["message"] != "Rating deleted" || body["averageRating"].(float64) != 0 || body["ratingCount"].(float64) != 0 {
		t.Fatalf("delete body: %v", body)
	}
	resp, data = doReq(t, ts.URL, "GET", "/api/ratings/check/"+approved.Hex(), tok, nil)
	if decodeObj(t, data)["score"].(float64) != 0 {
		t.Fatalf("check after delete: %s", data)
	}
	// 删除不存在的评分 → 404
	resp, data = doReq(t, ts.URL, "DELETE", "/api/ratings/"+pending.Hex(), tok, nil)
	if resp.StatusCode != 404 || decodeObj(t, data)["message"] != "Rating not found" {
		t.Fatalf("delete missing: %d %s", resp.StatusCode, data)
	}
	// 非法 hex → 500
	resp, data = doReq(t, ts.URL, "GET", "/api/ratings/check/xyz", tok, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("check bad hex: %d %s", resp.StatusCode, data)
	}
}

// TestRatingsValidation 评分校验分支。
func TestRatingsValidation(t *testing.T) {
	ts, db, repos, signer := newHrServer(t)
	_, tok := createUser(t, repos, signer)
	approved := insertEpisode(t, db, bson.M{"title": "a", "coverImage": "/u/a.png", "reviewStatus": "approved"})

	cases := []struct {
		name string
		body map[string]any
		code int
		msg  string
	}{
		{"missing episodeId", map[string]any{"score": 3}, 400, "Invalid rating data"},
		{"missing score", map[string]any{"episodeId": approved.Hex()}, 400, "Invalid rating data"},
		{"score 0", map[string]any{"episodeId": approved.Hex(), "score": 0}, 400, "Invalid rating data"},
		{"score 6", map[string]any{"episodeId": approved.Hex(), "score": 6}, 400, "Invalid rating data"},
		{"score 0.5", map[string]any{"episodeId": approved.Hex(), "score": 0.5}, 400, "Invalid rating data"},
		{"score null", map[string]any{"episodeId": approved.Hex(), "score": nil}, 400, "Invalid rating data"},
		{"score string 6", map[string]any{"episodeId": approved.Hex(), "score": "6"}, 400, "Invalid rating data"},
		{"episodeId null", map[string]any{"episodeId": nil, "score": 3}, 400, "Invalid rating data"},
		{"nonexistent ep", map[string]any{"episodeId": "000000000000000000000000", "score": 3}, 404, "Episode not found"},
		{"bad hex ep", map[string]any{"episodeId": "xyz", "score": 3}, 500, "Server error"},
	}
	for _, c := range cases {
		resp, data := doReq(t, ts.URL, "POST", "/api/ratings", tok, c.body)
		if resp.StatusCode != c.code {
			t.Fatalf("%s: %d %s", c.name, resp.StatusCode, data)
		}
		if msg, _ := decodeObj(t, data)["message"].(string); msg != c.msg {
			t.Fatalf("%s msg: %v", c.name, data)
		}
	}
	// 边界 score=1 / score=5 合法
	for _, s := range []int{1, 5} {
		resp, data := doReq(t, ts.URL, "POST", "/api/ratings", tok, map[string]any{"episodeId": approved.Hex(), "score": s})
		if resp.StatusCode != 200 {
			t.Fatalf("boundary %d: %d %s", s, resp.StatusCode, data)
		}
	}
}
