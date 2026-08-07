package service

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// ConcurrentRefreshGrace 是并发宽限期（对齐 authFactory.js 30s）。
const ConcurrentRefreshGrace = 30 * time.Second

// RefreshResult 是 refresh 校验通过后的结果。
type RefreshResult struct {
	User    *model.User
	Session *model.UserSession
}

// VerifyRefreshToken 校验 refresh token 并原子"取用并作废"（对齐 authFactory.js verifyRefreshToken）。
// 返回 *errors.AppError（带 messageKey），handler 依状态码渲染。
func (s *AuthService) VerifyRefreshToken(ctx context.Context, refreshToken string) (*RefreshResult, error) {
	if refreshToken == "" {
		return nil, errors.NewKey(401, "No refresh token", "auth.noRefreshToken")
	}
	claims, err := s.Signer.Verify(refreshToken)
	if err != nil {
		if err == auth.ErrTokenExpired {
			return nil, errors.NewKey(401, "Refresh token expired", "auth.refreshTokenExpired")
		}
		return nil, errors.NewKey(401, "Invalid refresh token", "auth.invalidToken")
	}
	if claims.Purpose != "refresh" {
		return nil, errors.NewKey(401, "Invalid token type", "auth.invalidToken")
	}

	hash := auth.HashToken(refreshToken)
	// 原子取用并作废：并发刷新时只有一个请求抢到 active session。
	session, err := s.Repos.Sessions.FindAndDeactivateRefresh(ctx, hash)
	if err != nil {
		if repository.IsNotFound(err) {
			return s.handleRefreshMiss(ctx, hash, claims.ID)
		}
		return nil, errors.NewKey(500, "服务器错误", "")
	}

	user, err := s.Repos.Users.FindByID(ctx, session.UserID)
	if err != nil {
		return nil, errors.NewKey(401, "User not found", "auth.userNotFound")
	}
	return &RefreshResult{User: user, Session: session}, nil
}

// handleRefreshMiss 处理未抢到 session：并发宽限内 → 409，否则吊销全部 → 401 reuse。
func (s *AuthService) handleRefreshMiss(ctx context.Context, hash, claimsID string) (*RefreshResult, error) {
	existing, err := s.Repos.Sessions.FindByRefreshTokenHash(ctx, hash)
	if err == nil && existing.LogoutAt != nil {
		if time.Since(*existing.LogoutAt) < ConcurrentRefreshGrace {
			// 同一 refresh token 刚被另一请求轮换（并发刷新，非重用攻击）。
			return nil, errors.NewKey(409, "Concurrent refresh", "auth.concurrentRefresh")
		}
	}
	// 真实重用：吊销该用户全部 active session。
	userIDHex := claimsID
	if existing != nil {
		userIDHex = existing.UserID.Hex()
	}
	if userIDHex != "" {
		if oid, err := primitive.ObjectIDFromHex(userIDHex); err == nil {
			_ = s.Repos.Sessions.DeactivateAllByUser(ctx, oid)
		}
	}
	return nil, errors.NewKey(401, "Refresh token reuse detected", "auth.refreshTokenReuse")
}

// RotateRefresh 轮换双 token：旧 session 已作废（VerifyRefreshToken 已做），
// 新建 session（继承 deviceInfo/ip/loginAt），设新 cookie。
// 对齐 session.js refresh 成功路径 L527-576。
func (s *AuthService) RotateRefresh(c *gin.Context, user *model.User, old *model.UserSession) error {
	accessToken, err := s.Signer.Sign(user.ID.Hex(), "access", s.Config.JWT.AccessTTL, nil)
	if err != nil {
		return err
	}
	refreshToken, err := s.Signer.Sign(user.ID.Hex(), "refresh", s.Config.JWT.RefreshTTL, map[string]string{"jti": auth.RandomHex(24)})
	if err != nil {
		return err
	}
	now := time.Now()
	newSession := &model.UserSession{
		UserID:           user.ID,
		RefreshTokenHash: auth.HashToken(refreshToken),
		DeviceInfo:       old.DeviceInfo,
		IP:               old.IP,
		IsActive:         true,
		LoginAt:          old.LoginAt,
		LastActiveAt:     now,
	}
	if err := s.Repos.Sessions.Create(c.Request.Context(), newSession); err != nil {
		return err
	}
	auth.SetAuthCookies(c, accessToken, refreshToken, !s.Config.IsDev)
	return nil
}

// DeactivateForLogout 登出吊销：双路置废（refresh hash + access/token hash）并清 cookie。
// 对齐 session.js L487-512。
func (s *AuthService) DeactivateForLogout(c *gin.Context, refreshToken, accessToken string) {
	if refreshToken != "" {
		hash := auth.HashToken(refreshToken)
		if sess, err := s.Repos.Sessions.FindByRefreshTokenHash(c.Request.Context(), hash); err == nil {
			_ = s.Repos.Sessions.DeactivateByID(c.Request.Context(), sess.ID)
		}
	}
	if accessToken != "" {
		hash := auth.HashToken(accessToken)
		_ = s.Repos.Sessions.DeactivateByTokenHash(c.Request.Context(), hash)
	}
	auth.ClearAuthCookies(c, !s.Config.IsDev)
}
