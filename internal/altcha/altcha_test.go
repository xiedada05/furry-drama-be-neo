package altcha

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 黄金向量：由 Node 端 altcha lib（backend/node_modules/altcha/dist/lib/index.umd.cjs
// v3.1.0）的 createChallenge + solveChallenge 现场生成（2026-08-08，node gen_vectors
// 脚本）。HMAC 密钥取 hex 字符串的 UTF-8 编码字节（与 lib hmac() 的
// TextEncoder().encode(keyStr) 一致）。expiresAt 固定为 2035-01-01，避免向量过期。
const (
	nodeAltchaHMACKey = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	nodeAltchaSig     = "1ac88ca096d0682dcfa25dad2e36b36806766bb769b38923e00b2b2f13afe775"
	nodeAltchaCounter = 80
	nodeAltchaDerived = "006817d719728d36edb29e7b0eecd98cc3542fd42cef4d7b715e5f69ee855a76"
	// node 生成的完整 payload（base64），含 challenge+signature+solution。
	nodeAltchaPayload = "eyJjaGFsbGVuZ2UiOnsicGFyYW1ldGVycyI6eyJhbGdvcml0aG0iOiJTSEEtMjU2IiwiY29zdCI6MTAwMDAsImV4cGlyZXNBdCI6MjA1MTIyMjQwMCwia2V5TGVuZ3RoIjozMiwia2V5UHJlZml4IjoiMDAiLCJub25jZSI6ImViMGJiZWM3MzE5ZmIyNzM5NzRjMzc3MTYwMjZhZjBmIiwic2FsdCI6Ijc2YTZkMmM1OGVlYTllZDA0MTcwYzZjNmE1ZThhOTAxIn0sInNpZ25hdHVyZSI6IjFhYzg4Y2EwOTZkMDY4MmRjZmEyNWRhZDJlMzZiMzY4MDY3NjZiYjc2OWIzODkyM2UwMGIyYjJmMTNhZmU3NzUifSwic29sdXRpb24iOnsiY291bnRlciI6ODAsImRlcml2ZWRLZXkiOiIwMDY4MTdkNzE5NzI4ZDM2ZWRiMjllN2IwZWVjZDk4Y2MzNTQyZmQ0MmNlZjRkN2I3MTVlNWY2OWVlODU1YTc2IiwidGltZSI6MjgwMTkuOH19"
)

// nodeParams 是 node createChallenge 输出的 parameters（黄金向量）。
func nodeParams() ChallengeParameters {
	return ChallengeParameters{
		Algorithm: "SHA-256",
		Cost:      10000,
		ExpiresAt: 2051222400,
		KeyLength: 32,
		KeyPrefix: "00",
		Nonce:     "eb0bbec7319fb273974c37716026af0f",
		Salt:      "76a6d2c58eea9ed04170c6c6a5e8a901",
	}
}

// TestCanonicalJSONMatchesNode 验证 canonicalJSON 与 node JSON.stringify(sortKeys())
// 逐字节一致（键排序、无空格、整数不加指数）。
func TestCanonicalJSONMatchesNode(t *testing.T) {
	want := `{"algorithm":"SHA-256","cost":10000,"expiresAt":2051222400,"keyLength":32,"keyPrefix":"00","nonce":"eb0bbec7319fb273974c37716026af0f","salt":"76a6d2c58eea9ed04170c6c6a5e8a901"}`
	got, err := canonicalJSON(nodeParams())
	if err != nil {
		t.Fatalf("canonicalJSON error: %v", err)
	}
	if got != want {
		t.Fatalf("canonicalJSON mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestSignMatchesNode 验证 Go 签名与 node 的 signature 完全一致。
func TestSignMatchesNode(t *testing.T) {
	sig, err := Sign(nodeParams(), []byte(nodeAltchaHMACKey))
	if err != nil {
		t.Fatalf("Sign error: %v", err)
	}
	if sig != nodeAltchaSig {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", sig, nodeAltchaSig)
	}
}

// TestDeriveKeyMatchesNodeSolution 验证 KDF 复算与 node solveChallenge 结果一致。
func TestDeriveKeyMatchesNodeSolution(t *testing.T) {
	nonce, err := hexToBuffer(nodeParams().Nonce)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := hexToBuffer(nodeParams().Salt)
	if err != nil {
		t.Fatal(err)
	}
	dk, err := deriveKey(nodeParams(), salt, passwordBuffer(nonce, nodeAltchaCounter))
	if err != nil {
		t.Fatalf("deriveKey error: %v", err)
	}
	if got := hex.EncodeToString(dk); got != nodeAltchaDerived {
		t.Fatalf("derivedKey mismatch:\n got %s\nwant %s", got, nodeAltchaDerived)
	}
}

// TestVerifyNodeGoldenPayload 验证 Go 完整校验 node 生成的 payload 通过。
func TestVerifyNodeGoldenPayload(t *testing.T) {
	ok, err := Verify(nodeAltchaPayload, []byte(nodeAltchaHMACKey))
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if !ok {
		t.Fatal("node-generated golden payload must verify")
	}
}

// TestVerifyRejectsTampered 验证各类篡改均被拒绝（对齐 verifyAltcha → false）。
// 篡改通过在解码后的 JSON 上定点修改再重新 base64，保证命中目标字段。
func TestVerifyRejectsTampered(t *testing.T) {
	key := []byte(nodeAltchaHMACKey)

	reencode := func(mutate func(m map[string]interface{})) string {
		raw, err := base64.StdEncoding.DecodeString(nodeAltchaPayload)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		mutate(m)
		doc, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(doc)
	}

	// 篡改 solution.derivedKey → 派生密钥不匹配。
	if ok, _ := Verify(reencode(func(m map[string]interface{}) {
		m["solution"].(map[string]interface{})["derivedKey"] = strings.Repeat("ff", 32)
	}), key); ok {
		t.Fatal("tampered derivedKey must be rejected")
	}

	// 篡改 solution.counter → 派生密钥不匹配。
	if ok, _ := Verify(reencode(func(m map[string]interface{}) {
		m["solution"].(map[string]interface{})["counter"] = 81.0
	}), key); ok {
		t.Fatal("tampered counter must be rejected")
	}

	// 篡改 challenge.signature → 签名不匹配。
	if ok, _ := Verify(reencode(func(m map[string]interface{}) {
		m["challenge"].(map[string]interface{})["signature"] = strings.Repeat("0", 64)
	}), key); ok {
		t.Fatal("tampered signature must be rejected")
	}

	// 篡改 parameters（如 keyPrefix）→ 签名必然不匹配。
	if ok, _ := Verify(reencode(func(m map[string]interface{}) {
		m["challenge"].(map[string]interface{})["parameters"].(map[string]interface{})["keyPrefix"] = "ff"
	}), key); ok {
		t.Fatal("tampered parameters must be rejected")
	}

	// 空 payload / 非法 base64 / 非 JSON → false。
	if ok, _ := Verify("", key); ok {
		t.Fatal("empty payload must be rejected")
	}
	if ok, _ := Verify("!!!not-base64!!!", key); ok {
		t.Fatal("invalid base64 must be rejected")
	}
	if ok, _ := Verify(base64.StdEncoding.EncodeToString([]byte("not json")), key); ok {
		t.Fatal("non-JSON payload must be rejected")
	}

	// 缺 challenge / solution。
	for _, doc := range []string{
		`{"solution":{"counter":1,"derivedKey":"aa"}}`,
		`{"challenge":null,"solution":{"counter":1,"derivedKey":"aa"}}`,
		`{"challenge":{}}`,
	} {
		b64 := base64.StdEncoding.EncodeToString([]byte(doc))
		if ok, _ := Verify(b64, key); ok {
			t.Fatalf("payload %s must be rejected (missing challenge/solution)", doc)
		}
	}

	// 错误 HMAC 密钥 → 签名不匹配。
	if ok, _ := Verify(nodeAltchaPayload, []byte("wrong-key")); ok {
		t.Fatal("wrong hmac key must be rejected")
	}
}

// TestVerifyExpired 验证过期挑战被拒绝（在签名有效的前提下）。
func TestVerifyExpired(t *testing.T) {
	key := []byte(nodeAltchaHMACKey)
	now := time.Unix(1770000000, 0)

	// 构造一个 expiresAt 已过、但签名自洽的挑战。
	params := nodeParams()
	params.ExpiresAt = now.Add(-1 * time.Minute).Unix()
	sig, err := Sign(params, key)
	if err != nil {
		t.Fatal(err)
	}
	ch := Challenge{Parameters: params, Signature: sig}
	sol := solution{Counter: nodeAltchaCounter, DerivedKey: nodeAltchaDerived}

	if verifySolution(ch, sol, key, now) {
		t.Fatal("expired challenge must be rejected")
	}
	// 同一挑战在过期前应通过（证明仅有过期导致失败）。
	if !verifySolution(ch, sol, key, now.Add(-2*time.Minute)) {
		t.Fatal("same challenge before expiry must verify")
	}
}

// TestVerifyAtInjectTime 验证 verifyAt 注入时间与 Verify 等价（payload 未过期）。
func TestVerifyAtInjectTime(t *testing.T) {
	key := []byte(nodeAltchaHMACKey)
	// node 向量 expiresAt=2051222400（2035），任意当前时间验证应通过。
	ok, err := verifyAt(nodeAltchaPayload, key, time.Unix(1770000000, 0))
	if err != nil || !ok {
		t.Fatalf("verifyAt should pass for fresh payload: ok=%v err=%v", ok, err)
	}
}

// TestCreateChallenge 验证默认参数与签名结构对齐 Express /api/auth/captcha 输出。
func TestCreateChallenge(t *testing.T) {
	key := []byte(nodeAltchaHMACKey)
	now := time.Unix(1770000000, 0)
	ch, err := CreateChallenge(ChallengeOpts{HMACKey: key, Now: now})
	if err != nil {
		t.Fatalf("CreateChallenge error: %v", err)
	}
	p := ch.Parameters
	if p.Algorithm != "SHA-256" || p.Cost != 10000 || p.KeyLength != 32 || p.KeyPrefix != "00" {
		t.Fatalf("unexpected default parameters: %+v", p)
	}
	if p.ExpiresAt != now.Add(ChallengeTTL).Unix() {
		t.Fatalf("expiresAt = %d, want %d", p.ExpiresAt, now.Add(ChallengeTTL).Unix())
	}
	if len(p.Nonce) != 32 || len(p.Salt) != 32 {
		t.Fatalf("nonce/salt should be 32 hex chars: nonce=%q salt=%q", p.Nonce, p.Salt)
	}
	if _, err := hex.DecodeString(p.Nonce); err != nil {
		t.Fatalf("nonce not valid hex: %v", err)
	}
	// 签名与 Sign 一致。
	sig, err := Sign(p, key)
	if err != nil || ch.Signature != sig {
		t.Fatalf("signature mismatch: got %q want %q (err %v)", ch.Signature, sig, err)
	}

	// 未传 HMACKey → 无签名（对齐 createChallenge 无 hmacSignatureSecret 分支）。
	unsigned, err := CreateChallenge(ChallengeOpts{Now: now})
	if err != nil || unsigned.Signature != "" {
		t.Fatalf("unsigned challenge should have empty signature (err %v)", err)
	}
}

// TestCreateChallengeJSONFieldSet 验证挑战 JSON 字段集合与基线一致
// （algorithm/cost/expiresAt/keyLength/keyPrefix/nonce/salt + signature）。
func TestCreateChallengeJSONFieldSet(t *testing.T) {
	ch, err := CreateChallenge(ChallengeOpts{HMACKey: []byte(nodeAltchaHMACKey), Now: time.Unix(1770000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ch)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["signature"] == "" {
		t.Fatal("signature must be present")
	}
	params, ok := m["parameters"].(map[string]interface{})
	if !ok {
		t.Fatal("parameters must be an object")
	}
	for _, k := range []string{"algorithm", "cost", "expiresAt", "keyLength", "keyPrefix", "nonce", "salt"} {
		if _, ok := params[k]; !ok {
			t.Fatalf("parameters missing key %q (got %v)", k, params)
		}
	}
	// 基线确认：不应出现 counter 分支引入的 memoryCost/parallelism/data/keySignature。
	for _, k := range []string{"memoryCost", "parallelism", "data", "keySignature", "challenge", "maxNumber"} {
		if _, ok := params[k]; ok {
			t.Fatalf("parameters should not contain key %q (got %v)", k, params)
		}
	}
}

// TestCreateChallengeVerifyRoundtrip 验证 Go 创建→求解→校验全链路。
// 用较低 cost 保证测试快速；求解器迭代 counter 直到派生密钥前缀匹配。
func TestCreateChallengeVerifyRoundtrip(t *testing.T) {
	key := []byte(nodeAltchaHMACKey)
	now := time.Unix(1770000000, 0)
	ch, err := CreateChallenge(ChallengeOpts{Cost: 1000, HMACKey: key, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	nonce, _ := hexToBuffer(ch.Parameters.Nonce)
	salt, _ := hexToBuffer(ch.Parameters.Salt)
	prefix, _ := hexToBuffer(ch.Parameters.KeyPrefix)

	var sol solution
	found := false
	for counter := 0; counter < 100000; counter++ {
		dk, err := deriveKey(ch.Parameters, salt, passwordBuffer(nonce, counter))
		if err != nil {
			t.Fatal(err)
		}
		if bytesHasPrefix(dk, prefix) {
			sol = solution{Counter: counter, DerivedKey: hex.EncodeToString(dk)}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("solver did not find a solution within 100000 counters")
	}

	payloadDoc, err := json.Marshal(map[string]interface{}{"challenge": ch, "solution": sol})
	if err != nil {
		t.Fatal(err)
	}
	payloadB64 := base64.StdEncoding.EncodeToString(payloadDoc)
	// 用注入时间验证（挑战 expiresAt = now+5min，真实 time.Now 会在向量时间之后）。
	ok, err := verifyAt(payloadB64, key, now)
	if err != nil || !ok {
		t.Fatalf("roundtrip verify failed: ok=%v err=%v", ok, err)
	}
	// 篡改派生密钥后必须失败。
	sol.DerivedKey = strings.Repeat("ff", 32)
	payloadDoc2, _ := json.Marshal(map[string]interface{}{"challenge": ch, "solution": sol})
	if ok, _ := verifyAt(base64.StdEncoding.EncodeToString(payloadDoc2), key, now); ok {
		t.Fatal("tampered roundtrip solution must be rejected")
	}
}

// bytesHasPrefix 判断 buffer 是否以 prefix 开头（对齐 lib bufferStartsWith）。
func bytesHasPrefix(buffer, prefix []byte) bool {
	if len(prefix) > len(buffer) {
		return false
	}
	for i := range prefix {
		if buffer[i] != prefix[i] {
			return false
		}
	}
	return true
}
