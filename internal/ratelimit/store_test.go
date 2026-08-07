package ratelimit

import (
	"testing"
	"time"
)

// fakeClock 是 SlidingWindow 的可注入时钟（测试用假时钟）。
type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time          { return f.now }
func (f *fakeClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

func newFakeStore(t *testing.T) (*SlidingWindow, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	sw := NewSlidingWindow()
	sw.now = clock.Now
	return sw, clock
}

func TestSlidingWindowCountsWithinWindow(t *testing.T) {
	sw, _ := newFakeStore(t)
	window := time.Minute

	for i := 1; i <= 3; i++ {
		count, retryAfter, err := sw.Inc("k", window, 5)
		if err != nil {
			t.Fatalf("Inc err: %v", err)
		}
		if count != i {
			t.Fatalf("第 %d 次 Inc: count=%d, want %d", i, count, i)
		}
		if retryAfter != 0 {
			t.Fatalf("未超限时 retryAfter 应为 0，得到 %v", retryAfter)
		}
	}
}

func TestSlidingWindowExceedsMax(t *testing.T) {
	sw, _ := newFakeStore(t)
	window := 15 * time.Minute

	for i := 1; i <= 5; i++ {
		_, _, _ = sw.Inc("login", window, 5)
	}
	count, retryAfter, err := sw.Inc("login", window, 5)
	if err != nil {
		t.Fatalf("Inc err: %v", err)
	}
	if count != 6 {
		t.Fatalf("第 6 次 count=%d, want 6", count)
	}
	if retryAfter <= 0 || retryAfter > window {
		t.Fatalf("retryAfter=%v 应在 (0, %v] 区间", retryAfter, window)
	}
}

func TestSlidingWindowExpiry(t *testing.T) {
	sw, clock := newFakeStore(t)
	window := time.Minute

	// t=0 连打 4 次（max=5 未超限）
	for i := 0; i < 4; i++ {
		_, _, _ = sw.Inc("k", window, 5)
	}
	count, _, _ := sw.Inc("k", window, 5)
	if count != 5 {
		t.Fatalf("count=%d, want 5", count)
	}

	// 窗口完全滑过后全部过期
	clock.Advance(window + time.Millisecond)
	count, retryAfter, _ := sw.Inc("k", window, 5)
	if count != 1 {
		t.Fatalf("全部过期后 count=%d, want 1", count)
	}
	if retryAfter != 0 {
		t.Fatalf("未超限时 retryAfter 应为 0，得到 %v", retryAfter)
	}
}

// TestSlidingWindowSlides 验证滑动窗口随时间的部分过期行为：
// 每分钟打一次，t=180 时 t=0、t=60 已滑出，t=120 恰在窗口边界内。
func TestSlidingWindowSlides(t *testing.T) {
	sw, clock := newFakeStore(t)
	window := time.Minute

	// 每分钟打一次（t=0,60,120）
	for i := 0; i < 3; i++ {
		_, _, _ = sw.Inc("k", window, 5)
		clock.Advance(window)
	}
	// 此刻 t=180：窗口 [120,180]，t=120 在边界内
	count, _, _ := sw.Inc("k", window, 5)
	if count != 2 { // t=120 + 本次
		t.Fatalf("t=180 时 count=%d, want 2", count)
	}

	// 再多推 1ns：t=120 严格滑出，仅剩本次
	clock.Advance(time.Nanosecond)
	count, _, _ = sw.Inc("k", window, 5)
	if count != 2 { // t=180（上一步那次）+ 本次
		t.Fatalf("t=180+1ns 时 count=%d, want 2", count)
	}

	// 再推一个窗口：t=180 滑出
	clock.Advance(window)
	count, _, _ = sw.Inc("k", window, 5)
	if count != 2 { // t=180+1ns（边界）保留 + 本次
		t.Fatalf("再推一个窗口 count=%d, want 2", count)
	}
}

func TestSlidingWindowRetryAfterMatchesOldestExpiry(t *testing.T) {
	sw, clock := newFakeStore(t)
	window := time.Minute

	// t=0,10,20,30,40 各打一次，然后推进到 t=50s
	for i := 0; i < 5; i++ {
		_, _, _ = sw.Inc("k", window, 5)
		clock.Advance(10 * time.Second)
	}
	// 第 6 次：窗口 [-10s,50s] 内 5 次全在 → count=6 超限
	count, retryAfter, _ := sw.Inc("k", window, 5)
	if count != 6 {
		t.Fatalf("count=%d, want 6", count)
	}
	// 最早存活请求 t=0，过期于 t=60，现在 t=50 → 需等 10s
	if got := retryAfter; got != 10*time.Second {
		t.Fatalf("retryAfter=%v, want 10s", got)
	}
	_ = count
}

func TestSlidingWindowKeysIndependent(t *testing.T) {
	sw, _ := newFakeStore(t)
	window := time.Minute

	for i := 0; i < 5; i++ {
		_, _, _ = sw.Inc("a", window, 5)
	}
	count, _, err := sw.Inc("b", window, 5)
	if err != nil {
		t.Fatalf("Inc err: %v", err)
	}
	if count != 1 {
		t.Fatalf("独立 key b count=%d, want 1", count)
	}
}
