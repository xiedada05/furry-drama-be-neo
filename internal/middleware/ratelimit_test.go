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

// TestRateLimitClientIPHeaders 生产（trustXFF）下客户端 IP 解析优先级：
// CF-Connecting-IP > X-Real-IP > X-Forwarded-For 首值 > RemoteAddr；
// 不同头代表不同用户时各自独立计数（不共享限流桶）。
// 注意 TrustXFF 由 app.go 传入（!IsDev），构造时必须显式置 true 才会走头解析分支。
func TestRateLimitClientIPHeaders(t *testing.T) {
	spec := ratelimit.Spec{Name: "probe", Mounts: []string{"/probe"}, Window: time.Minute, Max: 1, Message: "x"}
	r := newTestLimitEngine(spec, RateLimitOpts{IsDev: false, TrustXFF: true}, "/api/probe")

	get := func(headers map[string]string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/probe", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// 同一 CF IP 第 2 次 → 429；换一个 CF IP 又能过（桶按 IP 分离）。
	if c := get(map[string]string{"CF-Connecting-IP": "1.1.1.1"}); c != 200 {
		t.Fatalf("CF IP 第一次应通过: %d", c)
	}
	if c := get(map[string]string{"CF-Connecting-IP": "1.1.1.1"}); c != 429 {
		t.Fatalf("CF IP 第二次应 429: %d", c)
	}
	if c := get(map[string]string{"CF-Connecting-IP": "2.2.2.2"}); c != 200 {
		t.Fatalf("不同 CF IP 应独立计数: %d", c)
	}
	// CF 优先于 X-Real-IP 与 XFF（CF 与 XRI 不同值时按 CF 计）。
	if c := get(map[string]string{
		"CF-Connecting-IP": "1.1.1.1", "X-Real-IP": "9.9.9.9", "X-Forwarded-For": "8.8.8.8",
	}); c != 429 {
		t.Fatalf("CF-Connecting-IP 应优先: %d", c)
	}
	// 无 CF 时 X-Real-IP 优先于 XFF。
	if c := get(map[string]string{"X-Real-IP": "3.3.3.3", "X-Forwarded-For": "1.1.1.1"}); c != 200 {
		t.Fatalf("X-Real-IP 应优先于 XFF: %d", c)
	}
	// 仅 XFF 时取首值。
	if c := get(map[string]string{"X-Forwarded-For": "4.4.4.4, 5.5.5.5"}); c != 200 {
		t.Fatalf("XFF 首值应作为独立桶: %d", c)
	}
	if c := get(map[string]string{"X-Forwarded-For": "4.4.4.4, 5.5.5.5"}); c != 429 {
		t.Fatalf("同 XFF 首值第二次应 429: %d", c)
	}
	// 无任何头 → RemoteAddr（10.0.0.1 独立桶）。
	if c := get(nil); c != 200 {
		t.Fatalf("RemoteAddr 应作为独立桶: %d", c)
	}
}
