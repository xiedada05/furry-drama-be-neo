package upload

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	apperrors "github.com/xiedada05/furry-drama-be-neo/internal/errors"
)

// setupTest 把上传目录切到临时目录，测试结束恢复。
func setupTest(t *testing.T) {
	t.Helper()
	old := Dir
	SetDir(t.TempDir())
	t.Cleanup(func() { Dir = old })
}

// multipartRequest 构造带指定字段文件的多部分请求。
func multipartRequest(t *testing.T, field, filename, partCT string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filename))
	if partCT != "" {
		h.Set("Content-Type", partCT)
	}
	fw, err := w.CreatePart(h)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func newGinContext(req *http.Request) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

// 各格式魔数字节（前 12 字节 base64 前缀分别命中 /9j/、iVBOR、R0lGOD、UklGR）。
var (
	jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01}
	pngBytes  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}
	webpBytes = []byte{0x52, 0x49, 0x46, 0x46, 0x24, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50}
)

func TestSaveImageValidJPEG(t *testing.T) {
	setupTest(t)
	req := multipartRequest(t, "image", "photo.JPG", "image/jpeg", jpegBytes)
	c := newGinContext(req)

	url, err := SaveImage(c, "image", "avatar", 5<<20)
	if err != nil {
		t.Fatalf("SaveImage err: %v", err)
	}
	if !strings.HasPrefix(url, "/uploads/avatar-") || !strings.HasSuffix(url, ".JPG") {
		t.Fatalf("url = %q, want /uploads/avatar-<hex>.JPG", url)
	}
	name := strings.TrimPrefix(url, "/uploads/")
	fi, err := os.Stat(path.Join(Dir, name))
	if err != nil {
		t.Fatalf("文件未落盘: %v", err)
	}
	if fi.Size() != int64(len(jpegBytes)) {
		t.Fatalf("文件大小 = %d, want %d", fi.Size(), len(jpegBytes))
	}
}

func TestSaveImageValidPNGAndWebP(t *testing.T) {
	setupTest(t)
	for _, tc := range []struct {
		content []byte
		ext     string
		ct      string
	}{
		{pngBytes, ".png", "image/png"},
		{webpBytes, ".webp", "image/webp"},
	} {
		req := multipartRequest(t, "image", "img"+tc.ext, tc.ct, tc.content)
		url, err := SaveImage(newGinContext(req), "image", "cover", 5<<20)
		if err != nil {
			t.Fatalf("%s SaveImage err: %v", tc.ext, err)
		}
		if !strings.HasSuffix(url, tc.ext) {
			t.Fatalf("url = %q, want 后缀 %s", url, tc.ext)
		}
	}
}

func TestSaveImageWrongExtension(t *testing.T) {
	setupTest(t)
	req := multipartRequest(t, "image", "photo.txt", "image/jpeg", jpegBytes)
	_, err := SaveImage(newGinContext(req), "image", "avatar", 5<<20)
	var ue *apperrors.UploadError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UploadError{LIMIT_FILE_TYPE}", err)
	}
	if ue.Code != "LIMIT_FILE_TYPE" {
		t.Fatalf("code = %q, want LIMIT_FILE_TYPE", ue.Code)
	}
	if ue.Message != "仅支持图片文件 (jpg, jpeg, png, gif, webp)" {
		t.Fatalf("message = %q", ue.Message)
	}
}

func TestSaveImageWrongMIME(t *testing.T) {
	setupTest(t)
	// 扩展名对但 MIME 不对
	req := multipartRequest(t, "image", "photo.jpg", "application/octet-stream", jpegBytes)
	_, err := SaveImage(newGinContext(req), "image", "avatar", 5<<20)
	var ue *apperrors.UploadError
	if !errors.As(err, &ue) || ue.Code != "LIMIT_FILE_TYPE" {
		t.Fatalf("err = %v, want LIMIT_FILE_TYPE", err)
	}
}

func TestSaveImageBadMagic(t *testing.T) {
	setupTest(t)
	// 扩展名与 MIME 都对，但内容不是图片
	req := multipartRequest(t, "image", "photo.jpg", "image/jpeg", []byte("hello, this is not an image!"))
	_, err := SaveImage(newGinContext(req), "image", "avatar", 5<<20)
	var ue *apperrors.UploadError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UploadError{BAD_MAGIC}", err)
	}
	if ue.Code != "BAD_MAGIC" {
		t.Fatalf("code = %q, want BAD_MAGIC", ue.Code)
	}
	if ue.Message != "文件内容与类型不匹配，仅支持图片文件" {
		t.Fatalf("message = %q", ue.Message)
	}
	// 魔数失败后文件应被删除
	entries, _ := os.ReadDir(Dir)
	if len(entries) != 0 {
		t.Fatalf("BAD_MAGIC 后应删除落盘文件，剩余 %d 个", len(entries))
	}
}

func TestSaveImageOversize(t *testing.T) {
	setupTest(t)
	big := bytes.Repeat([]byte{0xFF, 0xD8, 0xFF}, 300) // ~900 字节
	req := multipartRequest(t, "image", "big.jpg", "image/jpeg", big)
	_, err := SaveImage(newGinContext(req), "image", "avatar", 256)
	var ue *apperrors.UploadError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want *UploadError{LIMIT_FILE_SIZE}", err)
	}
	if ue.Code != "LIMIT_FILE_SIZE" {
		t.Fatalf("code = %q, want LIMIT_FILE_SIZE", ue.Code)
	}
}

func TestSaveImageExactBoundary(t *testing.T) {
	setupTest(t)
	// 文件内容恰为 maxBytes：multipart 开销计入体上限但不算文件大小，
	// 应放行（对齐 multer fileSize 只统计文件内容）。
	content := append([]byte{0xFF, 0xD8, 0xFF}, bytes.Repeat([]byte{0x00}, 100)...) // 103 字节
	req := multipartRequest(t, "image", "photo.jpg", "image/jpeg", content)
	url, err := SaveImage(newGinContext(req), "image", "avatar", int64(len(content)))
	if err != nil {
		t.Fatalf("恰为 maxBytes 应放行: %v", err)
	}
	if !strings.HasPrefix(url, "/uploads/avatar-") {
		t.Fatalf("url = %q", url)
	}

	// 文件内容比 maxBytes 多 1 字节：拒绝（精限）。
	content2 := append(content, 0x00)
	req2 := multipartRequest(t, "image", "photo.jpg", "image/jpeg", content2)
	_, err = SaveImage(newGinContext(req2), "image", "avatar", int64(len(content)))
	var ue *apperrors.UploadError
	if !errors.As(err, &ue) || ue.Code != "LIMIT_FILE_SIZE" {
		t.Fatalf("超 1 字节应 LIMIT_FILE_SIZE, got %v", err)
	}
}

func TestSaveImageNoFile(t *testing.T) {
	setupTest(t)
	// 字段名不匹配 → ErrNoFile
	req := multipartRequest(t, "other", "photo.jpg", "image/jpeg", jpegBytes)
	_, err := SaveImage(newGinContext(req), "image", "avatar", 5<<20)
	if err != ErrNoFile {
		t.Fatalf("err = %v, want ErrNoFile", err)
	}
}

func TestSaveImageNonMultipart(t *testing.T) {
	setupTest(t)
	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	_, err := SaveImage(newGinContext(req), "image", "avatar", 5<<20)
	if err != ErrNoFile {
		t.Fatalf("非 multipart 请求 err = %v, want ErrNoFile", err)
	}
}

func TestSaveImageDefaultMaxBytes(t *testing.T) {
	setupTest(t)
	req := multipartRequest(t, "image", "photo.jpg", "image/jpeg", jpegBytes)
	// maxBytes<=0 → 默认 5MB
	_, err := SaveImage(newGinContext(req), "image", "avatar", 0)
	if err != nil {
		t.Fatalf("SaveImage err: %v", err)
	}
}
