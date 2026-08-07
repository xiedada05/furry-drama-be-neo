package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS 白名单跨域中间件，对齐 Express cors 包行为（src/index.js:198-225）：
//   - 无 Origin（服务器间请求/curl）→ 放行，不设 ACAO 头
//   - Origin 在白名单 → 设 ACAO/ACAC；预检（OPTIONS + Access-Control-Request-Method）204 短路
//   - Origin 不在白名单 → 403 {"message":"CORS policy denied"}（走全局错误语义）
//
// 白名单比对前统一剥离尾部斜杠。
func CORS(allowOrigins []string) gin.HandlerFunc {
	normalized := make(map[string]bool, len(allowOrigins))
	for _, o := range allowOrigins {
		normalized[strings.TrimRight(o, "/")] = true
	}
	return func(c *gin.Context) {
		origin := strings.TrimRight(c.GetHeader("Origin"), "/")
		if origin == "" {
			c.Next()
			return
		}
		if !normalized[origin] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"message": "CORS policy denied"})
			return
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Credentials", "true")
		if c.Request.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != "" {
			c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,PATCH,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization,X-Requested-With,X-XSRF-TOKEN")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
