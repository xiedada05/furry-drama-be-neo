package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func bodyRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyParse())
	handler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"body": GetBody(c)})
	}
	r.Any("/api/*rest", handler)
	r.NoRoute(handler)
	return r
}

func TestBodyParseJSON(t *testing.T) {
	r := bodyRouter()
	w := do(r, http.MethodPost, "/api/x", `{"a":1,"b":"hello"}`, "application/json")
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	var resp struct {
		Body map[string]any `json:"body"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Body["a"].(float64) != 1 || resp.Body["b"] != "hello" {
		t.Fatalf("body = %v", resp.Body)
	}
}

func TestBodyParseURLEncodedNested(t *testing.T) {
	r := bodyRouter()
	w := do(r, http.MethodPost, "/api/x", "a=1&b[c]=2&b[d]=3", "application/x-www-form-urlencoded")
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	var resp struct {
		Body map[string]any `json:"body"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	b := resp.Body["b"].(map[string]any)
	if b["c"] != "2" || b["d"] != "3" {
		t.Fatalf("嵌套表单解析错误: %v", resp.Body)
	}
	if resp.Body["a"] != "1" {
		t.Fatalf("a = %v, want 1", resp.Body["a"])
	}
}

func TestBodyParseOversizeJSON413(t *testing.T) {
	r := bodyRouter()
	// 构造 >1MB 的 JSON 体
	big := `{"data":"` + strings.Repeat("a", (1<<20)+100) + `"}`
	w := do(r, http.MethodPost, "/api/x", big, "application/json")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限状态码=%d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "request entity too large") {
		t.Fatalf("413 body = %s", w.Body.String())
	}
}

func TestBodyParseBackupImport50mb(t *testing.T) {
	r := bodyRouter()
	// 2MB > 1MB 但 < 50MB：备份导入路径应放行
	big := `{"data":"` + strings.Repeat("a", (2<<20)+100) + `"}`
	for _, p := range []string{"/api/backup/import", "/api/v1/backup/import"} {
		w := do(r, http.MethodPost, p, big, "application/json")
		if w.Code != 200 {
			t.Fatalf("GET %s 2MB body 状态码=%d, want 200", p, w.Code)
		}
	}
}

func TestBodyParseInvalidJSON400(t *testing.T) {
	r := bodyRouter()
	w := do(r, http.MethodPost, "/api/x", `{"a":`, "application/json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无效 JSON 状态码=%d, want 400", w.Code)
	}
}

func TestBodyParseOtherContentTypeSkipped(t *testing.T) {
	r := bodyRouter()
	// text/plain 不解析
	w := do(r, http.MethodPost, "/api/x", "hello", "text/plain")
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"body":null`) {
		t.Fatalf("text/plain 不应解析 body: %s", w.Body.String())
	}
}

func TestBodyParseEmptyJSON(t *testing.T) {
	r := bodyRouter()
	w := do(r, http.MethodPost, "/api/x", "", "application/json")
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	// 对齐 Express：空 JSON 体 → req.body = {}
	if !strings.Contains(w.Body.String(), `"body":{}`) {
		t.Fatalf("空 JSON 体应得到空对象: %s", w.Body.String())
	}
}

func TestGetSetBodyHelpers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		SetBody(c, map[string]any{"k": "v"})
		c.Next()
	})
	r.GET("/x", func(c *gin.Context) {
		b := GetBody(c)
		if b["k"] != "v" {
			t.Fatalf("GetBody = %v", b)
		}
		c.JSON(200, gin.H{"ok": true})
	})
	w := do(r, http.MethodGet, "/x", "", "")
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
}

func TestBodyParseRestoresBody(t *testing.T) {
	// BodyParse 读取后应还原 c.Request.Body，供后续 SanitizeInput / ShouldBindJSON 读取。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyParse())
	r.POST("/api/x", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		if err != nil {
			t.Fatalf("读取还原后的 body: %v", err)
		}
		if string(raw) != `{"a":1}` {
			t.Fatalf("还原后的 body = %q, want %q", string(raw), `{"a":1}`)
		}
		c.JSON(200, gin.H{"ok": true})
	})
	w := do(r, http.MethodPost, "/api/x", `{"a":1}`, "application/json")
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
}
