package middleware

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

// Cache 是线程安全的内存缓存（对齐 Express middlewares/cache.js）：
// LRU 淘汰 + TTL 过期。Get 命中时移动到 LRU 尾部，Set 已存在键时同样刷新
// 位置（对齐 Map.delete+set 的"移到最后"语义）。
type Cache struct {
	mu    sync.Mutex
	items map[string]*cacheEntry
	ll    *list.List
	ttl   time.Duration
	max   int
}

type cacheEntry struct {
	value     any
	timestamp time.Time
	elem      *list.Element
}

// NewCache 构造缓存；ttl 为有效期，maxEntries 为最大条目数（超出淘汰最久未访问）。
func NewCache(ttl time.Duration, maxEntries int) *Cache {
	if maxEntries <= 0 {
		maxEntries = 200
	}
	return &Cache{
		items: make(map[string]*cacheEntry),
		ll:    list.New(),
		ttl:   ttl,
		max:   maxEntries,
	}
}

// Get 读取缓存；不存在或已过期返回 (nil, false)。命中时刷新 LRU 位置。
func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Since(it.timestamp) > c.ttl {
		c.remove(key, it)
		return nil, false
	}
	c.ll.MoveToBack(it.elem)
	return it.value, true
}

// Set 写入缓存；已存在键则更新值并刷新位置，否则按 LRU 淘汰最久未访问项。
func (c *Cache) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[key]; ok {
		it.value = value
		it.timestamp = time.Now()
		c.ll.MoveToBack(it.elem)
		return
	}
	if len(c.items) >= c.max {
		front := c.ll.Front()
		if front != nil {
			if k, ok := front.Value.(string); ok {
				c.remove(k, c.items[k])
			}
		}
	}
	elem := c.ll.PushBack(key)
	c.items[key] = &cacheEntry{value: value, timestamp: time.Now(), elem: elem}
}

// Delete 删除指定键。
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[key]; ok {
		c.remove(key, it)
	}
}

// DeleteByPrefix 删除前缀匹配的全部键。
func (c *Cache) DeleteByPrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, it := range c.items {
		if strings.HasPrefix(k, prefix) {
			c.remove(k, it)
		}
	}
}

// Len 返回当前缓存条目数。
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *Cache) remove(key string, it *cacheEntry) {
	delete(c.items, key)
	if it.elem != nil {
		c.ll.Remove(it.elem)
	}
}

// EpisodeCache 是内容域共用的剧集缓存（5min TTL，最多 200 条，
// 对齐 config.CACHE_DURATION / CACHE_MAX_SIZE）。供 episodes 域读写，
// 审核/review 域写入审核结果后应调用其 Delete/DeleteByPrefix 清理。
var EpisodeCache = NewCache(5*time.Minute, 200)
