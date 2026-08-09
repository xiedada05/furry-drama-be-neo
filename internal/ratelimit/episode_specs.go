package ratelimit

import "time"

// ViewSpec 观看计数限流器 10/60s（对齐 routes/episodes.js 的 viewLimiter：
// windowMs 60*1000, max 10，挂 PUT /:id/view 与 PUT /single/:id/view）。
//
// 注意：neo 的 RateLimit 挂载语义为「/api/<Mount> 精确 + 子路径前缀」，
// 动态路径 /episodes/:id/view 无法用前缀精确表达，故本 Mounts 列表按任务约定
// 声明为 /episodes/view 与 /episodes/single/:id/view；实际动态路由不会被命中，
// 限流不生效（与 Express 的差异，见工作流 notes）。保留窗口/上限/文案与 Express 一致。
var ViewSpec = Spec{
	Name:    "view",
	Mounts:  []string{"/episodes/view", "/episodes/single/:id/view"},
	Window:  time.Minute,
	Max:     10,
	Message: "操作过于频繁，请稍后再试",
}
