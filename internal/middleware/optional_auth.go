package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
)

// OptionalAuth 构造可选鉴权中间件：尝试解析 access token，
// 成功则把用户挂到 ContextUserKey，无效/缺失/非 access purpose 均静默放行
// （对齐 routes/episodes.js 的 optionalAuth：公开接口区分访客/登录用户）。
// 与 Protect 不同：不设 ContextAuthTokenKey、不更新 lastActiveAt、不返回 401。
func (a *Auth) OptionalAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := auth.GetAccessToken(c)
		if token == "" {
			c.Next()
			return
		}
		claims, err := a.Signer.Verify(token)
		if err != nil {
			c.Next()
			return
		}
		// refresh/verify/2fa 等其它 purpose 令牌静默放行（不挂用户）。
		if claims.Purpose != "" && claims.Purpose != "access" {
			c.Next()
			return
		}
		user, err := a.Repos.Users.FindByID(c.Request.Context(), claims.ID)
		if err != nil {
			c.Next()
			return
		}
		c.Set(ContextUserKey, user)
		c.Next()
	}
}
