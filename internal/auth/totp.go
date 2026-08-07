package auth

// TOTP 基于时间的一次性口令，自实现 RFC 6238，行为对齐 Express utils/helpers.js
// verifyTOTP / generateTOTPSecret / generateBackupCodes：
//   - HMAC-SHA1、30 秒步长、6 位数字码
//   - 验证窗口 ±1（允许时钟偏移一个步长）
//   - secret 为 RFC 4648 标准 base64（20 字节）
//   - 码比较用常量时间比较，避免计时攻击
//   - 备用码为 10 个 8 字节（64 位熵）hex 字符串（16 字符）

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	// TimeStep 是 TOTP 时间步长（秒），对齐 30s。
	TimeStep int64 = 30
	// TOTPWindow 是验证窗口（±1 步长）。
	TOTPWindow = 1
	// totpDigits 是验证码位数。
	totpDigits = 6
	// totpSecretLen 是 secret 随机字节数（20 字节 = 160 位，RFC 4226 建议 ≥ 128 位）。
	totpSecretLen = 20
)

// GenerateTOTPSecret 生成 2FA secret：20 随机字节的标准 base64 字符串，
// 对齐 utils/helpers.js generateTOTPSecret（crypto.randomBytes(20).toString('base64')）。
func GenerateTOTPSecret() string {
	buf := make([]byte, totpSecretLen)
	if _, err := rand.Read(buf); err != nil {
		// 随机源故障极低概率：回退时间戳派生，保证不 panic。
		buf = []byte(time.Now().Format("20060102150405.000000000Z07:00"))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// GenerateSecret 是 GenerateTOTPSecret 的别名（模块接口约定名，行为相同）。
func GenerateSecret() string {
	return GenerateTOTPSecret()
}

// GenerateBackupCodes 生成 10 个 2FA 备用码，每个为 8 字节（64 位熵）的
// hex 字符串（16 字符），对齐 utils/helpers.js generateBackupCodes
// （crypto.randomBytes(8).toString('hex')）。
func GenerateBackupCodes() []string {
	codes := make([]string, 10)
	for i := range codes {
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			buf = []byte(time.Now().Format("20060102150405.000000000"))
		}
		codes[i] = hex.EncodeToString(buf)
	}
	return codes
}

// VerifyTOTP 校验 6 位 TOTP 验证码，使用当前时间（30s 步长，窗口 ±1）。
// 返回 true 表示 token 在当前步长或其前后各一个步长内有效。
func VerifyTOTP(secret, token string) bool {
	return verifyTOTPAt(secret, token, time.Now())
}

// Verify 是 VerifyTOTP 的别名（模块接口约定名，行为相同）。
func Verify(secret, token string) bool {
	return VerifyTOTP(secret, token)
}

// VerifyTOTPAt 同 VerifyTOTP，但时间点可注入（便于测试与固定时间向量）。
func VerifyTOTPAt(secret, token string, at time.Time) bool {
	return verifyTOTPAt(secret, token, at)
}

// verifyTOTPAt 对齐 helpers.js verifyTOTP 的逐字段行为：
//   - token 非 6 位数字字符串 → false
//   - secret 按标准 base64 解码（解码失败 → false）
//   - 窗口 [-1, +1]，步长 30s，HMAC-SHA1 取 31 位整数模 1e6，补零到 6 位
//   - 常量时间比较
func verifyTOTPAt(secret, token string, at time.Time) bool {
	if !isSixDigit(token) {
		return false
	}
	secretBytes, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return false
	}
	currentStep := at.Unix() / TimeStep
	verified := false
	for i := -TOTPWindow; i <= TOTPWindow; i++ {
		step := currentStep + int64(i)
		var timeBuffer [8]byte
		binary.BigEndian.PutUint64(timeBuffer[:], uint64(step))
		mac := hmac.New(sha1.New, secretBytes)
		mac.Write(timeBuffer[:])
		hash := mac.Sum(nil)
		offset := hash[len(hash)-1] & 0x0f
		code := (int32(hash[offset]&0x7f) << 24) |
			(int32(hash[offset+1]) << 16) |
			(int32(hash[offset+2]) << 8) |
			int32(hash[offset+3])
		generated := fmt.Sprintf("%0*d", totpDigits, code%1000000)
		if subtle.ConstantTimeCompare([]byte(generated), []byte(token)) == 1 {
			verified = true
		}
	}
	return verified
}

// isSixDigit 校验 token 恰好为 6 位十进制数字（对齐 /^\d{6}$/）。
func isSixDigit(token string) bool {
	if len(token) != totpDigits {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
