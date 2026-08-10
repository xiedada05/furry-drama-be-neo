package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
	"github.com/xiedada05/furry-drama-be-neo/internal/middleware"
	"github.com/xiedada05/furry-drama-be-neo/internal/ratelimit"
	"github.com/xiedada05/furry-drama-be-neo/internal/repository"
)

// 翻译域常量（对齐 routes/translate.js）。
const (
	translateCacheTTL        = 24 * time.Hour
	translateCacheMax        = 5000
	translateConcurrentLimit = 6
	maxSegmentLength         = 450
)

// translateHTTPClient 是所有外部翻译服务的共享客户端（10s 超时）。
var translateHTTPClient = &http.Client{Timeout: 10 * time.Second}

// cachedTranslation 是机器翻译内存缓存的条目（对齐 machineTranslationCache Map）。
type cachedTranslation struct {
	translation string
	timestamp   time.Time
}

// hardcodedTranslations 是硬编码翻译表（逐条照抄 translate.js:22-128 的 translations 对象）。
var hardcodedTranslations = map[string]map[string]string{
	"twoFactor.title":                {"en": "🔐 Two-Factor Authentication (2FA)"},
	"twoFactor.enableDesc":           {"en": "Enable 2FA to require a verification code in addition to your password when logging in, greatly improving account security."},
	"twoFactor.enable":               {"en": "Enable 2FA"},
	"twoFactor.setupStep1":           {"en": "1. Scan the QR code below with an authenticator app (e.g., Google Authenticator, Microsoft Authenticator) or enter the key manually:"},
	"twoFactor.manualKey":            {"en": "Manual entry key:"},
	"twoFactor.setupStep2":           {"en": "2. Save these backup codes carefully (each can only be used once). Use them to login when you cannot access your authenticator:"},
	"twoFactor.showBackupCodes":      {"en": "Show backup codes"},
	"twoFactor.setupStep3":           {"en": "3. Enter the 6-digit code from your authenticator app to confirm:"},
	"twoFactor.confirmEnable":        {"en": "Confirm Enable"},
	"twoFactor.enabled":              {"en": "✅ Two-Factor Authentication Enabled"},
	"twoFactor.disableDesc":          {"en": "Enter the 6-digit code from your authenticator app to disable 2FA:"},
	"twoFactor.disable":              {"en": "Disable 2FA"},
	"twoFactor.verifying":            {"en": "Verifying..."},
	"twoFactor.invalidCode":          {"en": "Invalid verification code"},
	"twoFactor.code":                 {"en": "Code"},
	"twoFactor.loginDesc":            {"en": "Enter the 6-digit code from your authenticator app to complete login."},
	"twoFactor.verify":               {"en": "Verify"},
	"twoFactor.enterCode":            {"en": "Enter 6-digit code"},
	"report.title":                   {"en": `Report "{targetName}"`},
	"report.reasonLabel":             {"en": "Reason *"},
	"report.selectReasonPlaceholder": {"en": "Select a reason"},
	"report.descriptionLabel":        {"en": "Additional details"},
	"report.descriptionPlaceholder":  {"en": "Please describe the situation..."},
	"report.submit":                  {"en": "Submit Report"},
	"report.submitted":               {"en": "Report submitted"},
	"report.willProcess":             {"en": "We will process it as soon as possible"},
	"report.selectReason":            {"en": "Please select a reason"},
	"report.submitFailed":            {"en": "Report failed"},
	"report.inappropriate":           {"en": "Inappropriate content"},
	"report.copyright":               {"en": "Copyright infringement"},
	"report.spam":                    {"en": "Spam"},
	"report.misleading":              {"en": "Misleading information"},
	"report.other":                   {"en": "Other"},
	"追番":                             {"en": "Following"},
	"追番列表":                           {"en": "My List"},
	"兽剧":                             {"en": "Furry Drama"},
	"兽剧聚合平台":                         {"en": "Furry Drama Hub"},
	"友链":                             {"en": "Friend Links"},
	"友链申请":                           {"en": "Friend Link Request"},
	"注销账号":                           {"en": "Delete Account"},
	"注销":                             {"en": "Delete"},
	"连载中":                            {"en": "Ongoing"},
	"已完结":                            {"en": "Completed"},
	"即将上映":                           {"en": "Upcoming"},
	"单集":                             {"en": "Episode"},
	"集":                              {"en": "Ep"},
	"第":                              {"en": "Ep "},
	"评分":                             {"en": "Rating"},
	"热度":                             {"en": "Views"},
	"分类":                             {"en": "Categories"},
	"标签":                             {"en": "Tags"},
	"创作者":                            {"en": "Creator"},
	"管理员":                            {"en": "Admin"},
	"超级管理员":                          {"en": "Super Admin"},
	"审核":                             {"en": "Review"},
	"待审核":                            {"en": "Pending Review"},
	"已通过":                            {"en": "Approved"},
	"已拒绝":                            {"en": "Rejected"},
	"举报":                             {"en": "Report"},
	"本网站部分内容由AI生成":                   {"en": "Some content on this site is generated by AI"},
	"欢迎来到兽剧聚合平台":                     {"en": "Welcome to Furry Drama Hub"},
	"发现和追踪你喜爱的兽剧内容": {"en": "Discover and track your favorite furry drama content"},
	"隐私政策":     {"en": "Privacy Policy"},
	"用户协议":     {"en": "User Agreement"},
	"关于我们":     {"en": "About Us"},
	"版权声明":     {"en": "Copyright Notice"},
	"联系我们":     {"en": "Contact Us"},
	"免责声明":     {"en": "Disclaimer"},
	"服务条款":     {"en": "Terms of Service"},
	"使用条款":     {"en": "Terms of Use"},
	"更新日志":     {"en": "Changelog"},
	"最新版本":     {"en": "Latest Version"},
	"账号注册":     {"en": "Account Registration"},
	"个人隐私信息保护": {"en": "Personal Privacy Protection"},
	"用户发布内容规范": {"en": "User Content Guidelines"},
	"使用规则":     {"en": "Usage Rules"},
	"您的权利":     {"en": "Your Rights"},
	"本政策如何更新":  {"en": "How This Policy Is Updated"},
	"如何联系我们":   {"en": "How to Contact Us"},
	"儿童个人信息保护": {"en": "Children's Privacy Protection"},
	"用户信息如何储存": {"en": "How User Information Is Stored"},
	"我们如何共享":   {"en": "How We Share"},
	"我们如何保护":   {"en": "How We Protect"},
	"我们如何使用":   {"en": "How We Use"},
	"本网站部分代码由生成式AI生成": {"en": "Some code on this website is generated by AI"},
	"请勿追踪":      {"en": "Do Not Track"},
	"网站信标和像素标签": {"en": "Web Beacons and Pixel Tags"},
	"征得授权同意的例外": {"en": "Exceptions to Consent"},
	"共享、转让、公开披露信息时事先征得授权同意的例外": {"en": "Exceptions to Prior Consent for Sharing, Transfer, and Public Disclosure"},
	"用户信息主体注销账户":               {"en": "Account Deletion"},
	"用户信息主体获取用户信息副本":           {"en": "Obtain a Copy of Your Information"},
	"约束信息系统自动决策":               {"en": "Restrict Automated Decision-Making"},
	"响应您的上述请求":                 {"en": "Responding to Your Requests"},
	"改变您授权同意的范围":               {"en": "Change the Scope of Your Consent"},
	"访问您的用户信息":                 {"en": "Access Your Information"},
	"更正您的用户信息":                 {"en": "Correct Your Information"},
	"删除您的用户信息":                 {"en": "Delete Your Information"},
	"开源许可":                     {"en": "Open Source License"},
	"前端项目":                     {"en": "Frontend Project"},
	"后端项目":                     {"en": "Backend Project"},
	"查看许可":                     {"en": "View License"},
	"GitHub项目":                 {"en": "GitHub Project"},
	"即将推出":                     {"en": "Coming Soon"},
	"上一页":                      {"en": "Previous"},
	"下一页":                      {"en": "Next"},
}

// englishStopWords 对齐 translate.js ENGLISH_STOP_WORDS（plausibility 英文单词判定用）。
var englishStopWords = map[string]bool{
	"the": true, "is": true, "a": true, "an": true, "of": true, "in": true, "to": true,
	"for": true, "and": true, "or": true, "with": true, "by": true, "on": true, "at": true,
	"from": true, "that": true, "this": true, "it": true, "are": true, "be": true, "has": true,
	"have": true, "will": true, "can": true, "may": true, "we": true, "you": true, "your": true,
	"our": true, "their": true, "not": true, "no": true, "all": true, "any": true, "each": true,
	"every": true, "which": true, "who": true, "what": true, "when": true, "where": true, "how": true,
	"do": true, "does": true, "did": true, "shall": true, "should": true, "would": true, "could": true,
	"must": true, "if": true, "then": true, "than": true, "so": true, "as": true, "but": true,
	"however": true, "therefore": true, "thus": true, "also": true, "only": true, "just": true,
	"more": true, "most": true, "such": true, "other": true, "some": true, "many": true, "much": true,
	"few": true, "less": true,
}

// bingScrape 相关正则（best-effort 对齐 bing-translate-api 的页面抓取）。
var (
	bingUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	bingIGRe      = regexp.MustCompile(`name="ig" content="([^"]+)"`)
	bingIIDRe     = regexp.MustCompile(`IID[:=]\s*"?([^"]*translator[^"]*)"?`)
	bingRichRe    = regexp.MustCompile(`params_RichTranslateHelper\s*=\s*\[([^\]]*)\]`)
	bingQuoteRe   = regexp.MustCompile(`"([^"]*)"`)
)

// batchNeed 是批量翻译中一个待翻译条目（保留原始值用于失败回退）。
type batchNeed struct {
	text  any
	index int
}

// batchRequest 是排队中的批量翻译任务。
type batchRequest struct {
	texts      []string
	targetLang string
	resolved   []any
	need       []batchNeed
	resultCh   chan []any
}

// Translate 是翻译域（/api/translate）handler 容器，行为逐端点对齐 backend/routes/translate.js。
type Translate struct {
	Repos  *repository.Repos
	Config *config.Config
	AuthMW *middleware.Auth
	RL     func(ratelimit.Spec) gin.HandlerFunc

	// azureKey/azureRegion 对齐 process.env.AZURE_TRANSLATOR_KEY / _REGION（构造时读取一次）。
	azureKey    string
	azureRegion string

	cacheMu sync.Mutex
	cache   map[string]cachedTranslation

	// batchCh 是串行批量翻译队列（对齐 translate.js 的 batchQueue + batchProcessing）。
	batchCh chan *batchRequest
}

// NewTranslate 构造翻译 handler 容器，并启动串行批量翻译处理器。
func NewTranslate(repos *repository.Repos, cfg *config.Config, amw *middleware.Auth,
	rl func(ratelimit.Spec) gin.HandlerFunc) *Translate {
	t := &Translate{
		Repos:       repos,
		Config:      cfg,
		AuthMW:      amw,
		RL:          rl,
		azureKey:    os.Getenv("AZURE_TRANSLATOR_KEY"),
		azureRegion: os.Getenv("AZURE_TRANSLATOR_REGION"),
		cache:       make(map[string]cachedTranslation),
		batchCh:     make(chan *batchRequest, 64),
	}
	go t.batchLoop()
	return t
}

// Register 挂载翻译路由（路径照抄 Express 子路径，不含 /api 前缀）。
// POST / 双注册 "" 与 "/" 覆盖 /api/translate 与 /api/translate/；均挂 translateLimiter。
func (h *Translate) Register(g *gin.RouterGroup) {
	g.POST("", h.RL(ratelimit.TranslateSpec), h.Translate)
	g.POST("/", h.RL(ratelimit.TranslateSpec), h.Translate)
	g.POST("/batch", h.RL(ratelimit.TranslateSpec), h.Batch)
}

// Translate POST /api/translate（translateLimiter，20s 超时 → 504）。
// @Summary 翻译文本（硬编码表 + 机器翻译）
// @Tags 翻译
// @Accept json
// @Param body body object true "{key, targetLang}"
// @Success 200 {object} map[string]any "translation（机器翻译失败为 null）"
// @Failure 400 {object} map[string]string "Missing key or targetLang / Unsupported language"
// @Failure 504 {object} map[string]string "Translation request timeout"
// @Router /translate [post]
func (h *Translate) Translate(c *gin.Context) {
	body := readBodyMap(c)
	keyVal, hasKey := body["key"]
	targetLang := asString(body["targetLang"])
	if !hasKey || translateJsFalsy(keyVal) || targetLang == "" {
		c.JSON(400, gin.H{"message": "Missing key or targetLang"})
		return
	}
	if !supportedTranslateLang(targetLang) {
		c.JSON(400, gin.H{"message": "Unsupported language"})
		return
	}
	if targetLang == "zh" {
		// 对齐 targetLang==='zh' 分支：原样返回 key（保留原始类型）。
		c.JSON(200, gin.H{"translation": keyVal})
		return
	}
	if s, isStr := keyVal.(string); isStr {
		if t, found := hardcodedTranslations[s][targetLang]; found && t != "" {
			c.JSON(200, gin.H{"translation": t})
			return
		}
	}

	key := jsStringOf(keyVal)
	resultCh := make(chan string, 1)
	go func() {
		resultCh <- h.getMachineTranslation(c.Request.Context(), key, "zh", targetLang)
	}()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case r := <-resultCh:
		var v any
		if r != "" {
			v = r
		}
		c.JSON(200, gin.H{"translation": v})
	case <-timer.C:
		c.JSON(504, gin.H{"message": "Translation request timeout"})
	case <-c.Request.Context().Done():
		return
	}
}

// Batch POST /api/translate/batch（translateLimiter，60s 超时 → 504，串行处理）。
// @Summary 批量翻译文本
// @Tags 翻译
// @Accept json
// @Param body body object true "{texts[], targetLang}"
// @Success 200 {object} map[string]any "translations"
// @Failure 400 {object} map[string]string "Missing texts or targetLang / Unsupported language"
// @Failure 504 {object} map[string]string "Translation request timeout"
// @Router /translate/batch [post]
func (h *Translate) Batch(c *gin.Context) {
	body := readBodyMap(c)
	textsVal, hasTexts := body["texts"]
	texts, isArray := textsVal.([]any)
	targetLang := asString(body["targetLang"])
	if !hasTexts || !isArray || texts == nil || targetLang == "" {
		c.JSON(400, gin.H{"message": "Missing texts or targetLang"})
		return
	}
	if !supportedTranslateLang(targetLang) {
		c.JSON(400, gin.H{"message": "Unsupported language"})
		return
	}
	if targetLang == "zh" {
		c.JSON(200, gin.H{"translations": texts})
		return
	}

	limited := texts
	if len(limited) > 200 {
		limited = limited[:200]
	}
	resolved := make([]any, len(limited))
	need := make([]batchNeed, 0)
	for i, raw := range limited {
		key := jsStringOf(raw)
		if t, found := hardcodedTranslations[key][targetLang]; found && t != "" {
			resolved[i] = t
		} else {
			need = append(need, batchNeed{text: raw, index: i})
		}
	}
	if len(need) == 0 {
		c.JSON(200, gin.H{"translations": resolved})
		return
	}
	textsToTranslate := make([]string, len(need))
	for i, n := range need {
		textsToTranslate[i] = jsStringOf(n.text)
	}
	item := &batchRequest{
		texts:      textsToTranslate,
		targetLang: targetLang,
		resolved:   resolved,
		need:       need,
		resultCh:   make(chan []any, 1),
	}
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	select {
	case h.batchCh <- item:
	case <-timer.C:
		c.JSON(504, gin.H{"message": "Translation request timeout"})
		return
	case <-c.Request.Context().Done():
		return
	}
	select {
	case results := <-item.resultCh:
		c.JSON(200, gin.H{"translations": results})
	case <-timer.C:
		c.JSON(504, gin.H{"message": "Translation request timeout"})
	case <-c.Request.Context().Done():
		return
	}
}

// batchLoop 串行处理批量翻译队列（对齐 processBatchQueue：一次只处理一个批次）。
func (h *Translate) batchLoop() {
	for item := range h.batchCh {
		machineResults := h.translateWithConcurrencyLimit(context.Background(), item.texts, "zh", item.targetLang)
		finalResults := make([]any, len(item.resolved))
		copy(finalResults, item.resolved)
		for i, r := range machineResults {
			idx := item.need[i].index
			if r != "" {
				finalResults[idx] = r
			} else {
				// 机器翻译失败回退原文（对齐 `result || needTranslation[i].text`）。
				finalResults[idx] = item.need[i].text
			}
		}
		item.resultCh <- finalResults
	}
}

// ---- 机器翻译管线（对齐 translate.js） ----

// getMachineTranslation 顶层机器翻译：缓存 → Azure → 长文本分段 → fallback。
func (h *Translate) getMachineTranslation(ctx context.Context, text, sourceLang, targetLang string) string {
	if sourceLang == targetLang {
		return text
	}
	if text == "" {
		return ""
	}
	cacheKey := sourceLang + ":" + targetLang + ":" + text
	if cached, ok := h.getCached(cacheKey); ok {
		return cached
	}
	if h.azureKey != "" {
		results := h.fetchAzureTranslation(ctx, []string{text}, sourceLang, targetLang)
		if results != nil && len(results) > 0 && results[0] != "" &&
			isPlausibleTranslation(text, targetLang, results[0]) {
			h.setCached(cacheKey, results[0])
			return results[0]
		}
	}
	segments := splitLongText(text)
	if len(segments) == 1 {
		return h.getMachineTranslationFallback(ctx, segments[0], sourceLang, targetLang)
	}
	var sb strings.Builder
	for _, seg := range segments {
		r := h.getMachineTranslationFallback(ctx, seg, sourceLang, targetLang)
		if r == "" {
			r = seg
		}
		sb.WriteString(r)
	}
	return sb.String()
}

// getMachineTranslationFallback 逐段翻译：缓存 → MyMemory → Google∥Bing 并行 → null。
func (h *Translate) getMachineTranslationFallback(ctx context.Context, text, sourceLang, targetLang string) string {
	if text == "" || jsUTF16Len(text) > 500 {
		return ""
	}
	cacheKey := sourceLang + ":" + targetLang + ":" + text
	if cached, ok := h.getCached(cacheKey); ok {
		return cached
	}
	tryResult := func(result string) string {
		if result != "" && isPlausibleTranslation(text, targetLang, result) {
			h.setCached(cacheKey, result)
			return result
		}
		return ""
	}
	if r := tryResult(h.fetchMyMemory(ctx, text, sourceLang, targetLang)); r != "" {
		return r
	}
	googleCh := make(chan string, 1)
	bingCh := make(chan string, 1)
	go func() { googleCh <- h.fetchGoogleTranslation(ctx, text, sourceLang, targetLang) }()
	go func() { bingCh <- h.fetchBingTranslation(ctx, text, sourceLang, targetLang) }()
	googleResult := <-googleCh
	bingResult := <-bingCh
	if r := tryResult(googleResult); r != "" {
		return r
	}
	if r := tryResult(bingResult); r != "" {
		return r
	}
	return ""
}

// translateWithConcurrencyLimit 批量翻译：Azure 可用时按 100 分批，否则 6 并发 worker 池。
func (h *Translate) translateWithConcurrencyLimit(ctx context.Context, texts []string, sourceLang, targetLang string) []string {
	if h.azureKey != "" {
		allResults := make([]string, 0, len(texts))
		for i := 0; i < len(texts); i += 100 {
			end := i + 100
			if end > len(texts) {
				end = len(texts)
			}
			batch := texts[i:end]
			if results := h.fetchAzureTranslation(ctx, batch, sourceLang, targetLang); results != nil {
				allResults = append(allResults, results...)
			} else {
				for _, t := range batch {
					allResults = append(allResults, h.getMachineTranslationFallback(ctx, t, sourceLang, targetLang))
				}
			}
		}
		return allResults
	}
	results := make([]string, len(texts))
	workers := len(texts)
	if workers > translateConcurrentLimit {
		workers = translateConcurrentLimit
	}
	queue := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range queue {
				results[idx] = h.getMachineTranslationFallback(ctx, texts[idx], sourceLang, targetLang)
			}
		}()
	}
	for i := range texts {
		queue <- i
	}
	close(queue)
	wg.Wait()
	return results
}

// ---- 翻译提供方（均失败返回空串 = null） ----

// fetchMyMemory 调 MyMemory API（对齐 translate.js fetchTranslation）。
func (h *Translate) fetchMyMemory(ctx context.Context, text, sourceLang, targetLang string) string {
	sl := translateLangMap(sourceLang)
	tl := translateLangMap(targetLang)
	u := "https://api.mymemory.translated.net/get?q=" + url.QueryEscape(text) +
		"&langpair=" + url.QueryEscape(sl) + "|" + url.QueryEscape(tl) +
		"&de=furrydrama2026@gmail.com"
	body, err := translateHTTPGet(ctx, u)
	if err != nil {
		return ""
	}
	var parsed struct {
		ResponseStatus  int    `json:"responseStatus"`
		ResponseDetails string `json:"responseDetails"`
		ResponseData    struct {
			TranslatedText string `json:"translatedText"`
			Match          string `json:"match"`
		} `json:"responseData"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.ResponseStatus == 429 || strings.Contains(parsed.ResponseDetails, "MYMEMORY WARNING") {
		return ""
	}
	translated := parsed.ResponseData.TranslatedText
	if translated == "" {
		return ""
	}
	matchQuality, _ := strconv.ParseFloat(parsed.ResponseData.Match, 64)
	if strings.EqualFold(translated, text) {
		return ""
	}
	if strings.HasPrefix(translated, "MYMEMORY") || strings.Contains(translated, "USAGE LIMITS") {
		return ""
	}
	if matchQuality < 0.5 && jsUTF16Len(text) > 10 {
		return ""
	}
	return translated
}

// fetchGoogleTranslation 调 Google 免费翻译接口（对齐 translate.js fetchGoogleTranslation）。
func (h *Translate) fetchGoogleTranslation(ctx context.Context, text, sourceLang, targetLang string) string {
	sl := sourceLang
	if sl == "zh" {
		sl = "zh-CN"
	}
	tl := targetLang
	if tl == "zh" {
		tl = "zh-CN"
	}
	u := "https://translate.googleapis.com/translate_a/single?client=gtx&sl=" +
		url.QueryEscape(sl) + "&tl=" + url.QueryEscape(tl) + "&dt=t&q=" + url.QueryEscape(text)
	body, err := translateHTTPGet(ctx, u)
	if err != nil {
		return ""
	}
	var parsed [][]any
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, item := range parsed[0] {
		seg, ok := item.([]any)
		if !ok || len(seg) == 0 {
			continue
		}
		if s, ok := seg[0].(string); ok && s != "" {
			sb.WriteString(s)
		}
	}
	translated := sb.String()
	if translated == "" || translated == text {
		return ""
	}
	return translated
}

// fetchBingTranslation 调 Bing 翻译（对齐 bing-translate-api 的 ttranslatev3，重试一次）。
func (h *Translate) fetchBingTranslation(ctx context.Context, text, sourceLang, targetLang string) string {
	sl := sourceLang
	if sl == "zh" {
		sl = "zh-Hans"
	}
	tl := targetLang
	if tl == "zh" {
		tl = "zh-Hans"
	}
	for attempt := 0; attempt < 2; attempt++ {
		if r := h.bingTranslateOnce(ctx, text, sl, tl); r != "" {
			return r
		}
	}
	return ""
}

// bingTranslateOnce 单次 Bing 翻译请求（8s 超时；任何环节失败返回空串）。
func (h *Translate) bingTranslateOnce(ctx context.Context, text, sl, tl string) string {
	bctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ig, iid, key, token := bingScrapeConfig(bctx)
	if key == "" || token == "" {
		return ""
	}
	if iid == "" {
		iid = "translator.5028.1"
	}
	form := url.Values{}
	form.Set("fromLang", "auto-detect")
	form.Set("text", text)
	form.Set("to", tl)
	form.Set("token", token)
	form.Set("key", key)
	req, err := http.NewRequestWithContext(bctx, http.MethodPost,
		"https://www.bing.com/ttranslatev3?isVertical=1&&IG="+url.QueryEscape(ig)+"&IID="+url.QueryEscape(iid),
		strings.NewReader(form.Encode()))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://www.bing.com/translator")
	req.Header.Set("User-Agent", bingUserAgent)
	resp, err := translateHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var parsed []struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed) == 0 {
		return ""
	}
	if len(parsed[0].Translations) == 0 {
		return ""
	}
	t := parsed[0].Translations[0].Text
	if t == "" || t == text {
		return ""
	}
	return t
}

// bingScrapeConfig 抓取 Bing 翻译页面的 IG/IID/key/token（best-effort，失败返回空串）。
func bingScrapeConfig(ctx context.Context) (ig, iid, key, token string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.bing.com/translator", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", bingUserAgent)
	resp, err := translateHTTPClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	html := string(data)
	if m := bingIGRe.FindStringSubmatch(html); len(m) > 1 {
		ig = m[1]
	}
	if m := bingIIDRe.FindStringSubmatch(html); len(m) > 1 {
		iid = m[1]
	}
	if m := bingRichRe.FindStringSubmatch(html); len(m) > 1 {
		quotes := bingQuoteRe.FindAllStringSubmatch(m[1], -1)
		if len(quotes) >= 1 {
			key = quotes[0][1]
		}
		if len(quotes) >= 2 {
			token = quotes[1][1]
		}
	}
	return
}

// fetchAzureTranslation 调 Azure Translator（对齐 translate.js fetchAzureTranslation）。
// AZURE_TRANSLATOR_KEY 未配置时返回 nil。
func (h *Translate) fetchAzureTranslation(ctx context.Context, texts []string, sourceLang, targetLang string) []string {
	if h.azureKey == "" {
		return nil
	}
	sl := translateLangMap(sourceLang)
	tl := translateLangMap(targetLang)
	payload := make([]map[string]string, len(texts))
	for i, t := range texts {
		payload[i] = map[string]string{"text": t}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	u := "https://api.cognitive.microsofttranslator.com/translate?api-version=3.0&from=" +
		url.QueryEscape(sl) + "&to=" + url.QueryEscape(tl)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Ocp-Apim-Subscription-Key", h.azureKey)
	region := h.azureRegion
	if region == "" {
		region = "global"
	}
	req.Header.Set("Ocp-Apim-Subscription-Region", region)
	resp, err := translateHTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var parsed []struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	out := make([]string, len(parsed))
	for i, item := range parsed {
		if len(item.Translations) > 0 {
			out[i] = item.Translations[0].Text
		}
	}
	return out
}

// ---- 判定与工具函数 ----

// translateLangMap 对齐 LANG_MAP = { zh:'zh-Hans', en:'en' }（未知语言原样返回）。
func translateLangMap(lang string) string {
	switch lang {
	case "zh":
		return "zh-Hans"
	case "en":
		return "en"
	}
	return lang
}

// supportedTranslateLang 判断目标语言是否受支持（对齐 !LANG_MAP[targetLang]）。
func supportedTranslateLang(targetLang string) bool {
	return targetLang == "zh" || targetLang == "en"
}

// translateJsFalsy 判断 JS 假值语义（nil/false/0/"" → true）。
func translateJsFalsy(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case string:
		return t == ""
	case float64:
		return t == 0
	case float32:
		return t == 0
	case int:
		return t == 0
	case int64:
		return t == 0
	case json.Number:
		n, err := t.Float64()
		return err != nil || n == 0
	}
	return false
}

// jsStringOf 对齐 JS String() 转换（机器翻译入参 stringify）。
func jsStringOf(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// isValidTranslation 对齐 translate.js isValidTranslation（过滤服务商告警文案）。
func isValidTranslation(text string) bool {
	if text == "" {
		return false
	}
	upper := strings.ToUpper(text)
	return !strings.Contains(upper, "MYMEMORY WARNING") &&
		!strings.Contains(upper, "USAGE LIMITS") &&
		!strings.Contains(upper, "VISIT HTTPS")
}

// isPlausibleTranslation 对齐 translate.js isPlausibleTranslation（targetLang=en 时
// 检查 CJK 占比与英文单词）。
func isPlausibleTranslation(sourceText, targetLang, result string) bool {
	if result == "" {
		return false
	}
	if !isValidTranslation(result) {
		return false
	}
	if targetLang == "en" {
		cjk := 0
		for _, r := range result {
			if isCJK(r) {
				cjk++
			}
		}
		if float64(cjk)/float64(jsUTF16Len(result)) > 0.3 {
			return false
		}
		if jsUTF16Len(sourceText) > 4 {
			hasEnglishWord := false
			for _, w := range strings.Fields(strings.ToLower(result)) {
				if englishStopWords[w] || (len(w) > 2 && isLowerAlpha(w)) {
					hasEnglishWord = true
					break
				}
			}
			if !hasEnglishWord {
				return false
			}
		}
	}
	return true
}

// isCJK 判断汉字字符（[一-鿿㐀-䶿]）。
func isCJK(r rune) bool {
	return (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3400 && r <= 0x4dbf)
}

// isLowerAlpha 判断是否全为小写英文字母（对齐 /^[a-z]+$/）。
func isLowerAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// splitLongText 对齐 translate.js splitLongText：按 450 长度与句读切分。
func splitLongText(text string) []string {
	runes := []rune(text)
	if len(runes) <= maxSegmentLength {
		return []string{text}
	}
	segments := make([]string, 0)
	remaining := runes
	for len(remaining) > 0 {
		if len(remaining) <= maxSegmentLength {
			segments = append(segments, string(remaining))
			break
		}
		splitPos := lastRuneIndex(remaining, '。', maxSegmentLength)
		if splitPos == -1 || splitPos < maxSegmentLength*3/10 {
			splitPos = lastRuneIndex(remaining, '，', maxSegmentLength)
		}
		if splitPos == -1 || splitPos < maxSegmentLength*3/10 {
			splitPos = lastRuneIndex(remaining, '.', maxSegmentLength)
		}
		if splitPos == -1 || splitPos < maxSegmentLength*3/10 {
			splitPos = lastRuneIndex(remaining, ' ', maxSegmentLength)
		}
		if splitPos == -1 || splitPos < maxSegmentLength*3/10 {
			splitPos = maxSegmentLength
		} else {
			splitPos++
		}
		segments = append(segments, string(remaining[:splitPos]))
		remaining = remaining[splitPos:]
	}
	return segments
}

// lastRuneIndex 对齐 JS String.lastIndexOf(target, fromIndex)（fromIndex 含）。
func lastRuneIndex(runes []rune, target rune, from int) int {
	if from >= len(runes) {
		from = len(runes) - 1
	}
	for i := from; i >= 0; i-- {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

// getCached 读取翻译缓存并懒惰清理过期条目（对齐 30 分钟 setInterval 清理语义）。
func (h *Translate) getCached(key string) (string, bool) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	now := time.Now()
	if len(h.cache) > translateCacheMax {
		for k, it := range h.cache {
			if now.Sub(it.timestamp) > translateCacheTTL {
				delete(h.cache, k)
			}
		}
	}
	c, ok := h.cache[key]
	if !ok {
		return "", false
	}
	if now.Sub(c.timestamp) > translateCacheTTL {
		delete(h.cache, key)
		return "", false
	}
	return c.translation, true
}

// setCached 写入翻译缓存，超限时淘汰一条（对齐 Express Map 插入前淘汰最旧）。
func (h *Translate) setCached(key, translation string) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if len(h.cache) >= translateCacheMax {
		for k := range h.cache {
			delete(h.cache, k)
			break
		}
	}
	h.cache[key] = cachedTranslation{translation: translation, timestamp: time.Now()}
}

// translateHTTPGet 发起 GET 请求并读取响应体。
func translateHTTPGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := translateHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
