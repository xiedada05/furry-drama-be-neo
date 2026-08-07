// Package altcha 实现 Altcha v2 工作量证明（PoW）的挑战创建与校验。
//
// 行为对齐 backend/utils/altcha.js 与 backend/node_modules/altcha/dist/lib/index.umd.cjs
// （altcha v3.1.0）的 createChallenge / verifySolution / sha.deriveKey：
//   - SHA-256 链式 KDF：迭代 max(1, cost) 次，首轮输入 salt||password，
//     之后输入上一轮哈希，每轮截断到 keyLength 字节
//   - password = nonce（16 原始字节）|| uint32BE(counter)
//   - 签名 = hex(HMAC-SHA256(hmacKey, canonicalJSON(parameters)))；
//     canonicalJSON 键排序、无空格、数字用 json.Number（防止指数化）
//   - 挑战默认 keyPrefix "00"、keyLength 32、expiresAt = now + 5min（秒）
//
// 注意：本包只提供基础的 CreateChallenge/Verify 能力。开发环境 x-dev-token 绕过
// （DEV_API_TOKEN）不属于本包，由 middleware/handler 层在调用 Verify 前判断。
package altcha

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 默认参数（对齐 Express 调用方）。
const (
	// DefaultCost 是默认 KDF 迭代次数。
	DefaultCost = 10000
	// DefaultAlgorithm 是默认哈希算法。
	DefaultAlgorithm = "SHA-256"
	// DefaultKeyPrefix 是默认求解目标前缀。
	DefaultKeyPrefix = "00"
	// DefaultKeyLength 是默认派生密钥长度（字节）。
	DefaultKeyLength = 32
	// defaultSaltLen 是默认盐长度（字节）。
	defaultSaltLen = 16
	// defaultNonceLen 是默认 nonce 长度（字节）。
	defaultNonceLen = 16
	// ChallengeTTL 是挑战有效期：now + 5min。
	ChallengeTTL = 5 * time.Minute
)

// Challenge 是 Altcha v2 挑战，结构对齐 createChallenge 输出：
// {parameters: {...}, signature: "<hex>"}。
type Challenge struct {
	Parameters ChallengeParameters `json:"parameters"`
	Signature  string              `json:"signature"`
}

// ChallengeParameters 是挑战参数。签名前按键排序生成 canonical JSON，
// 字段顺序不影响签名。
type ChallengeParameters struct {
	// Algorithm 是哈希算法，固定 "SHA-256"。
	Algorithm string `json:"algorithm"`
	// Cost 是 KDF 迭代次数（10000）。
	Cost int `json:"cost"`
	// ExpiresAt 是过期时间（Unix 秒）。
	ExpiresAt int64 `json:"expiresAt"`
	// KeyLength 是派生密钥长度（字节，32）。
	KeyLength int `json:"keyLength"`
	// KeyPrefix 是求解目标前缀（"00"）。
	KeyPrefix string `json:"keyPrefix"`
	// Nonce 是随机 nonce 的 hex（32 字符 = 16 字节）。
	Nonce string `json:"nonce"`
	// Salt 是随机盐的 hex（32 字符 = 16 字节）。
	Salt string `json:"salt"`
}

// ChallengeOpts 是 CreateChallenge 的输入。
type ChallengeOpts struct {
	// Cost 是 KDF 迭代次数，<= 0 用 DefaultCost（10000）。
	Cost int
	// Salt 是盐的 hex 字符串；空则随机生成 16 字节。
	Salt string
	// Algorithm 是哈希算法，空用 DefaultAlgorithm（"SHA-256"）。
	Algorithm string
	// HMACKey 是 hmacSignatureSecret 的 UTF-8 字节。
	// Express 侧 ALTCHA_HMAC_KEY 是一个 hex 字符串，HMAC 密钥取其 UTF-8 编码字节
	//（即 []byte(hexString)，而非解码后的原始字节）。调用方需按此传入。
	HMACKey []byte
	// KeyPrefix 是求解目标前缀，空用 DefaultKeyPrefix（"00"）。
	KeyPrefix string
	// Now 用于注入当前时间（测试用），零值用 time.Now()。
	Now time.Time
}

// CreateChallenge 生成一个 Altcha v2 挑战：随机 nonce/salt，SHA-256 KDF，
// expiresAt = now + 5min（秒）。HMACKey 非空时对 parameters 签名；
// HMACKey 为空则不签名（对齐 createChallenge：无 hmacSignatureSecret 时
// 返回无签名挑战）。
func CreateChallenge(opts ChallengeOpts) (Challenge, error) {
	o := defaults(opts)
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	nonce := randomHex(defaultNonceLen)
	salt := o.Salt
	if salt == "" {
		salt = randomHex(defaultSaltLen)
	}
	params := ChallengeParameters{
		Algorithm: o.Algorithm,
		Cost:      o.Cost,
		ExpiresAt: now.Add(ChallengeTTL).Unix(),
		KeyLength: DefaultKeyLength,
		KeyPrefix: o.KeyPrefix,
		Nonce:     nonce,
		Salt:      salt,
	}
	ch := Challenge{Parameters: params}
	if len(o.HMACKey) == 0 {
		return ch, nil
	}
	sig, err := Sign(params, o.HMACKey)
	if err != nil {
		return Challenge{}, err
	}
	ch.Signature = sig
	return ch, nil
}

// Sign 计算 parameters 的 Altcha 签名：hex(HMAC-SHA256(hmacKey, canonicalJSON))，
// 与 createChallenge/verifySolution 的签名算法一致。导出以便 handler 在
// 需要单独重签参数（如校验）时复用。
func Sign(params ChallengeParameters, hmacKey []byte) (string, error) {
	canon, err := canonicalJSON(params)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, hmacKey)
	if _, err := mac.Write([]byte(canon)); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// defaults 填充 ChallengeOpts 的默认值。
func defaults(o ChallengeOpts) ChallengeOpts {
	if o.Cost <= 0 {
		o.Cost = DefaultCost
	}
	if o.Algorithm == "" {
		o.Algorithm = DefaultAlgorithm
	}
	if o.KeyPrefix == "" {
		o.KeyPrefix = DefaultKeyPrefix
	}
	return o
}

// randomHex 返回 n 个随机字节的 hex 字符串（2n 字符）。
// 随机源失败时回退时间戳派生，保证不 panic（与 auth 包风格一致）。
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		buf = []byte(time.Now().Format("20060102150405.000000000Z07:00"))
	}
	return hex.EncodeToString(buf)
}

// canonicalJSON 生成 Altcha canonical JSON：键排序、无空格、数字用 json.Number
// 防止指数化。对齐 lib canonicalJSON = JSON.stringify(sortKeys(obj))。
// 键顺序：algorithm, cost, expiresAt, keyLength, keyPrefix, nonce, salt。
func canonicalJSON(params ChallengeParameters) (string, error) {
	m := map[string]interface{}{
		"algorithm": params.Algorithm,
		"cost":      json.Number(strconv.Itoa(params.Cost)),
		"expiresAt": json.Number(strconv.FormatInt(params.ExpiresAt, 10)),
		"keyLength": json.Number(strconv.Itoa(params.KeyLength)),
		"keyPrefix": params.KeyPrefix,
		"nonce":     params.Nonce,
		"salt":      params.Salt,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// hexToBuffer 解码 hex 字符串为原始字节，奇数长度报错
// （对齐 lib hexToBuffer）。
func hexToBuffer(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("hex string must have an even length, got %d", len(s))
	}
	return hex.DecodeString(s)
}
