package middleware

import (
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

// Gzip 响应压缩中间件（等价 Express compression()，src/index.js:68）。
//
// 使用 gin-contrib/gzip 默认压缩级别（等价 zlib default = compression() 默认）：
//   - 客户端 Accept-Encoding 含 gzip 时压缩，设置 Content-Encoding: gzip 与
//     Vary: Accept-Encoding；
//   - 默认排除已压缩的图片扩展名（.png/.gif/.jpeg/.jpg），与 compression() 的
//     compressible 过滤语义一致；
//   - 4xx/5xx 响应不压缩（与 compression() 只在 200 附近压缩的行为近似）。
//
// 注意：必须尽早注册（包裹 ResponseWriter），否则先前写入的头/体无法被压缩。
func Gzip() gin.HandlerFunc {
	return gzip.Gzip(gzip.DefaultCompression)
}
