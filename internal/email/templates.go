package email

import (
	"context"
	"fmt"
	"time"
)

// EmailButton 生成邮件按钮（对齐 email.js emailButton）。
func EmailButton(text, url, variant string) string {
	styles := map[string]string{
		"primary":   "background-color:#6366f1;color:#ffffff;",
		"success":   "background-color:#10b981;color:#ffffff;",
		"danger":    "background-color:#ef4444;color:#ffffff;",
		"secondary": "background-color:#f1f5f9;color:#475569;border:1px solid #e2e8f0;",
	}
	s := styles[variant]
	if s == "" {
		s = styles["primary"]
	}
	return fmt.Sprintf(`<a href="%s" style="display:inline-block;padding:12px 28px;%stext-decoration:none;border-radius:10px;font-size:14px;font-weight:600;letter-spacing:0.01em;">%s</a>`, url, s, text)
}

// EmailInfoBox 生成信息框（对齐 email.js emailInfoBox）。
func EmailInfoBox(content, variant string) string {
	styles := map[string][3]string{
		"info":    {"#f0f4ff", "#6366f1", "#3730a3"},
		"warning": {"#fef3c7", "#f59e0b", "#92400e"},
		"danger":  {"#fee2e2", "#ef4444", "#991b1b"},
		"success": {"#d1fae5", "#10b981", "#065f46"},
	}
	s := styles[variant]
	if s[0] == "" {
		s = styles["info"]
	}
	return fmt.Sprintf(`<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:%s;border-left:4px solid %s;border-radius:8px;margin:16px 0;"><tr><td style="padding:14px 18px;color:%s;font-size:14px;line-height:1.6;">%s</td></tr></table>`, s[0], s[1], s[2], content)
}

// BuildEmailHTML 组装完整邮件 HTML（品牌栏 + 内容 + 页脚），对齐 email.js buildEmailHTML。
// 站点名/logo 优先取 SiteContent 'settings'，页脚 ICP/公安备案/版本号取 'about'（均带缓存）。
func (c *Client) BuildEmailHTML(ctx context.Context, siteName, siteURL, bodyContent, preheader string) string {
	settings := c.GetSiteSettings(ctx)
	displayName := settings.SiteName
	if displayName == "" {
		displayName = siteName
	}
	logoURL := settings.NavLogo
	if logoURL == "" {
		logoURL = siteURL + "/icon-192x192.png"
	}
	if len(logoURL) >= 4 && logoURL[:4] != "http" && len(logoURL) >= 5 && logoURL[:5] != "data:" {
		logoURL = siteURL + logoURL
	}
	about := c.GetSiteAbout(ctx)

	footer := ""
	if about.ICP != "" {
		footer += fmt.Sprintf(`<p style="margin:2px 0;color:#94a3b8;font-size:11px;text-align:center;"><a href="https://beian.miit.gov.cn/#/Integrated/index" target="_blank" rel="noopener noreferrer" style="color:#94a3b8;text-decoration:none;">%s</a></p>`, about.ICP)
	}
	if about.PoliceRecord != "" {
		footer += fmt.Sprintf(`<p style="margin:2px 0;color:#94a3b8;font-size:11px;text-align:center;"><a href="https://beian.mps.gov.cn/#/query/webSearch" target="_blank" rel="noopener noreferrer" style="color:#94a3b8;text-decoration:none;">%s</a></p>`, about.PoliceRecord)
	}
	versionPart := ""
	if about.Version != "" {
		versionPart = " · v" + about.Version
	}
	footer += `<p style="margin:2px 0;color:#94a3b8;font-size:11px;text-align:center;"><a href="https://github.com/Furry09shou/furry-drama-tracker" target="_blank" rel="noopener noreferrer" style="color:#94a3b8;text-decoration:none;">GitHub 开源项目</a></p>`
	footer += fmt.Sprintf(`<p style="margin:2px 0;color:#94a3b8;font-size:11px;text-align:center;"><span style="color:#94a3b8;">GPL v3.0 / AGPL v3.0 许可协议</span>%s</p>`, versionPart)

	preheaderHTML := ""
	if preheader != "" {
		preheaderHTML = `<div style="display:none;max-height:0;overflow:hidden;opacity:0;mso-hide:all;">` + preheader + `</div>`
	}
	year := time.Now().Year()
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="X-UA-Compatible" content="IE=edge">
  <meta name="color-scheme" content="light only">
  <meta name="supported-color-schemes" content="light only">
  <title>%s</title>
</head>
<body style="margin:0;padding:0;background-color:#f1f5f9;font-family:'Segoe UI','Microsoft YaHei','PingFang SC','Helvetica Neue',Arial,sans-serif;-webkit-font-smoothing:antialiased;line-height:1.6;">
  %s
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#f1f5f9;">
    <tr>
      <td align="center" style="padding:28px 16px;">
        <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%%;background-color:#ffffff;border-radius:16px;overflow:hidden;box-shadow:0 4px 24px rgba(99,102,241,0.10),0 1px 3px rgba(0,0,0,0.04);">
          <tr>
            <td style="background-color:#6366f1;padding:20px 32px;">
              <table role="presentation" width="100%%" cellpadding="0" cellspacing="0">
                <tr>
                  <td style="vertical-align:middle;">
                    <img src="%s" alt="%s" width="36" height="36" style="display:inline-block;vertical-align:middle;margin-right:12px;border-radius:9px;">
                    <span style="display:inline-block;vertical-align:middle;color:#ffffff;font-size:19px;font-weight:700;letter-spacing:-0.02em;">%s</span>
                  </td>
                </tr>
              </table>
            </td>
          </tr>
          <tr>
            <td style="padding:36px 32px 12px;">
              %s
            </td>
          </tr>
          <tr>
            <td style="padding:16px 32px 28px;background-color:#f8fafc;border-top:1px solid #e2e8f0;">
              <p style="margin:0 0 4px;color:#64748b;font-size:12px;text-align:center;">&copy; %d %s</p>
              <p style="margin:0;color:#94a3b8;font-size:11px;text-align:center;">此邮件由系统自动发送，请勿直接回复</p>
              %s
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`, displayName, preheaderHTML, logoURL, displayName, displayName, bodyContent, year, displayName, footer)
}
