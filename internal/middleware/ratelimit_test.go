package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
)

// do 执行一次请求（供本包测试通用）。
func do(r *gin.Engine, method, path string, body string, ct string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func newTestLimitEngine(spec ratelimit.Spec, opts RateLimitOpts, paths ...string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	for _, p := range paths {
		r.GET(p, RateLimit(spec, opts), func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	}
	return r
}

// TestRateLimitTriggers429 超过上限 → 429 + message + 限流头。
func TestRateLimitTriggers429(t *testing.T) {
	spec := ratelimit.Spec{Name: "test", Window: time.Minute, Max: 2, Message: "too many"}
	r := newTestLimitEngine(spec, RateLimitOpts{IsDev: true}, "/api/probe")
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
		if w.Code != 200 {
			t.Fatalf("请求 %d 应通过: %d", i+1, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
	if w.Code != 429 {
		t.Fatalf("第 3 次应 429, got %d", w.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["message"] != "too many" {
		t.Fatalf("429 message 不符: %v", body)
	}
	if w.Header().Get("RateLimit-Limit") != "2" {
		t.Fatalf("RateLimit-Limit 应为 2: %s", w.Header().Get("RateLimit-Limit"))
	}
}

// TestRateLimitPerEndpointMount 端点型限流只匹配挂载路径（v1 不受影响）。
func TestRateLimitPerEndpointMount(t *testing.T) {
	spec := ratelimit.Spec{Name: "login", Mounts: []string{"/auth/login"}, Window: time.Minute, Max: 1, Message: "x"}
	r := newTestLimitEngine(spec, RateLimitOpts{IsDev: true}, "/api/auth/login", "/api/v1/auth/login")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	if w.Code != 200 {
		t.Fatalf("第一次应通过: %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	if w.Code != 429 {
		t.Fatalf("同路径第二次应 429: %d", w.Code)
	}
	// v1 路径不匹配端点限流 → 多次请求都通过。
	for i := 0; i < 3; i++ {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil))
		if w.Code != 200 {
			t.Fatalf("v1 路径不应触发端点限流, 第 %d 次: %d", i+1, w.Code)
		}
	}
}

// TestRateLimitAuthSkip dev + SKIP_RATE_LIMIT 时 auth 限流跳过。
func TestRateLimitAuthSkip(t *testing.T) {
	spec := ratelimit.AuthSpec
	r := newTestLimitEngine(spec, RateLimitOpts{IsDev: true, SkipRateLimit: true}, "/api/auth/login")
	for i := 0; i < 6; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
		if w.Code != 200 {
			t.Fatalf("跳过时应全通过, 第 %d 次: %d", i+1, w.Code)
		}
	}
}
