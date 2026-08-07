// Package ratelimit 提供限流基础原语：Store 接口 + 内存滑动窗口实现，
// 以及命名限流器定义（参数对齐 backend/config/rateLimits.js）。
//
// 行为对齐 express-rate-limit v8 的 MemoryStore 语义（windowMs/max/message，
// standardHeaders: true → draft-6 RateLimit-* 头），但实现采用滑动窗口：
// Inc 对 key 在 [now-window, now] 内的请求计数 +1，返回窗口内计数 count，
// 以及 count>max 时下一次放行前需等待的 retryAfter。
package ratelimit

import (
	"sync"
	"time"
)

// Store 是限流计数器存储。实现必须并发安全。
//
// Inc 在指定滑动窗口内对 key 的计数 +1，并返回：
//   - count：窗口 [now-window, now] 内的总请求数（含本次）；
//   - retryAfter：当 count > max 时，距窗口内最早一次请求过期（即可以再次放行）
//     还需等待的时间；未超限时为 0；
//   - err：存储层错误（内存实现通常为 nil）。
type Store interface {
	Inc(key string, window time.Duration, max int) (count int, retryAfter time.Duration, err error)
}

// SlidingWindow 是内存滑动窗口实现：每个 key 维护一条请求时间戳队列，
// 每次 Inc 时淘汰窗口外的旧时间戳，再追加当前时间戳，窗口内个数即计数。
//
// 与 express-rate-limit 的固定双窗口 MemoryStore 不同：滑动窗口的计数
// 随时间平滑下降（最早的请求逐个过期），retryAfter 为最早请求的剩余存活时长。
// 对高频突发场景两者语义接近，差分测试以 429 body / Retry-After 头为准。
type SlidingWindow struct {
	mu   sync.Mutex
	now  func() time.Time // 可注入的时钟（测试用假时钟）
	hits map[string][]time.Time
}

// NewSlidingWindow 构造滑动窗口限流器（使用真实时钟）。
func NewSlidingWindow() *SlidingWindow {
	return &SlidingWindow{now: time.Now, hits: make(map[string][]time.Time)}
}

// Inc 实现 Store：对 key 在 window 内的计数 +1。
func (s *SlidingWindow) Inc(key string, window time.Duration, max int) (count int, retryAfter time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	list := s.hits[key]
	cutoff := now.Add(-window)

	// 淘汰窗口外的时间戳（列表按时间有序，二分快速定位起点）。
	first := lowerBound(list, cutoff)
	list = list[first:]
	list = append(list, now)
	s.hits[key] = list

	count = len(list)
	if count > max {
		// 最早一次请求再过 window-(now-oldest) 即过期，届时腾出一个配额。
		retryAfter = list[0].Add(window).Sub(now)
		if retryAfter < 0 {
			retryAfter = 0
		}
	}
	return count, retryAfter, nil
}

// lowerBound 返回 list 中首个 >= cutoff 的下标（list 按时间升序）。
func lowerBound(list []time.Time, cutoff time.Time) int {
	lo, hi := 0, len(list)
	for lo < hi {
		mid := (lo + hi) / 2
		if list[mid].Before(cutoff) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
