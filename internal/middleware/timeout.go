package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

// requestTimeout 对齐 src/index.js:227-231 的 req/res 30s 超时。
const requestTimeout = 30 * time.Second

// Timeout 给每个请求注入 30s 的 context deadline（等价 Express 的
// req.setTimeout(30000)/res.setTimeout(30000)，src/index.js:228-229）。
//
// handler/service 应消费 c.Request.Context()，超时后数据库等阻塞操作会返回
// context deadline exceeded，从而中止慢请求。注意：与 Express 直接销毁 socket
// 不同，本实现不主动掐断已开始的写响应，仅在 handler 协作下生效（行为等价于
// 设置 deadline，差分测试以 30s 上限为准）。
func Timeout() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), requestTimeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
