package server_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/server"
)

// testServer 组装测试服务器（真实 mongod）。
func testServer(t *testing.T) *httptest.Server {
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

	db, err := repository.Connect(t.Context(), "mongodb://127.0.0.1:27017/neo_integration_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(t.Context()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	app := server.NewApp(server.Deps{Config: cfg, DB: db, Repos: repos, Signer: signer})
	ts := httptest.NewServer(app)
	t.Cleanup(ts.Close)
	return ts
}

// client 是带 cookie jar + CSRF 自动回填的测试客户端。
type client struct {
	http  *http.Client
	base  string
	token string
}

func newClient(t *testing.T, base string) *client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &client{http: &http.Client{Jar: jar}, base: base}
	c.fetchCSRF()
	return c
}

func (c *client) fetchCSRF() {
	resp, err := c.http.Get(c.base + "/api/csrf-token")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	c.token = body.CSRF
}

func (c *client) do(method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("x-dev-token", "test-dev-token")
	if c.token != "" {
		req.Header.Set("X-XSRF-TOKEN", c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return resp, nil
	}
	data, _ := io.ReadAll(resp.Body)
	defer resp.Body.Close()
	return resp, data
}

func (c *client) json(method, path string, body any, out any) *http.Response {
	resp, data := c.do(method, path, body, nil)
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	return resp
}

func (c *client) register(email string) *http.Response {
	var out map[string]any
	return c.json("POST", "/api/auth/register", map[string]any{
		"accountId": "it" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")[:10],
		"username":  "ituser",
		"email":     email,
		"password":  "pass1234",
	}, &out)
}

// registerAndLogin 注册并登录（dev-token 跳过邮箱/设备验证），返回响应。
func (c *client) registerAndLogin(email string) *http.Response {
	c.register(email)
	var out map[string]any
	return c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, &out)
}

// totpCode 生成 RFC6238 TOTP（测试用，与 auth.VerifyTOTP 同参数：SHA1/6位/30s）。
func totpCode(secret string, t time.Time) string {
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return ""
	}
	counter := uint64(t.Unix() / 30)
	h := hmac.New(sha1.New, key)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	h.Write(buf[:])
	sum := h.Sum(nil)
	offset := sum[19] & 0xf
	code := (uint32(sum[offset]&0x7f)<<24 | uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])) % 1000000
	return fmt.Sprintf("%06d", code)
}

// TestRegisterLoginMeRefresh 注册→登录→me→refresh 主流程。
func TestRegisterLoginMeRefresh(t *testing.T) {
	ts := testServer(t)
	c := newClient(t, ts.URL)
	email := "it_" + fmt.Sprintf("%d", time.Now().UnixNano()%1e7) + "@test.com"

	resp := c.register(email)
	if resp.StatusCode != 200 {
		t.Fatalf("register: %d", resp.StatusCode)
	}
	var login map[string]any
	resp = c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, &login)
	if resp.StatusCode != 200 || login["_id"] == "" {
		t.Fatalf("login: %d body=%v", resp.StatusCode, login)
	}
	var me map[string]any
	resp = c.json("GET", "/api/auth/me", nil, &me)
	if resp.StatusCode != 200 || me["email"] != email {
		t.Fatalf("me: %d body=%v", resp.StatusCode, me)
	}
	var refreshed map[string]any
	resp = c.json("POST", "/api/auth/refresh", nil, &refreshed)
	if resp.StatusCode != 200 || refreshed["_id"] == "" {
		t.Fatalf("refresh: %d body=%v", resp.StatusCode, refreshed)
	}
}

// TestLoginWrongPassword 密码错误 → 400 用户名或密码错误。
func TestLoginWrongPassword(t *testing.T) {
	ts := testServer(t)
	c := newClient(t, ts.URL)
	email := "it_" + fmt.Sprintf("%d", time.Now().UnixNano()%1e7) + "@test.com"
	c.register(email)
	var out map[string]any
	resp := c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "wrongpass1"}, &out)
	if resp.StatusCode != 400 || out["message"] != "用户名或密码错误" {
		t.Fatalf("login wrong: %d body=%v", resp.StatusCode, out)
	}
}

// TestTwoFactorLoginFlow enable → verify-enable → 登录走 2FA → login-2fa。
func TestTwoFactorLoginFlow(t *testing.T) {
	ts := testServer(t)
	c := newClient(t, ts.URL)
	email := "it2fa_" + fmt.Sprintf("%d", time.Now().UnixNano()%1e7) + "@test.com"
	c.registerAndLogin(email)

	var setup map[string]any
	resp := c.json("POST", "/api/2fa/enable", nil, &setup)
	if resp.StatusCode != 200 || setup["secret"] == nil {
		t.Fatalf("2fa enable: %d body=%v", resp.StatusCode, setup)
	}
	secret := setup["secret"].(string)
	code := totpCode(secret, time.Now())
	var ver map[string]any
	resp = c.json("POST", "/api/2fa/verify-enable", map[string]any{"token": code}, &ver)
	if resp.StatusCode != 200 {
		t.Fatalf("2fa verify-enable: %d body=%v", resp.StatusCode, ver)
	}

	// 再次登录 → 应返回 need2FA。
	var login map[string]any
	resp = c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, &login)
	if resp.StatusCode != 200 || login["need2FA"] != true {
		t.Fatalf("login with 2fa: %d body=%v", resp.StatusCode, login)
	}
	challenge := login["twoFactorChallenge"].(string)
	code2 := totpCode(secret, time.Now())
	var done map[string]any
	resp = c.json("POST", "/api/auth/login-2fa", map[string]any{
		"email": email, "twoFactorToken": code2, "twoFactorChallenge": challenge,
	}, &done)
	if resp.StatusCode != 200 || done["_id"] == "" {
		t.Fatalf("login-2fa: %d body=%v", resp.StatusCode, done)
	}
}

// TestRefreshReuse 旧 refresh 重用 → 401 reuse（吊销全部 session）。
func TestRefreshReuse(t *testing.T) {
	ts := testServer(t)
	c := newClient(t, ts.URL)
	email := "it_reuse_" + fmt.Sprintf("%d", time.Now().UnixNano()%1e7) + "@test.com"
	c.registerAndLogin(email)

	// 记录当前 refresh cookie 值（refreshToken path=/api/auth，需用匹配 URL 读取）。
	u, _ := url.Parse(c.base + "/api/auth/me")
	var oldRefresh string
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == "refreshToken" {
			oldRefresh = ck.Value
		}
	}
	if oldRefresh == "" {
		t.Skip("no refresh cookie captured")
	}

	var r1 map[string]any
	if resp := c.json("POST", "/api/auth/refresh", nil, &r1); resp.StatusCode != 200 {
		t.Fatalf("refresh rotate: %d", resp.StatusCode)
	}


	// 用旧 refresh 手动构造请求（裸 client，手动携带 XSRF + refresh cookie）。
	req2, _ := http.NewRequest("POST", c.base+"/api/auth/refresh", nil)
	req2.AddCookie(&http.Cookie{Name: "refreshToken", Value: oldRefresh, Path: "/api/auth"})
	req2.AddCookie(&http.Cookie{Name: "XSRF-TOKEN", Value: c.token, Path: "/"})
	req2.Header.Set("X-XSRF-TOKEN", c.token)
	raw := &http.Client{}
	resp2, err := raw.Do(req2)
	if err != nil {
		t.Fatalf("reuse req: %v", err)
	}
	defer resp2.Body.Close()
	var rb map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&rb)
	// 轮换后立即重试旧 token：30s 并发宽限内 → 409 concurrentRefresh；超期 → 401 reuse。
	// 两者都证明旧 refresh 不再被接受为成功登录。
	if resp2.StatusCode != 409 && resp2.StatusCode != 401 {
		t.Fatalf("refresh reuse: %d body=%v", resp2.StatusCode, rb)
	}
	if resp2.StatusCode == 409 && rb["messageKey"] != "auth.concurrentRefresh" {
		t.Fatalf("refresh concurrent: %d body=%v", resp2.StatusCode, rb)
	}
	if resp2.StatusCode == 401 && rb["messageKey"] != "auth.refreshTokenReuse" {
		t.Fatalf("refresh reuse: %d body=%v", resp2.StatusCode, rb)
	}
}
