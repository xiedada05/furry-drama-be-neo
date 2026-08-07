package server

import (
	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
)

// RegisterRoutes 挂载第一段业务路由到受限流的 /api 路由组下（继承 globalLimiter）。
// M1 为空骨架；M2 填充 auth / user-sessions / 2fa / users 四个域的挂载
// （用 router.MountDual(api, "/auth", ...) 同时获得 /api 与 /api/v1 镜像，
// 并在具体端点用 middleware.RateLimit(store, spec, opts) 施加 per-endpoint 限流）。
func RegisterRoutes(d Deps, api *gin.RouterGroup, store ratelimit.Store, opts middleware.RateLimitOpts) {
	_ = d
	_ = store
	_ = opts
}
