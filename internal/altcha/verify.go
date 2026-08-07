package altcha

// Verify 层：解析 base64 payload 并校验 Altcha v2 solution。
// 对齐 utils/altcha.js verifyAltcha 的校验主体（开发绕过口令除外，见包注释）。

import (
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"time"
)

// solution 是求解结果 {counter, derivedKey}（对齐 lib Solution）。
type solution struct {
	// Counter 是求解到的计数（uint32BE 写入 nonce 之后）。
	Counter int `json:"counter"`
	// DerivedKey 是 KDF(cost, salt, nonce||uint32BE(counter)) 的 hex。
	DerivedKey string `json:"derivedKey"`
}

// payload 是前端提交的 base64 明文：{challenge, solution}。
// 用指针区分字段缺失/为 null（对齐 JS 的 !challenge || !solution 真值检查）。
type payload struct {
	Challenge *Challenge `json:"challenge"`
	Solution  *solution  `json:"solution"`
}

// Verify 校验 Altcha v2 提交的 payload（base64 编码的 {challenge, solution}）。
//
// 对齐 utils/altcha.js verifyAltcha：除开发绕过口令外的所有路径。任一环节失败
// （payload 空、base64/JSON 解码失败、缺 challenge/solution、已过期、签名不匹配、
// 派生密钥不匹配）返回 (false, nil)，与 Express 的 try/catch→false 语义一致；
// 仅真正的内部错误才返回非 nil error（实际几乎不会发生）。
//
// hmacKey 必须是 ALTCHA_HMAC_KEY 字符串的 UTF-8 编码字节（[]byte(hexString)）。
func Verify(payloadStr string, hmacKey []byte) (bool, error) {
	return verifyAt(payloadStr, hmacKey, time.Now())
}

// verifyAt 同 Verify，但时间点可注入（测试用）。
func verifyAt(payloadStr string, hmacKey []byte, now time.Time) (bool, error) {
	if payloadStr == "" {
		return false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(payloadStr)
	if err != nil {
		return false, nil
	}
	var pl payload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return false, nil
	}
	if pl.Challenge == nil || pl.Solution == nil {
		return false, nil
	}
	return verifySolution(*pl.Challenge, *pl.Solution, hmacKey, now), nil
}

// verifySolution 逐条对齐 lib verifySolution（无 hmacKeySignatureSecret 分支，
// 与本服务创建方式一致——createChallenge 不带 counter，故无 keySignature）：
//  1. expiresAt 非 0 且 < now（秒）→ 过期
//  2. 无 signature → 非法签名
//  3. 重算 signature 与 challenge.signature 常量时间比较 → 不匹配非法
//  4. 重算 derivedKey = KDF(parameters, salt, nonce||uint32BE(counter))，
//     与 solution.derivedKey 常量时间比较 → 不匹配非法
//  5. 全部通过 → verified
func verifySolution(ch Challenge, sol solution, hmacKey []byte, now time.Time) bool {
	// 1) 过期（对齐 lib：expiresAt < Date.now()/1000，含小数比较）。
	if ch.Parameters.ExpiresAt > 0 && float64(ch.Parameters.ExpiresAt) < float64(now.UnixNano())/1e9 {
		return false
	}
	// 2) 签名存在性。
	if ch.Signature == "" {
		return false
	}
	// 3) 签名校验。
	sig, err := Sign(ch.Parameters, hmacKey)
	if err != nil || subtle.ConstantTimeCompare([]byte(sig), []byte(ch.Signature)) != 1 {
		return false
	}
	// 4) derivedKey 校验。
	nonce, err := hexToBuffer(ch.Parameters.Nonce)
	if err != nil {
		return false
	}
	salt, err := hexToBuffer(ch.Parameters.Salt)
	if err != nil {
		return false
	}
	password := passwordBuffer(nonce, sol.Counter)
	dk, err := deriveKey(ch.Parameters, salt, password)
	if err != nil {
		return false
	}
	dkHex := hex.EncodeToString(dk)
	if subtle.ConstantTimeCompare([]byte(dkHex), []byte(sol.DerivedKey)) != 1 {
		return false
	}
	return true
}

// deriveKey 对齐 lib sha.deriveKey：
//   - iterations = max(1, cost)
//   - i==0 时 data = salt||password，之后 data = 上一轮 derivedKey
//   - 每轮 digest(algorithm, data) 截断到 keyLength 字节
//
// 后端固定 SHA-256；其余算法（SHA-384/SHA-512）返回错误（服务不会签发此类挑战）。
func deriveKey(p ChallengeParameters, salt, password []byte) ([]byte, error) {
	iterations := p.Cost
	if iterations < 1 {
		iterations = 1
	}
	keyLength := p.KeyLength
	if keyLength <= 0 {
		keyLength = DefaultKeyLength
	}
	hasher := func(data []byte) []byte {
		switch p.Algorithm {
		case "SHA-256":
			sum := sha256.Sum256(data)
			return sum[:]
		case "SHA-512":
			sum := sha512.Sum512(data)
			return sum[:]
		case "SHA-384":
			h := sha512.New384()
			h.Write(data)
			return h.Sum(nil)
		default:
			return nil
		}
	}
	derivedKey := []byte(nil)
	for i := 0; i < iterations; i++ {
		var data []byte
		if i == 0 {
			data = make([]byte, 0, len(salt)+len(password))
			data = append(data, salt...)
			data = append(data, password...)
		} else {
			data = derivedKey
		}
		sum := hasher(data)
		if sum == nil {
			return nil, &unsupportedAlgorithmError{alg: p.Algorithm}
		}
		if keyLength < len(sum) {
			derivedKey = sum[:keyLength]
		} else {
			derivedKey = sum
		}
	}
	return derivedKey, nil
}

// unsupportedAlgorithmError 标识不支持的哈希算法。
type unsupportedAlgorithmError struct {
	alg string
}

func (e *unsupportedAlgorithmError) Error() string {
	return "unsupported altcha algorithm: " + e.alg
}

// passwordBuffer 构造 deriveKey 的 password：nonce（原始字节）|| uint32BE(counter)，
// 对齐 lib PasswordBuffer（counterMode "uint32"）。
func passwordBuffer(nonce []byte, counter int) []byte {
	out := make([]byte, len(nonce)+4)
	copy(out, nonce)
	binary.BigEndian.PutUint32(out[len(nonce):], uint32(counter))
	return out
}
