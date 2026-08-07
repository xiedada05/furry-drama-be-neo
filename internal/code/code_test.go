package code

import (
	"testing"
	"time"
)

func TestGenerateCodeSixDigits(t *testing.T) {
	for i := 0; i < 200; i++ {
		c := GenerateCode()
		if len(c) != 6 {
			t.Fatalf("code length %d != 6: %q", len(c), c)
		}
		for _, r := range c {
			if r < '0' || r > '9' {
				t.Fatalf("non-digit in code %q", c)
			}
		}
	}
}

func TestStoreSetGetDelete(t *testing.T) {
	s := NewStore(time.Hour)
	defer s.Stop()
	s.Set("123456", Entry{UserID: "u1", Email: "a@b.com"})
	e, ok := s.Get("123456")
	if !ok || e.UserID != "u1" || e.Email != "a@b.com" {
		t.Fatalf("get mismatch: %+v ok=%v", e, ok)
	}
	s.Delete("123456")
	if _, ok := s.Get("123456"); ok {
		t.Fatal("should be deleted")
	}
}

func TestStoreTTLExpiry(t *testing.T) {
	// 用极短 TTL 验证过期（Get 不主动判过期——与 Express 一致，由调用方判）。
	s := NewStore(time.Millisecond)
	defer s.Stop()
	s.Set("111111", Entry{UserID: "u1"})
	time.Sleep(5 * time.Millisecond)
	// Get 仍返回条目（Express 语义：verify 时检查 expiresAt）。
	if _, ok := s.Get("111111"); !ok {
		t.Fatal("Get should still return before cleanup")
	}
}
