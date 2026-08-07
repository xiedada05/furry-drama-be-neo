package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCSRFProbe() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRF())
	r.POST("/probe", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	return r
}

func doCSRFRequest(t *testing.T, r *gin.Engine, cookieVal, headerVal string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	if cookieVal != "" {
		req.AddCookie(&http.Cookie{Name: "XSRF-TOKEN", Value: cookieVal})
	}
	if headerVal != "" {
		req.Header.Set("X-XSRF-TOKEN", headerVal)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// TestCSRFValid 合法：cookie 与 header 相等 → 放行。
func TestCSRFValid(t *testing.T) {
	code, body := doCSRFRequest(t, newCSRFProbe(), "tok123", "tok123")
	if code != 200 {
		t.Fatalf("valid request rejected: %d %v", code, body)
	}
}

// TestCSRFMismatch cookie 与 header 不等 → 403 mismatch。
func TestCSRFMismatch(t *testing.T) {
	code, body := doCSRFRequest(t, newCSRFProbe(), "tok1", "tok2")
	if code != 403 || body["message"] != "CSRF token mismatch" {
		t.Fatalf("mismatch: %d %v", code, body)
	}
}

// TestCSRFMissingHeader cookie 有 header 无 → 403 missing header。
func TestCSRFMissingHeader(t *testing.T) {
	code, body := doCSRFRequest(t, newCSRFProbe(), "tok1", "")
	if code != 403 || body["message"] != "CSRF protection: missing X-XSRF-TOKEN header" {
		t.Fatalf("missing header: %d %v", code, body)
	}
}

// TestCSRFMissingCookie 无 cookie → 403 missing cookie（即使 header 存在）。
func TestCSRFMissingCookie(t *testing.T) {
	code, body := doCSRFRequest(t, newCSRFProbe(), "", "tok1")
	if code != 403 || body["message"] != "CSRF protection: missing XSRF-TOKEN cookie, please refresh the page" {
		t.Fatalf("missing cookie: %d %v", code, body)
	}
}
