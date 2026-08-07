// Package middleware 提供 Gin 全局与路径级中间件，装配顺序见 internal/server/app.go。
package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
)

// CSRF 校验中间件：对非 GET 请求校验 XSRF-TOKEN cookie 与 X-XSRF-TOKEN header
// 双拷贝相等（double-submit）。对齐 src/index.js:267-283 的三态 403。
// 注意：GET /api/csrf-token 端点注册在校验中间件之前，不受本中间件拦截。
func CSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == "GET" {
			c.Next()
			return
		}
		cookieTok := auth.GetCSRFToken(c)
		headerTok := auth.GetHeaderCSRFToken(c)
		switch {
		case cookieTok != "" && headerTok != "" && cookieTok != headerTok:
			c.AbortWithStatusJSON(403, gin.H{"message": "CSRF token mismatch"})
		case cookieTok != "" && headerTok == "":
			c.AbortWithStatusJSON(403, gin.H{"message": "CSRF protection: missing X-XSRF-TOKEN header"})
		case cookieTok == "":
			c.AbortWithStatusJSON(403, gin.H{"message": "CSRF protection: missing XSRF-TOKEN cookie, please refresh the page"})
		default:
			// cookie 与 header 均存在且相等：放行。
			c.Next()
		}
	}
}
