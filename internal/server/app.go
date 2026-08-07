// Package server 装配 Gin 引擎：中间件管线顺序、静态资源、双版本路由、优雅关停。
//
// 中间件顺序必须与 Express src/index.js 一致：
//
//	recovery → gzip → securityheaders → cors → 30s 超时 → slowlogger →
//	bodyparse(1mb/50mb) → sanitizeInput → sanitizeHeaders →
//	GET /api/csrf-token → CSRF 校验(非 GET) → apiTracker → requestLogger →
//	[受限流路由组 /api] globalLimiter(300/min) → /api/health →
//	双版本业务路由（M2）→ 全局错误处理
//
// 关键差异处理（对比 Express）：
//   - gin 的 r.Use 对所有请求生效（含 csrf-token），故 csrf-token 单独注册在
//     CSRF 校验之前；CSRF 中间件对 GET 放行，行为一致。
//   - globalLimiter 挂载到 /api 路由组（不含 csrf-token，对齐 Express 天然豁免）。
//   - per-endpoint 限流在 M2 业务路由上施加（RateLimit(store, spec, opts)）。
package server

import (
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/handler"
	"github.com/xiedada05/furry-drama-be-neo/internal/logging"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// Deps 是装配 Gin 引擎所需的全部依赖。
type Deps struct {
	Config *config.Config
	DB     *mongo.Database
	Repos  *repository.Repos
	Signer *auth.Signer
}

// NewApp 按管线顺序装配并返回 Gin 引擎。
func NewApp(d Deps) *gin.Engine {
	if d.Config.IsDev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())

	// ---- 全局中间件（顺序对齐 Express）----
	r.Use(middleware.Gzip())
	r.Use(middleware.SecurityHeaders())
	r.Use(middleware.CORS(d.Config.Server.AllowOrigins))
	r.Use(middleware.Timeout())
	log := logging.New(d.Config.IsDev)
	r.Use(middleware.SlowLogger(log, d.Config.IsDev))
	r.Use(middleware.BodyParse())
	r.Use(middleware.SanitizeInput())
	r.Use(middleware.SanitizeHeaders())

	// ---- csrf-token：注册在 CSRF 校验与限流之前（对齐 Express 天然豁免）----
	r.GET("/api/csrf-token", handler.CSRF(d.Config))

	r.Use(middleware.CSRF())
	r.Use(middleware.APITracker(d.Repos.ApiUsage))
	r.Use(middleware.RequestLogger(log, !d.Config.IsDev)) // trustXFF 仅生产

	// ---- 受限流路由组 /api（globalLimiter 300/min，含 /api/v1 与 health）----
	store := ratelimit.NewSlidingWindow()
	opts := middleware.RateLimitOpts{
		TrustXFF:      !d.Config.IsDev,
		IsDev:         d.Config.IsDev,
		SkipRateLimit: d.Config.Security.RateLimitSkip,
	}
	api := r.Group("/api")
	api.Use(middleware.RateLimit(store, ratelimit.GlobalSpec, opts))
	api.GET("/health", handler.Health(d.DB))

	// ---- 双版本业务路由挂载（M2 填充；挂 api 组下以继承 globalLimiter）----
	RegisterRoutes(d, api, store, opts)

	// TODO(M1): /uploads 静态服务（7d 缓存 + noindex/nosniff/inline）
	// TODO(M4): swagger（仅非生产）/api/docs

	// ---- 全局错误处理（最后注册）----
	r.Use(errors.Handler(func() bool { return d.Config.IsDev }))

	return r
}
