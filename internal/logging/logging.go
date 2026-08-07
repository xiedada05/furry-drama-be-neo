// Package logging 提供基于 slog 的结构化日志，等价于 Express 后端用 console 输出的两级日志。
//
// 级别策略：开发环境 Debug 级（含慢请求/错误堆栈），生产环境 Info 级。
package logging

import (
	"log/slog"
	"os"
)

// New 创建日志器。isDev=true 时输出 Debug 级，否则 Info 级。
func New(isDev bool) *slog.Logger {
	level := slog.LevelInfo
	if isDev {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
