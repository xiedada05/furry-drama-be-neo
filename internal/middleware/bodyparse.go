package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// bodyContextKey 是解析后请求体在 gin.Context 中的存储键。
const bodyContextKey = "bodyparse.body"

// 请求体大小上限（对齐 src/index.js:249-251）：
// 普通 JSON/urlencoded 1MB；/api/backup/import 与 /api/v1/backup/import 放宽到 50MB。
const (
	defaultBodyLimit = 1 << 20
	importBodyLimit  = 50 << 20
)

// backupImportPrefixes 是备份导入路径（精确或子路径命中则放大体限制）。
var backupImportPrefixes = []string{"/api/backup/import", "/api/v1/backup/import"}

// SetBody 把解析后的请求体存入上下文。供 W1 的 SanitizeInput 等后续中间件读取。
func SetBody(c *gin.Context, m map[string]any) {
	c.Set(bodyContextKey, m)
}

// GetBody 读取上下文中的解析后请求体；未解析或请求体不是对象时返回 nil。
// 对齐 Express 的 req.body：未解析时字段读取得到 undefined / 零值。
func GetBody(c *gin.Context) map[string]any {
	v, ok := c.Get(bodyContextKey)
	if !ok {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// BodyParse 解析 JSON / urlencoded 请求体（对齐 express.json + express.urlencoded）：
//   - Content-Type 为 application/json 或 application/*+json → JSON 解析；
//   - Content-Type 为 application/x-www-form-urlencoded → 表单解析（支持 a[b]=c 嵌套）；
//   - 其他 Content-Type 不解析（对齐 body-parser 的 content-type 匹配）。
//
// 大小上限 1MB，/api/backup/import 与 /api/v1/backup/import（含子路径）放宽到 50MB
// （仅 JSON；urlencoded 保持 1MB，对齐 Express 只给备份导入挂了 50mb json 解析器）。
// 超限 → 413 {"message":"request entity too large"}；JSON 语法错误 → 400。
// 解析结果经 SetBody 存入上下文，供 GetBody / 限流 key 使用；同时把原始体还原到
// c.Request.Body，保证后续 SanitizeInput（W1）与 handler 的 ShouldBindJSON 可读。
func BodyParse() gin.HandlerFunc {
	return func(c *gin.Context) {
		ct := c.ContentType()
		isJSON := ct == "application/json" || strings.HasSuffix(ct, "+json")
		isForm := ct == "application/x-www-form-urlencoded"
		if !isJSON && !isForm {
			c.Next()
			return
		}

		limit := int64(defaultBodyLimit)
		if isJSON && onImportPath(c.Request.URL.Path) {
			limit = importBodyLimit
		}

		data, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": "读取请求体失败: " + err.Error()})
			return
		}
		if int64(len(data)) > limit {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"message": "request entity too large"})
			return
		}
		// 还原 Body 供后续中间件/handler 读取（对齐 body-parser 只消费流但 Express
		// req.body 对象化；neo-server 侧 W1 的 SanitizeInput 与 handler 的
		// ShouldBindJSON 需要原始流可读，因此读完后替换回 NopCloser）。
		c.Request.Body = io.NopCloser(bytes.NewReader(data))

		if isJSON {
			// 空体 → req.body = {}（对齐 Express：express.json 对空体不报错，
			// 实测返回 {}）。
			if len(data) == 0 {
				SetBody(c, map[string]any{})
				c.Next()
				return
			}
			var v any
			if err := json.Unmarshal(data, &v); err != nil {
				// body-parser 的 SyntaxError 文案是 JS 风格（"Unexpected token ..."），
				// Go encoding/json 文案不同，此处用 Go 错误原文，状态码同为 400。
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": err.Error()})
				return
			}
			// 标量/数组体 → 不存（等价 req.body 非对象时字段读取为 undefined）。
			if m, ok := v.(map[string]any); ok {
				SetBody(c, m)
			}
		} else {
			values, err := url.ParseQuery(string(data))
			if err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"message": err.Error()})
				return
			}
			m := make(map[string]any, len(values))
			for k, vs := range values {
				v := ""
				if len(vs) > 0 {
					v = vs[len(vs)-1]
				}
				parseBracketPath(m, k, v)
			}
			SetBody(c, m)
		}
		c.Next()
	}
}

// onImportPath 判断路径是否命中备份导入前缀（精确或子路径）。
func onImportPath(path string) bool {
	for _, p := range backupImportPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// parseBracketPath 递归把 "a[b][c]=v" 形式的表单键写入嵌套 map
// （对齐 qs extended 的常见用法；简单键直接写入，重复键以最后一个为准）。
func parseBracketPath(m map[string]any, key, value string) {
	idx := strings.IndexByte(key, '[')
	if idx < 0 {
		m[key] = value
		return
	}
	root := key[:idx]
	rest := key[idx:] // 形如 "[b][c]"
	child, ok := m[root].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[root] = child
	}
	// 去掉首尾括号后按 "]" 切分得到各段。
	inner := strings.Trim(rest, "[]")
	if inner == "" {
		child[""] = value // "a[]" 追加模式：本项目表单无数组场景，简化为覆盖。
		return
	}
	segs := strings.Split(inner, "][")
	for i, seg := range segs {
		if i == len(segs)-1 {
			child[seg] = value
			return
		}
		next, ok := child[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			child[seg] = next
		}
		child = next
	}
}
