package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
)

// stubStore 记录所有 Inc 的 key 并给出单调计数，用于验证挂载语义与 429 触发。
type stubStore struct {
	mu   sync.Mutex
	keys []string
}

func (s *stubStore) Inc(key string, window time.Duration, max int) (int, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	n := 0
	for _, k := range s.keys {
		if k == key {
			n++
		}
	}
	if n > max {
		return n, time.Second, nil
	}
	return n, 0, nil
}

func (s *stubStore) keyCounts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, k := range s.keys {
		out[k]++
	}
	return out
}

func (s *stubStore) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

// newTestRouter 构造带 mw 的测试路由（任何路径都返回 200）。
func newTestRouter(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw)
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	r.Any("/api/*rest", ok)
	r.NoRoute(ok)
	return r
}

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

// httptest.NewRequest 的 RemoteAddr 固定为 "192.0.2.1:1234"。
const testRemoteAddr = "192.0.2.1"

func TestRateLimitGlobalMatching(t *testing.T) {
	store := &stubStore{}
	r := newTestRouter(RateLimit(store, ratelimit.GlobalSpec, RateLimitOpts{}))

	for _, p := range []string{"/api/health", "/api/v1/foo", "/api/anything"} {
		if w := do(r, http.MethodGet, p, "", ""); w.Code != 200 {
			t.Fatalf("GET %s => %d, want 200", p, w.Code)
		}
	}
	// global 跳过路径：translate 与 auth/captcha
	for _, p := range []string{"/api/translate/abc", "/api/auth/captcha"} {
		if w := do(r, http.MethodGet, p, "", ""); w.Code != 200 {
			t.Fatalf("GET %s => %d, want 200", p, w.Code)
		}
	}
	// 非 /api 前缀不计入
	do(r, http.MethodGet, "/uploads/x.png", "", "")

	counts := store.keyCounts()
	wantKey := "global:" + testRemoteAddr
	if counts[wantKey] != 3 {
		t.Fatalf("global key %q 计数 = %d, want 3 (keys=%v)", wantKey, counts[wantKey], counts)
	}
	if store.total() != 3 {
		t.Fatalf("全局共 %d 次计数, want 3", store.total())
	}
}

func TestRateLimitPerEndpointMatching(t *testing.T) {
	store := &stubStore{}
	spec := ratelimit.AuthSpec
	spec.Mounts = []string{"/auth/login"}
	r := newTestRouter(RateLimit(store, spec, RateLimitOpts{}))

	// 命中：精确 + 子路径
	do(r, http.MethodPost, "/api/auth/login", `{}`, "application/json")
	do(r, http.MethodPost, "/api/auth/login/2fa", `{}`, "application/json")
	// 不命中：/api/auth/login2、/api/v1/auth/login、/api/health
	do(r, http.MethodPost, "/api/auth/login2", `{}`, "application/json")
	do(r, http.MethodPost, "/api/v1/auth/login", `{}`, "application/json")
	do(r, http.MethodGet, "/api/health", "", "")

	counts := store.keyCounts()
	wantKey := "auth:" + testRemoteAddr
	if counts[wantKey] != 2 {
		t.Fatalf("auth key %q 计数 = %d, want 2 (keys=%v)", wantKey, counts[wantKey], counts)
	}
}

func TestRateLimit429(t *testing.T) {
	store := &stubStore{}
	spec := ratelimit.AuthSpec // 5/15min
	spec.Mounts = []string{"/auth/login"}
	r := newTestRouter(RateLimit(store, spec, RateLimitOpts{}))

	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		last = do(r, http.MethodPost, "/api/auth/login", `{}`, "application/json")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次状态码 = %d, want 429", last.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(last.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 body 解析失败: %v", err)
	}
	if body["message"] != "登录尝试过多，请15分钟后再试" {
		t.Fatalf("429 message = %v", body["message"])
	}
	if h := last.Header().Get("Retry-After"); h == "" {
		t.Fatal("429 应带 Retry-After 头")
	}
	if h := last.Header().Get("RateLimit-Policy"); h != "5;w=900" {
		t.Fatalf("RateLimit-Policy = %q, want 5;w=900", h)
	}
	if h := last.Header().Get("RateLimit-Limit"); h != "5" {
		t.Fatalf("RateLimit-Limit = %q, want 5", h)
	}
	if h := last.Header().Get("RateLimit-Remaining"); h != "0" {
		t.Fatalf("RateLimit-Remaining = %q, want 0", h)
	}

	// 前 5 次仍应放行且带 remaining 头
	store2 := &stubStore{}
	r2 := newTestRouter(RateLimit(store2, spec, RateLimitOpts{}))
	w := do(r2, http.MethodPost, "/api/auth/login", `{}`, "application/json")
	if w.Code != 200 {
		t.Fatalf("首次请求状态码 = %d, want 200", w.Code)
	}
	if h := w.Header().Get("RateLimit-Remaining"); h != "4" {
		t.Fatalf("首次 RateLimit-Remaining = %q, want 4", h)
	}
}

func TestRateLimitAuthSkip(t *testing.T) {
	spec := ratelimit.AuthSpec
	spec.Mounts = []string{"/auth/login"}

	// IsDev + SkipRateLimit → 完全不计数
	store := &stubStore{}
	r := newTestRouter(RateLimit(store, spec, RateLimitOpts{IsDev: true, SkipRateLimit: true}))
	for i := 0; i < 10; i++ {
		if w := do(r, http.MethodPost, "/api/auth/login", `{}`, "application/json"); w.Code != 200 {
			t.Fatalf("跳过时第 %d 次请求 = %d, want 200", i, w.Code)
		}
	}
	if store.total() != 0 {
		t.Fatalf("跳过限流时应不计数，实际 %d 次", store.total())
	}

	// 仅 IsDev 或仅 SkipRateLimit → 照常限流
	store = &stubStore{}
	r = newTestRouter(RateLimit(store, spec, RateLimitOpts{IsDev: true, SkipRateLimit: false}))
	for i := 0; i < 3; i++ {
		do(r, http.MethodPost, "/api/auth/login", `{}`, "application/json")
	}
	if store.total() != 3 {
		t.Fatalf("非跳过时 auth 计数 = %d, want 3", store.total())
	}
}

func TestRateLimitRequestEmailChangeKey(t *testing.T) {
	store := &stubStore{}
	spec := ratelimit.RequestEmailChangeSpec
	spec.Mounts = []string{"/auth/request-email-change"}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyParse())
	r.Use(RateLimit(store, spec, RateLimitOpts{}))
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	r.POST("/api/auth/request-email-change", ok)

	w := do(r, http.MethodPost, "/api/auth/request-email-change", `{"newEmail":"Foo@Example.com"}`, "application/json")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	counts := store.keyCounts()
	wantKey := "requestEmailChange:[object Object]:foo@example.com"
	if counts[wantKey] != 1 {
		t.Fatalf("key %q 计数 = %d, want 1 (counts=%v)", wantKey, counts[wantKey], counts)
	}

	// 无 newEmail → "unknown"
	w = do(r, http.MethodPost, "/api/auth/request-email-change", `{}`, "application/json")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	counts = store.keyCounts()
	if counts["requestEmailChange:[object Object]:unknown"] != 1 {
		t.Fatalf("无 newEmail 时应计入 unknown 键, counts=%v", counts)
	}
}

func TestRateLimitClientIPTrustXFF(t *testing.T) {
	// 生产（trustXFF）取 X-Forwarded-For 首值
	store := &stubStore{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimit(store, ratelimit.GlobalSpec, RateLimitOpts{TrustXFF: true}))
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	r.Any("/api/*rest", ok)

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	counts := store.keyCounts()
	if counts["global:203.0.113.9"] != 1 {
		t.Fatalf("trustXFF 应取首值 203.0.113.9, counts=%v", counts)
	}
}

func TestRateLimitGlobalBareAPI(t *testing.T) {
	// 裸 /api 计入全局（对齐 Express app.use('/api/', ...) 匹配 /api）。
	// 注意：gin 默认对 /api 做尾斜杠 301 重定向，中间件不会执行；因此这里
	// 显式注册 /api 路由，仅验证中间件的路径匹配逻辑。
	store := &stubStore{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimit(store, ratelimit.GlobalSpec, RateLimitOpts{}))
	r.GET("/api", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	do(r, http.MethodGet, "/api", "", "")
	if store.total() != 1 {
		t.Fatalf("裸 /api 应计数, total=%d", store.total())
	}
}
