package email

import (
	"sync"
	"time"
)

// TargetRate 目标邮箱限流：每收件邮箱在窗口内最多 max 封（对齐 email.js L5-38）。
// 默认 10 封 / 1 小时，每 10 分钟清理过期记录。
type TargetRate struct {
	mu     sync.Mutex
	tracks map[string][]time.Time
	max    int
	window time.Duration
}

// NewTargetRate 构造目标限流器。
func NewTargetRate(max int, window time.Duration) *TargetRate {
	if max <= 0 {
		max = 10
	}
	if window <= 0 {
		window = time.Hour
	}
	return &TargetRate{tracks: make(map[string][]time.Time), max: max, window: window}
}

// Allow 判断是否允许发送：窗口内未超限则记录并返回 true，超限返回 false。
func (t *TargetRate) Allow(key string) bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.tracks[key]
	valid := list[:0]
	for _, ts := range list {
		if now.Sub(ts) < t.window {
			valid = append(valid, ts)
		}
	}
	if len(valid) >= t.max {
		t.tracks[key] = valid
		return false
	}
	t.tracks[key] = append(valid, now)
	return true
}

// Cleanup 清理过期记录（每 10 分钟调用；空 track 删除）。
func (t *TargetRate) Cleanup() {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, list := range t.tracks {
		valid := list[:0]
		for _, ts := range list {
			if now.Sub(ts) < t.window {
				valid = append(valid, ts)
			}
		}
		if len(valid) == 0 {
			delete(t.tracks, key)
		} else {
			t.tracks[key] = valid
		}
	}
}
