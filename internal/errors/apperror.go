// Package errors 提供与 Express 后端等价的应用错误类型与全局错误处理中间件。
//
// 设计对齐 Express 的 src/index.js:414-432 内联错误处理中间件：
//   - CORS 拒绝 → 403 {"message":"CORS policy denied"}
//   - MulterError 等价 → 400 {"message":"文件上传错误: <msg>"}
//   - 其它 → err.Status || 500；生产环境 5xx 不泄露内部 message（"服务器内部错误"）
//   - 开发环境响应体附带 stack
package errors

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// AppError 是领域层抛出的可操作错误，等价于 Express 的 AppError 类
// （utils/errorHandler.js）：携带 HTTP 状态码与可选的 messageKey（前端 i18n 键）。
type AppError struct {
	Message    string
	Status     int
	MessageKey string
	// Cause 保留底层错误（DB/网络等），用于日志，不直接暴露给客户端。
	Cause error
	// IsOperational 标识"预期内的操作错误"；非操作错误（如 panic、DB 断连）在生产隐藏详情。
	IsOperational bool
}

// Error 实现 error 接口。
func (e *AppError) Error() string {
	if e.MessageKey != "" {
		return fmt.Sprintf("[%d] %s (key=%s)", e.Status, e.Message, e.MessageKey)
	}
	return fmt.Sprintf("[%d] %s", e.Status, e.Message)
}

// Unwrap 支持 errors.Is / errors.As 透传 Cause。
func (e *AppError) Unwrap() error { return e.Cause }

// New 构造一个操作错误。
func New(status int, message string) *AppError {
	return &AppError{Message: message, Status: status, IsOperational: true}
}

// NewKey 构造带 messageKey 的操作错误（鉴权中间件使用）。
func NewKey(status int, message, messageKey string) *AppError {
	return &AppError{Message: message, Status: status, MessageKey: messageKey, IsOperational: true}
}

// Wrap 包裹底层错误为一个操作错误。
func Wrap(status int, message string, cause error) *AppError {
	return &AppError{Message: message, Status: status, Cause: cause, IsOperational: true}
}

// IsNotFound 判断错误是否为"资源不存在"语义（404）。
func IsNotFound(err error) bool {
	var ae *AppError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// UploadError 是文件上传错误，等价于 multer 的 MulterError
// （LIMIT_FILE_SIZE / LIMIT_FILE_TYPE 等），由上传中间件抛给全局处理器。
type UploadError struct {
	Code    string // "LIMIT_FILE_SIZE" | "LIMIT_FILE_TYPE" | ...
	Message string
}

// Error 实现 error 接口。
func (e *UploadError) Error() string { return e.Message }

// IsUploadError 判断错误是否为上传错误。
func IsUploadError(err error) bool {
	var ue *UploadError
	return errors.As(err, &ue)
}

// AbortWithAppError 把 AppError 写入响应并中止请求链。
// 若错误不是 *AppError，回退为 500（生产隐藏 message）。
func AbortWithAppError(c *gin.Context, err error, isDev bool) {
	var ae *AppError
	if errors.As(err, &ae) {
		abortAppError(c, ae, isDev)
		return
	}
	var ue *UploadError
	if errors.As(err, &ue) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": "文件上传错误: " + ue.Message})
		return
	}
	abortAppError(c, &AppError{Message: err.Error(), Status: http.StatusInternalServerError}, isDev)
}

// abortAppError 写 AppError 响应，复刻 Express 的消息规则。
func abortAppError(c *gin.Context, ae *AppError, isDev bool) {
	status := ae.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	msg := ae.Message
	// 生产环境：5xx 隐藏内部 message，只回"服务器内部错误"。
	if status >= 500 && !isDev {
		msg = "服务器内部错误"
	} else if msg == "" {
		msg = "服务器错误"
	}
	body := gin.H{"message": msg}
	if ae.MessageKey != "" {
		body["messageKey"] = ae.MessageKey
	}
	if isDev && status >= 500 && ae.Cause != nil {
		body["stack"] = ae.Cause.Error()
	}
	c.AbortWithStatusJSON(status, body)
}

// Handler 是全局错误处理中间件，必须最后注册（gin 会捕获后续 handler 的 panic）。
// 对应 Express 内联 4 参错误中间件。
func Handler(isDev func() bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		// gin 约定：中间件若捕获到 panic 会写入 c.Errors，此处统一转成 AppError 形态。
		if len(c.Errors) > 0 {
			err := c.Errors.Last().Err
			if isUploadError(err) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": "文件上传错误: " + err.Error()})
				return
			}
			status := http.StatusInternalServerError
			msg := "服务器错误"
			if !isDev() {
				msg = "服务器内部错误"
			}
			c.AbortWithStatusJSON(status, gin.H{"message": msg})
		}
	}
}

func isUploadError(err error) bool {
	var ue *UploadError
	return errors.As(err, &ue)
}
