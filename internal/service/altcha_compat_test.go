package service

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"

	"github.com/xiedada05/furry-drama-be-neo/internal/config"
)

const altchaTestSecret = "aaaabbbbccccddddeeeeffff00001111"

func altchaTestSvc() *AuthService {
	cfg := &config.Config{}
	cfg.JWT.AltchaHMACKey = altchaTestSecret
	return &AuthService{Config: cfg}
}

// TestAltchaOfficialRoundtrip 官方库自生成挑战 → 官方库求解 → 官方库验证。
// 另已手动跨生态验证：官方库生成挑战 → node altcha 求解器（前端 widget 同款
// sha.deriveKey）求解 → 官方库验证 verified=true（KDF 完全一致）。
func TestAltchaOfficialRoundtrip(t *testing.T) {
	svc := altchaTestSvc()
	ch, err := svc.CreateCaptcha()
	if err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	if ch.Parameters.Algorithm != "SHA-256" || ch.Parameters.Cost != 10000 ||
		ch.Parameters.KeyLength != 32 || ch.Parameters.KeyPrefix != "00" {
		t.Fatalf("challenge 参数不符: %+v", ch.Parameters)
	}
	sol, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{
		Challenge: ch, DeriveKey: altcha.DeriveKeySHA(),
	})
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	payload, _ := json.Marshal(altcha.Payload{Challenge: ch, Solution: *sol})
	payloadB64 := base64.StdEncoding.EncodeToString(payload)
	if !svc.VerifyAltcha(payloadB64, "") {
		t.Fatal("roundtrip 应 verified")
	}
}

// TestAltchaTamperedRejected 篡改/非法 payload 应被拒绝。
func TestAltchaTamperedRejected(t *testing.T) {
	svc := altchaTestSvc()
	if svc.VerifyAltcha("not-a-valid-payload", "") {
		t.Fatal("非法 payload 应被拒绝")
	}
	if svc.VerifyAltcha("", "") {
		t.Fatal("空 payload 应被拒绝")
	}
	// 篡改黄金 payload 的 solution.counter → 应拒绝。
	tampered := "eyJjaGFsbGVuZ2UiOnsicGFyYW1ldGVycyI6eyJhbGdvcml0aG0iOiJTSEEtMjU2IiwiY29zdCI6MTAwLCJrZXlMZW5ndGgiOjMyLCJrZXlQcmVmaXgiOiIwMCIsIm5vbmNlIjoiYWRxMWYxNGVjZGUzYmVhNmRjOGI0ZjA5OWNjZTk3NGUiLCJzYWx0IjoiMTllNTVkYzUyNWIyMDQxMmRmN2M3NmExYjhkN2RjZjQifX0sInNvbHV0aW9uIjp7ImNvdW50ZXIiOjEsImRlcml2ZWRLZXkiOiIwMDJlMmQ5OTU4MDBjMWQ2MjE5MDE0NmRiM2JmZTUxYmM4YTk2YTc4MGEzYmU0NjU5M2IyYTVjYjg3YTRlOTU0In19"
	if svc.VerifyAltcha(tampered, "") {
		t.Fatal("篡改的 payload 应被拒绝")
	}
}

// TestAltchaDevTokenBypass dev-token 绕过（仅配置时）。
func TestAltchaDevTokenBypass(t *testing.T) {
	cfg := &config.Config{}
	cfg.JWT.AltchaHMACKey = altchaTestSecret
	cfg.JWT.DevAPIToken = "test-dev-token"
	svc := &AuthService{Config: cfg}
	if !svc.VerifyAltcha("anything", "test-dev-token") {
		t.Fatal("dev-token 应绕过 altcha")
	}
	if svc.VerifyAltcha("anything", "wrong-token") {
		t.Fatal("错误 dev-token 不应绕过")
	}
}

// TestAltchaHMACSecretDerivation 验证 HMAC secret 派生（对齐 utils/altcha.js:8）。
func TestAltchaHMACSecretDerivation(t *testing.T) {
	cfg := &config.Config{}
	cfg.JWT.Secret = "my-jwt-secret"
	svc := &AuthService{Config: cfg}
	// sha256("altcha-" + "my-jwt-secret") 的 hex。
	expected := "bc0734564b835612beb83b2fb8ae10679b39f58941b6e94ee044173c35f9fd85"
	if got := svc.AltchaHMACSecret(); got != expected {
		t.Fatalf("HMAC secret derivation mismatch:\n got %s\nwant %s", got, expected)
	}
	// 显式配置优先。
	cfg.JWT.AltchaHMACKey = "explicit-key"
	if got := svc.AltchaHMACSecret(); got != "explicit-key" {
		t.Fatalf("explicit config should win: %s", got)
	}
}

// TestAltchaCreateCaptchaExpiry 挑战带 5min 过期。
func TestAltchaCreateCaptchaExpiry(t *testing.T) {
	svc := altchaTestSvc()
	ch, err := svc.CreateCaptcha()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now().Unix()
	if ch.Parameters.ExpiresAt <= now || ch.Parameters.ExpiresAt > now+600 {
		t.Fatalf("expiresAt 应在 5min 窗口内: %d (now=%d)", ch.Parameters.ExpiresAt, now)
	}
}
