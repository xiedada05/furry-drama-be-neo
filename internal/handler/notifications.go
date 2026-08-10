package handler

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// SSE 连接管理常量（逐项对齐 routes/notifications.js 顶部）。
const (
	maxSSEConnections    = 500 // MAX_SSE_CONNECTIONS
	maxSSEPerIP          = 10  // MAX_SSE_PER_IP
	maxSSEClientsPerUser = 5   // 每用户最多连接数（超出驱逐最旧）
	sseHeartbeatInterval = 30 * time.Second
	sseConnectionTimeout = 30 * time.Minute
)

// sseClient 是一条活跃的 SSE 连接。cancel 用于驱逐最旧连接时终止其 handler。
type sseClient struct {
	cancel context.CancelFunc
}

// Notifications 是通知域（/api/notifications）handler 容器，行为逐端点对齐
// backend/routes/notifications.js。12 个端点通过 Register 挂载到
// /api/notifications（server.RegisterRoutes 的 MountDual 双版本镜像）。
type Notifications struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc

	// PushSubs 推送订阅仓储（pushsubscriptions 集合；repos.go 未注册，本域独占）。
	PushSubs *repository.PushSubscriptionRepo

	// SSE 连接簿（对齐 Express sseClients/IP 计数/总数，互斥保护）。
	sseMu       sync.Mutex
	sseClients  map[string][]*sseClient // userId → 连接列表
	sseTotal    int
	sseIPCounts map[string]int
}

// NewNotifications 构造通知 handler 容器。db 用于构造推送订阅仓储
// （collection pushsubscriptions，repos.go 未注册该集合）。
func NewNotifications(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc, db *mongo.Database) *Notifications {
	return &Notifications{
		Repos:       repos,
		Config:      cfg,
		AuthMW:      amw,
		RL:          rl,
		PushSubs:    repository.NewPushSubscriptionRepo(db.Collection("pushsubscriptions")),
		sseClients:  map[string][]*sseClient{},
		sseIPCounts: map[string]int{},
	}
}

// Register 挂载 /api/notifications 全部端点（不含 /api 前缀；顺序对齐
// routes/notifications.js：/stream 公开，其余 protect）。
func (h *Notifications) Register(g *gin.RouterGroup) {
	g.GET("/stream", h.Stream)
	g.POST("/subscribe-reminder", h.AuthMW.Protect(), h.SubscribeReminder)
	g.GET("/list", h.AuthMW.Protect(), h.List)
	g.GET("/unread-count", h.AuthMW.Protect(), h.UnreadCount)
	g.PUT("/read-all", h.AuthMW.Protect(), h.ReadAll)
	g.PUT("/read-episode/:episodeId", h.AuthMW.Protect(), h.ReadEpisode)
	g.PUT("/read/:id", h.AuthMW.Protect(), h.Read)
	g.DELETE("/clear-read", h.AuthMW.Protect(), h.ClearRead)
	g.DELETE("/:id", h.AuthMW.Protect(), h.Delete)
	g.GET("/vapid-public-key", h.VapidPublicKey)
	g.POST("/push/subscribe", h.AuthMW.Protect(), h.PushSubscribe)
	g.POST("/push/unsubscribe", h.AuthMW.Protect(), h.PushUnsubscribe)
}

// ---- 端点 ----

// Stream GET /api/notifications/stream（ticket 认证的 SSE 长连接：
// 心跳 30s、单连接超时 30min、IP 每 10 条、全局限 500 条、每用户限 5 条）。
// @Summary 通知实时推送（SSE）
// @Tags 通知
// @Param ticket query string false "sse-ticket（GET /auth/sse-ticket 签发，30s 有效）"
// @Success 200 {string} string "text/event-stream"
// @Router /notifications/stream [get]
func (h *Notifications) Stream(c *gin.Context) {
	userID, ok := h.verifySSETicket(c)
	if !ok {
		return
	}

	ip := h.clientIP(c)
	var client *sseClient
	var ipAdded, registered bool
	defer func() {
		h.sseMu.Lock()
		defer h.sseMu.Unlock()
		if registered {
			if clients, ok := h.sseClients[userID]; ok {
				idx := -1
				for i, cc := range clients {
					if cc == client {
						idx = i
						break
					}
				}
				if idx >= 0 {
					clients = append(clients[:idx], clients[idx+1:]...)
					if len(clients) == 0 {
						delete(h.sseClients, userID)
					} else {
						h.sseClients[userID] = clients
					}
				}
			}
			if h.sseTotal > 0 {
				h.sseTotal--
			}
		}
		if ipAdded {
			h.decSSEIPLocked(ip)
		}
	}()

	// IP 级别限制（对齐 /stream 顶部：先查再自增，超限直接结束）。
	h.sseMu.Lock()
	ipCount := h.sseIPCounts[ip]
	if ipCount >= maxSSEPerIP {
		h.sseMu.Unlock()
		h.writeSSEHeaders(c)
		h.writeSSEEvent(c, `{"type":"error","message":"Too many connections from this IP"}`)
		return
	}
	h.sseIPCounts[ip] = ipCount + 1
	ipAdded = true
	h.sseMu.Unlock()

	// 已通过 IP 校验：写头 + connected 事件（对齐 writeHead + res.write connected）。
	h.writeSSEHeaders(c)
	h.writeSSEEvent(c, `{"type":"connected"}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client = &sseClient{cancel: cancel}

	// 全局限 500 条：超过则发送 error 后结束（connected 已发，对齐 Express 顺序）。
	h.sseMu.Lock()
	if h.sseTotal >= maxSSEConnections {
		h.sseMu.Unlock()
		h.writeSSEEvent(c, `{"type":"error","message":"Too many connections"}`)
		return
	}
	// 每用户最多 5 条：超出驱逐最旧连接（对齐 shift + oldest.end()）。
	clients := h.sseClients[userID]
	if len(clients) >= maxSSEClientsPerUser {
		oldest := clients[0]
		clients = clients[1:]
		oldest.cancel()
	}
	h.sseClients[userID] = append(clients, client)
	h.sseTotal++
	registered = true
	h.sseMu.Unlock()

	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(sseConnectionTimeout)
	defer timeout.Stop()
	closeNotify := c.Writer.CloseNotify()
	for {
		select {
		case <-ctx.Done():
			return
		case <-closeNotify:
			return
		case <-timeout.C:
			return
		case <-ticker.C:
			if _, err := c.Writer.WriteString(":heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

// SubscribeReminder POST /api/notifications/subscribe-reminder（protect）。
// 订阅剧集更新提醒：复用追番(Follow)机制，未追番则创建追番关系。
// @Summary 订阅剧集更新提醒
// @Tags 通知
// @Security bearerAuth
// @Accept json
// @Param body body object true "episodeId"
// @Success 200 {object} map[string]any "message/subscribed"
// @Router /notifications/subscribe-reminder [post]
func (h *Notifications) SubscribeReminder(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		EpisodeID string `json:"episodeId"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.EpisodeID == "" {
		c.JSON(400, gin.H{"message": "缺少剧集ID"})
		return
	}
	oid, err := primitive.ObjectIDFromHex(req.EpisodeID)
	if err != nil {
		serverError(c)
		return
	}
	ctx := c.Request.Context()
	episode, err := h.Repos.Episodes.FindByID(ctx, oid)
	if err != nil {
		if repository.IsNotFound(err) {
			c.JSON(404, gin.H{"message": "剧集不存在"})
			return
		}
		serverError(c)
		return
	}
	follow, err := h.Repos.Follows.FollowFindByUserEpisode(ctx, user.ID, oid)
	if err != nil && !repository.IsNotFound(err) {
		serverError(c)
		return
	}
	if follow == nil {
		if err := h.Repos.Follows.FollowInsert(ctx, &model.Follow{
			UserID:             user.ID,
			EpisodeID:          oid,
			FollowedAtEpisodes: episode.CurrentEpisodes,
			CreatedAt:          time.Now(),
		}); err != nil {
			serverError(c)
			return
		}
	}
	c.JSON(200, gin.H{"message": "订阅提醒成功", "subscribed": true})
}

// List GET /api/notifications/list（protect，分页）。
// @Summary 通知列表
// @Tags 通知
// @Security bearerAuth
// @Param page query int false "页码（默认1）"
// @Param limit query int false "每页数量（默认20，≤100）"
// @Success 200 {object} map[string]any "list/page/limit/total/totalPages"
// @Router /notifications/list [get]
func (h *Notifications) List(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	page, limit := notificationsPageLimit(c)
	ctx := c.Request.Context()
	total, err := h.Repos.Notifications.CountByUser(ctx, user.ID)
	if err != nil {
		serverError(c)
		return
	}
	list, err := h.Repos.Notifications.FindByUser(ctx, user.ID, page, limit)
	if err != nil {
		serverError(c)
		return
	}
	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, notificationJSON(&list[i]))
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(limit) - 1) / int64(limit))
	}
	c.JSON(200, gin.H{
		"list":       items,
		"page":       page,
		"limit":      limit,
		"total":      total,
		"totalPages": totalPages,
	})
}

// UnreadCount GET /api/notifications/unread-count（protect）。
// @Summary 未读通知数
// @Tags 通知
// @Security bearerAuth
// @Success 200 {object} map[string]any "count"
// @Router /notifications/unread-count [get]
func (h *Notifications) UnreadCount(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	count, err := h.Repos.Notifications.CountUnread(c.Request.Context(), user.ID)
	if err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"count": count})
}

// ReadAll PUT /api/notifications/read-all（protect）：全部未读标记为已读。
// @Summary 全部标记为已读
// @Tags 通知
// @Security bearerAuth
// @Success 200 {object} map[string]string "message"
// @Router /notifications/read-all [put]
func (h *Notifications) ReadAll(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	if _, err := h.Repos.Notifications.MarkAllRead(c.Request.Context(), user.ID); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "All marked as read"})
}

// ReadEpisode PUT /api/notifications/read-episode/:episodeId（protect）：
// 某剧集的未读通知全部标记为已读。
// @Summary 某剧集通知标记为已读
// @Tags 通知
// @Security bearerAuth
// @Param episodeId path string true "剧集 ID"
// @Success 200 {object} map[string]string "message"
// @Router /notifications/read-episode/{episodeId} [put]
func (h *Notifications) ReadEpisode(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	oid, err := primitive.ObjectIDFromHex(c.Param("episodeId"))
	if err != nil {
		serverError(c)
		return
	}
	if _, err := h.Repos.Notifications.MarkEpisodeRead(c.Request.Context(), user.ID, oid); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "Episode notifications marked as read"})
}

// Read PUT /api/notifications/read/:id（protect）：单条通知标记为已读。
// @Summary 单条通知标记为已读
// @Tags 通知
// @Security bearerAuth
// @Param id path string true "通知 ID"
// @Success 200 {object} map[string]string "message"
// @Router /notifications/read/{id} [put]
func (h *Notifications) Read(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	if err := h.Repos.Notifications.MarkReadByID(c.Request.Context(), oid, user.ID); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "Marked as read"})
}

// ClearRead DELETE /api/notifications/clear-read（protect）：删除全部已读通知。
// @Summary 清理已读通知
// @Tags 通知
// @Security bearerAuth
// @Success 200 {object} map[string]string "message"
// @Router /notifications/clear-read [delete]
func (h *Notifications) ClearRead(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	if _, err := h.Repos.Notifications.ClearRead(c.Request.Context(), user.ID); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "Read notifications cleared"})
}

// Delete DELETE /api/notifications/:id（protect）：删除单条通知。
// @Summary 删除通知
// @Tags 通知
// @Security bearerAuth
// @Param id path string true "通知 ID"
// @Success 200 {object} map[string]string "message"
// @Router /notifications/{id} [delete]
func (h *Notifications) Delete(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	oid, err := primitive.ObjectIDFromHex(c.Param("id"))
	if err != nil {
		serverError(c)
		return
	}
	if err := h.Repos.Notifications.DeleteByIDForUser(c.Request.Context(), oid, user.ID); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "Notification deleted"})
}

// VapidPublicKey GET /api/notifications/vapid-public-key（公开）：返回 Web Push
// VAPID 公钥。未配置时输出 null（对齐 process.env.VAPID_PUBLIC_KEY 为 undefined）。
// @Summary VAPID 公钥
// @Tags 通知
// @Success 200 {object} map[string]any "publicKey"
// @Router /notifications/vapid-public-key [get]
func (h *Notifications) VapidPublicKey(c *gin.Context) {
	var publicKey any
	if h.Config.VAPID.PublicKey != "" {
		publicKey = h.Config.VAPID.PublicKey
	}
	c.JSON(200, gin.H{"publicKey": publicKey})
}

// PushSubscribe POST /api/notifications/push/subscribe（protect）：保存浏览器
// Web Push 订阅（userId+endpoint upsert）。
// @Summary 订阅 Web Push
// @Tags 通知
// @Security bearerAuth
// @Accept json
// @Param body body object true "subscription"
// @Success 200 {object} map[string]string "message"
// @Router /notifications/push/subscribe [post]
func (h *Notifications) PushSubscribe(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		Subscription *pushSubscriptionBody `json:"subscription"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Subscription == nil || req.Subscription.Endpoint == "" {
		c.JSON(400, gin.H{"message": "无效的订阅信息"})
		return
	}
	if err := h.PushSubs.PushSubUpsertByEndpoint(c.Request.Context(), user.ID,
		req.Subscription.Endpoint, req.Subscription.keys(), c.GetHeader("User-Agent")); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "推送订阅成功"})
}

// PushUnsubscribe POST /api/notifications/push/unsubscribe（protect）：取消
// Web Push 订阅（按 userId+endpoint 删除）。
// @Summary 取消 Web Push 订阅
// @Tags 通知
// @Security bearerAuth
// @Accept json
// @Param body body object true "endpoint"
// @Success 200 {object} map[string]string "message"
// @Router /notifications/push/unsubscribe [post]
func (h *Notifications) PushUnsubscribe(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Endpoint == "" {
		c.JSON(400, gin.H{"message": "缺少endpoint"})
		return
	}
	if err := h.PushSubs.PushSubDeleteByEndpoint(c.Request.Context(), user.ID, req.Endpoint); err != nil {
		serverError(c)
		return
	}
	c.JSON(200, gin.H{"message": "取消推送订阅成功"})
}

// ---- helpers ----

// pushSubscriptionBody 是浏览器 PushManager.subscribe() 返回的订阅对象。
type pushSubscriptionBody struct {
	Endpoint string         `json:"endpoint"`
	Keys     map[string]any `json:"keys"`
}

// keys 提取 {p256dh, auth}（缺失返回空串，对齐 mongoose 宽松 cast）。
func (b *pushSubscriptionBody) keys() model.PushSubscriptionKeys {
	return model.PushSubscriptionKeys{
		P256DH: asString(b.Keys["p256dh"]),
		Auth:   asString(b.Keys["auth"]),
	}
}

// verifySSETicket 校验 SSE ticket（对齐 /stream 顶部 verifyJwt + purpose 检查）。
// 失败时已写入 401 响应，返回 ok=false。
func (h *Notifications) verifySSETicket(c *gin.Context) (string, bool) {
	ticket := c.Query("ticket")
	if ticket == "" {
		c.JSON(401, gin.H{"message": "需要认证"})
		return "", false
	}
	claims, err := h.AuthMW.Signer.Verify(ticket)
	if err != nil {
		c.JSON(401, gin.H{"message": "认证信息无效"})
		return "", false
	}
	if claims.Purpose != "sse-ticket" {
		c.JSON(401, gin.H{"message": "无效的ticket"})
		return "", false
	}
	return claims.ID, true
}

// writeSSEHeaders 写 SSE 响应头（对齐 Express writeHead(200, {...})）。
func (h *Notifications) writeSSEHeaders(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(200)
}

// writeSSEEvent 写一条 SSE data 事件并立即 flush（对齐 res.write('data: ...\n\n')）。
func (h *Notifications) writeSSEEvent(c *gin.Context, payload string) {
	_, _ = c.Writer.WriteString("data: " + payload + "\n\n")
	c.Writer.Flush()
}

// decSSEIPLocked 回退某 IP 的连接计数（互斥锁内调用）。
func (h *Notifications) decSSEIPLocked(ip string) {
	if n, ok := h.sseIPCounts[ip]; ok && n > 1 {
		h.sseIPCounts[ip] = n - 1
	} else {
		delete(h.sseIPCounts, ip)
	}
}

// clientIP 提取客户端 IP（生产信任 X-Forwarded-For 首值，对齐 req.ip）。
func (h *Notifications) clientIP(c *gin.Context) string {
	if !h.Config.IsDev {
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			if first := ratelimit.NormalizeXFF(xff); first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

// notificationsPageLimit 对齐 routes/notifications.js 内联分页：
// page = Math.max(1, parseInt(page)||1)；limit = Math.min(100, Math.max(1, parseInt(limit)||20))。
func notificationsPageLimit(c *gin.Context) (int, int) {
	page := 1
	if p, err := strconv.Atoi(c.Query("page")); err == nil && p > 0 {
		page = p
	}
	limit := 20
	if l, err := strconv.Atoi(c.Query("limit")); err == nil {
		if l < 0 {
			limit = 1 // parseInt 负数 → Math.max(1, 负数) = 1
		} else if l > 0 {
			limit = l
		} // l == 0 → parseInt(0)||20 → 20
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit
}

// notificationJSON 组装单条通知响应（对齐 mongoose toJSON：
// _id/userId 输出 hex，episodeId 无值时输出 null，metadata 空输出 {}）。
func notificationJSON(n *model.Notification) gin.H {
	var episodeID any
	if n.EpisodeID != nil {
		episodeID = n.EpisodeID.Hex()
	}
	return gin.H{
		"_id":            n.ID.Hex(),
		"userId":         n.UserID.Hex(),
		"episodeId":      episodeID,
		"episodeTitle":   n.EpisodeTitle,
		"episodeTitleEn": n.EpisodeTitleEn,
		"type":           n.Type,
		"link":           n.Link,
		"message":        n.Message,
		"metadata":       orEmptyM(n.Metadata),
		"isRead":         n.IsRead,
		"createdAt":      n.CreatedAt,
		"__v":            0,
	}
}
