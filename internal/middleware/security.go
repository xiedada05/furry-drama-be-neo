// Package middleware 提供 Gin 全局与路径级中间件，装配顺序见 internal/server/app.go。
package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/security"
)

// SanitizeInput 对齐 Express middlewares/security.js 的 sanitizeInput：
// 递归清洗请求 body（JSON）、query 与 params 中的字符串值；剥离以 $ 开头或含 . 的键
// （防 NoSQL 注入）；密码类字段值不做 XSS 转义。
//
// body 处理：读取 c.Request.Body → 以 json.Decoder.UseNumber 解析（保留数字字面量）
// → security.SanitizeValue 递归清洗 → 重新序列化并重建 c.Request.Body，保证 handler
// 经 ShouldBindJSON 读到的是清洗后的值。非 JSON 体（如 multipart 上传）原样保留。
// 依赖前置 body 解析中间件保留 c.Request.Body 可读（建议装配顺序：bodyparse → sanitize）。
//
// query 处理：重写 r.URL.RawQuery，使后续 c.Query()/c.Request.URL.Query() 读到清洗值；
// 剥离 $/. 前缀键。params 处理：就地改写 c.Params[i].Value。
func SanitizeInput() gin.HandlerFunc {
	return func(c *gin.Context) {
		sanitizeBody(c.Request)
		sanitizeQuery(c.Request)
		sanitizeParams(c)
		c.Next()
	}
}

// SanitizeHeaders 对齐 Express middlewares/security.js 的 sanitizeHeaders：
// 仅清洗可能回显到响应中的 referer 头（XSS 转义）。
// 刻意不碰 x-forwarded-for / x-real-ip：它们被 req.ip 与限流 keyGenerator 使用，
// 清洗会改变原始内容并干扰 IP 限流；其可信度由信任代理配置与反向代理覆盖策略负责。
func SanitizeHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		if v := c.Request.Header.Get("Referer"); v != "" {
			c.Request.Header.Set("Referer", security.Sanitize(v))
		}
		c.Next()
	}
}

// sanitizeBody 读取并清洗 JSON body，非 JSON 或不含 JSON 时原样恢复。
func sanitizeBody(r *http.Request) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		// 非 JSON body（如 multipart）→ 原样保留
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	// 对齐 JSON.parse 语义：首个 JSON 值之后还有非空白内容 → 视为非法 JSON，原样保留
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	out, err := json.Marshal(security.SanitizeValue(v))
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(out))
	r.ContentLength = int64(len(out))
}

// sanitizeQuery 清洗并重写 query 字符串；剥离 $/. 前缀键。
func sanitizeQuery(r *http.Request) {
	values := r.URL.Query()
	if len(values) == 0 {
		return
	}
	sanitized := url.Values{}
	for k, vs := range values {
		if strings.HasPrefix(k, "$") || strings.Contains(k, ".") {
			continue
		}
		for _, v := range vs {
			sanitized.Add(k, security.Sanitize(v))
		}
	}
	if enc := sanitized.Encode(); enc != r.URL.RawQuery {
		r.URL.RawQuery = enc
	}
}

// sanitizeParams 清洗路由参数值（键为路由定义的固定名，无需剥离）。
func sanitizeParams(c *gin.Context) {
	for i := range c.Params {
		c.Params[i].Value = security.Sanitize(c.Params[i].Value)
	}
}
