package auth

import "testing"

// 黄金向量：由 Node 端 bcryptjs（backend/node_modules/bcryptjs，与
// backend/models/User.js 同库）以 cost=12 对密码 "P@ssw0rd!测试123" 生成。
// 2026-08-08 由 node gen_vectors 脚本现场产出（见 W3-auth 工作记录）。
// bcryptjs v3 默认输出 $2b$ 前缀，Go 的 x/crypto/bcrypt 原生支持
// $2a$/$2b$/$2y$，可互验。
const (
	nodeBcryptPassword = "P@ssw0rd!测试123"
	nodeBcryptHash     = "$2b$12$mgc64fBM4QWNCXcLt2NFkO.UIbZX8kXML75VYLRme/YL.9J/f935y"
)

// TestCompareNodeVector 验证 Go 能校验 bcryptjs 生成的哈希（互验兼容）。
func TestCompareNodeVector(t *testing.T) {
	if !Compare(nodeBcryptHash, nodeBcryptPassword) {
		t.Fatal("Compare should accept bcryptjs-generated hash for the correct password")
	}
	if Compare(nodeBcryptHash, "wrong-password") {
		t.Fatal("Compare must reject wrong password")
	}
}

// TestHashFormat 验证 Hash 输出 $2a$ 前缀、cost=12 的标准 bcrypt 串。
func TestHashFormat(t *testing.T) {
	hash, err := Hash(nodeBcryptPassword)
	if err != nil {
		t.Fatalf("Hash error: %v", err)
	}
	// Go x/crypto/bcrypt 生成 $2a$ 前缀；12 为轮数。
	wantPrefix := "$2a$12$"
	if len(hash) != 60 || hash[:7] != wantPrefix {
		t.Fatalf("Hash output malformed: %q (want prefix %q, len 60)", hash, wantPrefix)
	}
}

// TestHashRoundtrip 验证 Hash→Compare 往返。
func TestHashRoundtrip(t *testing.T) {
	for _, pwd := range []string{"", "short", "中文密码+symbols§", "a very long password exceeding the typical short length 0123456789"} {
		hash, err := Hash(pwd)
		if err != nil {
			t.Fatalf("Hash(%q) error: %v", pwd, err)
		}
		if !Compare(hash, pwd) {
			t.Fatalf("roundtrip mismatch for %q", pwd)
		}
		if Compare(hash, pwd+"x") {
			t.Fatalf("Compare must reject modified password for %q", pwd)
		}
	}
}

// TestHashCost 验证轮数为 12（对齐 models/User.js bcrypt.genSalt(12)）。
func TestHashCost(t *testing.T) {
	hash, err := Hash("cost-check")
	if err != nil {
		t.Fatalf("Hash error: %v", err)
	}
	// 格式 $2a$<cost>$<22 salt><31 hash>，cost 为 12。
	if hash[4:6] != "12" {
		t.Fatalf("unexpected cost in hash: %q", hash)
	}
}

// TestHashRejectsTooLongPassword 验证 bcrypt 72 字节口令上限返回错误。
func TestHashRejectsTooLongPassword(t *testing.T) {
	long := make([]byte, 73)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Hash(string(long)); err == nil {
		t.Fatal("Hash should error for password > 72 bytes")
	}
}

// TestCompareRejectsMalformedHash 验证非法哈希返回 false（不 panic）。
func TestCompareRejectsMalformedHash(t *testing.T) {
	for _, h := range []string{"", "not-a-bcrypt", "$2a$12$short", "$2a$12$too-short-salt-and-hash-to-parse-ok"} {
		if Compare(h, "password") {
			t.Fatalf("Compare must reject malformed hash %q", h)
		}
	}
}
