package middleware

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 接口调用统计缓冲参数（对齐 middlewares/apiTracker.js）。
const (
	maxBufferSize = 50               // 满 50 条请求即 flush
	flushInterval = 60 * time.Second // 兜底每 60s flush
	dateLayout    = "2006-01-02"     // UTC 日期（对齐 toISOString().split('T')[0]）
	usageItemSep  = "|"
	usageFlushCtx = 5 * time.Second
)

// usageCount 是按 endpoint+date 聚合后的缓冲条目。
type usageCount struct {
	endpoint string
	date     string
	count    int
}

// usageRepo 是 flush 需要的仓储最小接口（*repository.ApiUsageRepo 实现之）。
// 便于测试注入伪造仓储，避免依赖 mongod。
type usageRepo interface {
	UpsertInc(ctx context.Context, endpoint, method, date string, count int64) error
}

// apiUsageTracker 持有接口调用统计缓冲。所有方法并发安全。
type apiUsageTracker struct {
	repo    usageRepo
	mu      sync.Mutex
	counts  map[string]*usageCount // key = endpoint|date
	pending int                    // 已缓冲的请求条数（满 maxBufferSize 触发 flush）
	now     func() time.Time
}

// newUsageTracker 构造统计缓冲器（测试可直接使用，不启动后台 flush 循环）。
func newUsageTracker(repo usageRepo) *apiUsageTracker {
	return &apiUsageTracker{
		repo:   repo,
		counts: make(map[string]*usageCount),
		now:    time.Now,
	}
}

// APITracker 构造接口调用统计中间件：响应完成后把 {method, path} 计入缓冲，
// 满 50 条或每 60s 批量 upsert 到 ApiUsage（endpoint+date 聚合，$inc）。
// 对齐 middlewares/apiTracker.js + src/index.js:285。失败静默（不阻断业务）。
func APITracker(repo *repository.ApiUsageRepo) gin.HandlerFunc {
	t := newUsageTracker(repo)
	go t.flushLoop()
	return t.handle
}

// flushLoop 每 flushInterval 兜底 flush 一次。
func (t *apiUsageTracker) flushLoop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for range ticker.C {
		t.flush()
	}
}

// handle 是 Gin 中间件：只在路径以 /api/ 开头时计数（对齐 req.path.startsWith('/api/')）。
// endpoint 为 "<METHOD> <path>"（对齐 `${req.method} ${req.route.path}`）。
func (t *apiUsageTracker) handle(c *gin.Context) {
	c.Next()
	path := c.Request.URL.Path
	if !strings.HasPrefix(path, "/api/") {
		return
	}
	routePath := c.FullPath()
	if routePath == "" {
		routePath = path // 未命中路由（404）时退回原始路径
	}
	t.add(c.Request.Method + " " + routePath)
}

// add 把一条请求计入缓冲；达到 maxBufferSize 时异步 flush。
// endpoint 已含 "<METHOD> <path>"（对齐 Express 存储格式）。
func (t *apiUsageTracker) add(endpoint string) {
	t.mu.Lock()
	date := t.now().UTC().Format(dateLayout)
	key := endpoint + usageItemSep + date
	item := t.counts[key]
	if item == nil {
		item = &usageCount{endpoint: endpoint, date: date}
		t.counts[key] = item
	}
	item.count++
	t.pending++
	full := t.pending >= maxBufferSize
	t.mu.Unlock()

	if full {
		go t.flush()
	}
}

// flush 取走全部缓冲并批量 upsert（endpoint+date 聚合，$inc）。
// 单个失败静默忽略（对齐 flushBuffer 的 .catch(()=>{})）。
func (t *apiUsageTracker) flush() {
	t.mu.Lock()
	if len(t.counts) == 0 {
		t.mu.Unlock()
		return
	}
	items := make([]*usageCount, 0, len(t.counts))
	for _, it := range t.counts {
		items = append(items, it)
	}
	t.counts = make(map[string]*usageCount)
	t.pending = 0
	t.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), usageFlushCtx)
	defer cancel()
	for _, it := range items {
		// method 传空串：对齐 Express（flushBuffer 的 upsert 从不写 method 字段，
		// 由 schema 默认值 '' 填充）。
		_ = t.repo.UpsertInc(ctx, it.endpoint, "", it.date, int64(it.count))
	}
}
