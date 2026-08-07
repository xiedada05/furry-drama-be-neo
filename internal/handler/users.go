package handler

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/errors"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
	"github.com/xiedada05/furry-drama-be-neo/internal/upload"
)

// Avatar POST /api/users/avatar（protect + 2MB）。
func (h *Auth) Avatar(c *gin.Context) {
	url, err := upload.SaveImage(c, "avatar", "avatar", 2<<20)
	if err != nil {
		h.abortUploadError(c, err, 2)
		return
	}
	user, _ := middleware.GetUser(c)
	// 删除旧头像文件（忽略错误）。
	if user.Avatar != "" {
		_ = removeUploadedFile(user.Avatar)
	}
	if err := h.Svc.Repos.Users.UpdateAvatar(c.Request.Context(), user.ID, url); err != nil {
		c.JSON(500, gin.H{"message": "头像上传失败"})
		return
	}
	c.JSON(200, gin.H{"url": url})
}

// BackgroundUpload POST /api/users/background-upload（protect + 5MB）。
func (h *Auth) BackgroundUpload(c *gin.Context) {
	url, err := upload.SaveImage(c, "image", "bg", 5<<20)
	if err != nil {
		h.abortUploadError(c, err, 5)
		return
	}
	user, _ := middleware.GetUser(c)
	if err := h.Svc.Repos.Users.UpdateBackgroundPrefs(c.Request.Context(), user.ID,
		repository.BackgroundPrefsPatch{Image: &url}); err != nil {
		c.JSON(500, gin.H{"message": "背景图片上传失败"})
		return
	}
	c.JSON(200, gin.H{"url": url})
}

// BackgroundPrefs PUT /api/users/background-prefs（protect）。
func (h *Auth) BackgroundPrefs(c *gin.Context) {
	var req struct {
		Enabled *bool   `json:"enabled"`
		Opacity *int    `json:"opacity"`
		Blur    *int    `json:"blur"`
		Image   *string `json:"image"`
	}
	_ = c.ShouldBindJSON(&req)
	// 对齐 users.js：opacity clamp [0,100]，blur clamp [0,20]。
	patch := repository.BackgroundPrefsPatch{
		Enabled: req.Enabled,
		Image:   req.Image,
	}
	if req.Opacity != nil {
		v := clampInt(*req.Opacity, 0, 100)
		patch.Opacity = &v
	}
	if req.Blur != nil {
		v := clampInt(*req.Blur, 0, 20)
		patch.Blur = &v
	}
	user, _ := middleware.GetUser(c)
	if err := h.Svc.Repos.Users.UpdateBackgroundPrefs(c.Request.Context(), user.ID, patch); err != nil {
		c.JSON(500, gin.H{"message": "更新背景偏好失败"})
		return
	}
	u, err := h.Svc.Repos.Users.FindByID(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "更新背景偏好失败"})
		return
	}
	c.JSON(200, gin.H{"backgroundPrefs": u.BackgroundPrefs})
}

// Profile PUT /api/users/profile（protect）。
func (h *Auth) Profile(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
	}
	_ = c.ShouldBindJSON(&req)
	username := strings.TrimSpace(req.Username)
	if len([]rune(username)) > 20 {
		c.JSON(400, gin.H{"message": "昵称长度不能超过20个字符"})
		return
	}
	if username == "" {
		c.JSON(400, gin.H{"message": "没有需要更新的数据"})
		return
	}
	user, _ := middleware.GetUser(c)
	if err := h.Svc.Repos.Users.UpdateUsername(c.Request.Context(), user.ID, username); err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	u, err := h.Svc.Repos.Users.FindByID(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, gin.H{"message": "服务器错误"})
		return
	}
	c.JSON(200, gin.H{
		"_id":             u.ID.Hex(),
		"accountId":       u.AccountID,
		"username":        u.Username,
		"email":           u.Email,
		"isEmailVerified": u.IsEmailVerified,
		"role":            u.Role,
		"avatar":          u.Avatar,
	})
}

// ExportMyData GET /api/users/export-my-data（protect + exportLimiter 3/h）。
func (h *Auth) ExportMyData(c *gin.Context) {
	user, _ := middleware.GetUser(c)
	data, err := h.Svc.BuildExportData(c.Request.Context(), user)
	if err != nil {
		c.JSON(500, gin.H{"message": "导出失败"})
		return
	}
	dateStr := time.Now().Format("2006-01-02")
	format := c.Query("format")
	if format == "csv" {
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=my_data_%s.csv", dateStr))
		c.String(200, "\uFEFF"+data.ExportCSV())
		return
	}
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=my_data_%s.json", dateStr))
	c.JSON(200, data)
}

// ---- helpers ----

// abortUploadError 渲染上传错误（对齐 Express 上传端点文案）。
func (h *Auth) abortUploadError(c *gin.Context, err error, maxMB int) {
	if err == upload.ErrNoFile {
		c.JSON(400, gin.H{"message": "请选择要上传的图片"})
		return
	}
	if ue, ok := err.(*errors.UploadError); ok {
		switch ue.Code {
		case "LIMIT_FILE_SIZE":
			c.JSON(400, gin.H{"message": fmt.Sprintf("文件大小不能超过%dMB", maxMB)})
		case "LIMIT_FILE_TYPE":
			c.JSON(400, gin.H{"message": "仅支持图片文件 (jpg, jpeg, png, gif, webp)"})
		case "BAD_MAGIC":
			c.JSON(400, gin.H{"message": "文件内容与类型不匹配，仅支持图片文件"})
		default:
			c.JSON(400, gin.H{"message": "文件上传错误: " + ue.Message})
		}
		return
	}
	c.JSON(500, gin.H{"message": "服务器错误"})
}

// removeUploadedFile 删除已上传文件（忽略错误）。
func removeUploadedFile(url string) error {
	path := strings.TrimPrefix(url, "/uploads/")
	return upload.RemoveFile(path)
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
