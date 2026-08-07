// Package auth 提供认证核心原语：JWT 签发/校验、HTTP cookie 管理、令牌哈希。
//
// 行为对齐 Express utils/helpers.js：
//   - access token 15m，refresh token 7d，HS256 固定
//   - claims: {id, purpose, jti?}，jsonwebtoken 输出 iat/exp（秒）
//   - cookie 属性精确：accessToken(path=/, maxAge 15m)、refreshToken(path=/api/auth, maxAge 7d)
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims 是 JWT 载荷，对齐 jsonwebtoken 输出。
type Claims struct {
	ID              string `json:"id"`
	Purpose         string `json:"purpose,omitempty"`
	JTI             string `json:"jti,omitempty"`
	NewEmail        string `json:"newEmail,omitempty"`
	Type            string `json:"type,omitempty"`
	DeviceLoginCode string `json:"deviceLoginCode,omitempty"`
	jwt.RegisteredClaims
}

// ErrTokenExpired 标识令牌过期。
var ErrTokenExpired = errors.New("token expired")

// ErrInvalidToken 标识令牌非法（签名/alg/purpose 等）。
var ErrInvalidToken = errors.New("invalid token")

// Signer 用 HS256 签发/校验 JWT。
type Signer struct {
	secret []byte
}

// NewSigner 构造 Signer，secret 为 JWT_SECRET。
func NewSigner(secret string) *Signer {
	return &Signer{secret: []byte(secret)}
}

// Sign 签发 JWT：HS256，包含 id/purpose，ttl 为有效期。
// extra 为可选的额外 claim（如 jti/newEmail/type/deviceLoginCode）。
func (s *Signer) Sign(id, purpose string, ttl time.Duration, extra map[string]string) (string, error) {
	now := time.Now()
	claims := Claims{
		ID:      id,
		Purpose: purpose,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	if extra != nil {
		claims.JTI = extra["jti"]
		claims.NewEmail = extra["newEmail"]
		claims.Type = extra["type"]
		claims.DeviceLoginCode = extra["deviceLoginCode"]
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

// Verify 校验 JWT：强制 HS256，返回 Claims。
// 过期返回 ErrTokenExpired，其余非法返回 ErrInvalidToken。
func (s *Signer) Verify(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}
	if !token.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// HashToken 计算令牌哈希：sha256 hex（对齐 utils/helpers.js hashToken）。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RandomHex 生成 n 字节随机数的 hex 串（对齐 crypto.randomBytes(n).toString('hex')）。
func RandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(buf)
}
