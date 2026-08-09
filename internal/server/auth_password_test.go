package server_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestChangePassword 验证登录后修改密码的完整流程（改密成功 + 新密码可登录）。
// 差分场景 change-password 的 neo 侧覆盖：差分脚本中该场景因两端 register/
// passwordReset 限流(3/h)计数不同步（Express 常驻进程 vs neo 重启清零）而级联分叉
// （register 429→login 400→改密 429），顺序已对齐（rl 前置），行为由本测试锁定。
func TestChangePassword(t *testing.T) {
	ts := registerTestServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("cp_%d@test.com", time.Now().UnixNano())

	c.register(email)
	if resp := c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d want 200", resp.StatusCode)
	}

	var out struct {
		Message string `json:"message"`
	}
	resp := c.json("PUT", "/api/auth/change-password", map[string]any{
		"currentPassword": "pass1234", "newPassword": "newpass123",
	}, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change-password status=%d want 200", resp.StatusCode)
	}
	if out.Message != "密码修改成功" {
		t.Fatalf("change-password message=%q want 密码修改成功", out.Message)
	}

	// 用新密码重新登录，验证改密生效。
	resp = c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "newpass123"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login with new password status=%d want 200", resp.StatusCode)
	}
}

// TestChangePasswordWrongCurrent 验证当前密码错误时改密被拒绝（400）。
func TestChangePasswordWrongCurrent(t *testing.T) {
	ts := registerTestServer(t)
	c := newClient(t, ts.URL)
	email := fmt.Sprintf("cpw_%d@test.com", time.Now().UnixNano())

	c.register(email)
	if resp := c.json("POST", "/api/auth/login", map[string]any{"email": email, "password": "pass1234"}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d want 200", resp.StatusCode)
	}

	resp := c.json("PUT", "/api/auth/change-password", map[string]any{
		"currentPassword": "wrongpass1", "newPassword": "newpass123",
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("change-password wrong current status=%d want 400", resp.StatusCode)
	}
}
