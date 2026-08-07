package security

import (
	"encoding/json"
	"testing"
)

// TestSanitize 黄金语料：逐条对齐 xss npm 库（lib/xss.js + lib/default.js 默认配置）的实测输出。
func TestSanitize(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// 非白名单标签转义而非剥除
		{"script escaped", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"script with inner lt", "<script>if(a<b){x()}</script>", "&lt;script&gt;if(a&lt;b){x()}&lt;/script&gt;"},
		{"unknown tag", "<foo>bar</foo>", "&lt;foo&gt;bar&lt;/foo&gt;"},
		{"input tag", "<input disabled>", "&lt;input disabled&gt;"},
		{"form tag", `<form action="x">f</form>`, `&lt;form action="x"&gt;f&lt;/form&gt;`},
		{"iframe tag", `<iframe src="x">i</iframe>`, `&lt;iframe src="x"&gt;i&lt;/iframe&gt;`},
		{"svg tag", "<svg><script>alert(1)</script></svg>", "&lt;svg&gt;&lt;script&gt;alert(1)&lt;/script&gt;&lt;/svg&gt;"},
		{"textarea", "<textarea>tx</textarea>", "&lt;textarea&gt;tx&lt;/textarea&gt;"},
		{"select", "<select><option>o</option></select>", "&lt;select&gt;&lt;option&gt;o&lt;/option&gt;&lt;/select&gt;"},
		{"doctype", "<!DOCTYPE html><html></html>", "&lt;!DOCTYPE html&gt;&lt;html&gt;&lt;/html&gt;"},
		{"style tag", "<style>body{color:red}</style>", "&lt;style&gt;body{color:red}&lt;/style&gt;"},
		{"button tag", `<button onclick="x()">b</button>`, `&lt;button onclick="x()"&gt;b&lt;/button&gt;`},
		{"noscript", "<noscript>no</noscript>", "&lt;noscript&gt;no&lt;/noscript&gt;"},

		// 文本：仅转义 < 与 >，& 与引号不动
		{"plain lt gt", "plain text < and >", "plain text &lt; and &gt;"},
		{"amp untouched", "a & b < c", "a & b &lt; c"},
		{"entity untouched", "x &amp; y < z > w", "x &amp; y &lt; z &gt; w"},
		{"entity text", "&lt;b&gt;", "&lt;b&gt;"},
		{"lone lt", "<", "&lt;"},
		{"lone gt", ">", "&gt;"},
		{"empty", "", ""},

		// 白名单标签保留，非白名单属性丢弃
		{"onclick dropped", `<a onclick="alert(1)">x</a>`, "<a>x</a>"},
		{"class dropped", `<div class="a">x</div>`, "<div>x</div>"},
		{"img onerror dropped", `<img src="x" onerror="alert(1)">`, "<img src>"},
		{"mixed attrs", `hello <b onclick="x()">world</b> bye`, "hello <b>world</b> bye"},
		{"upper tag attr", `<a HREF="https://x.com" TARGET="_blank">x</A>`, `<a href="https://x.com" target="_blank">x</a>`},
		{"attr with eq in value", `<a data-x="a=b" href="https://x.com">z</a>`, `<a href="https://x.com">z</a>`},
		{"table", "<table><tr><td>cell</td></tr></table>", "<table><tr><td>cell</td></tr></table>"},
		{"p nested b", "<p><b>x</b></p>", "<p><b>x</b></p>"},
		{"b bold", "<b>bold</b>", "<b>bold</b>"},
		{"br selfclose", "<br/>", "<br />"},
		{"div selfclose", "<div/>", "<div />"},
		{"hr", "<hr>", "<hr>"},

		// href/src 前缀白名单
		{"href javascript", `<a href="javascript:alert(1)">x</a>`, "<a href>x</a>"},
		{"href javascript case", `<a href="JaVaScRiPt:alert(1)">x</a>`, "<a href>x</a>"},
		{"href javascript entity", `<a href="&#106;avascript:alert(1)">x</a>`, "<a href>x</a>"},
		{"href javascript hex entity", `<a href="jav&#x61;script:alert(1)">x</a>`, "<a href>x</a>"},
		{"href javascript colon entity", `<a href="javascript&colon;alert(1)">x</a>`, "<a href>x</a>"},
		{"href javascript newline", "<a href=\"ja\nvascript:alert(1)\">x</a>", "<a href>x</a>"},
		{"href javascript padded", `<a href="  javascript:alert(1)  ">t</a>`, "<a href>t</a>"},
		{"href empty", `<a href="">x</a>`, "<a href>x</a>"},
		{"href spaces", `<a href="  ">x</a>`, "<a href>x</a>"},
		{"href bare", "<a href>", "<a href>"},
		{"href single quote", `<a href='javascript:alert(1)'>x</a>`, "<a href>x</a>"},
		{"href unquoted", `<a href=javascript:alert(1)>x</a>`, "<a href>x</a>"},
		{"href unquoted safe", `<a href=https://example.com>y</a>`, `<a href="https://example.com">y</a>`},
		{"href unquoted multi", `<a href=https://x.com target=_blank>y</a>`, `<a href="https://x.com" target="_blank">y</a>`},
		{"href unquoted js", `<a href=javascript:alert(1) target=_blank>x</a>`, `<a href target="_blank">x</a>`},
		{"href https", `<a href="https://example.com">x</a>`, `<a href="https://example.com">x</a>`},
		{"href anchor", `<a href="#anchor">x</a>`, `<a href="#anchor">x</a>`},
		{"href hash only", `<a href="#">x</a>`, `<a href="#">x</a>`},
		{"href protocol relative", `<a href="//cdn.example.com/x">x</a>`, `<a href="//cdn.example.com/x">x</a>`},
		{"href ftp", `<a href="ftp://example.com">x</a>`, `<a href="ftp://example.com">x</a>`},
		{"href tel", `<a href="tel:+123456">call</a>`, `<a href="tel:+123456">call</a>`},
		{"href mailto", `<a href="mailto:a@b.com">m</a>`, `<a href="mailto:a@b.com">m</a>`},
		{"href data image", `<a href="data:image/png;base64,AAAA">x</a>`, `<a href="data:image/png;base64,AAAA">x</a>`},
		{"href relative x", `<a href="x">x</a>`, "<a href>x</a>"},
		{"href empty quote first", `<a href="" target="_blank">y</a>`, `<a href target="_blank">y</a>`},
		{"href mixed with title", `<a href="javascript:alert(1)" title="hi">x</a>`, `<a href title="hi">x</a>`},
		{"href double escape", `<a href="&quot;x">q</a>`, "<a href>q</a>"},
		{"href space after eq", `<a href = "https://x.com">y</a>`, `<a href="https://x.com">y</a>`},
		{"href tab sep", "<a\thref=\"https://x.com\">y</a>", `<a href="https://x.com">y</a>`},
		{"href newline sep", "<a\nhref=\"https://x.com\">y</a>", `<a href="https://x.com">y</a>`},
		{"href crlf sep", "<a\r\nhref=\"https://x.com\">y</a>", `<a href="https://x.com">y</a>`},
		{"href with query eq", `<a href="https://x.com?a=1&b=2">y</a>`, `<a href="https://x.com?a=1&b=2">y</a>`},
		{"attr order", `<a target=_blank href="https://x.com">y</a>`, `<a target="_blank" href="https://x.com">y</a>`},
		{"src javascript", `<img src="javascript:alert(1)">`, "<img src>"},
		{"src relative", `<img src="a/b/c.png">`, "<img src>"},
		{"src relative alt", `<img src="x.png" alt="pic">`, `<img src alt="pic">`},
		{"src data non-image", `<img src="data:text/html;base64,AAAA">`, "<img src>"},
		{"src data image", `<img src="data:image/png;base64,AAAA" alt="p">`, `<img src="data:image/png;base64,AAAA" alt="p">`},

		// style 属性：默认白名单无标签允许 style → 属性丢弃
		{"style dropped", `<div style="color:red;background:blue">x</div>`, "<div>x</div>"},
		{"style url javascript", `<div style="background:url(javascript:alert(1))">x</div>`, "<div>x</div>"},
		{"style expression", `<span style="width:expression(alert(1))">x</span>`, "<span>x</span>"},
		{"style position", `<div style="position:fixed">x</div>`, "<div>x</div>"},
		{"style no value", "<div style>x</div>", "<div>x</div>"},

		// 注释剥离
		{"comment stripped", "a<!-- comment -->b", "ab"},
		{"comment unclosed", "a<!-- b", "a"},

		// 未闭合标签
		{"unclosed tag", "<div>unclosed", "<div>unclosed"},

		// 白名单保留 span with width... (width not in span whitelist)
		{"spaced", "  <b>spaced</b>  ", "  <b>spaced</b>  "},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("%s: Sanitize(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestSanitizeValue 对齐 Express middlewares/security.js sanitize() 的递归清洗语料：
// 字符串转义、$/. 键剥离、密码字段保留原值。
func TestSanitizeValue(t *testing.T) {
	cases := []struct {
		name     string
		in, want any
	}{
		{"script string", map[string]any{"username": "<script>alert(1)</script>"},
			map[string]any{"username": "&lt;script&gt;alert(1)&lt;/script&gt;"}},
		{"nested gt key", map[string]any{"username": map[string]any{"$gt": ""}},
			map[string]any{"username": map[string]any{}}},
		{"top gt key", map[string]any{"$gt": ""}, map[string]any{}},
		{"password kept raw", map[string]any{"password": "<script>x</script>"},
			map[string]any{"password": "<script>x</script>"}},
		{"newPassword kept raw", map[string]any{"newPassword": "<b>p</b>", "username": "ok"},
			map[string]any{"newPassword": "<b>p</b>", "username": "ok"}},
		{"dot key stripped", map[string]any{"a.b": "x"}, map[string]any{}},
		{"nested strip and keep", map[string]any{"nested": map[string]any{"a.b": "y", "$ne": "z", "keep": "<i>v</i>"}},
			map[string]any{"nested": map[string]any{"keep": "<i>v</i>"}}},
		{"array elements", map[string]any{"arr": []any{"<script>", "plain", json.Number("42"), true, nil}},
			map[string]any{"arr": []any{"&lt;script&gt;", "plain", json.Number("42"), true, nil}}},
		{"bio href javascript", map[string]any{"bio": `<a href="javascript:alert(1)" onclick="x">hi</a>`},
			map[string]any{"bio": "<a href>hi</a>"}},
		{"nested password raw", map[string]any{"profile": map[string]any{"password": "<script>", "nickname": "<b>n</b>"}},
			map[string]any{"profile": map[string]any{"password": "<script>", "nickname": "<b>n</b>"}}},
		{"password object recursed", map[string]any{"password": map[string]any{"$gt": "x"}},
			map[string]any{"password": map[string]any{}}},
		{"numbers preserved", json.Number("1234567890123456789"), json.Number("1234567890123456789")},
	}
	for _, c := range cases {
		if got := SanitizeValue(c.in); !jsonEqual(t, got, c.want) {
			t.Errorf("%s: SanitizeValue(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestValidatePassword 密码强度文案与安全长度边界（含多字节字符按 UTF-16 计数）。
func TestValidatePassword(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc12345", ""},
		{"abc12345!", ""},
		{"AbC1DeFg2", ""},
		{"abc1234", "密码长度至少8位"},
		{"", "密码长度至少8位"},
		{"12345678", "密码必须包含至少一个字母"},
		{"abcdefgh", "密码必须包含至少一个数字"},
		{"abcdefg1", ""},
		{"密码长度12", "密码长度至少8位"}, // 4 中文字 + 2 数字 = 6 个 UTF-16 单元 <8 先报长度错误
	}
	for _, c := range cases {
		if got := ValidatePassword(c.in); got != c.want {
			t.Errorf("ValidatePassword(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCSSProcess 对齐 cssfilter FilterCSS.process 输出（含尾部分号）。
func TestCSSProcess(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"color:red;background:blue", "color:red; background:blue;"},
		{"color:red", "color:red;"},
		{"width:100px", "width:100px;"},
		{"position:fixed", ""},
		{"background-color:#fff;color:red", "background-color:#fff; color:red;"},
		{"color:red;", "color:red;"},
		{"", ""},
		{"width:expression(alert(1))", "width:expression(alert(1));"}, // expression 过滤在 xss safeAttrValue 层，不在 cssfilter 层
		{"background:url(javascript:alert(1))", ""},                   // cssfilter 层 safeAttrValue 拦截 javascript:
	}
	for _, c := range cases {
		if got := cssProcess(c.in); got != c.want {
			t.Errorf("cssProcess(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSafeAttrValue 对齐 xss safeAttrValue 行为（href 前缀、style 走 CSS 过滤）。
func TestSafeAttrValue(t *testing.T) {
	cases := []struct {
		name, tag, attr, in, want string
	}{
		{"href javascript", "a", "href", "javascript:alert(1)", ""},
		{"href https", "a", "href", "https://x.com", "https://x.com"},
		{"href hash", "a", "href", "#anchor", "#anchor"},
		{"href bare hash", "a", "href", "#", "#"},
		{"href dotdot", "a", "href", "../x", "../x"},
		{"href dot", "a", "href", "./x", "./x"},
		{"src http", "img", "src", "http://x/y.png", "http://x/y.png"},
		{"style whitelist", "div", "style", "color:red;background:blue", "color:red; background:blue;"},
		{"style javascript url", "div", "style", "background:url(javascript:alert(1))", ""},
		{"style expression", "div", "style", "width:expression(alert(1))", ""},
		{"title plain", "a", "title", "hi\"<", "hi&quot;&lt;"},
		{"background javascript", "body", "background", "url(javascript:x)", ""},
	}
	for _, c := range cases {
		if got := safeAttrValue(c.tag, c.attr, c.in); got != c.want {
			t.Errorf("%s: safeAttrValue(%q,%q,%q) = %q, want %q", c.name, c.tag, c.attr, c.in, got, c.want)
		}
	}
}

// TestFriendlyAttrValue 实体反序列化语料。
func TestFriendlyAttrValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`&quot;hi&quot;`, `"hi"`},
		{"&#106;avascript", "javascript"},
		{"jav&#x61;script", "javascript"},
		{"javascript&colon;alert", "javascript:alert"},
		{"a&newline;b", "a b"},
		{"a\tb", "a b"}, // 控制字符 → 空格并 trim
	}
	for _, c := range cases {
		if got := friendlyAttrValue(c.in); got != c.want {
			t.Errorf("friendlyAttrValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEscapeHTMLEntities 实体解码（十进制/十六进制/宽松前缀解析）。
func TestEscapeHTMLEntities(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"&#65;&#x42;", "AB"},
		{"&#65abc;", "A"},
		{"&#x41;", "A"},
		{"&lt;", "&lt;"}, // 非 &# 形式不动
		{"plain", "plain"},
	}
	for _, c := range cases {
		if got := escapeHTMLEntities(c.in); got != c.want {
			t.Errorf("escapeHTMLEntities(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// jsonEqual 用 JSON 序列化比较两个任意值（忽略 map 键顺序）。
func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	aj, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	bj, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	return string(aj) == string(bj)
}
