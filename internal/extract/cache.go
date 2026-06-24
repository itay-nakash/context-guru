package extract

import "sync"

// Cache memoizes extraction results by ContentKey, so the same large output re-sent
// on a later turn skips the paid model call. In-memory, process-local.
//
// ponytail: a plain map+mutex, unbounded. Add an LRU/TTL only if memory ever bites.
type Cache struct {
	mu sync.Mutex
	m  map[string]string
}

func NewCache() *Cache { return &Cache{m: map[string]string{}} }

func (c *Cache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *Cache) Put(key, val string) {
	c.mu.Lock()
	c.m[key] = val
	c.mu.Unlock()
}
