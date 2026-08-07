package auth

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/xiedada05/furry-drama-be-neo/internal/model"
)

// ParsedUA 是设备信息解析结果（对齐 helpers.js parseUserAgent）。
type ParsedUA struct {
	Browser        string
	BrowserVersion string
	OS             string
	OSVersion      string
	DeviceType     string
	DeviceModel    string
}

// 正则（对齐 helpers.js parseUserAgent）。
var (
	reMobile       = regexp.MustCompile(`(?i)Mobile|Android|iPhone`)
	reTablet       = regexp.MustCompile(`(?i)iPad|Tablet`)
	reEdge         = regexp.MustCompile(`(?i)Edg/(\d+)`)
	reChrome       = regexp.MustCompile(`(?i)Chrome/(\d+)`)
	reHasEdge      = regexp.MustCompile(`(?i)Edg`)
	reFirefox      = regexp.MustCompile(`(?i)Firefox/(\d+)`)
	reSafari       = regexp.MustCompile(`(?i)Safari/(\d+)`)
	reHasChrome    = regexp.MustCompile(`(?i)Chrome`)
	reIE           = regexp.MustCompile(`(?i)MSIE|Trident`)
	reWinNT        = regexp.MustCompile(`(?i)Windows NT (\d+\.\d+)`)
	reMacOS        = regexp.MustCompile(`(?i)Mac OS X (\d+[._\d]*)`)
	reAndroid      = regexp.MustCompile(`(?i)Android (\d+\.?\d*)`)
	reIPhoneOS     = regexp.MustCompile(`(?i)iPhone OS (\d+[_\d]*)`)
	reIPadOS       = regexp.MustCompile(`(?i)(?:CPU|iPhone) OS (\d+[_\d]*)`)
	reLinux        = regexp.MustCompile(`(?i)Linux`)
	reVersion      = regexp.MustCompile(`(?i)Version/(\d+)(?:\.(\d+))?`)
	reVersionFloat = regexp.MustCompile(`(?i)Version/(\d+[\.\d]*)`)
	reNonWebKit    = regexp.MustCompile(`(?i)Chrome|Firefox|Edg|OPR`)
	reBrowserKit   = regexp.MustCompile(`(?i)CriOS|FxiOS|EdgiOS|OPiOS`)
	reParen        = regexp.MustCompile(`\(([^)]+)\)`)
	reModelSkip    = regexp.MustCompile(`(?i)^(Mozilla|Compatible|Windows|Mac|Linux|Android|iPhone|iPad)`)
)

// ParseUserAgent 解析 UA 字符串（对齐 helpers.js parseUserAgent）。
func ParseUserAgent(ua string) ParsedUA {
	r := ParsedUA{DeviceType: "desktop"}
	if ua == "" {
		return r
	}
	if reMobile.MatchString(ua) {
		r.DeviceType = "mobile"
	}
	if reTablet.MatchString(ua) {
		r.DeviceType = "tablet"
	}

	if m := reEdge.FindStringSubmatch(ua); m != nil {
		r.Browser = "Edge"
		r.BrowserVersion = m[1]
	} else if m := reChrome.FindStringSubmatch(ua); m != nil && !reHasEdge.MatchString(ua) {
		r.Browser = "Chrome"
		r.BrowserVersion = m[1]
	} else if m := reFirefox.FindStringSubmatch(ua); m != nil {
		r.Browser = "Firefox"
		r.BrowserVersion = m[1]
	} else if reSafari.MatchString(ua) && !reHasChrome.MatchString(ua) {
		r.Browser = "Safari"
		if m := reVersion.FindStringSubmatch(ua); m != nil {
			r.BrowserVersion = m[1]
		}
	} else if reIE.MatchString(ua) {
		r.Browser = "IE"
	}

	switch {
	case reWinNT.MatchString(ua):
		r.OS = "Windows"
		r.OSVersion = reWinNT.FindStringSubmatch(ua)[1]
	case reMacOS.MatchString(ua):
		r.OS = "macOS"
		r.OSVersion = strings.ReplaceAll(reMacOS.FindStringSubmatch(ua)[1], "_", ".")
		if strings.HasPrefix(r.OSVersion, "10.15") && !reNonWebKit.MatchString(ua) {
			if m := reVersion.FindStringSubmatch(ua); m != nil {
				safariMajor, _ := strconv.Atoi(m[1])
				safariMinor := m[2]
				if safariMinor == "" {
					safariMinor = "0"
				}
				if safariMajor >= 26 {
					if safariMinor != "0" {
						r.OSVersion = m[1] + "." + safariMinor
					} else {
						r.OSVersion = m[1]
					}
				} else if safariMajor >= 14 && safariMajor <= 18 {
					r.OSVersion = strconv.Itoa(safariMajor-3) + "." + safariMinor
				}
			}
		}
	case reAndroid.MatchString(ua):
		r.OS = "Android"
		r.OSVersion = reAndroid.FindStringSubmatch(ua)[1]
	case reIPhoneOS.MatchString(ua):
		r.OS = "iOS"
		r.OSVersion = strings.ReplaceAll(reIPhoneOS.FindStringSubmatch(ua)[1], "_", ".")
		if !reBrowserKit.MatchString(ua) {
			if m := reVersionFloat.FindStringSubmatch(ua); m != nil {
				if major, err := strconv.Atoi(strings.Split(m[1], ".")[0]); err == nil && major >= 26 {
					r.OSVersion = m[1]
				}
			}
		}
	case strings.Contains(ua, "iPad") && reIPadOS.MatchString(ua):
		r.OS = "iPadOS"
		r.OSVersion = strings.ReplaceAll(reIPadOS.FindStringSubmatch(ua)[1], "_", ".")
		if !reBrowserKit.MatchString(ua) {
			if m := reVersionFloat.FindStringSubmatch(ua); m != nil {
				if major, err := strconv.Atoi(strings.Split(m[1], ".")[0]); err == nil && major >= 26 {
					r.OSVersion = m[1]
				}
			}
		}
	case reLinux.MatchString(ua):
		r.OS = "Linux"
	}

	if m := reParen.FindStringSubmatch(ua); m != nil && reMobile.MatchString(ua) {
		parts := strings.Split(m[1], ";")
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" && !reModelSkip.MatchString(last) {
			r.DeviceModel = last
		}
	}
	return r
}

// BuildDeviceInfo 合并客户端上报 deviceInfo、UA 解析结果与 accept-language。
// 对齐 helpers.js buildDeviceInfo。
func BuildDeviceInfo(deviceInfo *DeviceInfoPayload, parsed ParsedUA, ua, acceptLang string) model.DeviceInfo {
	di := model.DeviceInfo{}
	if deviceInfo != nil {
		di.Browser = deviceInfo.Browser
		di.BrowserVersion = deviceInfo.BrowserVersion
		di.OS = deviceInfo.OS
		di.OSVersion = deviceInfo.OSVersion
		di.DeviceType = deviceInfo.DeviceType
		di.DeviceModel = deviceInfo.DeviceModel
		di.ScreenWidth = deviceInfo.ScreenWidth
		di.ScreenHeight = deviceInfo.ScreenHeight
		di.Language = deviceInfo.Language
		di.Carrier = deviceInfo.Carrier
	}
	if di.Browser == "" {
		di.Browser = parsed.Browser
	}
	if di.BrowserVersion == "" {
		di.BrowserVersion = parsed.BrowserVersion
	}
	if di.OS == "" {
		di.OS = parsed.OS
	}
	if di.OSVersion == "" {
		di.OSVersion = parsed.OSVersion
	}
	if di.DeviceType == "" {
		di.DeviceType = parsed.DeviceType
	}
	if di.DeviceModel == "" {
		di.DeviceModel = parsed.DeviceModel
	}
	if di.Language == "" && acceptLang != "" {
		di.Language = acceptLang
	}
	di.UserAgent = ua
	return di
}

// DeviceInfoPayload 是客户端上报的设备信息（handler 解析 JSON body 用）。
type DeviceInfoPayload struct {
	Browser        string `json:"browser"`
	BrowserVersion string `json:"browserVersion"`
	OS             string `json:"os"`
	OSVersion      string `json:"osVersion"`
	DeviceType     string `json:"deviceType"`
	DeviceModel    string `json:"deviceModel"`
	ScreenWidth    int    `json:"screenWidth"`
	ScreenHeight   int    `json:"screenHeight"`
	Language       string `json:"language"`
	Carrier        string `json:"carrier"`
}
