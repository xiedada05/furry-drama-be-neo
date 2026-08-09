package security

import "testing"

// TestSanitizeStripsScript script 标签（含内容）被剥除——防 XSS 的核心。
func TestSanitizeStripsScript(t *testing.T) {
	if got := Sanitize(`<script>alert(1)</script>`); got != "" {
		t.Fatalf("script 应被剥除, got %q", got)
	}
	if got := Sanitize(`<script>alert(1)</script> after`); got != " after" {
		t.Fatalf("script 后文本应保留, got %q", got)
	}
}

// TestSanitizeKeepsWhitelist 白名单标签保留。
func TestSanitizeKeepsWhitelist(t *testing.T) {
	if got := Sanitize(`<b>bold</b>`); got != `<b>bold</b>` {
		t.Fatalf("b 标签应保留, got %q", got)
	}
	if got := Sanitize(`<p>para</p>`); got != `<p>para</p>` {
		t.Fatalf("p 标签应保留, got %q", got)
	}
}

// TestSanitizeJavascriptURL javascript: URL 被拒绝（href 剥除）。
func TestSanitizeJavascriptURL(t *testing.T) {
	if got := Sanitize(`<a href="javascript:alert(1)">x</a>`); got != "x" {
		t.Fatalf("javascript href 应被拒绝, got %q", got)
	}
	// 合法 https 链接保留。
	if got := Sanitize(`<a href="https://example.com">link</a>`); got != `<a href="https://example.com">link</a>` {
		t.Fatalf("合法链接应保留, got %q", got)
	}
}

// TestSanitizeDropsEventHandler 事件处理属性（onerror/onclick）被剥除。
func TestSanitizeDropsEventHandler(t *testing.T) {
	if got := Sanitize(`<img src="x" onerror="alert(1)">`); got != `<img src="x">` {
		t.Fatalf("onerror 应被剥除, got %q", got)
	}
	if got := Sanitize(`<div onclick="evil()">div</div>`); got != `<div>div</div>` {
		t.Fatalf("onclick 应被剥除, got %q", got)
	}
}

// TestSanitizeDropsComment HTML 注释被剥除。
func TestSanitizeDropsComment(t *testing.T) {
	if got := Sanitize(`<!-- comment -->visible`); got != "visible" {
		t.Fatalf("注释应被剥除, got %q", got)
	}
}

// TestSanitizeDropsUnknownTag 白名单外标签剥除（保留文本）。
func TestSanitizeDropsUnknownTag(t *testing.T) {
	if got := Sanitize(`plain <tag> text`); got != "plain  text" {
		t.Fatalf("未知标签应剥除, got %q", got)
	}
}

// TestSanitizeValueKeyStrip 剥离 $ 前缀与含 . 的键（防 NoSQL 注入）。
func TestSanitizeValueKeyStrip(t *testing.T) {
	in := map[string]any{
		"email":    "a@b.com",
		"$gt":      "evil",
		"a.b":      "nested",
		"username": "<script>u</script>",
	}
	out := SanitizeValue(in).(map[string]any)
	if _, ok := out["$gt"]; ok {
		t.Fatal("$ 前缀键应被剥离")
	}
	if _, ok := out["a.b"]; ok {
		t.Fatal("含 . 键应被剥离")
	}
	if out["email"] != "a@b.com" {
		t.Fatalf("普通字段应保留: %v", out["email"])
	}
}

// TestSanitizeValuePasswordSkip 密码类字段不转义（保留原值）。
func TestSanitizeValuePasswordSkip(t *testing.T) {
	in := map[string]any{
		"password":    "p<ass>1234",
		"newPassword": "n<ew>1234",
		"username":    "<b>u</b>",
	}
	out := SanitizeValue(in).(map[string]any)
	if out["password"] != "p<ass>1234" {
		t.Fatalf("密码字段应保留原值: %v", out["password"])
	}
	if out["newPassword"] != "n<ew>1234" {
		t.Fatalf("newPassword 字段应保留原值: %v", out["newPassword"])
	}
}

// TestSanitizeValueArray 数组逐元素递归清洗。
func TestSanitizeValueArray(t *testing.T) {
	in := []any{"<b>x</b>", map[string]any{"$ne": 1, "k": "<script>y</script>"}}
	out := SanitizeValue(in).([]any)
	if out[0] != "<b>x</b>" {
		t.Fatalf("数组元素应保留白名单标签: %v", out[0])
	}
	m := out[1].(map[string]any)
	if _, ok := m["$ne"]; ok {
		t.Fatal("数组内 $ 键应剥离")
	}
}

// TestValidatePassword 密码规则：≥8 位 + 字母 + 数字。
func TestValidatePassword(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"short1", "密码长度至少8位"},
		{"12345678", "密码必须包含至少一个字母"},
		{"abcdefgh", "密码必须包含至少一个数字"},
		{"", "密码长度至少8位"},
		{"pass1234", ""},
	}
	for _, c := range cases {
		if got := ValidatePassword(c.in); got != c.want {
			t.Fatalf("ValidatePassword(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
