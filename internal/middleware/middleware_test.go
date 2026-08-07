package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/x", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	w := do(r, http.MethodGet, "/x", "", "")

	// 期望值来自实测 helmet 8.2.0 相同配置的输出（src/index.js:179-196）。
	expected := map[string]string{
		"Content-Security-Policy":           "default-src 'self';script-src 'self' 'unsafe-inline' blob:;style-src 'self' 'unsafe-inline';img-src 'self' data: blob:;font-src 'self';connect-src 'self' https://api.mymemory.translated.net https://translate.googleapis.com https://api.cognitive.microsofttranslator.com https://ipapi.co;frame-src 'self' https://player.bilibili.com https://www.youtube.com https://embed.nicovideo.jp;object-src 'none';upgrade-insecure-requests;base-uri 'self';form-action 'self';frame-ancestors 'self';script-src-attr 'none'",
		"Origin-Agent-Cluster":              "?1",
		"Referrer-Policy":                   "no-referrer",
		"Strict-Transport-Security":         "max-age=31536000; includeSubDomains",
		"X-Content-Type-Options":            "nosniff",
		"X-DNS-Prefetch-Control":            "off",
		"X-Download-Options":                "noopen",
		"X-Frame-Options":                   "SAMEORIGIN",
		"X-Permitted-Cross-Domain-Policies": "none",
		"X-XSS-Protection":                  "0",
	}
	for k, v := range expected {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestGzipCompressesWhenRequested(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Gzip())
	payload := `{"data":"` + strings.Repeat("x", 5000) + `"}`
	r.GET("/api/big", func(c *gin.Context) { c.Data(200, "application/json", []byte(payload)) })

	req := httptest.NewRequest(http.MethodGet, "/api/big", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", w.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("缺少 Vary: Accept-Encoding, got %q", w.Header().Get("Vary"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip 解压失败: %v", err)
	}
	decoded, _ := io.ReadAll(zr)
	if string(decoded) != payload {
		t.Fatalf("解压后内容不匹配")
	}
}

func TestGzipSkipsWithoutAcceptEncoding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Gzip())
	r.GET("/api/big", func(c *gin.Context) { c.Data(200, "application/json", []byte(`{"ok":true}`)) })

	w := do(r, http.MethodGet, "/api/big", "", "")
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Fatalf("无 Accept-Encoding 不应压缩")
	}
}

func TestTimeoutSetsContextDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timeout())
	r.GET("/x", func(c *gin.Context) {
		dl, ok := c.Request.Context().Deadline()
		if !ok {
			t.Fatalf("未设置 deadline")
		}
		remaining := time.Until(dl)
		if remaining < 29*time.Second || remaining > 30*time.Second {
			t.Fatalf("deadline 剩余 = %v, want 约 30s", remaining)
		}
		c.JSON(200, gin.H{"ok": true})
	})
	w := do(r, http.MethodGet, "/x", "", "")
	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
}

// captureHandler 收集 slog 记录，用于验证日志输出条件。
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}
func (c *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(string) slog.Handler      { return c }

func (c *captureHandler) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.records)
}

func TestRequestLoggerConditions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cap := &captureHandler{}
	log := slog.New(cap)

	r := gin.New()
	r.Use(RequestLogger(log, false))
	r.GET("/fast", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.POST("/write", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.GET("/err", func(c *gin.Context) { c.JSON(400, gin.H{"message": "bad"}) })

	do(r, http.MethodGet, "/fast", "", "")
	if cap.count() != 0 {
		t.Fatalf("GET 200 快速请求不应记录, got %d", cap.count())
	}
	do(r, http.MethodPost, "/write", `{}`, "application/json")
	do(r, http.MethodGet, "/err", "", "")
	if cap.count() != 2 {
		t.Fatalf("POST 与 4xx 应记录, got %d", cap.count())
	}
}

func TestRequestLoggerSlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cap := &captureHandler{}
	log := slog.New(cap)

	r := gin.New()
	r.Use(RequestLogger(log, false))
	r.GET("/slow", func(c *gin.Context) {
		time.Sleep(1100 * time.Millisecond)
		c.JSON(200, gin.H{"ok": true})
	})

	do(r, http.MethodGet, "/slow", "", "")
	if cap.count() != 1 {
		t.Fatalf(">1000ms 的 GET 应记录, got %d", cap.count())
	}
}

func TestSlowLoggerDev(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cap := &captureHandler{}
	log := slog.New(cap)

	r := gin.New()
	r.Use(SlowLogger(log, true))
	r.GET("/api/series/list", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.GET("/api/plain", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	// dev：路径含 /list 即使 200 也记录
	do(r, http.MethodGet, "/api/series/list", "", "")
	if cap.count() != 1 {
		t.Fatalf("dev 下 /list 路径应记录, got %d", cap.count())
	}
	// 普通快速 GET 200 不记录
	do(r, http.MethodGet, "/api/plain", "", "")
	if cap.count() != 1 {
		t.Fatalf("dev 下普通 GET 200 不应记录, got %d", cap.count())
	}
}

func TestSlowLoggerProdOnlySlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cap := &captureHandler{}
	log := slog.New(cap)

	r := gin.New()
	r.Use(SlowLogger(log, false))
	r.GET("/api/series/list", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	// prod：快速请求即使含 /list 也不记录
	do(r, http.MethodGet, "/api/series/list", "", "")
	if cap.count() != 0 {
		t.Fatalf("prod 下快速请求不应记录, got %d", cap.count())
	}
}
