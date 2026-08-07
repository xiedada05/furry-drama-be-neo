package middleware

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// fakeUsageRepo 记录 UpsertInc 调用，用于验证缓冲聚合。
type fakeUsageRepo struct {
	mu    sync.Mutex
	calls []usageCall
}

type usageCall struct {
	endpoint, method, date string
	count                  int64
}

func (f *fakeUsageRepo) UpsertInc(_ context.Context, endpoint, method, date string, count int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, usageCall{endpoint, method, date, count})
	return nil
}

func (f *fakeUsageRepo) snapshot() []usageCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]usageCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func fixedNow() time.Time {
	return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
}

func TestAPITrackerFlushAggregates(t *testing.T) {
	repo := &fakeUsageRepo{}
	tr := newUsageTracker(repo)
	tr.now = fixedNow

	// 3 条同 endpoint 请求 → flush 聚合为 1 条 upsert，count=3
	for i := 0; i < 3; i++ {
		tr.add("GET /api/series")
	}
	tr.flush()

	calls := repo.snapshot()
	if len(calls) != 1 {
		t.Fatalf("upsert 调用数 = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.endpoint != "GET /api/series" {
		t.Fatalf("endpoint = %q, want %q", c.endpoint, "GET /api/series")
	}
	if c.count != 3 {
		t.Fatalf("count = %d, want 3", c.count)
	}
	if c.date != "2026-08-08" {
		t.Fatalf("date = %q, want 2026-08-08", c.date)
	}
	if c.method != "" {
		t.Fatalf("method = %q, want ''（对齐 Express 不写 method 字段）", c.method)
	}
}

func TestAPITrackerFlushGroupsByEndpoint(t *testing.T) {
	repo := &fakeUsageRepo{}
	tr := newUsageTracker(repo)
	tr.now = fixedNow

	tr.add("GET /api/series")
	tr.add("GET /api/series/:id")
	tr.add("POST /api/series")
	tr.flush()

	calls := repo.snapshot()
	if len(calls) != 3 {
		t.Fatalf("upsert 调用数 = %d, want 3", len(calls))
	}
}

func TestAPITrackerEmptyFlushNoop(t *testing.T) {
	repo := &fakeUsageRepo{}
	tr := newUsageTracker(repo)
	tr.flush()
	if n := len(repo.snapshot()); n != 0 {
		t.Fatalf("空缓冲 flush 应无调用，实际 %d", n)
	}
}

func TestAPITrackerAutoFlushAt50(t *testing.T) {
	repo := &fakeUsageRepo{}
	tr := newUsageTracker(repo)
	tr.now = fixedNow

	for i := 0; i < maxBufferSize; i++ {
		tr.add("GET /api/series")
	}
	// add 满 50 条后异步 flush
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n := len(repo.snapshot()); n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := repo.snapshot()
	if len(calls) != 1 || calls[0].count != maxBufferSize {
		t.Fatalf("满 %d 条应聚合为一条 count=%d 的 upsert，实际 %v", maxBufferSize, maxBufferSize, calls)
	}
}

func TestAPITrackerHandleOnlyAPI(t *testing.T) {
	repo := &fakeUsageRepo{}
	tr := newUsageTracker(repo)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(tr.handle)
	ok := func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) }
	r.GET("/api/health", ok)
	r.GET("/uploads/x.png", ok)

	do(r, http.MethodGet, "/uploads/x.png", "", "")
	do(r, http.MethodGet, "/api/health", "", "")

	tr.flush()
	calls := repo.snapshot()
	if len(calls) != 1 {
		t.Fatalf("只应统计 /api/ 路径，实际 %v", calls)
	}
	if calls[0].endpoint != "GET /api/health" {
		t.Fatalf("endpoint = %q", calls[0].endpoint)
	}
}

func TestAPITrackerHandleFallbackToPath(t *testing.T) {
	repo := &fakeUsageRepo{}
	tr := newUsageTracker(repo)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(tr.handle)
	r.GET("/api/health", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	// /api/nonexistent 未命中路由 → c.FullPath() 为空，回退原始路径
	do(r, http.MethodGet, "/api/nonexistent", "", "")
	tr.flush()
	calls := repo.snapshot()
	if len(calls) != 1 || calls[0].endpoint != "GET /api/nonexistent" {
		t.Fatalf("未命中路由应回退原始路径，实际 %v", calls)
	}
}
