package handler_test

import (
	"bytes"
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
	"github.com/xiedada05/furry-drama-be-neo/internal/indexes"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件是 /api/follows 与 /api/favorites 域 handler 的 httptest 集成测试
//（依赖本机 mongod，对齐 server/integration_test.go 的测试方式）。

// testEnv 组装挂载了 follows/favorites 路由的 gin 引擎与一个已登录用户。
type testEnv struct {
	r      *gin.Engine
	db     *mongo.Database
	userID primitive.ObjectID
	token  string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.IsDev = true

	db, err := repository.Connect(t.Context(), "mongodb://127.0.0.1:27017/neo_follows_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(t.Context()) })
	if err := indexes.Ensure(t.Context(), db); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	repos := repository.NewRepos(db, 5, 30)
	signer := auth.NewSigner(strings.Repeat("s", 40))
	amw := middleware.NewAuth(repos, signer)
	noopRL := func(ratelimit.Spec) gin.HandlerFunc { return func(*gin.Context) {} }

	r := gin.New()
	api := r.Group("/api")
	handler.NewFollows(repos, cfg, amw, noopRL).Register(api.Group("/follows"))
	handler.NewFavorites(repos, cfg, amw, noopRL).Register(api.Group("/favorites"))

	userID := primitive.NewObjectID()
	if _, err := db.Collection("users").InsertOne(t.Context(), bson.M{
		"_id": userID, "accountId": "it" + userID.Hex(), "username": "it",
		"email": "it_" + userID.Hex() + "@test.com", "role": "user",
	}); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	token, err := signer.Sign(userID.Hex(), "access", time.Hour, nil)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return &testEnv{r: r, db: db, userID: userID, token: token}
}

// do 以登录用户身份发起请求，返回 recorder 与响应体。
func (e *testEnv) do(t *testing.T, method, path string, body any) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w, w.Body.Bytes()
}

// insertEpisode 插入一条剧集（字段对齐 FollowsEpisodeDoc / models/Episode.js）。
func insertEpisode(t *testing.T, db *mongo.Database, currentEpisodes int, reviewStatus string, averageRating float64) primitive.ObjectID {
	t.Helper()
	id := primitive.NewObjectID()
	_, err := db.Collection("episodes").InsertOne(t.Context(), bson.M{
		"_id": id, "title": "测试剧集", "titleEn": "Test", "titleJa": "",
		"description": "desc", "descriptionEn": "", "descriptionJa": "",
		"coverImage": "/uploads/c.jpg", "totalEpisodes": nil, "currentEpisodes": currentEpisodes,
		"status": "ongoing", "category": []string{}, "tags": []string{}, "platformLinks": bson.M{},
		"views": 0, "averageRating": averageRating, "ratingCount": 0, "updateDay": "",
		"premiereDate": nil, "createdBy": nil, "hideCreator": false,
		"allowedEditors": []primitive.ObjectID{}, "customAuthors": []primitive.ObjectID{},
		"qqGroupLink": "", "reviewStatus": reviewStatus, "reviewNote": "",
		"pendingChanges": nil, "hasPendingChanges": false, "pendingChangeSummary": "",
		"reviewedBy": nil, "reviewedAt": nil,
		"createdAt": time.Now(), "updatedAt": time.Now(),
	})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}
	return id
}

// insertFolder 插入一个收藏夹（默认归属当前测试用户）。
func (e *testEnv) insertFolder(t *testing.T, name, typ string, owner *primitive.ObjectID) primitive.ObjectID {
	t.Helper()
	id := primitive.NewObjectID()
	uid := e.userID
	if owner != nil {
		uid = *owner
	}
	_, err := e.db.Collection("folders").InsertOne(t.Context(), bson.M{
		"_id": id, "userId": uid, "name": name, "type": typ,
		"description": "", "sortOrder": 0, "shareToken": nil, "createdAt": time.Now(),
	})
	if err != nil {
		t.Fatalf("insert folder: %v", err)
	}
	return id
}

func TestFollowsAddCheckList(t *testing.T) {
	e := newTestEnv(t)
	ep := insertEpisode(t, e.db, 12, "approved", 4.5)

	w, body := e.do(t, "POST", "/api/follows/add", map[string]any{"episodeId": ep.Hex()})
	if w.Code != 200 {
		t.Fatalf("follows/add: %d body=%s", w.Code, body)
	}
	var add struct {
		ID                 string `json:"_id"`
		EpisodeID          string `json:"episodeId"`
		FolderID           any    `json:"folderId"`
		FollowedAtEpisodes int    `json:"followedAtEpisodes"`
	}
	if err := json.Unmarshal(body, &add); err != nil {
		t.Fatalf("decode add: %v", err)
	}
	if add.ID == "" || add.EpisodeID != ep.Hex() {
		t.Fatalf("add shape: %+v", add)
	}
	if add.FollowedAtEpisodes != 12 {
		t.Fatalf("followedAtEpisodes: got %d want 12", add.FollowedAtEpisodes)
	}
	if add.FolderID != nil {
		t.Fatalf("folderId should be null, got %v", add.FolderID)
	}

	// 重复追番 → 400 Already following
	w, body = e.do(t, "POST", "/api/follows/add", map[string]any{"episodeId": ep.Hex()})
	if w.Code != 400 || !strings.Contains(string(body), "Already following") {
		t.Fatalf("dup add: %d body=%s", w.Code, body)
	}

	// check → 已追番
	var chk struct {
		IsFollowing        bool `json:"isFollowing"`
		FollowedAtEpisodes *int `json:"followedAtEpisodes"`
	}
	w, body = e.do(t, "GET", "/api/follows/check/"+ep.Hex(), nil)
	if w.Code != 200 || json.Unmarshal(body, &chk) != nil {
		t.Fatalf("check: %d body=%s", w.Code, body)
	}
	if !chk.IsFollowing || chk.FollowedAtEpisodes == nil || *chk.FollowedAtEpisodes != 12 {
		t.Fatalf("check shape: %+v", chk)
	}

	// list → 分页 + episodeId 填充
	var list struct {
		List       []map[string]any `json:"list"`
		Page       int              `json:"page"`
		Limit      int              `json:"limit"`
		Total      int              `json:"total"`
		TotalPages int              `json:"totalPages"`
	}
	w, body = e.do(t, "GET", "/api/follows/list?page=1&limit=20", nil)
	if w.Code != 200 || json.Unmarshal(body, &list) != nil {
		t.Fatalf("list: %d body=%s", w.Code, body)
	}
	if list.Total != 1 || list.TotalPages != 1 || len(list.List) != 1 {
		t.Fatalf("list meta: %+v", list)
	}
	row := list.List[0]
	epObj, ok := row["episodeId"].(map[string]any)
	if !ok || epObj["_id"] != ep.Hex() || epObj["title"] != "测试剧集" {
		t.Fatalf("list episodeId populate: %v", row["episodeId"])
	}
	if row["followedAtEpisodes"].(float64) != 12 {
		t.Fatalf("list followedAtEpisodes: %v", row["followedAtEpisodes"])
	}
	if row["folderId"] != nil {
		t.Fatalf("list folderId should be null: %v", row["folderId"])
	}
}

func TestFollowsAddNotFoundAndPending(t *testing.T) {
	e := newTestEnv(t)

	// 不存在的剧集 → 404
	w, body := e.do(t, "POST", "/api/follows/add", map[string]any{"episodeId": primitive.NewObjectID().Hex()})
	if w.Code != 404 || !strings.Contains(string(body), "Episode not found") {
		t.Fatalf("not found: %d body=%s", w.Code, body)
	}

	// pending 剧集 → 403
	pend := insertEpisode(t, e.db, 5, "pending", 0)
	w, body = e.do(t, "POST", "/api/follows/add", map[string]any{"episodeId": pend.Hex()})
	if w.Code != 403 || !strings.Contains(string(body), "该剧集暂不可追番") {
		t.Fatalf("pending: %d body=%s", w.Code, body)
	}

	// check 未追番的剧集 → isFollowing false（无 followedAtEpisodes 字段）
	var chk map[string]any
	w, body = e.do(t, "GET", "/api/follows/check/"+pend.Hex(), nil)
	if w.Code != 200 || json.Unmarshal(body, &chk) != nil {
		t.Fatalf("check: %d body=%s", w.Code, body)
	}
	if chk["isFollowing"] != false {
		t.Fatalf("check not following: %v", chk)
	}
	if _, has := chk["followedAtEpisodes"]; has {
		t.Fatalf("followedAtEpisodes should be omitted, got %v", chk)
	}
}

func TestFollowsListEmptyAndSorts(t *testing.T) {
	e := newTestEnv(t)

	// 空列表 → list 为 [] 而非 null
	var list struct {
		List       []any `json:"list"`
		Total      int   `json:"total"`
		TotalPages int   `json:"totalPages"`
	}
	w, body := e.do(t, "GET", "/api/follows/list", nil)
	if w.Code != 200 || json.Unmarshal(body, &list) != nil {
		t.Fatalf("empty list: %d body=%s", w.Code, body)
	}
	if list.List == nil || len(list.List) != 0 || list.Total != 0 || list.TotalPages != 0 {
		t.Fatalf("empty list shape: %+v", list)
	}

	// sort=name 分支：追两条后按标题排序（titleEn 均为 Test 时保持稳定）
	epA := insertEpisode(t, e.db, 3, "approved", 2.0)
	epB := insertEpisode(t, e.db, 7, "approved", 4.0)
	e.do(t, "POST", "/api/follows/add", map[string]any{"episodeId": epA.Hex()})
	e.do(t, "POST", "/api/follows/add", map[string]any{"episodeId": epB.Hex()})

	w, body = e.do(t, "GET", "/api/follows/list?sort=rating", nil)
	if w.Code != 200 {
		t.Fatalf("rating sort: %d body=%s", w.Code, body)
	}
	var rl struct {
		List []map[string]any `json:"list"`
	}
	if err := json.Unmarshal(body, &rl); err != nil {
		t.Fatalf("decode rating sort: %v", err)
	}
	if len(rl.List) != 2 {
		t.Fatalf("rating sort len: %d", len(rl.List))
	}
	first := rl.List[0]["episodeId"].(map[string]any)
	if first["_id"] != epB.Hex() {
		t.Fatalf("rating sort should put epB (4.0) first, got %v", first["_id"])
	}
}

func TestFavoritesAddCountsCheckList(t *testing.T) {
	e := newTestEnv(t)
	ep := insertEpisode(t, e.db, 10, "approved", 3.5)
	folder := e.insertFolder(t, "追剧", "favorite", nil)

	// add 成功 → {message: Favorited}
	w, body := e.do(t, "POST", "/api/favorites/add", map[string]any{"episodeId": ep.Hex(), "folderId": folder.Hex()})
	if w.Code != 200 || !strings.Contains(string(body), "Favorited") {
		t.Fatalf("favorites/add: %d body=%s", w.Code, body)
	}

	// 重复收藏 → 400 Already favorited
	w, body = e.do(t, "POST", "/api/favorites/add", map[string]any{"episodeId": ep.Hex(), "folderId": folder.Hex()})
	if w.Code != 400 || !strings.Contains(string(body), "Already favorited") {
		t.Fatalf("dup fav: %d body=%s", w.Code, body)
	}

	// counts → total/unclassified/folders
	var counts struct {
		Total        int64            `json:"total"`
		Unclassified int64            `json:"unclassified"`
		Folders      map[string]int64 `json:"folders"`
	}
	w, body = e.do(t, "GET", "/api/favorites/counts", nil)
	if w.Code != 200 || json.Unmarshal(body, &counts) != nil {
		t.Fatalf("counts: %d body=%s", w.Code, body)
	}
	if counts.Total != 1 || counts.Unclassified != 0 {
		t.Fatalf("counts totals: %+v", counts)
	}
	if n, ok := counts.Folders[folder.Hex()]; !ok || n != 1 {
		t.Fatalf("counts folders: %+v", counts.Folders)
	}

	// check → isFavorite true
	var chk struct {
		IsFavorite bool `json:"isFavorite"`
	}
	w, body = e.do(t, "GET", "/api/favorites/check/"+ep.Hex(), nil)
	if w.Code != 200 || json.Unmarshal(body, &chk) != nil || !chk.IsFavorite {
		t.Fatalf("fav check: %d body=%s", w.Code, body)
	}

	// list folderId=null 过滤 → 无该收藏
	var list struct {
		List  []map[string]any `json:"list"`
		Total int              `json:"total"`
	}
	w, body = e.do(t, "GET", "/api/favorites/list?folderId=null", nil)
	if w.Code != 200 || json.Unmarshal(body, &list) != nil {
		t.Fatalf("fav list: %d body=%s", w.Code, body)
	}
	if list.Total != 0 || len(list.List) != 0 {
		t.Fatalf("fav list null filter: %+v", list)
	}

	// 完整 list → 1 条，folderId 填充对象
	var full struct {
		List []map[string]any `json:"list"`
	}
	w, body = e.do(t, "GET", "/api/favorites/list", nil)
	if w.Code != 200 || json.Unmarshal(body, &full) != nil {
		t.Fatalf("fav full list: %d body=%s", w.Code, body)
	}
	if len(full.List) != 1 {
		t.Fatalf("fav full list len: %d", len(full.List))
	}
	folderObj, ok := full.List[0]["folderId"].(map[string]any)
	if !ok || folderObj["_id"] != folder.Hex() || folderObj["name"] != "追剧" {
		t.Fatalf("fav folderId populate: %v", full.List[0]["folderId"])
	}
}

func TestFavoritesAddFolderOwnership(t *testing.T) {
	e := newTestEnv(t)
	ep := insertEpisode(t, e.db, 2, "approved", 0)
	other := primitive.NewObjectID()
	if _, err := e.db.Collection("users").InsertOne(t.Context(), bson.M{
		"_id": other, "accountId": "other" + other.Hex(), "username": "other",
		"email": "other_" + other.Hex() + "@test.com", "role": "user",
	}); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	otherFolder := e.insertFolder(t, "别人的夹", "favorite", &other)

	// 收藏夹属于其它用户 → 400
	w, body := e.do(t, "POST", "/api/favorites/add", map[string]any{"episodeId": ep.Hex(), "folderId": otherFolder.Hex()})
	if w.Code != 400 || !strings.Contains(string(body), "收藏夹不存在或不属于当前用户") {
		t.Fatalf("folder ownership: %d body=%s", w.Code, body)
	}
}

func TestFollowsRemoveAndFavoritesRemove(t *testing.T) {
	e := newTestEnv(t)
	ep := insertEpisode(t, e.db, 6, "approved", 0)

	e.do(t, "POST", "/api/follows/add", map[string]any{"episodeId": ep.Hex()})
	w, body := e.do(t, "POST", "/api/follows/remove", map[string]any{"episodeId": ep.Hex()})
	if w.Code != 200 || !strings.Contains(string(body), "Unfollowed successfully") {
		t.Fatalf("follows/remove: %d body=%s", w.Code, body)
	}
	var chkF struct {
		IsFollowing bool `json:"isFollowing"`
	}
	w, body = e.do(t, "GET", "/api/follows/check/"+ep.Hex(), nil)
	if w.Code != 200 || json.Unmarshal(body, &chkF) != nil || chkF.IsFollowing {
		t.Fatalf("follows check after remove: %d body=%s", w.Code, body)
	}

	e.do(t, "POST", "/api/favorites/add", map[string]any{"episodeId": ep.Hex()})
	w, body = e.do(t, "POST", "/api/favorites/remove", map[string]any{"episodeId": ep.Hex()})
	if w.Code != 200 || !strings.Contains(string(body), "Unfavorited") {
		t.Fatalf("favorites/remove: %d body=%s", w.Code, body)
	}
	var chkV struct {
		IsFavorite bool `json:"isFavorite"`
	}
	w, body = e.do(t, "GET", "/api/favorites/check/"+ep.Hex(), nil)
	if w.Code != 200 || json.Unmarshal(body, &chkV) != nil || chkV.IsFavorite {
		t.Fatalf("favorites check after remove: %d body=%s", w.Code, body)
	}
}
