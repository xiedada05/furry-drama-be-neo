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

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/server"
)

// registerTestServer 组装 register 测试专用服务器（独立库 neo_register_test）。
// 不用 integration_test.go 的 testServer：其 Drop 用 t.Context()（测试结束已 cancel，
// Drop 被忽略导致 neo_integration_test 残留累积），本测试用 context.Background() 清理。
func registerTestServer(t *testing.T) *httptest.Server {
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

	db, err := repository.Connect(context.Background(), "mongodb://127.0.0.1:27017/neo_register_test", "", 10)
	if err != nil {
		t.Fatalf("connect db: %v", err)
	}
	t.Cleanup(func() { _ = db.Drop(context.Background()) })
	repos := repository.NewRepos(db, cfg.Security.LoginMaxAttempts, cfg.Security.LoginLockMinutes)
	signer := auth.NewSigner(cfg.JWT.Secret)
	app := server.NewApp(server.Deps{Config: cfg, DB: db, Repos: repos, Signer: signer})
	ts := httptest.NewServer(app)
	t.Cleanup(ts.Close)
	return ts
}

// TestRegisterDuplicateEmail 验证同邮箱重复注册被拒绝（400 该邮箱已被注册）。
// 差分场景 register-duplicate 的 neo 侧覆盖：差分脚本中该场景因两端 register
// 限流(3/h)计数不同步（Express 常驻进程 vs neo 重启清零）而偶发分叉，行为由本测试锁定。
func TestRegisterDuplicateEmail(t *testing.T) {
	ts := registerTestServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("dup_%d@test.com", time.Now().UnixNano())

	resp, body := c.do("POST", "/api/auth/register", map[string]any{
		"accountId": "dupacc1", "username": "u1", "email": email, "password": "pass1234",
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first register status=%d body=%s want 200", resp.StatusCode, string(body))
	}

	var out struct {
		Message string `json:"message"`
	}
	resp = c.json("POST", "/api/auth/register", map[string]any{
		"accountId": "other", "username": "u2", "email": email, "password": "pass1234",
	}, &out)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("second register status=%d want 400", resp.StatusCode)
	}
	if out.Message != "该邮箱已被注册" {
		t.Fatalf("second register message=%q want 该邮箱已被注册", out.Message)
	}
}

// TestRegisterDuplicateAccountID 验证同 accountId 重复注册被拒绝（400 该账号ID已被占用）。
func TestRegisterDuplicateAccountID(t *testing.T) {
	ts := registerTestServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("dupa_%d@test.com", time.Now().UnixNano())

	resp, body := c.do("POST", "/api/auth/register", map[string]any{
		"accountId": "dupaccx", "username": "u1", "email": email, "password": "pass1234",
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first register status=%d body=%s want 200", resp.StatusCode, string(body))
	}

	var out struct {
		Message string `json:"message"`
	}
	resp = c.json("POST", "/api/auth/register", map[string]any{
		"accountId": "dupaccx", "username": "u2", "email": "other_" + email, "password": "pass1234",
	}, &out)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("second register status=%d want 400", resp.StatusCode)
	}
	if out.Message != "该账号ID已被占用" {
		t.Fatalf("second register message=%q want 该账号ID已被占用", out.Message)
	}
}
