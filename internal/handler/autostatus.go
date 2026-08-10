package handler

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 本文件实现 /api/auto-status 子域（行为逐分支照抄 backend/routes/autoStatus.js）。
// 两个端点均为 superAdminProtect（仅 superadmin）。

// AutoStatus 是 /api/auto-status 域 handler 容器。
type AutoStatus struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc
}

// NewAutoStatus 构造后台任务 handler 容器。
func NewAutoStatus(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth, rl func(ratelimit.Spec) gin.HandlerFunc) *AutoStatus {
	return &AutoStatus{Repos: repos, Config: cfg, AuthMW: amw, RL: rl}
}

// Register 挂载 /api/auto-status 全部端点（不含 /api 前缀；路径对齐 autoStatus.js 子路径）。
// 仅 superadmin 可调用。
func (h *AutoStatus) Register(g *gin.RouterGroup) {
	g.POST("/auto-complete", h.AuthMW.Protect(middleware.RoleSuperAdmin), h.AutoComplete)
	g.POST("/check-premieres", h.AuthMW.Protect(middleware.RoleSuperAdmin), h.CheckPremieres)
}

// AutoComplete POST /api/auto-status/auto-complete（superAdminProtect）。
// @Summary 自动完结剧集
// @Tags 自动任务
// @Security bearerAuth
// @Success 200 {object} map[string]any "message/updated"
// @Router /auto-status/auto-complete [post]
func (h *AutoStatus) AutoComplete(c *gin.Context) {
	ctx := c.Request.Context()
	episodes, err := h.Repos.Episodes.AutoStatusFindOngoing(ctx)
	if err != nil {
		serverError(c)
		return
	}
	updated := 0
	for i := range episodes {
		ep := &episodes[i]
		// 对齐 autoStatus.js：currentEpisodes>0 && totalEpisodes>0 && current>=total。
		if ep.CurrentEpisodes > 0 && ep.TotalEpisodes != nil && *ep.TotalEpisodes > 0 &&
			ep.CurrentEpisodes >= *ep.TotalEpisodes {
			ep.Status = "completed"
			if err := h.Repos.Episodes.Save(ctx, ep); err != nil {
				serverError(c)
				return
			}
			updated++
		}
	}
	c.JSON(200, gin.H{
		"message": fmt.Sprintf("已自动将 %d 部剧集标记为已完结", updated),
		"updated": updated,
	})
}

// CheckPremieres POST /api/auto-status/check-premieres（superAdminProtect）。
// @Summary 自动发布预告单集
// @Tags 自动任务
// @Security bearerAuth
// @Success 200 {object} map[string]any "message/released"
// @Router /auto-status/check-premieres [post]
func (h *AutoStatus) CheckPremieres(c *gin.Context) {
	now := time.Now()
	ctx := c.Request.Context()
	singles, err := h.Repos.SingleEpisodes.AutoStatusFindDuePremieres(ctx, now)
	if err != nil {
		serverError(c)
		return
	}
	released := 0
	for i := range singles {
		se := &singles[i]
		// 对齐 autoStatus.js：isUpcoming=false，releaseDate=premiereDate。
		se.IsUpcoming = false
		se.ReleaseDate = se.PremiereDate
		if err := h.Repos.SingleEpisodes.AutoStatusSave(ctx, se); err != nil {
			serverError(c)
			return
		}
		released++
	}
	c.JSON(200, gin.H{
		"message":  fmt.Sprintf("已自动发布 %d 个预告单集", released),
		"released": released,
	})
}
