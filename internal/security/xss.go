// Package security 提供 HTML 输入清洗与密码强度校验，供 middleware.SanitizeInput /
// SanitizeHeaders 及密码相关 handler 使用。
//
// Sanitize 基于 github.com/microcosm-cc/bluemonday（Go 生态成熟的 HTML sanitizer）。
// 白名单标签与属性对齐 xss npm 库默认配置；与 xss npm 的差异是：bluemonday 对白名单外
// 标签【剥除】（保留文本内容），而 xss npm 是【转义】为 &lt;...&gt;——两者都防 XSS。
//
// SanitizeValue 递归清洗 JSON 值：剥离 $ 前缀/含 . 的键（防 NoSQL 注入）、密码字段跳过。
package security

import (
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// xssPolicy 是全局只读的清洗策略（bluemonday 并发安全）。
var xssPolicy = buildPolicy()

// xssTagNames 对齐 xss lib/default.js getDefaultWhiteList() 的标签白名单。
var xssTagNames = []string{
	"a", "abbr", "address", "area", "article", "aside", "audio", "b", "bdi", "bdo",
	"big", "blockquote", "br", "caption", "center", "cite", "code", "col", "colgroup",
	"dd", "del", "details", "div", "dl", "dt", "em", "figcaption", "figure", "font",
	"footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "i", "img", "ins",
	"kbd", "li", "mark", "nav", "ol", "p", "pre", "s", "section", "small", "span",
	"sub", "summary", "sup", "strong", "strike", "table", "tbody", "td", "tfoot", "th",
	"thead", "tr", "tt", "u", "ul", "video",
}

// buildPolicy 构造接近 xss npm 默认白名单的 bluemonday 策略。
func buildPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(xssTagNames...)
	// 全局允许的常见属性（对齐 xss 各标签白名单属性的并集）。
	p.AllowAttrs("title", "class", "id", "name", "value", "type", "placeholder",
		"width", "height", "colspan", "rowspan", "align", "valign", "loading",
		"controls", "loop", "muted", "autoplay", "playsinline", "crossorigin",
		"preload", "poster", "target", "rel", "dir", "open", "datetime", "shape",
		"coords", "span", "border", "cite", "color", "size", "face").Globally()
	p.AllowAttrs("href").OnElements("a", "area")
	p.AllowAttrs("src").OnElements("img", "audio", "video")
	p.AllowAttrs("alt").OnElements("img")
	// URL scheme 白名单（对齐 xss href/src 前缀白名单）。
	p.AllowURLSchemes("http", "https", "mailto", "tel", "ftp")
	p.AllowRelativeURLs(true)
	p.AllowDataURIImages()
	p.AllowStyling()
	return p
}

// skipFields 是不做 XSS 清洗（保留原值）的字段名，对齐 security.js sanitizeInput 的 skipFields。
var skipFields = map[string]bool{
	"password":        true,
	"newPassword":     true,
	"currentPassword": true,
	"confirmPassword": true,
	"oldPassword":     true,
}

// Sanitize 对输入应用 bluemonday 清洗（白名单外标签剥除、危险属性/URL 过滤）。
func Sanitize(s string) string {
	return xssPolicy.Sanitize(s)
}

// SanitizeValue 递归清洗 JSON 值，对齐 Express middlewares/security.js 的 sanitize()：
//   - string → Sanitize()
//   - map[string]any → 剥离以 $ 开头或含 . 的键（防 NoSQL 注入）；
//     密码类字段（skipFields）值为 string 时原样保留不转义，非 string 仍递归
//   - []any → 逐元素递归
//   - 其他类型（json.Number/bool/nil 等）原样返回
func SanitizeValue(v any) any {
	switch t := v.(type) {
	case string:
		return Sanitize(t)
	case []any:
		out := make([]any, 0, len(t))
		for _, item := range t {
			out = append(out, SanitizeValue(item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if strings.HasPrefix(k, "$") || strings.Contains(k, ".") {
				continue
			}
			if skipFields[k] {
				if s, ok := val.(string); ok {
					out[k] = s
					continue
				}
			}
			out[k] = SanitizeValue(val)
		}
		return out
	default:
		return v
	}
}

// ValidatePassword 校验密码强度，返回错误文案；合法则返回空串。
// 规则对齐 security.js validatePassword：长度（UTF-16 码元）≥8、含字母、含数字。
func ValidatePassword(password string) string {
	if password == "" || utf16Length(password) < 8 {
		return "密码长度至少8位"
	}
	hasLetter, hasDigit := false, false
	for _, r := range password {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			hasLetter = true
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter {
		return "密码必须包含至少一个字母"
	}
	if !hasDigit {
		return "密码必须包含至少一个数字"
	}
	return ""
}

// utf16Length 以 UTF-16 码元计数，对齐 JS 字符串 .length（代理对计 2）。
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
