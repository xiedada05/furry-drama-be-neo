package middleware

import (
	"log/slog"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ContextUserID 是认证中间件写入当前用户 _id（hex 字符串）的 gin.Context 键。
// requestLogger 读取它填充 userId 字段（对齐 Express 的 req.user?._id）。
// M2 的 auth 中间件应在鉴权成功后 c.Set(middleware.ContextUserID, <hex _id>)。
const ContextUserID = "userId"

// 日志阈值（对齐 src/index.js:238 与 middlewares/requestLogger.js:18）。
const (
	devSlowThreshold  = 1000 * time.Millisecond
	prodSlowThreshold = 3000 * time.Millisecond
)

// userIDFromContext 读取上下文中当前用户 ID（string 或 fmt.Stringer）。
func userIDFromContext(c *gin.Context) string {
	v, ok := c.Get(ContextUserID)
	if !ok {
		return ""
	}
	switch id := v.(type) {
	case string:
		return id
	case interface{ String() string }:
		return id.String()
	}
	return ""
}

// SlowLogger 慢请求 / 错误请求日志（对齐 src/index.js:233-246）：
//   - dev：status>=400 或 duration>1000ms，或路径含 /list、/histories、/follows
//     → INFO 记录；duration>1000ms 时附加 level=SLOW 标记；
//   - prod：仅 duration>3000ms → WARN 记录（"[Slow]" 等价）。
func SlowLogger(log *slog.Logger, isDev bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start)
		status := c.Writer.Status()
		path := c.Request.URL.Path

		if isDev {
			if status >= 400 || duration > devSlowThreshold ||
				strings.Contains(path, "/list") || strings.Contains(path, "/histories") || strings.Contains(path, "/follows") {
				attrs := []any{
					"method", c.Request.Method,
					"path", path,
					"status", status,
					"duration_ms", duration.Milliseconds(),
				}
				if duration > devSlowThreshold {
					attrs = append(attrs, "level", "SLOW")
				}
				log.Info("request", attrs...)
			}
		} else if duration > prodSlowThreshold {
			log.Warn("slow request",
				"method", c.Request.Method,
				"path", path,
				"status", status,
				"duration_ms", duration.Milliseconds(),
			)
		}
	}
}

// RequestLogger 通用请求日志（对齐 middlewares/requestLogger.js）：仅当
// duration>1000ms 或 status>=400 或方法非 GET 时输出。级别：5xx=ERROR、
// 4xx=WARN、duration>1000ms=WARN、其余=INFO。trustXFF 生产传 true 以正确
// 取 req.ip（对齐 app.set('trust proxy', 1)）。
func RequestLogger(log *slog.Logger, trustXFF bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		duration := time.Since(start)
		status := c.Writer.Status()
		method := c.Request.Method
		path := c.Request.URL.Path

		if duration <= devSlowThreshold && status < 400 && method == "GET" {
			return
		}

		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		case duration > devSlowThreshold:
			level = slog.LevelWarn
		}

		log.Log(c.Request.Context(), level, "request",
			"method", method,
			"path", path,
			"status", status,
			"duration_ms", duration.Milliseconds(),
			"ip", clientIP(c, trustXFF),
			"userId", userIDFromContext(c),
		)
	}
}
