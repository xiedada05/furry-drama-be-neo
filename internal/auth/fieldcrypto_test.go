package auth

import "testing"

// 黄金向量：由 Node 端 utils/crypto.js 同算法（key=sha256(secret) raw，AES-256-CBC）
// 用 secret="my-secret-key" 加密 "hello世界123" 得到。用于验证与旧数据解密兼容。
const (
	nodeVectorSecret = "my-secret-key"
	nodeVectorCipher = "enc:6dd9c98664e83b7e8bdb6235155c98df:724fd420c02a6d0a45a6d9d1c189af20"
	nodeVectorPlain  = "hello世界123"
	// key = sha256("my-secret-key") 的 hex（Node 端计算值）。
	nodeVectorKeyHex = "1311f8fc80a7ea28d78dd7723f09c44c1754cd35160ca8e7133ae3d7f636a19a"
)

// TestFieldKeyDerivation 验证 key 派生与 Node 一致（ENCRYPTION_KEY 缺省回退 JWT_SECRET）。
func TestFieldKeyDerivation(t *testing.T) {
	if got := FieldKey("", nodeVectorSecret); got != nodeVectorKeyHex {
		t.Fatalf("FieldKey derivation mismatch: got %s want %s", got, nodeVectorKeyHex)
	}
	// ENCRYPTION_KEY 优先。
	if got := FieldKey("enc-first", nodeVectorSecret); got == nodeVectorKeyHex {
		t.Fatalf("FieldKey should prefer encryptionKey over jwtSecret")
	}
}

// TestDecryptNodeVector 验证能解密 Node 生成的旧数据。
func TestDecryptNodeVector(t *testing.T) {
	got := DecryptField(nodeVectorCipher, nodeVectorKeyHex)
	if got != nodeVectorPlain {
		t.Fatalf("decrypt node vector mismatch: got %q want %q", got, nodeVectorPlain)
	}
}

// TestEncryptRoundtrip 验证加密→解密往返一致，且格式为 enc:iv:ct。
func TestEncryptRoundtrip(t *testing.T) {
	for _, plain := range []string{"hello", "中文内容 with 空格 and §ymbols", "very long string padded beyond block size 0123456789abcdef"} {
		enc := EncryptField(plain, nodeVectorKeyHex)
		if got := DecryptField(enc, nodeVectorKeyHex); got != plain {
			t.Fatalf("roundtrip mismatch for %q: got %q", plain, got)
		}
	}
}

// TestEncryptFieldIdempotent 验证空值/已加密串原样返回。
func TestEncryptFieldIdempotent(t *testing.T) {
	if EncryptField("", nodeVectorKeyHex) != "" {
		t.Fatal("empty string should pass through")
	}
	if EncryptField("enc:abc", nodeVectorKeyHex) != "enc:abc" {
		t.Fatal("already-encrypted string should pass through")
	}
	if DecryptField("plain", nodeVectorKeyHex) != "plain" {
		t.Fatal("non-enc string should pass through")
	}
}

// TestEncryptArray 验证数组逐元素加解密。
func TestEncryptArray(t *testing.T) {
	in := []string{"a", "bb", "ccc"}
	enc := EncryptArray(in, nodeVectorKeyHex)
	dec := DecryptArray(enc, nodeVectorKeyHex)
	if len(dec) != len(in) {
		t.Fatalf("array length mismatch: got %d want %d", len(dec), len(in))
	}
	for i := range in {
		if dec[i] != in[i] {
			t.Fatalf("array element %d mismatch: got %q want %q", i, dec[i], in[i])
		}
	}
}
