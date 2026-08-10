package handler

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// 本文件为 announcements / wallpapers / friend-links 三域共享的 DTO 组装与
// 工具函数（对齐 backend/routes/announcements.js / wallpapers.js / friendLinks.js）。
// 函数名带 cms 前缀，避免与他域 handler 并发定义冲突。

// cmsReadBody 读取（已过 SanitizeInput 的）JSON 请求体为 map；非对象/空体返回空 map。
func cmsReadBody(c *gin.Context) map[string]any {
	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		return map[string]any{}
	}
	return body
}

// cmsCastBool 对齐 mongoose Boolean 类型 cast（update 场景的宽松转换）：
// "false"/"0"/"off"/"" → false，其余非空 → true（与 truthy 的差异仅在于字符串 "false"）。
func cmsCastBool(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		switch t {
		case "false", "0", "off", "":
			return false
		}
		return true
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	default:
		return true
	}
}

// cmsDateOrNil 对齐 `expireAt || null`：falsy（nil/false/""/数字0）→ nil，
// 否则按 mongoose Date cast 解析。
func cmsDateOrNil(v any) *time.Time {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return nil
	case float64:
		if t == 0 {
			return nil
		}
		tm := time.UnixMilli(int64(t))
		return &tm
	case int:
		if t == 0 {
			return nil
		}
		tm := time.UnixMilli(int64(t))
		return &tm
	case int64:
		if t == 0 {
			return nil
		}
		tm := time.UnixMilli(t)
		return &tm
	case string:
		if t == "" {
			return nil
		}
		return toDate(v)
	case time.Time:
		return &t
	default:
		return toDate(v)
	}
}

// cmsPublishAtOrNow 对齐 `publishAt || Date.now()`：falsy 值 → 当前时间。
func cmsPublishAtOrNow(v any) time.Time {
	if d := cmsDateOrNil(v); d != nil {
		return *d
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

// cmsParseIntJS 对齐 JS Number.parseInt：解析前导数字（含符号），无法解析返回 0。
func cmsParseIntJS(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:i])
	if neg {
		n = -n
	}
	return n
}

// cmsIsValidHTTPURL 对齐 friendLinks.js isValidUrl：仅接受 http/https 绝对 URL。
func cmsIsValidHTTPURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// cmsFrontendURL 返回前端站点 URL（对齐 getSiteUrl：FRONTEND_URL || 'http://localhost:3000'）。
func cmsFrontendURL(cfg *config.Config) string {
	if cfg.Server.FrontendURL != "" {
		return cfg.Server.FrontendURL
	}
	return "http://localhost:3000"
}

// cmsAnnouncementJSON 组装公告响应对象（对齐 mongoose 文档 toJSON/lean 输出，
// _id 为 hex 字符串，时间 RFC3339）。
func cmsAnnouncementJSON(a *model.Announcement) gin.H {
	return gin.H{
		"_id":              a.ID.Hex(),
		"title":            a.Title,
		"titleEn":          a.TitleEn,
		"content":          a.Content,
		"contentEn":        a.ContentEn,
		"type":             a.Type,
		"showPopup":        a.ShowPopup,
		"showBanner":       a.ShowBanner,
		"sendNotification": a.SendNotification,
		"sendEmail":        a.SendEmail,
		"dismissible":      a.Dismissible,
		"active":           a.Active,
		"pinned":           a.Pinned,
		"publishAt":        a.PublishAt,
		"expireAt":         a.ExpireAt,
		"notificationSent": a.NotificationSent,
		"emailSent":        a.EmailSent,
		"emailSentAt":      a.EmailSentAt,
		"emailSentCount":   a.EmailSentCount,
		"link":             a.Link,
		"createdBy":        a.CreatedBy,
		"createdAt":        a.CreatedAt,
		"updatedAt":        a.UpdatedAt,
		"__v":              a.VersionKey,
	}
}

// cmsWallpaperJSON 组装系统壁纸响应对象（完整字段，对齐 GET /system/all 的 toJSON）。
func cmsWallpaperJSON(w *model.SystemWallpaper) gin.H {
	return gin.H{
		"_id":          w.ID.Hex(),
		"name":         w.Name,
		"url":          w.URL,
		"thumbnailUrl": w.ThumbnailURL,
		"enabled":      w.Enabled,
		"sortOrder":    w.SortOrder,
		"uploadedBy":   w.UploadedBy,
		"createdAt":    w.CreatedAt,
		"updatedAt":    w.UpdatedAt,
		"__v":          w.VersionKey,
	}
}

// cmsWallpaperPublicJSON 组装系统壁纸公开视图（仅 GET /system 的 select 字段）。
func cmsWallpaperPublicJSON(w *model.SystemWallpaper) gin.H {
	return gin.H{
		"_id":          w.ID.Hex(),
		"name":         w.Name,
		"url":          w.URL,
		"thumbnailUrl": w.ThumbnailURL,
		"sortOrder":    w.SortOrder,
	}
}

// cmsFriendLinkJSON 组装友链响应对象（对齐 FriendLink 文档 toJSON/lean 输出）。
func cmsFriendLinkJSON(l *model.FriendLink) gin.H {
	return gin.H{
		"_id":           l.ID.Hex(),
		"name":          l.Name,
		"nameEn":        l.NameEn,
		"nameJa":        l.NameJa,
		"url":           l.URL,
		"logo":          l.Logo,
		"description":   l.Description,
		"descriptionEn": l.DescriptionEn,
		"descriptionJa": l.DescriptionJa,
		"order":         l.Order,
		"isActive":      l.IsActive,
		"status":        l.Status,
		"applicantId":   l.ApplicantID,
		"createdAt":     l.CreatedAt,
		"updatedAt":     l.UpdatedAt,
		"__v":           l.VersionKey,
	}
}
