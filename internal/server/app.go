// Package server 装配 Gin 引擎：中间件管线顺序、静态资源、双版本路由、优雅关停。
//
// 中间件顺序必须与 Express src/index.js 一致（详见 app.go 的 Use 注册顺序）：
//
//	cookie → gzip → helmet → cors → 30s 超时 → body 解析(1mb/50mb) → sanitize →
//	GET /api/csrf-token → CSRF 校验(非 GET) → apiTracker → requestLogger →
//	globalLimiter(300/min) → 各路径限流 → /uploads 静态 → /api/health → swagger(非生产) →
//	双版本路由 → 全局错误处理
package server

import (
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/handler"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
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
	r.Use(middleware.CORS(d.Config.Server.AllowOrigins))
	r.Use(middleware.CSRF())
	// TODO(M1): gzip / 30s 超时 / body 解析 / sanitize / apiTracker / requestLogger
	// TODO(M1): globalLimiter(300/min) 挂 /api/、各路径限流

	// ---- csrf-token 路由：注册在限流之前，不受 globalLimiter 限制（对齐 Express L255<L297）----
	r.GET("/api/csrf-token", handler.CSRF(d.Config))

	// ---- 健康检查：注册在限流之后，受 globalLimiter 限制（对齐 Express L335 在 L297 后）----
	r.GET("/api/health", handler.Health(d.DB))

	// TODO(M1): /uploads 静态服务（7d 缓存 + noindex/nosniff/inline）
	// TODO(M4): swagger（仅非生产）/api/docs

	// ---- 双版本路由挂载（M2 填充 auth/user-sessions/2fa/users）----

	// ---- 全局错误处理（最后注册）----

	return r
}
