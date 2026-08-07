// Package code 提供验证码内存存储（对齐 Express utils/emailVerifyCodes.js 与
// deviceLoginCodes.js 的内存 Map 语义）。
//
// 特性：进程内存储、TTL 过期、单码尝试次数上限、用后即删、60s 定时清理。
// 多实例部署需换共享存储（当前单实例内存语义，接口化便于将来换 Redis）。
package code

import (
	"crypto/rand"
	"math/big"
	"sync"
	"time"
)

// Entry 是验证码条目。
type Entry struct {
	UserID    string
	Email     string
	ExpiresAt time.Time
	Attempts  int
	// Need2FA 仅设备登录码使用（deviceLoginCodes 条目携带）。
	Need2FA bool
}

// Store 是验证码内存存储。key 为 6 位数字验证码。
type Store struct {
	mu    sync.Mutex
	items map[string]Entry
	ttl   time.Duration
	done  chan struct{}
}

// NewStore 创建验证码存储，并启动 60s 定时清理过期条目的 goroutine。
// 用 Store.Stop 停止清理（测试用）。
func NewStore(ttl time.Duration) *Store {
	s := &Store{
		items: make(map[string]Entry),
		ttl:   ttl,
		done:  make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// cleanupLoop 每 60s 清理过期条目。
func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for k, e := range s.items {
				if now.After(e.ExpiresAt) {
					delete(s.items, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Stop 停止后台清理（测试回收）。
func (s *Store) Stop() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// Set 存入验证码（自动计算过期时间 = now + ttl）。
func (s *Store) Set(code string, e Entry) {
	e.ExpiresAt = time.Now().Add(s.ttl)
	s.mu.Lock()
	s.items[code] = e
	s.mu.Unlock()
}

// Get 取出条目（不过期判断由调用方按 ExpiresAt 处理，与 Express 一致）。
func (s *Store) Get(code string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[code]
	return e, ok
}

// Delete 删除验证码（一次性消费）。
func (s *Store) Delete(code string) {
	s.mu.Lock()
	delete(s.items, code)
	s.mu.Unlock()
}

// GenerateCode 生成 6 位数字验证码（对齐 crypto.randomInt(100000,1000000)）。
func GenerateCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		// 极低概率随机源失败：回退时间戳微秒派生，避免 panic。
		return time.Now().Format("150405.000")[6:9] + "000"
	}
	return n.Add(n, big.NewInt(100000)).String()
}
