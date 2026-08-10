package server

import (
	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/email"
	"github.com/xiedada05/furry-drama-be-neo/internal/handler"
	"github.com/xiedada05/furry-drama-be-neo/internal/ipregion"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/router"
	"github.com/xiedada05/furry-drama-be-neo/internal/service"
)

// RegisterRoutes 挂载第一段业务路由（认证 + 用户域）到受限流的 /api 路由组下。
// 每个端点经 MountDual 同时获得 /api 与 /api/v1 镜像；per-endpoint 限流由
// middleware.RateLimit 施加（内部按 path 判断，v1 不触发端点限流）。
func RegisterRoutes(d Deps, api *gin.RouterGroup, opts middleware.RateLimitOpts) {
	mail := email.NewClient(d.Config, d.Repos.SiteContents)
	ipr := ipregion.NewClient(nil)
	svc := service.NewAuthService(d.Config, d.Repos, d.Signer, mail, ipr)
	amw := middleware.NewAuth(d.Repos, d.Signer)
	h := handler.NewAuth(svc, amw, d.Config)
	rl := func(spec ratelimit.Spec) gin.HandlerFunc { return middleware.RateLimit(spec, opts) }

	router.MountDual(api, "/auth", func(g *gin.RouterGroup) {
		g.GET("/captcha", rl(ratelimit.CaptchaSpec), h.Captcha)
		g.GET("/check-accountId", rl(ratelimit.CheckAccountIDSpec), h.CheckAccountID)
		g.POST("/register", rl(ratelimit.RegisterSpec), h.Register)
		g.POST("/login", rl(ratelimit.AuthSpec), h.Login)
		g.POST("/logout", amw.Protect(), h.Logout)
		g.POST("/refresh", rl(ratelimit.TwoFactorSpec), h.Refresh)
		g.GET("/me", amw.Protect(), h.Me)
		g.GET("/sse-ticket", amw.Protect(), h.SSETicket)
		g.POST("/verify-device", rl(ratelimit.TwoFactorSpec), h.VerifyDevice)
		g.POST("/confirm-device-login", rl(ratelimit.TwoFactorSpec), h.ConfirmDeviceLogin)
		g.POST("/login-2fa", rl(ratelimit.TwoFactorSpec), h.Login2FA)
		g.PUT("/change-password", rl(ratelimit.PasswordResetSpec), amw.Protect(), h.ChangePassword)
		g.POST("/forgot-password", rl(ratelimit.PasswordResetSpec), h.ForgotPassword)
		g.POST("/reset-password", rl(ratelimit.PasswordResetSpec), h.ResetPassword)
		g.PUT("/change-email", amw.Protect("superadmin"), amw.RequireEmailChanged(), rl(ratelimit.ChangeEmailSpec), h.ChangeEmail)
		g.PUT("/email-notification-prefs", amw.Protect(), h.EmailNotificationPrefs)
		g.POST("/verify-email", rl(ratelimit.EmailVerifySpec), h.VerifyEmail)
		g.POST("/resend-verification", amw.Protect(), rl(ratelimit.EmailVerifySpec), h.ResendVerification)
		g.POST("/resend-verification-by-email", rl(ratelimit.EmailVerifySpec), h.ResendVerificationByEmail)
		g.POST("/request-email-change", amw.Protect(), rl(ratelimit.RequestEmailChangeSpec), h.RequestEmailChange)
		g.POST("/verify-email-change", h.VerifyEmailChange)
		g.POST("/request-deletion", amw.Protect(), h.RequestDeletion)
		g.POST("/cancel-deletion", amw.Protect(), h.CancelDeletion)
		g.GET("/deletion-status", amw.Protect(), h.DeletionStatus)
	})

	router.MountDual(api, "/user-sessions", func(g *gin.RouterGroup) {
		g.POST("/create", amw.Protect(), h.CreateUserSession)
		g.GET("/my", amw.Protect(), h.MyUserSessions)
		g.PUT("/:id/name", amw.Protect(), h.RenameUserSession)
		g.DELETE("/:id", amw.Protect(), h.DeleteUserSession)
		g.DELETE("/my/all", amw.Protect(), h.DeleteAllOtherSessions)
		g.POST("/heartbeat", amw.Protect(), h.Heartbeat)
		g.GET("/all", amw.Protect("superadmin"), h.AllUserSessions)
		g.DELETE("/admin/:id", amw.Protect("superadmin"), h.AdminDeleteSession)
		g.DELETE("/admin/user/:userId/all", amw.Protect("superadmin"), h.AdminDeleteUserSessions)
	})

	router.MountDual(api, "/2fa", func(g *gin.RouterGroup) {
		g.POST("/enable", amw.Protect(), rl(ratelimit.TwoFactorSpec), h.Enable2FA)
		g.POST("/verify-enable", amw.Protect(), rl(ratelimit.TwoFactorSpec), h.VerifyEnable2FA)
		g.POST("/disable", amw.Protect(), rl(ratelimit.TwoFactorSpec), h.Disable2FA)
		g.POST("/verify", amw.Protect(), rl(ratelimit.TwoFactorSpec), h.Verify2FA)
	})

	router.MountDual(api, "/users", func(g *gin.RouterGroup) {
		g.POST("/avatar", amw.Protect(), h.Avatar)
		g.POST("/background-upload", amw.Protect(), h.BackgroundUpload)
		g.PUT("/background-prefs", amw.Protect(), h.BackgroundPrefs)
		g.PUT("/profile", amw.Protect(), h.Profile)
		g.GET("/export-my-data", amw.Protect(), rl(ratelimit.ExportSpec), h.ExportMyData)
	})

	// ---- 内容域（挂载前缀对照 backend/src/index.js routeMounts）----
	ep := handler.NewEpisodes(d.Repos, d.Config, amw, rl, mail)
	router.MountDual(api, "/episodes", ep.Register)

	se := handler.NewSeries(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/series", se.Register)

	fw := handler.NewFollows(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/follows", fw.Register)

	fv := handler.NewFavorites(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/favorites", fv.Register)

	fo := handler.NewFolders(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/folders", fo.Register)

	sf := handler.NewSavedFolders(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/saved-folders", sf.Register)

	hi := handler.NewHistories(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/histories", hi.Register)

	ra := handler.NewRatings(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/ratings", ra.Register)

	// ---- CMS 域（公告/壁纸/友链，挂载前缀对照 backend/src/index.js routeMounts）----
	an := handler.NewAnnouncements(d.Repos, d.Config, amw, rl, mail)
	router.MountDual(api, "/announcements", an.Register)

	wp := handler.NewWallpapers(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/wallpapers", wp.Register)

	fl := handler.NewFriendLinks(d.Repos, d.Config, amw, rl, mail, svc.VerifyAltcha)
	router.MountDual(api, "/friend-links", fl.Register)

	// ---- 管理后台 + 审计日志域 ----
	ad := handler.NewAdmin(d.Repos, d.Config, amw, rl, mail)
	router.MountDual(api, "/admin", ad.Register)

	al := handler.NewAuditLogs(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/audit-logs", al.Register)

	// ---- 通知域 ----
	nt := handler.NewNotifications(d.Repos, d.Config, amw, rl, d.DB)
	router.MountDual(api, "/notifications", nt.Register)

	// ---- 统计 / 举报 / 审核域（挂载前缀对照 backend/src/index.js routeMounts）----
	st := handler.NewStats(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/stats", st.Register)

	rp := handler.NewReports(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/reports", rp.Register)

	rv := handler.NewReview(d.Repos, d.Config, amw, rl, mail)
	router.MountDual(api, "/review", rv.Register)

	// ---- 分类 / 轮播图 / 后台自动任务 / 版本历史域 ----
	cat := handler.NewCategories(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/categories", cat.Register)

	bn := handler.NewBanners(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/banners", bn.Register)

	as := handler.NewAutoStatus(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/auto-status", as.Register)

	vs := handler.NewVersions(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/versions", vs.Register)

	// ---- RSS 订阅 / 翻译 / 数据备份域（挂载前缀对照 backend/src/index.js routeMounts）----
	rs := handler.NewRSS(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/rss", rs.Register)

	tl := handler.NewTranslate(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/translate", tl.Register)

	bk := handler.NewBackup(d.Repos, d.Config, amw, rl, d.DB)
	router.MountDual(api, "/backup", bk.Register)

	// ---- 创作者中心 / 创作者主页 / 站点内容 / 反馈 / 动态流域
	// （挂载前缀对照 backend/src/index.js routeMounts）----
	cr := handler.NewCreator(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/creator", cr.Register)

	cp := handler.NewCreatorProfiles(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/creator-profile", cp.Register)

	sc := handler.NewSiteContents(d.Repos, d.Config, amw, rl, mail)
	router.MountDual(api, "/site-content", sc.Register)

	fb := handler.NewFeedback(d.Repos, d.Config, amw, rl, mail)
	router.MountDual(api, "/feedback", fb.Register)

	ac := handler.NewActivity(d.Repos, d.Config, amw, rl)
	router.MountDual(api, "/activity", ac.Register)
}
