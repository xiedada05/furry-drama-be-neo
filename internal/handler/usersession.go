package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/xiedada05/furry-drama-be-neo/internal/auth"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// sessionJSON 组装会话对象（对齐 userSessions.js toObject + isCurrent）。
func sessionJSON(s *model.UserSession, isCurrent bool) gin.H {
	return gin.H{
		"_id":              s.ID.Hex(),
		"tokenHash":        s.TokenHash,
		"refreshTokenHash": s.RefreshTokenHash,
		"deviceInfo":       s.DeviceInfo,
		"ip":               s.IP,
		"isActive":         s.IsActive,
		"loginAt":          s.LoginAt,
		"lastActiveAt":     s.LastActiveAt,
		"logoutAt":         s.LogoutAt,
		"isCurrent":        isCurrent,
	}
}

// CreateUserSession POST /api/user-sessions/create（protect）。
func (h *Auth) CreateUserSession(c *gin.Context) {
	var req struct {
		ScreenWidth  int    `json:"screenWidth"`
		ScreenHeight int    `json:"screenHeight"`
		Language     string `json:"language"`
	}
	_ = c.ShouldBindJSON(&req)
	user, _ := middleware.GetUser(c)
	accessToken, _ := c.Get(middleware.ContextAuthTokenKey)
	tokenStr, _ := accessToken.(string)
	ua := c.Request.UserAgent()
	parsed := auth.ParseUserAgent(ua)
	di := model.DeviceInfo{
		Browser:        parsed.Browser,
		BrowserVersion: parsed.BrowserVersion,
		OS:             parsed.OS,
		OSVersion:      parsed.OSVersion,
		DeviceType:     parsed.DeviceType,
		DeviceModel:    parsed.DeviceModel,
		UserAgent:      ua,
	}
	if req.ScreenWidth > 0 {
		di.ScreenWidth = req.ScreenWidth
	}
	if req.ScreenHeight > 0 {
		di.ScreenHeight = req.ScreenHeight
	}
	if req.Language != "" {
		di.Language = req.Language
	}
	sess, err := h.Svc.Repos.Sessions.UpsertByTokenHash(c.Request.Context(), auth.HashToken(tokenStr), user.ID, di, h.clientIP(c), time.Now())
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"sessionId": sess.ID.Hex()})
}

// MyUserSessions GET /api/user-sessions/my（protect）。
func (h *Auth) MyUserSessions(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	accessToken, _ := c.Get(middleware.ContextAuthTokenKey)
	tokenStr, _ := accessToken.(string)
	currentHash := auth.HashToken(tokenStr)
	sessions, err := h.Svc.Repos.Sessions.FindByUser(c.Request.Context(), user.ID, 20)
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	result := make([]gin.H, 0, len(sessions))
	for i := range sessions {
		result = append(result, sessionJSON(&sessions[i], sessions[i].TokenHash == currentHash))
	}
	c.JSON(200, result)
}

// RenameUserSession PUT /api/user-sessions/:id/name（protect）。
func (h *Auth) RenameUserSession(c *gin.Context) {
	var req struct {
		DeviceName string `json:"deviceName"`
	}
	_ = c.ShouldBindJSON(&req)
	name := strings.TrimSpace(req.DeviceName)
	if name == "" {
		c.JSON(400, gin.H{"message": "设备名称不能为空"})
		return
	}
	if len([]rune(name)) > 50 {
		name = string([]rune(name)[:50])
	}
	user, _ := middleware.GetUser(c)
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "会话不存在"})
		return
	}
	sess, err := h.Svc.Repos.Sessions.FindByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(404, gin.H{"message": "会话不存在"})
		return
	}
	if sess.UserID != user.ID {
		c.JSON(403, gin.H{"message": "权限不足"})
		return
	}
	if err := h.Svc.Repos.Sessions.UpdateDeviceName(c.Request.Context(), id, user.ID, name); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "设备名称已更新"})
}

// DeleteUserSession DELETE /api/user-sessions/:id（protect）。不能下线当前设备。
func (h *Auth) DeleteUserSession(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	accessToken, _ := c.Get(middleware.ContextAuthTokenKey)
	tokenStr, _ := accessToken.(string)
	currentHash := auth.HashToken(tokenStr)
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "会话不存在"})
		return
	}
	sess, err := h.Svc.Repos.Sessions.FindByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(404, gin.H{"message": "会话不存在"})
		return
	}
	if sess.UserID != user.ID {
		c.JSON(403, gin.H{"message": "权限不足"})
		return
	}
	if sess.TokenHash == currentHash {
		c.JSON(400, gin.H{"message": "不能下线当前设备，请使用退出登录"})
		return
	}
	if err := h.Svc.Repos.Sessions.DeactivateByID(c.Request.Context(), id); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "已下线该设备"})
}

// DeleteAllOtherSessions DELETE /api/user-sessions/my/all（protect）。
func (h *Auth) DeleteAllOtherSessions(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	accessToken, _ := c.Get(middleware.ContextAuthTokenKey)
	tokenStr, _ := accessToken.(string)
	if err := h.Svc.Repos.Sessions.DeactivateAllOtherByTokenHash(c.Request.Context(), user.ID, auth.HashToken(tokenStr)); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "已下线其他所有设备"})
}

// Heartbeat POST /api/user-sessions/heartbeat（protect）。
func (h *Auth) Heartbeat(c *gin.Context) {
	accessToken, _ := c.Get(middleware.ContextAuthTokenKey)
	tokenStr, _ := accessToken.(string)
	if err := h.Svc.Repos.Sessions.UpdateLastActiveByTokenHash(c.Request.Context(), auth.HashToken(tokenStr), time.Now()); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

// AllUserSessions GET /api/user-sessions/all（superAdminProtect）。
func (h *Auth) AllUserSessions(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	_ = user
	accessToken, _ := c.Get(middleware.ContextAuthTokenKey)
	tokenStr, _ := accessToken.(string)
	currentHash := auth.HashToken(tokenStr)
	sessions, err := h.Svc.Repos.Sessions.FindAll(c.Request.Context(), 200)
	if err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	result := make([]gin.H, 0, len(sessions))
	for i := range sessions {
		s := &sessions[i]
		row := sessionJSON(s, s.TokenHash == currentHash)
		if u, err := h.Svc.Repos.Users.FindByID(c.Request.Context(), s.UserID); err == nil {
			row["username"] = u.Username
			row["userRole"] = u.Role
			row["userId"] = u.ID.Hex()
		}
		result = append(result, row)
	}
	c.JSON(200, result)
}

// AdminDeleteSession DELETE /api/user-sessions/admin/:id（superAdminProtect）。
func (h *Auth) AdminDeleteSession(c *gin.Context) {
	id, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"message": "会话不存在"})
		return
	}
	if _, err := h.Svc.Repos.Sessions.FindByID(c.Request.Context(), id); err != nil {
		c.JSON(404, gin.H{"message": "会话不存在"})
		return
	}
	if err := h.Svc.Repos.Sessions.DeactivateByID(c.Request.Context(), id); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "已下线该设备"})
}

// AdminDeleteUserSessions DELETE /api/user-sessions/admin/user/:userId/all（superAdminProtect）。
func (h *Auth) AdminDeleteUserSessions(c *gin.Context) {
	userID, err := primitive.ObjectIDFromHex(c.Param("userId"))
	if err != nil {
		c.JSON(400, gin.H{"message": "会话不存在"})
		return
	}
	if err := h.Svc.Repos.Sessions.DeactivateAllByUser(c.Request.Context(), userID); err != nil {
		c.JSON(500, gin.H{"message": "Server error"})
		return
	}
	c.JSON(200, gin.H{"message": "已下线该用户全部会话"})
}
