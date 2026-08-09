package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/handler"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// folderTestEnv 组装收藏夹域测试环境（真实 mongod 127.0.0.1:27017）。
type folderTestEnv struct {
	db     *mongo.Database
	repos  *repository.Repos
	signer *auth.Signer
	router *gin.Engine
}

func newFolderTestEnv(t *testing.T) *folderTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := repository.Connect(context.Background(), "mongodb://127.0.0.1:27017/neo_folders_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	// 测试开始时先清空集合，避免跨测试/跨运行残留（t.Context() 在 cleanup 时已被取消，
	// 不能用于 Drop，需用带超时的 background context）。
	dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = db.Drop(dropCtx)
	cancel()
	t.Cleanup(func() {
		dc, cc := context.WithTimeout(context.Background(), 5*time.Second)
		_ = db.Drop(dc)
		cc()
	})
	repos := repository.NewRepos(db, 5, 30)
	signer := auth.NewSigner(strings.Repeat("s", 40))
	amw := middleware.NewAuth(repos, signer)
	cfg := &config.Config{IsDev: true}

	router := gin.New()
	h := handler.NewFolders(repos, cfg, amw, nil)
	h.Register(router.Group("/api/folders"))
	sh := handler.NewSavedFolders(repos, cfg, amw, nil)
	sh.Register(router.Group("/api/saved-folders"))
	return &folderTestEnv{db: db, repos: repos, signer: signer, router: router}
}

// createUser 直接插入用户并签发 access token。
func (e *folderTestEnv) createUser(t *testing.T, username string) (*model.User, string) {
	t.Helper()
	u := &model.User{
		AccountID: "fld_" + username,
		Username:  username,
		Email:     username + "@test.com",
		Password:  "x",
		Role:      "user",
		CreatedAt: time.Now(),
	}
	if err := e.repos.Users.Create(t.Context(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	// UserRepo.Create 不回填 _id；重新读取以获得真实 ID。
	var got model.User
	if err := e.db.Collection("users").FindOne(t.Context(), primitive.M{"email": u.Email}).Decode(&got); err != nil {
		t.Fatalf("refetch user: %v", err)
	}
	u.ID = got.ID
	tok, err := e.signer.Sign(u.ID.Hex(), "access", 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return u, tok
}

// folderDo 执行一次请求；token 为空表示无鉴权。
func folderDo(t *testing.T, r *gin.Engine, method, path string, body any, token string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (e *folderTestEnv) insertEpisode(t *testing.T, ep *model.Episode) primitive.ObjectID {
	t.Helper()
	res, err := e.db.Collection("episodes").InsertOne(t.Context(), ep)
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}
	return res.InsertedID.(primitive.ObjectID)
}

func (e *folderTestEnv) insertFavorite(t *testing.T, uID, epID primitive.ObjectID, folderID *primitive.ObjectID) primitive.ObjectID {
	t.Helper()
	f := &model.Favorite{UserID: uID, EpisodeID: epID, FolderID: folderID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	res, err := e.db.Collection("favorites").InsertOne(t.Context(), f)
	if err != nil {
		t.Fatalf("insert favorite: %v", err)
	}
	return res.InsertedID.(primitive.ObjectID)
}

func (e *folderTestEnv) insertFollow(t *testing.T, uID, epID primitive.ObjectID, folderID *primitive.ObjectID) primitive.ObjectID {
	t.Helper()
	f := &model.Follow{UserID: uID, EpisodeID: epID, FolderID: folderID, FollowedAtEpisodes: 1, CreatedAt: time.Now()}
	res, err := e.db.Collection("follows").InsertOne(t.Context(), f)
	if err != nil {
		t.Fatalf("insert follow: %v", err)
	}
	return res.InsertedID.(primitive.ObjectID)
}

// TestFolderCreateValidateListUpdate 创建/校验/列表/重命名/删除主流程。
func TestFolderCreateValidateListUpdate(t *testing.T) {
	env := newFolderTestEnv(t)
	u, tok := env.createUser(t, "alice")

	// 创建成功：name trim、默认字段补齐。
	code, body := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "  我的收藏  ", "type": "favorite"}, tok)
	if code != 200 {
		t.Fatalf("create: %d body=%s", code, body)
	}
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	if created["_id"] == nil || created["name"] != "我的收藏" || created["type"] != "favorite" {
		t.Fatalf("create body=%s", body)
	}
	if created["description"] != "" || created["sortOrder"] != float64(0) || created["shareToken"] != nil {
		t.Fatalf("create defaults body=%s", body)
	}
	if created["userId"] != u.ID.Hex() {
		t.Fatalf("create userId=%v want %s", created["userId"], u.ID.Hex())
	}
	if _, ok := created["createdAt"]; !ok {
		t.Fatalf("create missing createdAt: %s", body)
	}
	folderID := created["_id"].(string)

	// 校验失败分支。
	if code, _ := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "  ", "type": "favorite"}, tok); code != 400 {
		t.Fatalf("empty name: %d", code)
	}
	if code, body := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": strings.Repeat("长", 51), "type": "favorite"}, tok); code != 400 || !strings.Contains(string(body), "50") {
		t.Fatalf("too long name: %d body=%s", code, body)
	}
	if code, body := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "x", "type": "bad"}, tok); code != 400 || !strings.Contains(string(body), "无效的文件夹类型") {
		t.Fatalf("bad type: %d body=%s", code, body)
	}

	// 列表：userId 被 populate 成 {_id, username} 对象。
	code, body = folderDo(t, env.router, "GET", "/api/folders", nil, tok)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	var list []map[string]any
	_ = json.Unmarshal(body, &list)
	if len(list) != 1 {
		t.Fatalf("list len=%d body=%s", len(list), body)
	}
	uidObj, ok := list[0]["userId"].(map[string]any)
	if !ok || uidObj["username"] != "alice" || uidObj["_id"] != u.ID.Hex() {
		t.Fatalf("list userId populate=%v", list[0]["userId"])
	}

	// type 过滤：follow 应返回空数组 []。
	code, body = folderDo(t, env.router, "GET", "/api/folders?type=follow", nil, tok)
	if code != 200 || string(body) != "[]" {
		t.Fatalf("list filtered: %d body=%s", code, body)
	}

	// 重命名 + 描述。
	code, body = folderDo(t, env.router, "PUT", "/api/folders/"+folderID, map[string]any{"name": "新名字", "description": "  说明  "}, tok)
	if code != 200 {
		t.Fatalf("update: %d body=%s", code, body)
	}
	var updated map[string]any
	_ = json.Unmarshal(body, &updated)
	if updated["name"] != "新名字" || updated["description"] != "说明" {
		t.Fatalf("update body=%s", body)
	}
	if code, body := folderDo(t, env.router, "PUT", "/api/folders/"+folderID, map[string]any{"name": "  "}, tok); code != 400 || !strings.Contains(string(body), "不能为空") {
		t.Fatalf("update empty name: %d body=%s", code, body)
	}

	// 删除 → 列表空。
	if code, body := folderDo(t, env.router, "DELETE", "/api/folders/"+folderID, nil, tok); code != 200 || string(body) != `{"message":"Folder deleted"}` {
		t.Fatalf("delete: %d body=%s", code, body)
	}
	code, body = folderDo(t, env.router, "GET", "/api/folders", nil, tok)
	if code != 200 || string(body) != "[]" {
		t.Fatalf("list after delete: %d body=%s", code, body)
	}

	// 不存在 / 非法 ID 分支。
	if code, _ := folderDo(t, env.router, "PUT", "/api/folders/"+primitive.NewObjectID().Hex(), map[string]any{"name": "x"}, tok); code != 404 {
		t.Fatalf("update missing: %d", code)
	}
	if code, body := folderDo(t, env.router, "PUT", "/api/folders/nothex", map[string]any{"name": "x"}, tok); code != 500 || string(body) != `{"message":"Server error"}` {
		t.Fatalf("update invalid id: %d body=%s", code, body)
	}
}

// TestFolderItemsAndDeleteClearsFolderId 条目归入/移出收藏夹 + 删除收藏夹清空 folderId。
func TestFolderItemsAndDeleteClearsFolderId(t *testing.T) {
	env := newFolderTestEnv(t)
	u, tok := env.createUser(t, "bob")

	code, body := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "Favs", "type": "favorite"}, tok)
	if code != 200 {
		t.Fatalf("create folder: %d", code)
	}
	var folder map[string]any
	_ = json.Unmarshal(body, &folder)
	folderID, _ := primitive.ObjectIDFromHex(folder["_id"].(string))
	epID := env.insertEpisode(t, &model.Episode{Title: "Ep1", CoverImage: "img", CurrentEpisodes: 3, Status: "ongoing", ReviewStatus: "approved", CreatedAt: time.Now(), UpdatedAt: time.Now()})

	// 未收藏的条目 → 404 'Item not found'。
	if code, body := folderDo(t, env.router, "POST", "/api/folders/"+folderID.Hex()+"/items", map[string]any{"episodeId": primitive.NewObjectID().Hex()}, tok); code != 404 || string(body) != `{"message":"Item not found"}` {
		t.Fatalf("add missing item: %d body=%s", code, body)
	}

	// 归入收藏夹。
	_ = env.insertFavorite(t, u.ID, epID, nil)
	code, body = folderDo(t, env.router, "POST", "/api/folders/"+folderID.Hex()+"/items", map[string]any{"episodeId": epID.Hex()}, tok)
	if code != 200 {
		t.Fatalf("add item: %d body=%s", code, body)
	}
	var item map[string]any
	_ = json.Unmarshal(body, &item)
	if item["folderId"] != folderID.Hex() || item["episodeId"] != epID.Hex() {
		t.Fatalf("add item body=%s", body)
	}

	// 移出收藏夹。
	if code, body := folderDo(t, env.router, "DELETE", "/api/folders/"+folderID.Hex()+"/items/"+epID.Hex(), nil, tok); code != 200 || string(body) != `{"message":"Item removed from folder"}` {
		t.Fatalf("remove item: %d body=%s", code, body)
	}

	// 再归入，然后删除收藏夹 → folderId 被清空。
	folderDo(t, env.router, "POST", "/api/folders/"+folderID.Hex()+"/items", map[string]any{"episodeId": epID.Hex()}, tok)
	if code, body := folderDo(t, env.router, "DELETE", "/api/folders/"+folderID.Hex(), nil, tok); code != 200 {
		t.Fatalf("delete folder: %d body=%s", code, body)
	}
	var fav model.Favorite
	if err := env.db.Collection("favorites").FindOne(t.Context(), primitive.M{"userId": u.ID, "episodeId": epID}).Decode(&fav); err != nil {
		t.Fatalf("find favorite: %v", err)
	}
	if fav.FolderID != nil {
		t.Fatalf("folderId not cleared: %v", *fav.FolderID)
	}
}

// TestFolderShareFlowAndPublic 分享/取消分享/公开访问 + 未审核剧集过滤。
func TestFolderShareFlowAndPublic(t *testing.T) {
	env := newFolderTestEnv(t)
	creator, tok := env.createUser(t, "creator")

	code, body := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "公开收藏", "type": "favorite"}, tok)
	if code != 200 {
		t.Fatalf("create folder: %d", code)
	}
	var folder map[string]any
	_ = json.Unmarshal(body, &folder)
	folderID, _ := primitive.ObjectIDFromHex(folder["_id"].(string))
	// 对齐 Express POST /：description 不入库，需经 PUT /:id 设置。
	if code, _ := folderDo(t, env.router, "PUT", "/api/folders/"+folderID.Hex(), map[string]any{"description": "描述"}, tok); code != 200 {
		t.Fatalf("set description: %d", code)
	}

	// 生成 shareToken（24 hex）。
	code, body = folderDo(t, env.router, "POST", "/api/folders/"+folderID.Hex()+"/share", nil, tok)
	if code != 200 {
		t.Fatalf("share: %d body=%s", code, body)
	}
	var share map[string]any
	_ = json.Unmarshal(body, &share)
	token := share["shareToken"].(string)
	if len(token) != 24 {
		t.Fatalf("shareToken len=%d", len(token))
	}
	// 幂等：再次分享返回同一 token。
	code, body2 := folderDo(t, env.router, "POST", "/api/folders/"+folderID.Hex()+"/share", nil, tok)
	if code != 200 || string(body2) != string(body) {
		t.Fatalf("share idempotent: %d body=%s", code, body2)
	}

	// 公开访问（无鉴权）→ 只返回 approved 剧集。
	epApproved := env.insertEpisode(t, &model.Episode{Title: "已过审", CoverImage: "a", CurrentEpisodes: 1, TotalEpisodes: folderIntPtr(12), Status: "ongoing", ReviewStatus: "approved", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	epPending := env.insertEpisode(t, &model.Episode{Title: "待审核", CoverImage: "b", CurrentEpisodes: 1, Status: "ongoing", ReviewStatus: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	env.insertFavorite(t, creator.ID, epApproved, &folderID)
	env.insertFavorite(t, creator.ID, epPending, &folderID)

	code, body = folderDo(t, env.router, "GET", "/api/folders/shared/"+token, nil, "")
	if code != 200 {
		t.Fatalf("shared: %d body=%s", code, body)
	}
	var shared map[string]any
	_ = json.Unmarshal(body, &shared)
	if shared["name"] != "公开收藏" || shared["description"] != "描述" || shared["type"] != "favorite" {
		t.Fatalf("shared fields=%v", shared)
	}
	if shared["creatorName"] != "creator" || shared["creatorId"] != creator.ID.Hex() {
		t.Fatalf("shared creator=%v", shared["creatorName"])
	}
	episodes := shared["episodes"].([]any)
	if shared["count"] != float64(1) || len(episodes) != 1 {
		t.Fatalf("shared episodes=%v count=%v", episodes, shared["count"])
	}
	ep0 := episodes[0].(map[string]any)
	if ep0["_id"] != epApproved.Hex() || ep0["title"] != "已过审" || ep0["totalEpisodes"] != float64(12) {
		t.Fatalf("shared ep0=%v", ep0)
	}
	if _, ok := ep0["description"]; ok {
		t.Fatalf("shared episode should only carry projected fields: %v", ep0)
	}

	// 取消分享 → 公开访问 404。
	if code, body := folderDo(t, env.router, "DELETE", "/api/folders/"+folderID.Hex()+"/share", nil, tok); code != 200 || string(body) != `{"message":"Share revoked"}` {
		t.Fatalf("revoke: %d body=%s", code, body)
	}
	if code, body := folderDo(t, env.router, "GET", "/api/folders/shared/"+token, nil, ""); code != 404 || string(body) != `{"message":"Shared folder not found"}` {
		t.Fatalf("shared after revoke: %d body=%s", code, body)
	}
}

// TestShareUnclassified 默认收藏夹分享（幂等）+ 不出现在列表。
func TestShareUnclassified(t *testing.T) {
	env := newFolderTestEnv(t)
	u, tok := env.createUser(t, "carol")

	code, body := folderDo(t, env.router, "POST", "/api/folders/share-unclassified", nil, tok)
	if code != 200 {
		t.Fatalf("share unclassified: %d body=%s", code, body)
	}
	var first map[string]any
	_ = json.Unmarshal(body, &first)
	code, body2 := folderDo(t, env.router, "POST", "/api/folders/share-unclassified", nil, tok)
	if code != 200 || string(body2) != string(body) {
		t.Fatalf("share unclassified idempotent: %d body=%s", code, body2)
	}
	// 列表不应包含 __unclassified__。
	code, body = folderDo(t, env.router, "GET", "/api/folders", nil, tok)
	if code != 200 || string(body) != "[]" {
		t.Fatalf("list should exclude unclassified: %d body=%s", code, body)
	}
	// 虚拟收藏夹落库：name/type/sortOrder=-1。
	var uf model.Folder
	if err := env.db.Collection("folders").FindOne(t.Context(), primitive.M{"userId": u.ID, "name": "__unclassified__"}).Decode(&uf); err != nil {
		t.Fatalf("find unclassified: %v", err)
	}
	if uf.Type != "favorite" || uf.SortOrder != -1 || uf.ShareToken == nil || *uf.ShareToken != first["shareToken"] {
		t.Fatalf("unclassified folder=%+v", uf)
	}

	// 公开访问默认收藏夹：name='默认收藏夹'，且只含 folderId=null 的收藏。
	ep := env.insertEpisode(t, &model.Episode{Title: "未分类剧集", CoverImage: "c", CurrentEpisodes: 1, Status: "ongoing", ReviewStatus: "approved", CreatedAt: time.Now(), UpdatedAt: time.Now()})
	env.insertFavorite(t, u.ID, ep, nil) // folderId=null
	code, body = folderDo(t, env.router, "GET", "/api/folders/shared/"+first["shareToken"].(string), nil, "")
	if code != 200 {
		t.Fatalf("shared unclassified: %d body=%s", code, body)
	}
	var shared map[string]any
	_ = json.Unmarshal(body, &shared)
	if shared["name"] != "默认收藏夹" || shared["description"] != "" || shared["count"] != float64(1) {
		t.Fatalf("shared unclassified fields=%v", shared)
	}
}

// TestSavedFolders 收藏/取消收藏他人收藏夹。
func TestSavedFolders(t *testing.T) {
	env := newFolderTestEnv(t)
	creator, creatorTok := env.createUser(t, "creator2")
	_, savedTok := env.createUser(t, "saver")

	// creator 建收藏夹并分享。
	code, body := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "要收藏的", "type": "favorite"}, creatorTok)
	if code != 200 {
		t.Fatalf("create folder: %d", code)
	}
	var folder map[string]any
	_ = json.Unmarshal(body, &folder)
	folderID := folder["_id"].(string)
	// 对齐 Express POST /：description 需经 PUT 设置。
	if code, _ := folderDo(t, env.router, "PUT", "/api/folders/"+folderID, map[string]any{"description": "描述"}, creatorTok); code != 200 {
		t.Fatalf("set description: %d", code)
	}
	code, body = folderDo(t, env.router, "POST", "/api/folders/"+folderID+"/share", nil, creatorTok)
	if code != 200 {
		t.Fatalf("share: %d", code)
	}
	var share map[string]any
	_ = json.Unmarshal(body, &share)
	token := share["shareToken"].(string)

	// 空列表。
	code, body = folderDo(t, env.router, "GET", "/api/saved-folders", nil, savedTok)
	if code != 200 || string(body) != "[]" {
		t.Fatalf("list empty: %d body=%s", code, body)
	}

	// 校验分支。
	if code, body := folderDo(t, env.router, "POST", "/api/saved-folders", map[string]any{}, savedTok); code != 400 || string(body) != `{"message":"shareToken is required"}` {
		t.Fatalf("missing token: %d body=%s", code, body)
	}
	if code, body := folderDo(t, env.router, "POST", "/api/saved-folders", map[string]any{"shareToken": "nope"}, savedTok); code != 404 || string(body) != `{"message":"Folder not found"}` {
		t.Fatalf("bad token: %d body=%s", code, body)
	}
	if code, body := folderDo(t, env.router, "POST", "/api/saved-folders", map[string]any{"shareToken": token}, creatorTok); code != 400 || string(body) != `{"message":"不能收藏自己的收藏夹"}` {
		t.Fatalf("own folder: %d body=%s", code, body)
	}

	// 成功收藏。
	code, body = folderDo(t, env.router, "POST", "/api/saved-folders", map[string]any{"shareToken": token, "creatorName": "creator2"}, savedTok)
	if code != 200 {
		t.Fatalf("save folder: %d body=%s", code, body)
	}
	var sf map[string]any
	_ = json.Unmarshal(body, &sf)
	if sf["folderName"] != "要收藏的" || sf["creatorId"] != creator.ID.Hex() || sf["creatorName"] != "creator2" || sf["description"] != "描述" || sf["folderType"] != "favorite" {
		t.Fatalf("saved folder body=%s", body)
	}
	sfID := sf["_id"].(string)

	// 重复收藏 → 400。
	if code, body := folderDo(t, env.router, "POST", "/api/saved-folders", map[string]any{"shareToken": token}, savedTok); code != 400 || string(body) != `{"message":"已收藏过该收藏夹"}` {
		t.Fatalf("dup save: %d body=%s", code, body)
	}

	// 列表长度 1；取消收藏。
	code, body = folderDo(t, env.router, "GET", "/api/saved-folders", nil, savedTok)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	var list []map[string]any
	_ = json.Unmarshal(body, &list)
	if len(list) != 1 || list[0]["shareToken"] != token {
		t.Fatalf("list body=%s", body)
	}
	if code, body := folderDo(t, env.router, "DELETE", "/api/saved-folders/"+sfID, nil, savedTok); code != 200 || string(body) != `{"message":"Removed"}` {
		t.Fatalf("unsave: %d body=%s", code, body)
	}
	if code, body := folderDo(t, env.router, "DELETE", "/api/saved-folders/"+primitive.NewObjectID().Hex(), nil, savedTok); code != 404 || string(body) != `{"message":"Saved folder not found"}` {
		t.Fatalf("unsave missing: %d body=%s", code, body)
	}
}

// TestSavedUnclassifiedFolderName 收藏默认收藏夹时 folderName/description 特判。
func TestSavedUnclassifiedFolderName(t *testing.T) {
	env := newFolderTestEnv(t)
	_, creatorTok := env.createUser(t, "creator3")
	_, saverTok := env.createUser(t, "saver2")

	code, body := folderDo(t, env.router, "POST", "/api/folders/share-unclassified", nil, creatorTok)
	if code != 200 {
		t.Fatalf("share unclassified: %d", code)
	}
	var share map[string]any
	_ = json.Unmarshal(body, &share)
	token := share["shareToken"].(string)

	code, body = folderDo(t, env.router, "POST", "/api/saved-folders", map[string]any{"shareToken": token}, saverTok)
	if code != 200 {
		t.Fatalf("save unclassified: %d body=%s", code, body)
	}
	var sf map[string]any
	_ = json.Unmarshal(body, &sf)
	if sf["folderName"] != "默认收藏夹" || sf["description"] != "" {
		t.Fatalf("unclassified saved body=%s", body)
	}
	if sf["creatorName"] != "Unknown" {
		t.Fatalf("creatorName fallback=%v", sf["creatorName"])
	}
}

// TestFolderAuthRequired 未登录访问 protect 端点 → 401。
func TestFolderAuthRequired(t *testing.T) {
	env := newFolderTestEnv(t)
	if code, body := folderDo(t, env.router, "GET", "/api/folders", nil, ""); code != 401 || !strings.Contains(string(body), "Not authorized") {
		t.Fatalf("unauth list: %d body=%s", code, body)
	}
	if code, _ := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "x", "type": "favorite"}, ""); code != 401 {
		t.Fatalf("unauth create: %d", code)
	}
	// 公开端点无需鉴权即可访问（404 而非 401）。
	if code, _ := folderDo(t, env.router, "GET", "/api/folders/shared/nonexistent", nil, ""); code != 404 {
		t.Fatalf("public shared: %d", code)
	}
}

// TestFolderReorder PUT /reorder（静态路径不与 /:id 冲突）+ sortOrder 落库。
func TestFolderReorder(t *testing.T) {
	env := newFolderTestEnv(t)
	_, tok := env.createUser(t, "dave")

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		code, body := folderDo(t, env.router, "POST", "/api/folders", map[string]any{"name": "F" + string(rune('A'+i)), "type": "favorite"}, tok)
		if code != 200 {
			t.Fatalf("create: %d", code)
		}
		var f map[string]any
		_ = json.Unmarshal(body, &f)
		ids[i] = f["_id"].(string)
	}
	// 逆序排序 → sortOrder 为数组下标。
	reversed := []string{ids[2], ids[1], ids[0]}
	code, body := folderDo(t, env.router, "PUT", "/api/folders/reorder", map[string]any{"folderIds": reversed}, tok)
	if code != 200 || string(body) != `{"message":"Reordered"}` {
		t.Fatalf("reorder: %d body=%s", code, body)
	}
	var list []map[string]any
	code, body = folderDo(t, env.router, "GET", "/api/folders", nil, tok)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	_ = json.Unmarshal(body, &list)
	if len(list) != 3 || list[0]["_id"] != ids[2] || list[2]["_id"] != ids[0] {
		t.Fatalf("reorder list=%v", list)
	}
	// 非法 folderId → 500（对齐 bulkWrite CastError）。
	if code, body := folderDo(t, env.router, "PUT", "/api/folders/reorder", map[string]any{"folderIds": []string{"badid"}}, tok); code != 500 || string(body) != `{"message":"Server error"}` {
		t.Fatalf("reorder invalid id: %d body=%s", code, body)
	}
	// 非本人收藏夹的 id 被静默忽略（filter 限定 userId），不报错。
	if code, _ := folderDo(t, env.router, "PUT", "/api/folders/reorder", map[string]any{"folderIds": []string{primitive.NewObjectID().Hex()}}, tok); code != 200 {
		t.Fatalf("reorder foreign id: %d", code)
	}
}

func folderIntPtr(v int) *int { return &v }
