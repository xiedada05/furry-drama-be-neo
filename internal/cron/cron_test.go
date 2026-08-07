package cron

import (
	"testing"
	"time"
)

// TestNextHourlyDelay 验证对齐到下一个整点（cron '0 * * * *'）。
func TestNextHourlyDelay(t *testing.T) {
	now := time.Date(2026, 8, 8, 10, 30, 0, 0, time.UTC)
	if d := nextHourlyDelay(now); d != 30*time.Minute {
		t.Errorf("nextHourlyDelay(10:30) = %v, want 30m", d)
	}
	nowExact := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	if d := nextHourlyDelay(nowExact); d != time.Hour {
		t.Errorf("nextHourlyDelay(10:00) = %v, want 1h", d)
	}
}

// TestNextDailyDelay 验证对齐到每天 hour 点整（cron '0 3 * * *'）。
func TestNextDailyDelay(t *testing.T) {
	loc := time.UTC
	// 10:00 时当天的 03:00 已过 → 次日 03:00（17h）
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, loc)
	if d := nextDailyDelay(now, 3); d != 17*time.Hour {
		t.Errorf("nextDailyDelay(10:00, 3) = %v, want 17h", d)
	}
	// 01:00 → 当天 03:00（2h）
	now2 := time.Date(2026, 8, 8, 1, 0, 0, 0, loc)
	if d := nextDailyDelay(now2, 3); d != 2*time.Hour {
		t.Errorf("nextDailyDelay(01:00, 3) = %v, want 2h", d)
	}
	// 正好 03:00 → 次日 03:00（24h）
	now3 := time.Date(2026, 8, 8, 3, 0, 0, 0, loc)
	if d := nextDailyDelay(now3, 3); d != 24*time.Hour {
		t.Errorf("nextDailyDelay(03:00, 3) = %v, want 24h", d)
	}
}
