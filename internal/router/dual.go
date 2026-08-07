// Package router 实现双版本路由挂载（/api/X 与 /api/v1/X 镜像）与路由注册表。
//
// 对齐 src/index.js:369-412：同一份 handler 同时挂两个前缀，v1 额外注入
// Deprecation: true 与 Sunset: Sat, 01 Jan 2027 00:00:00 GMT 头。
package router

import (
	"github.com/gin-gonic/gin"
)

// SunsetHeader 是 v1 镜像的弃用截止（对齐 Express 常量）。
const SunsetHeader = "Sat, 01 Jan 2027 00:00:00 GMT"

// deprecationHeaders 为 v1 分组注入弃用头。
func deprecationHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Deprecation", "true")
		c.Header("Sunset", SunsetHeader)
		c.Next()
	}
}

// MountDual 把 register 注册的 handler 同时挂载到 <parent>/<mountPath> 与
// <parent>/v1<mountPath>。典型用法：parent 为 /api 路由组（继承全局中间件与限流），
// mountPath 形如 "/auth"（首部含斜杠）。
func MountDual(parent *gin.RouterGroup, mountPath string, register func(g *gin.RouterGroup)) {
	register(parent.Group(mountPath))
	v1 := parent.Group("/v1" + mountPath)
	v1.Use(deprecationHeaders())
	register(v1)
}
