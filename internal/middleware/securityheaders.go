package middleware

import (
	"github.com/gin-gonic/gin"
)

// helmet 等价安全头（对齐 src/index.js:179-196 的 helmet 配置，helmet v8）。
//
// 配置项 crossOriginEmbedderPolicy / crossOriginOpenerPolicy /
// crossOriginResourcePolicy 均为 false（不设置对应头）；其余默认项全部设置。
//
// Strict-Transport-Security：helmet v8 无条件设置 max-age=31536000;
// includeSubDomains（dev 下 Express oracle 同样输出该头），故本实现不区分
// 生产/开发。差分测试以 oracle 输出为准。
const (
	cspHeader = "default-src 'self';script-src 'self' 'unsafe-inline' blob:;style-src 'self' 'unsafe-inline';img-src 'self' data: blob:;font-src 'self';connect-src 'self' https://api.mymemory.translated.net https://translate.googleapis.com https://api.cognitive.microsofttranslator.com https://ipapi.co;frame-src 'self' https://player.bilibili.com https://www.youtube.com https://embed.nicovideo.jp;object-src 'none';upgrade-insecure-requests;base-uri 'self';form-action 'self';frame-ancestors 'self';script-src-attr 'none'"
)

// SecurityHeaders 设置 helmet 等价安全响应头。必须尽早注册（在会写头的
// 中间件之前），对齐 Express 中 helmet 挂在 cors/body 解析之前的位置。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", cspHeader)
		c.Header("Origin-Agent-Cluster", "?1")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-DNS-Prefetch-Control", "off")
		c.Header("X-Download-Options", "noopen")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("X-Permitted-Cross-Domain-Policies", "none")
		c.Header("X-XSS-Protection", "0")
		c.Next()
	}
}
