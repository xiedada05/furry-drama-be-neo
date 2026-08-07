// Package handler 是 HTTP 层（薄），负责请求解析、校验、调用 service 并组装响应。
package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
)

// CSRF 处理器：GET /api/csrf-token。签发 XSRF-TOKEN cookie（非 httpOnly）并返回 {csrfToken}。
// 对齐 src/index.js:255-265。注册在 CSRF 校验中间件与限流器之前。
func CSRF(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := auth.SetCSRFCookie(c, !cfg.IsDev, cfg.Security.CSRFMaxAgeHours)
		c.JSON(200, gin.H{"csrfToken": token})
	}
}

// Health 处理器：GET /api/health。Ping MongoDB，成功 200 失败 503。
// 对齐 src/index.js:335-350，timestamp 为 ISO8601 UTC 毫秒。
func Health(db *mongo.Database) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		if err := db.Client().Ping(ctx, nil); err != nil {
			c.JSON(503, gin.H{"status": "error", "timestamp": ts, "db": "disconnected"})
			return
		}
		c.JSON(200, gin.H{"status": "ok", "timestamp": ts, "db": "connected"})
	}
}
