package middleware

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// ContextUserKey 是 gin.Context 中登录用户的键。
const ContextUserKey = "auth.user"

// ContextAuthTokenKey 是 gin.Context 中原始 access token 的键。
const ContextAuthTokenKey = "auth.token"

// ConcurrentRefreshGrace 是 refresh 轮换并发宽限期（对齐 authFactory.js 的 30s）。
const ConcurrentRefreshGrace = 30 * time.Second

// Role 常量（对齐 User 模型 enum）。
const (
	RoleUser      = "user"
	RoleCreator   = "creator"
	RoleAdmin     = "admin"
	RoleSuperAdmin = "superadmin"
)

// Auth 是认证中间件容器，持有仓储与签名器。
type Auth struct {
	Repos  *repository.Repos
	Signer *auth.Signer
}

// NewAuth 构造认证容器。
func NewAuth(repos *repository.Repos, signer *auth.Signer) *Auth {
	return &Auth{Repos: repos, Signer: signer}
}

// Protect 构造鉴权中间件：校验 JWT + 用户 + 角色（对齐 authFactory.js createAuthMiddleware）。
//   - 无 token → 401 {"message":"Not authorized, no token","messageKey":"auth.noToken"}
//   - 过期 → 419 {"message":"Access token expired","messageKey":"auth.accessTokenExpired"}
//   - 其它非法 / purpose 非 access → 401 {"message":"...","messageKey":"auth.invalidToken"}
//   - 用户不存在 → 401 {"message":"Not authorized, user not found","messageKey":"auth.userNotFound"}
//   - 角色不符 → 403 {"message":"Not authorized","messageKey":"auth.forbidden"}
func (a *Auth) Protect(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := auth.GetAccessToken(c)
		if token == "" {
			c.AbortWithStatusJSON(401, gin.H{"message": "Not authorized, no token", "messageKey": "auth.noToken"})
			return
		}
		claims, err := a.Signer.Verify(token)
		if err != nil {
			if err == auth.ErrTokenExpired {
				c.AbortWithStatusJSON(419, gin.H{"message": "Access token expired", "messageKey": "auth.accessTokenExpired"})
				return
			}
			c.AbortWithStatusJSON(401, gin.H{"message": "Not authorized, token failed", "messageKey": "auth.invalidToken"})
			return
		}
		// refresh/verify/2fa 等其它 purpose 不可用于访问（兼容历史无 purpose 令牌）。
		if claims.Purpose != "" && claims.Purpose != "access" {
			c.AbortWithStatusJSON(401, gin.H{"message": "Invalid token type", "messageKey": "auth.invalidToken"})
			return
		}
		user, err := a.Repos.Users.FindByID(c.Request.Context(), claims.ID)
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"message": "Not authorized, user not found", "messageKey": "auth.userNotFound"})
			return
		}
		if len(allowedRoles) > 0 && !containsRole(allowedRoles, user.Role) {
			c.AbortWithStatusJSON(403, gin.H{"message": "Not authorized", "messageKey": "auth.forbidden"})
			return
		}
		c.Set(ContextUserKey, user)
		c.Set(ContextAuthTokenKey, token)
		// fire-and-forget 更新 lastActiveAt（对齐 authFactory.js:54-57）。
		go func() {
			_ = a.Repos.Sessions.UpdateLastActiveByRefresh(c.Request.Context(), user.ID, time.Now())
		}()
		c.Next()
	}
}

// RequireEmailChanged 拦截超管未改默认邮箱时的写操作（对齐 authFactory.js requireEmailChanged）。
// 放行 GET、change-email、logout、verify 路径；其余 403 {"forceEmailChange":true}。
func (a *Auth) RequireEmailChanged() gin.HandlerFunc {
	return func(c *gin.Context) {
		if u, ok := GetUser(c); ok && u.Role == RoleSuperAdmin && u.Email == "admin@furry09.com" {
			path := c.Request.URL.Path
			method := c.Request.Method
			if method == "GET" || containsFold(path, "change-email") || containsFold(path, "logout") || containsFold(path, "verify") {
				c.Next()
				return
			}
			c.AbortWithStatusJSON(403, gin.H{"message": "请先修改管理员邮箱后再进行操作", "forceEmailChange": true})
			return
		}
		c.Next()
	}
}

// GetUser 从 gin.Context 取当前登录用户。
func GetUser(c *gin.Context) (*model.User, bool) {
	v, ok := c.Get(ContextUserKey)
	if !ok {
		return nil, false
	}
	u, ok := v.(*model.User)
	return u, ok
}

func containsRole(roles []string, r string) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 'a' - 'A'
		}
		return b
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
