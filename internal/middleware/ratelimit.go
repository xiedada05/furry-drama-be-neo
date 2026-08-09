package middleware

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	ulule "github.com/ulule/limiter/v3"
	memory "github.com/ulule/limiter/v3/drivers/store/memory"

	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
)

// RateLimitOpts 是 RateLimit 的挂载与开关参数。
type RateLimitOpts struct {
	// TrustXFF 生产环境为 true：优先取 X-Forwarded-For 首值作为客户端 IP
	// （对齐 Express app.set('trust proxy', 1)）。开发环境忽略该头。
	TrustXFF bool
	// IsDev 等价于 NODE_ENV !== 'production'。
	IsDev bool
	// SkipRateLimit 对应 SKIP_RATE_LIMIT=1。
	SkipRateLimit bool
}

// specLimiter 封装 ulule/limiter（每个命名限流器一个独立 limiter + 内存 store）。
type specLimiter struct {
	spec ratelimit.Spec
	lim  *ulule.Limiter
}

// RateLimit 构造一个命名限流中间件（对齐 src/index.js:297-317 的挂载语义），
// 计数由 github.com/ulule/limiter 提供：
//
//   - spec.Mounts 为空 → 全局型：匹配 /api 与全部 /api/ 前缀（含 /api/v1/、
//     /api/health），并跳过 /api/translate 与 /api/auth/captcha 路径
//     （对齐 globalLimiter.skip）；
//   - spec.Mounts 非空 → 端点型：只匹配 /api/<Mount> 精确与子路径，
//     【不】匹配 /api/v1/ 前缀（对齐限流挂载不对称铁律）。
//
// auth（AuthSpec）在非生产且 SKIP_RATE_LIMIT=1 时整体跳过（对齐 authLimiter.skip）。
// requestEmailChange 的 key 逐字复刻 Express：[object Object]:<newEmail 小写>
// （Express 的 keyGenerator 把请求对象传给了 ipKeyGenerator）。
//
// 429 响应体 {"message": <spec.Message>}，并设置 express-rate-limit draft-6
// 等价头：RateLimit-Policy / -Limit / -Remaining / -Reset，以及 Retry-After。
func RateLimit(spec ratelimit.Spec, opts RateLimitOpts) gin.HandlerFunc {
	sl := &specLimiter{
		spec: spec,
		lim: ulule.New(memory.NewStore(), ulule.Rate{
			Period: spec.Window,
			Limit:  int64(spec.Max),
		}),
	}
	global := len(spec.Mounts) == 0
	windowSeconds := int64(spec.Window.Seconds())
	skipAuth := opts.IsDev && opts.SkipRateLimit

	type prefixPair struct{ exact, sub string }
	var mounts []prefixPair
	for _, m := range spec.Mounts {
		p := "/api" + m
		mounts = append(mounts, prefixPair{exact: p, sub: p + "/"})
	}

	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if global {
			if !(path == "/api" || strings.HasPrefix(path, "/api/")) {
				c.Next()
				return
			}
			// globalLimiter.skip：跳过翻译与验证码路径。
			if strings.HasPrefix(path, "/api/translate") || strings.HasPrefix(path, "/api/auth/captcha") {
				c.Next()
				return
			}
		} else {
			matched := false
			for _, m := range mounts {
				if path == m.exact || strings.HasPrefix(path, m.sub) {
					matched = true
					break
				}
			}
			if !matched {
				c.Next()
				return
			}
		}

		if skipAuth && spec.Name == ratelimit.AuthName {
			c.Next()
			return
		}

		ip := clientIP(c, opts.TrustXFF)
		key := spec.Name + ":" + ratelimit.IPKey(ip)
		if spec.Name == ratelimit.RequestEmailChangeName {
			key = spec.Name + ":[object Object]:" + strings.ToLower(newEmailFromBody(c))
		}

		ctx, err := sl.lim.Get(c.Request.Context(), key)
		if err != nil {
			// 存储异常不阻断请求（对齐 express-rate-limit 的降级语义）。
			c.Next()
			return
		}

		// express-rate-limit draft-6（standardHeaders: true）等价头。
		c.Header("RateLimit-Policy", fmt.Sprintf("%d;w=%d", spec.Max, windowSeconds))
		c.Header("RateLimit-Limit", strconv.FormatInt(ctx.Limit, 10))
		c.Header("RateLimit-Remaining", strconv.FormatInt(ctx.Remaining, 10))
		c.Header("RateLimit-Reset", strconv.FormatInt(ctx.Reset, 10))

		if ctx.Reached {
			retryAfter := ctx.Reset - time.Now().Unix()
			if retryAfter < 0 {
				retryAfter = 0
			}
			c.Header("Retry-After", strconv.FormatInt(retryAfter, 10))
			c.AbortWithStatusJSON(429, gin.H{"message": spec.Message})
			return
		}
		c.Next()
	}
}

// newEmailFromBody 读取请求体中 newEmail 字段（对齐 req.body?.newEmail || 'unknown'）。
func newEmailFromBody(c *gin.Context) string {
	body := GetBody(c)
	if body == nil {
		return "unknown"
	}
	if v, ok := body["newEmail"].(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// clientIP 解析客户端 IP：生产（trustXFF）优先 X-Forwarded-For 首值，
// 否则取 RemoteAddr 去端口。
func clientIP(c *gin.Context, trustXFF bool) string {
	if trustXFF {
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			if first := ratelimit.NormalizeXFF(xff); first != "" {
				return first
			}
		}
	}
	return remoteIP(c.Request.RemoteAddr)
}

// remoteIP 去掉 "IP:port" 中的端口（兼容 "[v6]:port"）。
func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
