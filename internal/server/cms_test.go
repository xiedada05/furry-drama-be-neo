package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/server"
	"github.com/xiedada05/furry-drama-be-neo/internal/upload"
)

// cmsTestEnv 组装公告/壁纸/友链域测试环境（真实 mongod，独立库 neo_cms_test）。
// 上传目录指向 t.TempDir()，避免测试污染仓库 uploads/。
type cmsTestEnv struct {
	ts     *httptest.Server
	repos  *repository.Repos
	signer *auth.Signer
	cfg    *config.Config
}

func cmsTestServer(t *testing.T) *cmsTestEnv {
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

	upload.SetDir(t.TempDir())

	db, err := repository.Connect(context.Background(), "mongodb://127.0.0.1:27017/neo_cms_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	app := server.NewApp(server.Deps{Config: cfg, DB: db, Repos: repos, Signer: signer})
	ts := httptest.NewServer(app)
	t.Cleanup(ts.Close)
	return &cmsTestEnv{ts: ts, repos: repos, signer: signer, cfg: cfg}
}

// createSuperAdmin 直插一个超管用户并返回其 access token。
func (e *cmsTestEnv) createSuperAdmin(t *testing.T) string {
	t.Helper()
	u := &model.User{
		AccountID:              fmt.Sprintf("sa%d", time.Now().UnixNano()),
		Username:               "sadmin",
		Email:                  fmt.Sprintf("sa%d@test.com", time.Now().UnixNano()),
		Password:               "x",
		Role:                   "superadmin",
		EmailNotificationPrefs: model.DefaultEmailNotificationPrefs(),
		CreatedAt:              time.Now(),
	}
	if err := e.repos.Users.Create(context.Background(), u); err != nil {
		t.Fatalf("create superadmin: %v", err)
	}
	tok, err := e.signer.Sign(u.ID.Hex(), "access", 15*time.Minute, nil)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

// jsonH 发送带自定义头的 JSON 请求并解码到 out（client.json 不传自定义头，
// 超管端点需要 Authorization: Bearer）。
func (c *client) jsonH(method, path string, body any, headers map[string]string, out any) *http.Response {
	resp, data := c.do(method, path, body, headers)
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	return resp
}

// doMultipart 发送 multipart/form-data 请求（image 文件 + 表单字段）。
func (c *client) doMultipart(path, token string, fields map[string]string) (*http.Response, []byte) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// 1x1 PNG：写入解码后的原始字节（前 12 字节 base64 前缀为 iVBOR，通过魔数校验）。
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="image"; filename="test.png"`)
	h.Set("Content-Type", "image/png")
	fw, _ := w.CreatePart(h)
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	_, _ = fw.Write(png)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	_ = w.Close()

	req, _ := http.NewRequest("POST", c.base+path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("x-dev-token", "test-dev-token")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c.token != "" {
		req.Header.Set("X-XSRF-TOKEN", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return resp, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

// ---- 公告 ----

// TestAnnouncementsPublic 公开端点：active 列表（含 channel 过滤）、详情、404/500。
func TestAnnouncementsPublic(t *testing.T) {
	e := cmsTestServer(t)
	c := newClient(t, e.ts.URL)
	now := time.Now()
	// a1 置顶、永久有效；a2 showPopup（带未来过期时间，仍在有效期内）；a3 已下线。
	a1 := &model.Announcement{Title: "欢迎", Content: "欢迎来到兽剧", Pinned: true,
		PublishAt: now.Add(-time.Hour), Active: true}
	exp := now.Add(time.Hour)
	a2 := &model.Announcement{Title: "活动", Content: "活动内容", ShowPopup: true,
		PublishAt: now.Add(-30 * time.Minute), Active: true, ExpireAt: &exp}
	a3 := &model.Announcement{Title: "下线", Content: "不展示", PublishAt: now.Add(-time.Minute), Active: false}
	for _, a := range []*model.Announcement{a1, a2, a3} {
		if err := e.repos.Announcements.Create(context.Background(), a); err != nil {
			t.Fatalf("create announcement: %v", err)
		}
	}

	var list []map[string]any
	resp := c.json("GET", "/api/announcements/active", nil, &list)
	if resp.StatusCode != 200 || len(list) != 2 {
		t.Fatalf("active status=%d list=%d want 200/2", resp.StatusCode, len(list))
	}
	if list[0]["_id"] != a1.ID.Hex() {
		t.Fatalf("active[0]._id=%v want %s (pinned first)", list[0]["_id"], a1.ID.Hex())
	}
	if list[0]["pinned"] != true || list[0]["active"] != true {
		t.Fatalf("active[0] shape=%v", list[0])
	}

	var popup []map[string]any
	resp = c.json("GET", "/api/announcements/active?channel=popup", nil, &popup)
	if resp.StatusCode != 200 || len(popup) != 1 || popup[0]["_id"] != a2.ID.Hex() {
		t.Fatalf("popup status=%d list=%v want 1 with a2", resp.StatusCode, popup)
	}

	var detail map[string]any
	resp = c.json("GET", "/api/announcements/"+a1.ID.Hex(), nil, &detail)
	if resp.StatusCode != 200 || detail["title"] != "欢迎" || detail["_id"] != a1.ID.Hex() {
		t.Fatalf("detail status=%d body=%v", resp.StatusCode, detail)
	}

	resp = c.json("GET", "/api/announcements/"+primitive.NewObjectID().Hex(), nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("missing id status=%d want 404", resp.StatusCode)
	}

	resp = c.json("GET", "/api/announcements/not-hex", nil, nil)
	if resp.StatusCode != 500 {
		t.Fatalf("invalid id status=%d want 500", resp.StatusCode)
	}
}

// TestAnnouncementsAdminCRUD 超管 CRUD：403 权限、400 校验、增删改查。
func TestAnnouncementsAdminCRUD(t *testing.T) {
	e := cmsTestServer(t)
	c := newClient(t, e.ts.URL)
	token := e.createSuperAdmin(t)
	authH := map[string]string{"Authorization": "Bearer " + token}

	// 普通用户无权限。
	c.registerAndLogin("user_" + fmt.Sprintf("%d", time.Now().UnixNano()) + "@test.com")
	resp := c.json("POST", "/api/announcements", map[string]any{"title": "x", "content": "y"}, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-superadmin create status=%d want 403", resp.StatusCode)
	}

	// 400：标题/内容缺失。
	resp = c.jsonH("POST", "/api/announcements", map[string]any{"title": "只有标题"}, authH, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("missing content status=%d want 400", resp.StatusCode)
	}

	// 创建。
	var created map[string]any
	resp = c.jsonH("POST", "/api/announcements", map[string]any{
		"title": "更新公告", "content": "内容", "type": "update", "showPopup": true,
	}, authH, &created)
	if resp.StatusCode != 200 {
		t.Fatalf("create status=%d body=%v", resp.StatusCode, created)
	}
	id := created["_id"].(string)
	if created["active"] != true || created["dismissible"] != true ||
		created["type"] != "update" || created["showPopup"] != true ||
		created["pinned"] != false || created["sendNotification"] != false {
		t.Fatalf("create shape=%v", created)
	}

	// 列表包含新建项。
	var all []map[string]any
	resp = c.jsonH("GET", "/api/announcements", nil, authH, &all)
	if resp.StatusCode != 200 || len(all) != 1 || all[0]["_id"] != id {
		t.Fatalf("list status=%d all=%v", resp.StatusCode, all)
	}

	// 更新。
	var updated map[string]any
	resp = c.jsonH("PUT", "/api/announcements/"+id, map[string]any{"active": false, "title": "改标题"}, authH, &updated)
	if resp.StatusCode != 200 || updated["active"] != false || updated["title"] != "改标题" {
		t.Fatalf("update status=%d body=%v", resp.StatusCode, updated)
	}

	// 更新不存在的 → 404。
	resp = c.jsonH("PUT", "/api/announcements/"+primitive.NewObjectID().Hex(), map[string]any{"title": "x"}, authH, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("update missing status=%d want 404", resp.StatusCode)
	}

	// 删除。
	var del map[string]string
	resp = c.jsonH("DELETE", "/api/announcements/"+id, nil, authH, &del)
	if resp.StatusCode != 200 || del["message"] != "已删除" {
		t.Fatalf("delete status=%d body=%v", resp.StatusCode, del)
	}
	// 删除后列表为空。
	resp = c.jsonH("GET", "/api/announcements", nil, authH, &all)
	if resp.StatusCode != 200 || len(all) != 0 {
		t.Fatalf("list after delete=%v", all)
	}
}

// TestAnnouncementsPublishNotification 发布带通知的公告会批量写通知中心条目。
func TestAnnouncementsPublishNotification(t *testing.T) {
	e := cmsTestServer(t)
	c := newClient(t, e.ts.URL)
	token := e.createSuperAdmin(t)
	authH := map[string]string{"Authorization": "Bearer " + token}

	// 预置 3 个普通用户。
	uids := make([]primitive.ObjectID, 0, 3)
	for i := 0; i < 3; i++ {
		u := &model.User{
			AccountID:              fmt.Sprintf("an%d", time.Now().UnixNano()+int64(i)),
			Username:               "u",
			Email:                  fmt.Sprintf("an%d@test.com", time.Now().UnixNano()+int64(i)),
			Password:               "x",
			Role:                   "user",
			EmailNotificationPrefs: model.DefaultEmailNotificationPrefs(),
			CreatedAt:              time.Now(),
		}
		if err := e.repos.Users.Create(context.Background(), u); err != nil {
			t.Fatalf("create user: %v", err)
		}
		uids = append(uids, u.ID)
	}

	var created map[string]any
	resp := c.jsonH("POST", "/api/announcements", map[string]any{
		"title": "站务", "content": "通知", "sendNotification": true, "link": "https://x.test",
	}, authH, &created)
	if resp.StatusCode != 200 {
		t.Fatalf("create status=%d body=%v", resp.StatusCode, created)
	}
	if created["notificationSent"] != true {
		t.Fatalf("notificationSent=%v want true", created["notificationSent"])
	}

	// 三个用户都应收到 announcement 通知。
	for _, uid := range uids {
		notifs, err := e.repos.Notifications.FindByUser(context.Background(), uid, 1, 20)
		if err != nil {
			t.Fatalf("find notifs: %v", err)
		}
		found := false
		for _, n := range notifs {
			if n.Type != "announcement" || n.Message != "站务" {
				continue
			}
			// metadata.announcementId 落库为 ObjectID；created["_id"] 为 JSON hex 字符串。
			midHex := ""
			switch v := n.Metadata["announcementId"].(type) {
			case string:
				midHex = v
			case primitive.ObjectID:
				midHex = v.Hex()
			}
			if midHex == created["_id"] {
				found = true
			}
		}
		if !found {
			t.Fatalf("user %s missing announcement notification: %v", uid.Hex(), notifs)
		}
	}
}

// ---- 壁纸 ----

// TestWallpapersSystem 系统壁纸：公开列表、超管上传/列表/更新/删除。
func TestWallpapersSystem(t *testing.T) {
	e := cmsTestServer(t)
	c := newClient(t, e.ts.URL)
	token := e.createSuperAdmin(t)
	authH := map[string]string{"Authorization": "Bearer " + token}

	var pub []map[string]any
	resp := c.json("GET", "/api/wallpapers/system", nil, &pub)
	if resp.StatusCode != 200 || len(pub) != 0 {
		t.Fatalf("system empty status=%d list=%v", resp.StatusCode, pub)
	}

	// 上传。
	resp, body := c.doMultipart("/api/wallpapers/system", token, map[string]string{"name": "海边", "sortOrder": "3"})
	var wp map[string]any
	_ = json.Unmarshal(body, &wp)
	if resp.StatusCode != 200 {
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, string(body))
	}
	id := wp["_id"].(string)
	url := wp["url"].(string)
	if !strings.HasPrefix(url, "/uploads/wallpaper-") {
		t.Fatalf("upload url=%q", url)
	}
	if wp["enabled"] != true || wp["sortOrder"] != float64(3) {
		t.Fatalf("upload shape=%v", wp)
	}

	// 公开列表只含 select 字段（无 enabled/createdAt）。
	resp = c.json("GET", "/api/wallpapers/system", nil, &pub)
	if resp.StatusCode != 200 || len(pub) != 1 {
		t.Fatalf("system status=%d list=%v", resp.StatusCode, pub)
	}
	item := pub[0]
	if _, has := item["enabled"]; has {
		t.Fatalf("public item must not expose enabled: %v", item)
	}
	if item["name"] != "海边" || item["sortOrder"] != float64(3) {
		t.Fatalf("public item=%v", item)
	}

	// 管理列表含全部字段。
	var all []map[string]any
	resp = c.jsonH("GET", "/api/wallpapers/system/all", nil, authH, &all)
	if resp.StatusCode != 200 || len(all) != 1 {
		t.Fatalf("all status=%d list=%v", resp.StatusCode, all)
	}
	if all[0]["enabled"] != true || all[0]["uploadedBy"] == nil {
		t.Fatalf("all item=%v", all[0])
	}

	// 下架。
	var updated map[string]any
	resp = c.jsonH("PUT", "/api/wallpapers/system/"+id, map[string]any{"enabled": false}, authH, &updated)
	if resp.StatusCode != 200 || updated["enabled"] != false {
		t.Fatalf("update status=%d body=%v", resp.StatusCode, updated)
	}

	// 公开列表不再包含。
	resp = c.json("GET", "/api/wallpapers/system", nil, &pub)
	if resp.StatusCode != 200 || len(pub) != 0 {
		t.Fatalf("system after disable=%v", pub)
	}

	// 删除。
	var del map[string]string
	resp = c.jsonH("DELETE", "/api/wallpapers/system/"+id, nil, authH, &del)
	if resp.StatusCode != 200 || del["message"] != "已删除" {
		t.Fatalf("delete status=%d body=%v", resp.StatusCode, del)
	}
	// 删除不存在的 → 404。
	resp = c.jsonH("DELETE", "/api/wallpapers/system/"+id, nil, authH, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("delete missing status=%d want 404", resp.StatusCode)
	}
}

// TestWallpapersPersonal 个人壁纸：登录用户上传/列表/删除。
func TestWallpapersPersonal(t *testing.T) {
	e := cmsTestServer(t)
	c := newClient(t, e.ts.URL)
	email := "wp_" + fmt.Sprintf("%d", time.Now().UnixNano()) + "@test.com"
	c.registerAndLogin(email)

	var list []map[string]any
	resp := c.json("GET", "/api/wallpapers/personal", nil, &list)
	if resp.StatusCode != 200 || len(list) != 0 {
		t.Fatalf("personal empty status=%d list=%v", resp.StatusCode, list)
	}

	resp, body := c.doMultipart("/api/wallpapers/personal", "", map[string]string{"name": "我的"})
	var uploaded map[string]any
	_ = json.Unmarshal(body, &uploaded)
	if resp.StatusCode != 200 {
		t.Fatalf("personal upload status=%d body=%s", resp.StatusCode, string(body))
	}
	if uploaded["name"] != "我的" || uploaded["url"] == "" || uploaded["addedAt"] == nil {
		t.Fatalf("personal upload shape=%v", uploaded)
	}

	resp = c.json("GET", "/api/wallpapers/personal", nil, &list)
	if resp.StatusCode != 200 || len(list) != 1 || list[0]["url"] != uploaded["url"] {
		t.Fatalf("personal list=%v", list)
	}

	var del map[string]string
	resp = c.json("DELETE", "/api/wallpapers/personal", map[string]any{"url": uploaded["url"]}, &del)
	if resp.StatusCode != 200 || del["message"] != "已删除" {
		t.Fatalf("personal delete status=%d body=%v", resp.StatusCode, del)
	}
	resp = c.json("GET", "/api/wallpapers/personal", nil, &list)
	if resp.StatusCode != 200 || len(list) != 0 {
		t.Fatalf("personal after delete=%v", list)
	}
}

// ---- 友链 ----

// TestFriendLinksApplyAndAdmin 友链申请 + 超管审核（权限/校验/公开列表过滤）。
func TestFriendLinksApplyAndAdmin(t *testing.T) {
	e := cmsTestServer(t)
	c := newClient(t, e.ts.URL)
	token := e.createSuperAdmin(t)
	authH := map[string]string{"Authorization": "Bearer " + token}

	// 缺参 → 400。
	var out map[string]any
	resp := c.json("POST", "/api/friend-links/apply", map[string]any{"name": "站"}, &out)
	if resp.StatusCode != 400 || out["message"] != "站点名称和链接为必填项" {
		t.Fatalf("apply missing url status=%d body=%v", resp.StatusCode, out)
	}

	// 非 http/https URL → 400。
	resp = c.json("POST", "/api/friend-links/apply", map[string]any{"name": "站", "url": "ftp://x"}, &out)
	if resp.StatusCode != 400 || out["message"] != "链接格式不合法，仅支持 http/https 协议" {
		t.Fatalf("apply bad url status=%d body=%v", resp.StatusCode, out)
	}

	// 无效 altcha（去掉 dev-token 绕过）→ 400。
	resp = c.jsonH("POST", "/api/friend-links/apply", map[string]any{
		"name": "站", "url": "https://a.test", "altcha": "garbage",
	}, map[string]string{"x-dev-token": ""}, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("apply bad altcha status=%d want 400", resp.StatusCode)
	}

	// 匿名申请（dev-token 绕过验证码）。
	resp = c.json("POST", "/api/friend-links/apply", map[string]any{
		"name": "兽兽站", "url": "https://shoushou.test", "description": "好站",
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("apply status=%d body=%v", resp.StatusCode, out)
	}

	// 公开列表为空（pending 不展示）。
	var pub []map[string]any
	resp = c.json("GET", "/api/friend-links", nil, &pub)
	if resp.StatusCode != 200 || len(pub) != 0 {
		t.Fatalf("public list=%v", pub)
	}

	// 超管查看全部。
	var all []map[string]any
	resp = c.jsonH("GET", "/api/friend-links/all", nil, authH, &all)
	if resp.StatusCode != 200 || len(all) != 1 {
		t.Fatalf("all status=%d list=%v", resp.StatusCode, all)
	}
	linkID := all[0]["_id"].(string)
	if all[0]["status"] != "pending" || all[0]["isActive"] != false {
		t.Fatalf("pending shape=%v", all[0])
	}

	// 非超管查 all → 403。
	c.registerAndLogin("u_" + fmt.Sprintf("%d", time.Now().UnixNano()) + "@test.com")
	resp = c.json("GET", "/api/friend-links/all", nil, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("non-superadmin all status=%d want 403", resp.StatusCode)
	}

	// 超管直接创建友链。
	var created map[string]any
	resp = c.jsonH("POST", "/api/friend-links", map[string]any{"name": "官方", "url": "https://official.test"}, authH, &created)
	if resp.StatusCode != 200 || created["status"] != "approved" || created["isActive"] != true {
		t.Fatalf("create status=%d body=%v", resp.StatusCode, created)
	}

	// 无效 status → 400。
	resp = c.jsonH("PUT", "/api/friend-links/"+linkID, map[string]any{"status": "nope"}, authH, nil)
	if resp.StatusCode != 400 {
		t.Fatalf("bad status=%d want 400", resp.StatusCode)
	}

	// 审核通过（匿名申请者无 applicantId，不写通知）。
	var updated map[string]any
	resp = c.jsonH("PUT", "/api/friend-links/"+linkID, map[string]any{"status": "approved"}, authH, &updated)
	if resp.StatusCode != 200 || updated["status"] != "approved" || updated["isActive"] != true {
		t.Fatalf("approve status=%d body=%v", resp.StatusCode, updated)
	}

	// 公开列表现在有 2 条（官方 + 通过的申请）。
	resp = c.json("GET", "/api/friend-links", nil, &pub)
	if resp.StatusCode != 200 || len(pub) != 2 {
		t.Fatalf("public after approve=%v", pub)
	}

	// 删除。
	resp = c.jsonH("DELETE", "/api/friend-links/"+linkID, nil, authH, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
	resp = c.jsonH("DELETE", "/api/friend-links/"+linkID, nil, authH, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("delete missing status=%d want 404", resp.StatusCode)
	}
}

// TestFriendLinksApplicantNotification 登录用户申请被拒绝时写 friend_link_status 通知。
func TestFriendLinksApplicantNotification(t *testing.T) {
	e := cmsTestServer(t)
	c := newClient(t, e.ts.URL)
	token := e.createSuperAdmin(t)
	authH := map[string]string{"Authorization": "Bearer " + token}

	// 注册 + 登录一个普通用户（cookie 会话）。
	email := "fl_" + fmt.Sprintf("%d", time.Now().UnixNano()) + "@test.com"
	c.register(email)
	var login map[string]any
	c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, &login)
	userID := login["_id"].(string)
	if userID == "" {
		t.Fatalf("login failed: %v", login)
	}

	resp := c.json("POST", "/api/friend-links/apply", map[string]any{
		"name": "我的站", "url": "https://mine.test",
	}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("apply status=%d", resp.StatusCode)
	}

	// 超管查询并拒绝。
	var all []map[string]any
	resp = c.jsonH("GET", "/api/friend-links/all", nil, authH, &all)
	if resp.StatusCode != 200 || len(all) != 1 {
		t.Fatalf("all=%v", all)
	}
	linkID := all[0]["_id"].(string)

	var updated map[string]any
	resp = c.jsonH("PUT", "/api/friend-links/"+linkID, map[string]any{"status": "rejected"}, authH, &updated)
	if resp.StatusCode != 200 || updated["status"] != "rejected" || updated["isActive"] != false {
		t.Fatalf("reject status=%d body=%v", resp.StatusCode, updated)
	}

	// 申请者收到 friend_link_status 通知。
	uid, _ := primitive.ObjectIDFromHex(userID)
	notifs, err := e.repos.Notifications.FindByUser(context.Background(), uid, 1, 20)
	if err != nil {
		t.Fatalf("find notifs: %v", err)
	}
	found := false
	for _, n := range notifs {
		if n.Type == "friend_link_status" && strings.Contains(n.Message, "已拒绝") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no friend_link_status notification: %v", notifs)
	}
}
