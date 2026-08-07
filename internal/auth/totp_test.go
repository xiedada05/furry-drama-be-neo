package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

// 黄金向量：由 Node 端按 helpers.js verifyTOTP 同一算法（HMAC-SHA1、30s 步长、
// 6 位码）对固定 secret/时间点生成。2026-08-08 node gen_vectors 脚本现场产出。
// secret = 20 字节（hex 0123456789abcdef0123456789abcdef01234567）的 base64。
const (
	nodeTOTPSecret = "ASNFZ4mrze8BI0VniavN7wEjRWc="
	// 固定 Unix 秒，避免时间依赖。
	nodeTOTPTime     int64 = 1770000000
	nodeTOTPCode           = "373590"
	nodeTOTPPrevCode       = "257980" // at t-30
	nodeTOTPNextCode       = "081867" // at t+30
	nodeTOTPFarCode        = "547288" // at t+60（窗口外，应拒绝）
)

// TestVerifyTOTPNodeVector 验证 Go 复算的码与 Node 完全一致。
func TestVerifyTOTPNodeVector(t *testing.T) {
	at := time.Unix(nodeTOTPTime, 0)
	if !VerifyTOTPAt(nodeTOTPSecret, nodeTOTPCode, at) {
		t.Fatalf("expected %q at time %d to verify", nodeTOTPCode, nodeTOTPTime)
	}
}

// TestVerifyTOTPWindow 验证 ±1 窗口内有效、窗口外拒绝。
func TestVerifyTOTPWindow(t *testing.T) {
	base := time.Unix(nodeTOTPTime, 0)
	cases := []struct {
		at   time.Time
		code string
		want bool
	}{
		{base, nodeTOTPPrevCode, true},                      // step -1（窗口内）
		{base, nodeTOTPCode, true},                          // step 0
		{base, nodeTOTPNextCode, true},                      // step +1（窗口内）
		{base, nodeTOTPFarCode, false},                      // step +2（窗口外，拒绝）
		{base.Add(60 * time.Second), nodeTOTPFarCode, true}, // step +2 现在恰好是当前步
		{base.Add(60 * time.Second), nodeTOTPCode, false},   // step 0 现已移出窗口
	}
	for _, c := range cases {
		if got := VerifyTOTPAt(nodeTOTPSecret, c.code, c.at); got != c.want {
			t.Fatalf("VerifyTOTPAt(%s @ %v) = %v, want %v", c.code, c.at, got, c.want)
		}
	}
}

// TestVerifyTOTPInvalidToken 验证非 6 位数字 token 一律拒绝（对齐 /^\d{6}$/）。
func TestVerifyTOTPInvalidToken(t *testing.T) {
	at := time.Unix(nodeTOTPTime, 0)
	for _, tok := range []string{"", "12345", "1234567", "12345a", "abc123", "373590 ", " 373590"} {
		if VerifyTOTPAt(nodeTOTPSecret, tok, at) {
			t.Fatalf("invalid token %q must be rejected", tok)
		}
	}
}

// TestVerifyTOTPInvalidSecret 验证非法 base64 secret 返回 false（不 panic）。
func TestVerifyTOTPInvalidSecret(t *testing.T) {
	at := time.Unix(nodeTOTPTime, 0)
	for _, secret := range []string{"", "!!!not-base64!!!", "ASNFZ4mrze8BI0VniavN7wEjR"} {
		if VerifyTOTPAt(secret, nodeTOTPCode, at) {
			t.Fatalf("invalid secret %q must be rejected", secret)
		}
	}
}

// TestGenerateTOTPSecretFormat 验证 secret 为标准 base64 且解码为 20 字节。
func TestGenerateTOTPSecretFormat(t *testing.T) {
	secret := GenerateTOTPSecret()
	raw, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		t.Fatalf("secret is not valid base64: %v", err)
	}
	if len(raw) != 20 {
		t.Fatalf("secret decoded length = %d, want 20", len(raw))
	}
	// 两次生成不应相同（20 字节熵）。
	if GenerateTOTPSecret() == secret {
		t.Fatal("two generated secrets must differ")
	}
}

// TestGenerateBackupCodes 验证 10 个 16 字符 hex 备用码。
func TestGenerateBackupCodes(t *testing.T) {
	codes := GenerateBackupCodes()
	if len(codes) != 10 {
		t.Fatalf("got %d backup codes, want 10", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 16 {
			t.Fatalf("code %q length = %d, want 16 (8 字节 hex)", c, len(c))
		}
		for _, r := range c {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				t.Fatalf("code %q contains non-hex char %q", c, r)
			}
		}
		if seen[c] {
			t.Fatalf("duplicate backup code %q", c)
		}
		seen[c] = true
	}
}

// TestVerifyTOTPNowSelfConsistent 验证 VerifyTOTP（time.Now 路径）接受
// 当前步长生成的码。期望码用与 Node 相同的公式独立计算（HMAC-SHA1、30s 步长、
// 6 位），具体算法正确性由上方黄金向量覆盖。
func TestVerifyTOTPNowSelfConsistent(t *testing.T) {
	secret := GenerateTOTPSecret()
	now := time.Now()
	code := computeTOTPNodeFormula(secret, now)
	if code == "" {
		t.Skip("cannot decode generated secret")
	}
	if !VerifyTOTP(secret, code) {
		t.Fatal("code generated for current step must verify via VerifyTOTP")
	}
}

// computeTOTPNodeFormula 复制 helpers.js verifyTOTP 的码生成公式（供自洽测试，
// 独立于包内实现）。
func computeTOTPNodeFormula(secret string, at time.Time) string {
	raw, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return ""
	}
	step := at.Unix() / TimeStep
	var timeBuf [8]byte
	for i := 0; i < 8; i++ {
		timeBuf[7-i] = byte(step >> (8 * i))
	}
	mac := hmac.New(sha1.New, raw)
	mac.Write(timeBuf[:])
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 0x0f
	code := (int(hash[offset]&0x7f) << 24) |
		(int(hash[offset+1]) << 16) |
		(int(hash[offset+2]) << 8) |
		int(hash[offset+3])
	return fmt.Sprintf("%06d", code%1000000)
}
