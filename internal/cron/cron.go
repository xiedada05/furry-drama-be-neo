// Package cron 实现后台定时任务（对齐 backend/src/cron.js 与 src/index.js 的 cron.schedule）。
//
// 每个任务一个 goroutine，先用 time.Timer 对齐到首个执行点（整点 / 每天 03:00），
// 之后由 time.Ticker 驱动周期性执行；传入的 ctx 取消时 goroutine 退出。
package cron

import (
	"context"
	"log/slog"
	"time"

	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

const (
	// sessionTTL 会话 30 天无活跃即清理（src/index.js 的 30*24h）。
	sessionTTL = 30 * 24 * time.Hour
	// readNotificationTTL 已读通知 30 天后清理（src/index.js cron '0 3 * * *'）。
	readNotificationTTL = 30 * 24 * time.Hour
)

// StartSessions 启动认证/用户域相关的后台定时任务（本阶段只含会话与已读通知清理）。
//
// 调度计划（对齐 Express）：
//   - 会话清理：每小时整点（cron '0 * * * *'）把 lastActiveAt 超过 30 天的 active
//     会话批量置为 inactive + logoutAt（SessionRepo.MarkInactiveOlderThan）。
//   - 已读通知清理：每天 03:00（cron '0 3 * * *'）删除 isRead=true 且 createdAt
//     超过 30 天的通知（NotificationRepo.DeleteReadOlderThan）。
//
// TODO（依赖 Episode 等后续段仓储，M2 之后再启用，见 AGENTS.md）：
//   - 账号删除：deletionRequestedAt 超过 7 天宽限期的用户物理删除并清理关联数据
//     （cron.js checkExpiredAccountDeletion，Express 为每 6 小时执行）。
//   - 自动完结：连载剧 currentEpisodes >= totalEpisodes 时置 status='completed' 并向
//     追番用户发送站内通知 / Web Push / 邮件（cron.js checkAutoComplete，每小时执行）。
//
// 调用方应在服务优雅关停时 cancel ctx；本函数立即返回，不阻塞。
func StartSessions(ctx context.Context, repos *repository.Repos) {
	now := time.Now()
	go runAligned(ctx, nextHourlyDelay(now), time.Hour, func() {
		cleanupExpiredSessions(ctx, repos)
	})
	go runAligned(ctx, nextDailyDelay(now, 3), 24*time.Hour, func() {
		cleanupReadNotifications(ctx, repos)
	})
}

// runAligned 等待 first 后执行一次 job，随后每 interval 执行一次；ctx 取消即退出。
func runAligned(ctx context.Context, first, interval time.Duration, job func()) {
	timer := time.NewTimer(first)
	select {
	case <-ctx.Done():
		timer.Stop()
		return
	case <-timer.C:
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		job()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// nextHourlyDelay 返回从 now 到下一个整点的时长（对齐 cron '0 * * * *'）。
func nextHourlyDelay(now time.Time) time.Duration {
	next := now.Truncate(time.Hour).Add(time.Hour)
	return next.Sub(now)
}

// nextDailyDelay 返回从 now 到下一个 hour 点整的时长（对齐 cron '0 3 * * *'）。
func nextDailyDelay(now time.Time, hour int) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

// cleanupExpiredSessions 会话清理任务体。
func cleanupExpiredSessions(ctx context.Context, repos *repository.Repos) {
	cutoff := time.Now().Add(-sessionTTL)
	n, err := repos.Sessions.MarkInactiveOlderThan(ctx, cutoff)
	if err != nil {
		slog.Error("[Cron] Session cleanup error", "err", err)
		return
	}
	if n > 0 {
		slog.Info("[Cron] Cleaned expired sessions", "count", n)
	}
}

// cleanupReadNotifications 已读通知清理任务体。
func cleanupReadNotifications(ctx context.Context, repos *repository.Repos) {
	cutoff := time.Now().Add(-readNotificationTTL)
	n, err := repos.Notifications.DeleteReadOlderThan(ctx, cutoff)
	if err != nil {
		slog.Error("[Cron] Notification cleanup error", "err", err)
		return
	}
	if n > 0 {
		slog.Info("[Cron] Cleaned old read notifications", "count", n)
	}
}
