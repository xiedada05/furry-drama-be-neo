// Package upload 实现 multer 等价的上传能力（backend/utils/upload.js）：
// 磁盘存储、扩展名 + MIME 过滤、魔数校验、大小限制。
//
// 错误契约（配合 internal/errors 的 AbortWithAppError / UploadError 渲染）：
//   - 类型不符 → UploadError{Code:"LIMIT_FILE_TYPE"}
//   - 魔数不符 → UploadError{Code:"BAD_MAGIC"}（文件已删除）
//   - 超出大小 → UploadError{Code:"LIMIT_FILE_SIZE"}
//   - 无文件   → ErrNoFile（调用方应回 400 {"message":"请选择要上传的图片"}，
//     对齐 Express 各路由对 !req.file 的校验）
package upload

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	apperrors "github.com/xiedada05/furry-drama-be-neo/internal/errors"
)

// DefaultUploadsDir 与 config.ServerConfig.UploadsDir 的默认值一致（./uploads）。
const DefaultUploadsDir = "./uploads"

// Dir 是当前上传存储目录（SaveImage 的落盘位置），默认 ./uploads。
// 启动时用 SetDir 覆盖为 cfg.Server.UploadsDir。
var Dir = DefaultUploadsDir

// SetDir 覆盖上传存储目录（空串忽略）。应传 cfg.Server.UploadsDir。
func SetDir(dir string) {
	if dir != "" {
		Dir = dir
	}
}

// ErrNoFile 表示请求中没有指定字段的文件（对齐 Express 路由层
// 400 {"message":"请选择要上传的图片"} 的校验，不是 multer 错误）。
var ErrNoFile = errors.New("请选择要上传的图片")

// 扩展名与 MIME 过滤正则（逐字对齐 utils/upload.js）：
//   - ext：/jpeg|jpg|png|gif|webp/（非锚定子串匹配，对 path.extname 的小写结果测试）
//   - mime：/^image\/(jpeg|png|gif|webp)$/
var (
	allowedExtRe  = regexp.MustCompile(`jpeg|jpg|png|gif|webp`)
	allowedMimeRe = regexp.MustCompile(`^image/(jpeg|png|gif|webp)$`)
)

// magicPrefixes 是前 12 字节 base64 后的魔数前缀（对齐 utils/upload.js MAGIC_BYTES 的 key）。
var magicPrefixes = []string{"/9j/", "iVBOR", "R0lGOD", "UklGR"}

// 错误文案（逐字对齐 Express）。
const (
	typeErrMsg  = "仅支持图片文件 (jpg, jpeg, png, gif, webp)"
	magicErrMsg = "文件内容与类型不匹配，仅支持图片文件"
	sizeErrMsg  = "File too large" // multer LIMIT_FILE_SIZE 的 message
)

// uploadBodySlack 是 multipart 体限制相对 maxBytes 的余量（multipart 边界、
// 表单头与其它字段的开销），配合落盘后的 file.Size 精确判断实现 multer 的
// "文件内容 ≤ maxBytes" 语义（multer 的 fileSize 只统计文件内容，不含开销）。
const uploadBodySlack = 1 << 20

// SaveImage 读取并保存一个上传图片，返回可访问 URL（"/uploads/<文件名>"）。
// 对齐 createUploadConfig(prefix, maxBytes).single(field) 的完整流程：
//
//  1. 请求体超 maxBytes+1MB（粗限）或落盘后文件内容超 maxBytes（精限）
//     → UploadError{LIMIT_FILE_SIZE}。
//  2. 扩展名（originalname 的小写 ext 命中 /jpeg|jpg|png|gif|webp/）且 MIME
//     （Content-Type 命中 ^image/(jpeg|png|gif|webp)$）不满足 → UploadError{LIMIT_FILE_TYPE}。
//  3. 落盘到 Dir，文件名为 "<prefix>-<16 字节 hex><原扩展名>"。
//  4. 前 12 字节 base64 魔数校验失败 → 删除文件并 UploadError{BAD_MAGIC}。
//
// 无文件（含非 multipart 请求）→ ErrNoFile。maxBytes<=0 时默认 5MB
// （对齐 createUploadConfig 的 maxFileSize 默认值）。
func SaveImage(c *gin.Context, field, prefix string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 5 << 20
	}
	if !strings.HasPrefix(c.ContentType(), "multipart/form-data") {
		return "", ErrNoFile
	}

	// 粗限：整个 multipart 体（含开销）上限 maxBytes+1MB。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+uploadBodySlack)
	file, err := c.FormFile(field)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return "", &apperrors.UploadError{Code: "LIMIT_FILE_SIZE", Message: sizeErrMsg}
		}
		if errors.Is(err, http.ErrMissingFile) {
			return "", ErrNoFile
		}
		return "", fmt.Errorf("读取上传文件失败: %w", err)
	}

	// 精限：文件内容本身不能超过 maxBytes（对齐 multer fileSize 语义）。
	if file.Size > maxBytes {
		return "", &apperrors.UploadError{Code: "LIMIT_FILE_SIZE", Message: sizeErrMsg}
	}

	// 扩展名 + MIME 过滤（先于魔数校验，与 multer fileFilter 一致）。
	ext := strings.ToLower(path.Ext(file.Filename))
	if !allowedExtRe.MatchString(ext) || !allowedMimeRe.MatchString(file.Header.Get("Content-Type")) {
		return "", &apperrors.UploadError{Code: "LIMIT_FILE_TYPE", Message: typeErrMsg}
	}

	// 落盘：<prefix>-<16字节hex><原扩展名>（保留原始大小写，对齐 path.extname）。
	filename := fmt.Sprintf("%s-%s%s", prefix, randHex(16), path.Ext(file.Filename))
	dstPath := path.Join(Dir, filename)
	if err := saveToDisk(file, dstPath); err != nil {
		return "", fmt.Errorf("保存上传文件失败: %w", err)
	}

	// 魔数校验：前 12 字节 base64 前缀匹配；失败删除已落盘文件。
	if !validateMagicBytes(dstPath) {
		_ = os.Remove(dstPath)
		return "", &apperrors.UploadError{Code: "BAD_MAGIC", Message: magicErrMsg}
	}

	return "/uploads/" + filename, nil
}

// saveToDisk 把 multipart 文件写入 dstPath。
func saveToDisk(file *multipart.FileHeader, dstPath string) error {
	src, err := file.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return closeErr
	}
	return nil
}

// validateMagicBytes 读取文件前 12 字节，base64 后检查魔数前缀
// （对齐 utils/upload.js validateMagicBytes；读失败视为不匹配）。
func validateMagicBytes(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 12)
	_, _ = f.Read(buf) // 短文件时剩余字节为 0，与 Buffer.alloc(12) 行为一致
	header := base64.StdEncoding.EncodeToString(buf)
	for _, magic := range magicPrefixes {
		if strings.HasPrefix(header, magic) {
			return true
		}
	}
	return false
}

// randHex 生成 n 字节随机数的 hex 字符串（对齐 crypto.randomBytes(16).toString('hex')）。
func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// 随机源极低概率失败：回退纳秒时间戳派生，保证不 panic。
		return hex.EncodeToString([]byte(fmt.Sprintf("%020d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(buf)
}
