package upload

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"mime/multipart"

	"github.com/gin-gonic/gin"
)

// IconsDir 是 SVG 图标的独立存储目录（可通过 SetIconsDir / cfg.Server.IconsDir 配置，
// 默认 ./uploads/icons）。返回 URL 前缀固定为 /uploads/icons/，要求 IconsDir
// 位于 uploads 目录之下（默认即满足；若自定义目录不在 uploads 下，则文件
// 无法通过 /uploads 静态服务访问，需自行挂载）。
var IconsDir = DefaultUploadsDir + "/icons"

// SetIconsDir 覆盖图标存储目录（空串忽略）。应传 cfg.Server.IconsDir。
func SetIconsDir(dir string) {
	if dir != "" {
		IconsDir = dir
	}
}

// svgMaxBytes 单个 SVG 图标大小上限（1MB）。
const svgMaxBytes = 1 << 20

// validateSVGContent 校验 SVG 文本内容：
//   - 去掉 UTF-8 BOM 与首部空白后必须以 <?xml 或 <svg 开头；
//   - 不允许包含 <script（防 XSS；inline 渲染侧同样会再过滤一次）。
func validateSVGContent(content string) bool {
	s := strings.TrimPrefix(content, "\ufeff")
	s = strings.TrimLeft(s, " \t\r\n")
	if !strings.HasPrefix(s, "<?xml") && !strings.HasPrefix(s, "<svg") {
		return false
	}
	if strings.Contains(strings.ToLower(s), "<script") {
		return false
	}
	return true
}

// SaveSVG 读取并保存一个上传的 SVG 图标文件，返回可访问 URL
// （"/uploads/icons/<文件名>"）。无文件 → ErrNoFile。校验规则见 SaveSVGHeader。
func SaveSVG(c *gin.Context, field string) (string, error) {
	if !strings.HasPrefix(c.ContentType(), "multipart/form-data") {
		return "", ErrNoFile
	}
	file, err := c.FormFile(field)
	if err != nil {
		return "", ErrNoFile
	}
	return SaveSVGHeader(file)
}

// SaveSVGHeader 校验并保存一个 SVG 图标文件头，返回可访问 URL。
// 校验规则：
//
//  1. 扩展名必须为 .svg；
//  2. MIME 建议为 image/svg+xml（容忍 text/xml、application/xml、
//     text/plain、application/octet-stream —— 浏览器拖拽上传常见）；
//  3. 文件内容必须以 <?xml 或 <svg 开头，且不含 <script（防 XSS）；
//  4. 大小 ≤ 1MB。
func SaveSVGHeader(file *multipart.FileHeader) (string, error) {
	if file.Size > svgMaxBytes {
		return "", fmt.Errorf("SVG 图标不能超过 1MB")
	}
	ext := strings.ToLower(path.Ext(file.Filename))
	if ext != ".svg" {
		return "", fmt.Errorf("仅支持 SVG 图标文件")
	}
	mime := file.Header.Get("Content-Type")
	if mime != "" && mime != "image/svg+xml" && mime != "text/xml" &&
		mime != "application/xml" && mime != "application/octet-stream" && mime != "text/plain" {
		return "", fmt.Errorf("仅支持 SVG 图标文件")
	}

	// 读入内容做安全校验（≤1MB，直接全量读入内存）。
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("读取上传文件失败: %w", err)
	}
	defer src.Close()
	buf := make([]byte, file.Size)
	if _, err := readFull(src, buf); err != nil {
		return "", fmt.Errorf("读取上传文件失败: %w", err)
	}
	if !validateSVGContent(string(buf)) {
		return "", fmt.Errorf("SVG 内容不合法（必须为 SVG 文档且不含脚本）")
	}

	// 目录不存在时创建（含多级）。
	if err := os.MkdirAll(IconsDir, 0o755); err != nil {
		return "", fmt.Errorf("创建图标目录失败: %w", err)
	}
	filename := fmt.Sprintf("icon-%s.svg", randHex(16))
	dstPath := filepath.Join(IconsDir, filename)
	if err := os.WriteFile(dstPath, buf, 0o644); err != nil {
		return "", fmt.Errorf("保存上传文件失败: %w", err)
	}
	return "/uploads/icons/" + filename, nil
}

// readFull 读取恰好 len(buf) 字节（io.ReadFull 的本地包装，减少依赖）。
func readFull(f multipart.File, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := f.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}

// RemoveIconFile 删除图标目录下的文件（path 为相对文件名或含 /uploads/icons/ 前缀的 URL）。
func RemoveIconFile(nameOrURL string) error {
	name := nameOrURL
	if i := strings.Index(nameOrURL, "/icons/"); i >= 0 {
		name = nameOrURL[i+len("/icons/"):]
	}
	full := filepath.Join(IconsDir, name)
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(full)
}
