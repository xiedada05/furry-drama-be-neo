package ratelimit

import (
	"testing"
	"time"
)

func TestIPKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"192.168.1.5", "192.168.1.5"},
		{"10.0.0.1", "10.0.0.1"},
		// IPv4-mapped IPv6 → 归一化为 IPv4（对齐 ip-address is4() → to4().correctForm()）
		{"::ffff:127.0.0.1", "127.0.0.1"},
		{"::ffff:192.168.1.5", "192.168.1.5"},
		// 纯 IPv6 → /56 网络前缀 + "/56" 后缀
		{"2001:db8:0:1:2:3:4:5", "2001:db8::/56"},
		{"::1", "::/56"},
		{"fe80::1", "fe80::/56"},
		{"2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "2001:db8:ffff:ff00::/56"},
		// 无法解析 → 原样返回
		{"", ""},
		{"not-an-ip", "not-an-ip"},
	}
	for _, tc := range cases {
		if got := IPKey(tc.in); got != tc.want {
			t.Errorf("IPKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeXFF(t *testing.T) {
	cases := []struct{ in, want string }{
		{"203.0.113.7, 10.0.0.1", "203.0.113.7"},
		{"203.0.113.7", "203.0.113.7"},
		{"  203.0.113.7 ,10.0.0.1", "203.0.113.7"},
		{"", ""},
		{" , ", ""},
	}
	for _, tc := range cases {
		if got := NormalizeXFF(tc.in); got != tc.want {
			t.Errorf("NormalizeXFF(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSpecParams 校验命名限流器参数逐项对齐 rateLimits.js（防回归）。
func TestSpecParams(t *testing.T) {
	cases := []struct {
		name   string
		spec   Spec
		window time.Duration
		max    int
		msg    string
		mounts []string
	}{
		{"global", GlobalSpec, time.Minute, 300, "请求过于频繁，请稍后再试", nil},
		{"captcha", CaptchaSpec, time.Minute, 60, "请求过于频繁，请稍后再试", []string{"/auth/captcha"}},
		{"auth", AuthSpec, 15 * time.Minute, 5, "登录尝试过多，请15分钟后再试", []string{"/auth/login"}},
		{"adminAuth", AdminAuthSpec, 15 * time.Minute, 5, "登录尝试过多，请15分钟后再试", []string{"/admin/login"}},
		{"register", RegisterSpec, time.Hour, 3, "注册尝试过多，请1小时后再试", []string{"/auth/register"}},
		{"checkAccountId", CheckAccountIDSpec, time.Minute, 20, "请求过于频繁，请稍后再试", []string{"/auth/check-accountId"}},
		{"passwordReset", PasswordResetSpec, time.Hour, 3, "操作过于频繁，请稍后再试",
			[]string{"/auth/forgot-password", "/auth/reset-password", "/auth/change-password"}},
		{"changeEmail", ChangeEmailSpec, time.Hour, 3, "操作过于频繁，请稍后再试", []string{"/auth/change-email"}},
		{"twoFactor", TwoFactorSpec, 15 * time.Minute, 5, "操作过于频繁，请15分钟后再试",
			[]string{"/auth/login-2fa", "/2fa/verify-enable", "/2fa/disable",
				"/2fa/verify", "/auth/refresh", "/auth/verify-device", "/auth/confirm-device-login"}},
		{"emailVerify", EmailVerifySpec, time.Hour, 5, "操作过于频繁，请稍后再试",
			[]string{"/auth/verify-email", "/auth/resend-verification", "/auth/resend-verification-by-email"}},
		{"requestEmailChange", RequestEmailChangeSpec, time.Hour, 3, "操作过于频繁，请稍后再试",
			[]string{"/auth/request-email-change"}},
	}
	for _, tc := range cases {
		if tc.spec.Window != tc.window || tc.spec.Max != tc.max || tc.spec.Message != tc.msg {
			t.Errorf("%s: window=%v max=%d msg=%q, want %v/%d/%q",
				tc.name, tc.spec.Window, tc.spec.Max, tc.spec.Message, tc.window, tc.max, tc.msg)
		}
		if tc.spec.Name != tc.name {
			t.Errorf("%s: Name=%q", tc.name, tc.spec.Name)
		}
		if len(tc.spec.Mounts) != len(tc.mounts) {
			t.Errorf("%s: Mounts=%v, want %v", tc.name, tc.spec.Mounts, tc.mounts)
		}
		for i := range tc.mounts {
			if i < len(tc.spec.Mounts) && tc.spec.Mounts[i] != tc.mounts[i] {
				t.Errorf("%s: Mounts=%v, want %v", tc.name, tc.spec.Mounts, tc.mounts)
			}
		}
	}
}
