// Package security 提供行为等价的 XSS 过滤原语（xss npm 库默认过滤器的 Go 移植）
// 与密码强度校验，供 middleware.SanitizeInput / SanitizeHeaders 及密码相关 handler 使用。
//
// Sanitize 逐行对齐 xss 库默认配置（xss() 无参数调用）：
//   - 非白名单标签【转义】为 &lt;...&gt; 而非剥除
//   - 文本仅转义 < 与 >（& 与引号原样保留）
//   - HTML 注释恒剥离（allowCommentTag 默认 false）
//   - 白名单标签仅保留白名单属性，href/src 值按前缀白名单校验，style 值走 CSS 过滤
//
// 注意：默认白名单中没有任何标签允许 style/background 属性，因此 style 属性在
// onIgnoreTagAttr 阶段即被丢弃（CSS 过滤分支 safeAttrValue 中的实现保留以对齐库行为）。
package security

import (
	"regexp"
	"strings"
	"unicode"
)

// tagWhiteList 对应 xss lib/default.js getDefaultWhiteList() 的标签白名单（小写）。
var tagWhiteList = map[string][]string{
	"a":          {"target", "href", "title"},
	"abbr":       {"title"},
	"address":    {},
	"area":       {"shape", "coords", "href", "alt"},
	"article":    {},
	"aside":      {},
	"audio":      {"autoplay", "controls", "crossorigin", "loop", "muted", "preload", "src"},
	"b":          {},
	"bdi":        {"dir"},
	"bdo":        {"dir"},
	"big":        {},
	"blockquote": {"cite"},
	"br":         {},
	"caption":    {},
	"center":     {},
	"cite":       {},
	"code":       {},
	"col":        {"align", "valign", "span", "width"},
	"colgroup":   {"align", "valign", "span", "width"},
	"dd":         {},
	"del":        {"datetime"},
	"details":    {"open"},
	"div":        {},
	"dl":         {},
	"dt":         {},
	"em":         {},
	"figcaption": {},
	"figure":     {},
	"font":       {"color", "size", "face"},
	"footer":     {},
	"h1":         {},
	"h2":         {},
	"h3":         {},
	"h4":         {},
	"h5":         {},
	"h6":         {},
	"header":     {},
	"hr":         {},
	"i":          {},
	"img":        {"src", "alt", "title", "width", "height", "loading"},
	"ins":        {"datetime"},
	"kbd":        {},
	"li":         {},
	"mark":       {},
	"nav":        {},
	"ol":         {},
	"p":          {},
	"pre":        {},
	"s":          {},
	"section":    {},
	"small":      {},
	"span":       {},
	"sub":        {},
	"summary":    {},
	"sup":        {},
	"strong":     {},
	"strike":     {},
	"table":      {"width", "border", "align", "valign"},
	"tbody":      {"align", "valign"},
	"td":         {"width", "rowspan", "colspan", "align", "valign"},
	"tfoot":      {"align", "valign"},
	"th":         {"width", "rowspan", "colspan", "align", "valign"},
	"thead":      {"align", "valign"},
	"tr":         {"rowspan", "align", "valign"},
	"tt":         {},
	"u":          {},
	"ul":         {},
	"video":      {"autoplay", "controls", "crossorigin", "loop", "muted", "playsinline", "poster", "preload", "src", "height", "width"},
}

// cssWhiteList 对应 cssfilter lib/default.js getDefaultWhiteList() 中值为 true 的属性
// （值为 false 的一律不允许，故此处只收录 true 项）。
var cssWhiteList = map[string]bool{
	"background":                  true,
	"background-attachment":       true,
	"background-clip":             true,
	"background-color":            true,
	"background-image":            true,
	"background-origin":           true,
	"background-position":         true,
	"background-repeat":           true,
	"background-size":             true,
	"border":                      true,
	"border-bottom":               true,
	"border-bottom-color":         true,
	"border-bottom-left-radius":   true,
	"border-bottom-right-radius":  true,
	"border-bottom-style":         true,
	"border-bottom-width":         true,
	"border-collapse":             true,
	"border-color":                true,
	"border-image":                true,
	"border-image-outset":         true,
	"border-image-repeat":         true,
	"border-image-slice":          true,
	"border-image-source":         true,
	"border-image-width":          true,
	"border-left":                 true,
	"border-left-color":           true,
	"border-left-style":           true,
	"border-left-width":           true,
	"border-radius":               true,
	"border-right":                true,
	"border-right-color":          true,
	"border-right-style":          true,
	"border-right-width":          true,
	"border-spacing":              true,
	"border-style":                true,
	"border-top":                  true,
	"border-top-color":            true,
	"border-top-left-radius":      true,
	"border-top-right-radius":     true,
	"border-top-style":            true,
	"border-top-width":            true,
	"border-width":                true,
	"box-decoration-break":        true,
	"box-shadow":                  true,
	"box-sizing":                  true,
	"box-snap":                    true,
	"box-suppress":                true,
	"break-after":                 true,
	"break-before":                true,
	"break-inside":                true,
	"clear":                       true,
	"color":                       true,
	"color-interpolation-filters": true,
	"display":                     true,
	"display-inside":              true,
	"display-list":                true,
	"display-outside":             true,
	"font":                        true,
	"font-family":                 true,
	"font-feature-settings":       true,
	"font-kerning":                true,
	"font-language-override":      true,
	"font-size":                   true,
	"font-size-adjust":            true,
	"font-stretch":                true,
	"font-style":                  true,
	"font-synthesis":              true,
	"font-variant":                true,
	"font-variant-alternates":     true,
	"font-variant-caps":           true,
	"font-variant-east-asian":     true,
	"font-variant-ligatures":      true,
	"font-variant-numeric":        true,
	"font-variant-position":       true,
	"font-weight":                 true,
	"height":                      true,
	"letter-spacing":              true,
	"lighting-color":              true,
	"list-style":                  true,
	"list-style-image":            true,
	"list-style-position":         true,
	"list-style-type":             true,
	"margin":                      true,
	"margin-bottom":               true,
	"margin-left":                 true,
	"margin-right":                true,
	"margin-top":                  true,
	"max-height":                  true,
	"max-width":                   true,
	"min-height":                  true,
	"min-width":                   true,
	"padding":                     true,
	"padding-bottom":              true,
	"padding-left":                true,
	"padding-right":               true,
	"padding-top":                 true,
	"text-align":                  true,
	"text-align-last":             true,
	"text-combine-upright":        true,
	"text-decoration":             true,
	"text-decoration-color":       true,
	"text-decoration-line":        true,
	"text-decoration-skip":        true,
	"text-decoration-style":       true,
	"text-emphasis":               true,
	"text-emphasis-color":         true,
	"text-emphasis-position":      true,
	"text-emphasis-style":         true,
	"text-height":                 true,
	"text-indent":                 true,
	"text-justify":                true,
	"text-orientation":            true,
	"text-overflow":               true,
	"text-shadow":                 true,
	"text-space-collapse":         true,
	"text-transform":              true,
	"text-underline-position":     true,
	"text-wrap":                   true,
	"width":                       true,
	"word-break":                  true,
	"word-spacing":                true,
	"word-wrap":                   true,
}

// 与 xss lib/default.js 一致的规则正则。
var (
	// 非法属性名中保留的字符：字母、数字、反斜杠、下划线、冒号、点、连字符
	reIllegalAttrName = regexp.MustCompile(`[^a-zA-Z0-9\\_:.\-]`)
	// REGEXP_DEFAULT_ON_TAG_ATTR_4：javascript: | vbscript: | livescript: | mocha:
	reJSVB       = regexp.MustCompile(`(?i)((j\s*a\s*v\s*a|v\s*b|l\s*i\s*v\s*e)\s*s\s*c\s*r\s*i\s*p\s*t\s*|m\s*o\s*c\s*h\s*a):`)
	reExpression = regexp.MustCompile(`(?i)e\s*x\s*p\s*r\s*e\s*s\s*s\s*i\s*o\s*n\s*\(.*`)
	reURL        = regexp.MustCompile(`(?i)u\s*r\s*l\s*\(.*`)
	// cssfilter 的 REGEXP_URL_JAVASCRIPT
	reCSSJS = regexp.MustCompile(`(?i)javascript\s*:`)
	// HTML5 危险实体：&colon; &newline;
	reColon   = regexp.MustCompile(`(?i)&colon;?`)
	reNewline = regexp.MustCompile(`(?i)&newline;?`)
	// 密码强度校验
	reLetter = regexp.MustCompile(`[A-Za-z]`)
	reDigit  = regexp.MustCompile(`[0-9]`)
)

// skipFields 是不做 XSS 转义（保留原值）的字段名，对齐 security.js sanitizeInput 的 skipFields。
var skipFields = map[string]bool{
	"password":        true,
	"newPassword":     true,
	"currentPassword": true,
	"confirmPassword": true,
	"oldPassword":     true,
}

// Sanitize 对输入应用 xss() 默认过滤器的 Go 移植。
//
// 对齐 xss lib/xss.js process() 与 lib/default.js 默认配置：非白名单标签整体转义、
// 文本仅转义 < 与 >、注释剥离、白名单标签属性白名单过滤、href/src 前缀白名单校验。
func Sanitize(s string) string {
	if s == "" {
		return ""
	}
	s = stripCommentTag(s)
	return parseTag(s, onTag)
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
// 规则对齐 security.js validatePassword：
//   - 长度（UTF-16 码元）< 8 → "密码长度至少8位"
//   - 不含 ASCII 字母 → "密码必须包含至少一个字母"
//   - 不含 ASCII 数字 → "密码必须包含至少一个数字"
func ValidatePassword(password string) string {
	if password == "" || utf16Length(password) < 8 {
		return "密码长度至少8位"
	}
	if !reLetter.MatchString(password) {
		return "密码必须包含至少一个字母"
	}
	if !reDigit.MatchString(password) {
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

// onTag 对应 xss.js process 中 parseTag 的 onTag 回调（默认 onTag/onIgnoreTag/onTagAttr/
// onIgnoreTagAttr 均无自定义实现）。
func onTag(tag, tagHTML string, isClosing bool) string {
	whiteAttrList, isWhite := tagWhiteList[tag]
	if !isWhite {
		return escapeHTML(tagHTML)
	}
	if isClosing {
		return "</" + tag + ">"
	}
	attrs, closing := getAttrs(tagHTML)
	attrsHTML := parseAttr(attrs, func(name, value string) string {
		if !containsString(whiteAttrList, name) {
			return "" // onIgnoreTagAttr 默认返回空 → 丢弃属性
		}
		v := safeAttrValue(tag, name, value)
		if v != "" {
			return name + `="` + v + `"`
		}
		return name
	})
	out := "<" + tag
	if attrsHTML != "" {
		out += " " + attrsHTML
	}
	if closing {
		out += " /"
	}
	out += ">"
	return out
}

// parseTag 对应 xss lib/parser.js parseTag 的字节级移植。
func parseTag(s string, onTag func(tag, tagHTML string, isClosing bool) string) string {
	var rethtml strings.Builder
	lastPos := 0
	tagStart := -1
	var quoteStart byte
	n := len(s)

	for currentPos := 0; currentPos < n; currentPos++ {
		c := s[currentPos]
		if tagStart == -1 {
			if c == '<' {
				tagStart = currentPos
			}
			continue
		}
		if quoteStart != 0 {
			if c == quoteStart {
				quoteStart = 0
			}
			continue
		}
		switch {
		case c == '<':
			rethtml.WriteString(escapeHTML(s[lastPos:currentPos]))
			tagStart = currentPos
			lastPos = currentPos
		case c == '>' || currentPos == n-1:
			rethtml.WriteString(escapeHTML(s[lastPos:tagStart]))
			currentHTML := s[tagStart : currentPos+1]
			rethtml.WriteString(onTag(getTagName(currentHTML), currentHTML, isClosing(currentHTML)))
			lastPos = currentPos + 1
			tagStart = -1
		case c == '"' || c == '\'':
			// 向后探测：若前导（允许空格/等号）是 '='，则该引号是属性值起始引号
			for i := 1; currentPos-i >= 0; i++ {
				ic := s[currentPos-i]
				if ic == '=' {
					quoteStart = c
					break
				}
				if !isSpaceByte(ic) {
					break
				}
			}
		}
	}
	if lastPos < n {
		rethtml.WriteString(escapeHTML(s[lastPos:]))
	}
	return rethtml.String()
}

// getTagName 对应 parser.js getTagName。
func getTagName(s string) string {
	i := spaceIndex(s)
	var name string
	if i == -1 {
		name = s[1 : len(s)-1]
	} else {
		name = s[1 : i+1]
	}
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	if strings.HasPrefix(name, "/") {
		name = name[1:]
	}
	if strings.HasSuffix(name, "/") {
		name = name[:len(name)-1]
	}
	return name
}

// isClosing 对应 parser.js isClosing。
func isClosing(s string) bool {
	return strings.HasPrefix(s, "</")
}

// getAttrs 对应 xss.js getAttrs。
func getAttrs(s string) (attrs string, closing bool) {
	i := spaceIndex(s)
	if i == -1 {
		return "", len(s) >= 2 && s[len(s)-2] == '/'
	}
	start := i + 1
	end := len(s) - 1
	if start <= end {
		attrs = strings.TrimSpace(s[start:end])
	}
	if len(attrs) > 0 && attrs[len(attrs)-1] == '/' {
		closing = true
		attrs = strings.TrimSpace(attrs[:len(attrs)-1])
	}
	return attrs, closing
}

// parseAttr 对应 parser.js parseAttr。
func parseAttr(s string, onAttr func(name, value string) string) string {
	lastPos := 0
	lastMarkPos := 0
	haveName := false
	tmpName := ""
	retAttrs := make([]string, 0, 4)
	n := len(s)

	addAttr := func(name, value string) {
		name = strings.TrimSpace(name)
		name = reIllegalAttrName.ReplaceAllString(name, "")
		name = strings.ToLower(name)
		if len(name) < 1 {
			return
		}
		if ret := onAttr(name, value); ret != "" {
			retAttrs = append(retAttrs, ret)
		}
	}

	for i := 0; i < n; i++ {
		c := s[i]
		if !haveName && c == '=' {
			tmpName = s[lastPos:i]
			haveName = true
			lastPos = i + 1
			if lastPos < n && (s[lastPos] == '"' || s[lastPos] == '\'') {
				lastMarkPos = lastPos
			} else {
				lastMarkPos = findNextQuotationMark(s, i+1)
			}
			continue
		}
		if haveName && i == lastMarkPos {
			j := indexOfChar(s, c, i+1)
			if j == -1 {
				break
			}
			v := strings.TrimSpace(s[lastMarkPos+1 : j])
			addAttr(tmpName, v)
			haveName = false
			i = j
			lastPos = i + 1
			continue
		}
		if isSpaceByte(c) {
			s = normalizeWhitespace(s)
			if !haveName {
				j := findNextEqual(s, i)
				if j == -1 {
					v := strings.TrimSpace(s[lastPos:i])
					addAttr(v, "")
					haveName = false
					lastPos = i + 1
					continue
				}
				i = j - 1
				continue
			}
			j := findBeforeEqual(s, i-1)
			if j == -1 {
				v := strings.TrimSpace(s[lastPos:i])
				v = stripQuoteWrap(v)
				addAttr(tmpName, v)
				haveName = false
				lastPos = i + 1
				continue
			}
			continue
		}
	}

	if lastPos < n {
		if !haveName {
			addAttr(s[lastPos:], "")
		} else {
			addAttr(tmpName, stripQuoteWrap(strings.TrimSpace(s[lastPos:])))
		}
	}
	return strings.TrimSpace(strings.Join(retAttrs, " "))
}

// findNextQuotationMark 对应 parser.js findNextQuotationMark（仅跳过空格）。
func findNextQuotationMark(s string, i int) int {
	for ; i < len(s); i++ {
		switch s[i] {
		case ' ':
			continue
		case '\'', '"':
			return i
		default:
			return -1
		}
	}
	return -1
}

// findNextEqual 对应 parser.js findNextEqual（仅跳过空格）。
func findNextEqual(s string, i int) int {
	for ; i < len(s); i++ {
		if s[i] == ' ' {
			continue
		}
		if s[i] == '=' {
			return i
		}
		return -1
	}
	return -1
}

// findBeforeEqual 对应 parser.js findBeforeEqual（仅跳过空格）。
func findBeforeEqual(s string, i int) int {
	for ; i > 0; i-- {
		if s[i] == ' ' {
			continue
		}
		if s[i] == '=' {
			return i
		}
		return -1
	}
	return -1
}

// stripQuoteWrap 对应 parser.js stripQuoteWrap。
func stripQuoteWrap(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// indexOfChar 查找 byte c 在 s 中从 from 起的首次出现（from<0 视为 0）。
func indexOfChar(s string, c byte, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// normalizeWhitespace 将每个空白字符替换为单个空格（长度不变），对应 JS 的
// html.replace(/\s|\n|\t/g, " ")。空白集采用 unicode.IsSpace 近似 JS \s。
func normalizeWhitespace(s string) string {
	hasWS := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			hasWS = true
			break
		}
	}
	if !hasWS {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// spaceIndex 返回首个空白字节（空格/\t/\n/\r/\v/\f）的下标，无则 -1。
func spaceIndex(s string) int {
	for i := 0; i < len(s); i++ {
		if isSpaceByte(s[i]) {
			return i
		}
	}
	return -1
}

// isSpaceByte 判断 ASCII 空白字节。
func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// containsString 判断切片是否含目标字符串。
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// stripCommentTag 对应 default.js stripCommentTag。
func stripCommentTag(s string) string {
	var out strings.Builder
	lastPos := 0
	for lastPos < len(s) {
		i := strings.Index(s[lastPos:], "<!--")
		if i == -1 {
			out.WriteString(s[lastPos:])
			break
		}
		abs := lastPos + i
		out.WriteString(s[lastPos:abs])
		j := strings.Index(s[abs:], "-->")
		if j == -1 {
			break
		}
		lastPos = abs + j + 3
	}
	return out.String()
}

// escapeHTML 对应 default.js escapeHtml：仅转义 < 与 >。
func escapeHTML(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "<", "&lt;"), ">", "&gt;")
}

// safeAttrValue 对应 default.js safeAttrValue：href/src 前缀白名单校验，
// background/style 的危险表达式过滤，style 走 CSS 过滤，最后转义 `"<`。
func safeAttrValue(tag, name, value string) string {
	value = friendlyAttrValue(value)
	switch name {
	case "href", "src":
		value = strings.TrimSpace(value)
		if value == "#" {
			return "#"
		}
		if !(hasPrefixAny(value,
			"http://", "https://", "mailto:", "tel:", "data:image/", "ftp://", "./", "../") ||
			(len(value) > 0 && (value[0] == '#' || value[0] == '/'))) {
			return ""
		}
	case "background":
		if reJSVB.MatchString(value) {
			return ""
		}
	case "style":
		if reExpression.MatchString(value) {
			return ""
		}
		if reURL.MatchString(value) && reJSVB.MatchString(value) {
			return ""
		}
		value = cssProcess(value)
	}
	return escapeAttrValue(value)
}

// friendlyAttrValue 对应 default.js friendlyAttrValue：属性值反实体化。
func friendlyAttrValue(s string) string {
	s = unescapeQuote(s)
	s = escapeHTMLEntities(s)
	s = escapeDangerHTML5Entities(s)
	s = clearNonPrintableCharacter(s)
	return s
}

// unescapeQuote 对应 default.js unescapeQuote：&quot; → "。
func unescapeQuote(s string) string {
	return strings.ReplaceAll(s, "&quot;", `"`)
}

// escapeHTMLEntities 对应 default.js escapeHtmlEntities：&#\d+; / &#x[0-9a-f]+;
// 解码为字符（parseInt 宽松解析，仅取前导合法数字；结果 & 0xFFFF 对齐 fromCharCode）。
func escapeHTMLEntities(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '&' && i+1 < len(s) && s[i+1] == '#' {
			j := i + 2
			for j < len(s) && isAlnumASCII(s[j]) {
				j++
			}
			code := s[i+2 : j]
			if len(code) > 0 && (code[0] == 'x' || code[0] == 'X') {
				out.WriteRune(jsParseIntRune(code[1:], 16))
			} else {
				out.WriteRune(jsParseIntRune(code, 10))
			}
			if j < len(s) && s[j] == ';' {
				j++
			}
			i = j
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// escapeDangerHTML5Entities 对应 default.js escapeDangerHtml5Entities：&colon → :、
// &newline → 空格。
func escapeDangerHTML5Entities(s string) string {
	s = reColon.ReplaceAllString(s, ":")
	s = reNewline.ReplaceAllString(s, " ")
	return s
}

// clearNonPrintableCharacter 对应 default.js clearNonPrintableCharacter：
// 码值 <32 的字符替换为空格并 trim。
func clearNonPrintableCharacter(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] < 32 {
			out.WriteByte(' ')
		} else {
			out.WriteByte(s[i])
		}
	}
	return strings.TrimSpace(out.String())
}

// escapeAttrValue 对应 default.js escapeAttrValue：转义 `"` 与 < >。
func escapeAttrValue(s string) string {
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return escapeHTML(s)
}

// jsParseIntRune 模拟 JS parseInt 的宽松解析（跳过前导空白、前缀合法数字），
// 结果 & 0xFFFF 对齐 String.fromCharCode。
func jsParseIntRune(s string, base int) rune {
	s = strings.TrimLeft(s, " \t\n\v\f\r   ")
	if s == "" {
		return 0
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		var d uint64
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			d = uint64(c - '0')
		case c >= 'a' && c <= 'z':
			d = uint64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			d = uint64(c-'A') + 10
		default:
			return maskToRune(v, neg)
		}
		if d >= uint64(base) {
			return maskToRune(v, neg)
		}
		v = v*uint64(base) + d
		if v > 1<<53 {
			v = 1 << 53 // 防 uint64 溢出；最终只取低 16 位
		}
	}
	return maskToRune(v, neg)
}

// maskToRune 将解析值取低 16 位（负数按补码），对齐 fromCharCode 的 ToUint16。
func maskToRune(v uint64, neg bool) rune {
	if neg {
		return rune(uint64(-int64(v)) & 0xFFFF)
	}
	return rune(v & 0xFFFF)
}

// isAlnumASCII 判断 ASCII 字母/数字。
func isAlnumASCII(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// hasPrefixAny 判断 s 是否以任一前缀开头。
func hasPrefixAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// cssProcess 对应 cssfilter FilterCSS.prototype.process + parser.js parseStyle：
// 仅保留白名单内且不含 javascript: 的 CSS 属性，输出 "name:value; " 连接后 trim。
func cssProcess(css string) string {
	if css == "" {
		return ""
	}
	css = strings.TrimRightFunc(css, unicode.IsSpace)
	if css[len(css)-1] != ';' {
		css += ";"
	}
	n := len(css)
	isParen := false
	lastPos := 0
	var out strings.Builder

	for i := 0; i < n; i++ {
		c := css[i]
		switch {
		case c == '/' && i+1 < n && css[i+1] == '*':
			j := strings.Index(css[i+2:], "*/")
			if j == -1 {
				i = n // 注释未闭合 → 结束解析
				break
			}
			// 注释结束处 A = j+i+2；对齐 JS 的 i=j+1;lastPos=i+1; 与循环 i++：
			// 循环 i++ 后 i 与 lastPos 都应等于 A+2
			i = j + i + 3
			lastPos = i + 1
			isParen = false
		case c == '(':
			isParen = true
		case c == ')':
			isParen = false
		case c == ';':
			if !isParen {
				cssAttr(&out, css, lastPos, i)
				lastPos = i + 1
			}
		case c == '\n':
			if !isParen {
				cssAttr(&out, css, lastPos, i)
			}
			lastPos = i + 1
		}
	}
	return strings.TrimSpace(out.String())
}

// cssAttr 解析一段 "name:value" 并决定是否保留（白名单 + 无 javascript: + 非空值）。
func cssAttr(out *strings.Builder, css string, lastPos, i int) {
	source := strings.TrimSpace(css[lastPos:i])
	j := strings.IndexByte(source, ':')
	if j == -1 {
		return
	}
	name := strings.TrimSpace(source[:j])
	value := strings.TrimSpace(source[j+1:])
	if name == "" || value == "" {
		return
	}
	if !cssWhiteList[name] {
		return
	}
	if reCSSJS.MatchString(value) {
		return
	}
	out.WriteString(name)
	out.WriteByte(':')
	out.WriteString(value)
	out.WriteString("; ")
}
