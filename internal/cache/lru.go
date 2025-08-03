package cache

import (
	"container/list"
	"sync"
	"time"
)

// thread-safe LRU cache with optional TTL
type LRUCache struct {
	mu       sync.RWMutex
	capacity int
	ttl      time.Duration
	items    map[string]*list.Element
	lru      *list.List
}

// holds cached value and metadata
type cacheEntry struct {
	key       string
	value     interface{}
	expiresAt time.Time
}

// creates new LRU cache with given capacity and TTL
func NewLRUCache(capacity int, ttl time.Duration) *LRUCache {
	return &LRUCache{
		capacity: capacity,
		ttl:      ttl,
		items:    make(map[string]*list.Element),
		lru:      list.New(),
	}
}

// retrieves value from cache
func (c *LRUCache) Get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, found := c.items[key]
	if !found {
		return nil, false
	}

	entry := elem.Value.(*cacheEntry)

	// sheck expired
	if c.ttl > 0 && time.Now().After(entry.expiresAt) {
		c.removeElement(elem)
		return nil, false
	}

	// move to most recent, front
	c.lru.MoveToFront(elem)
	return entry.value, true
}

// adds/updates value in cache
func (c *LRUCache) Set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// checks if already exists
	if elem, found := c.items[key]; found {
		c.lru.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		entry.value = value
		if c.ttl > 0 {
			entry.expiresAt = time.Now().Add(c.ttl)
		}
		return
	}

	// add new entry
	entry := &cacheEntry{
		key:   key,
		value: value,
	}
	if c.ttl > 0 {
		entry.expiresAt = time.Now().Add(c.ttl)
	}

	elem := c.lru.PushFront(entry)
	c.items[key] = elem

	// evict if > capacity
	if c.lru.Len() > c.capacity {
		c.removeOldest()
	}
}

// rm key from cache
func (c *LRUCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, found := c.items[key]; found {
		c.removeElement(elem)
	}
}

// returns number of items in cache
func (c *LRUCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Len()
}

// removes all items from cache
func (c *LRUCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.lru.Init()
}

// remove least recently used item
func (c *LRUCache) removeOldest() {
	elem := c.lru.Back()
	if elem != nil {
		c.removeElement(elem)
	}
}

// removes an element from cache
func (c *LRUCache) removeElement(elem *list.Element) {
	c.lru.Remove(elem)
	entry := elem.Value.(*cacheEntry)
	delete(c.items, entry.key)
}

// returns all keys in cache
func (c *LRUCache) GetAll() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]interface{})
	for key, elem := range c.items {
		entry := elem.Value.(*cacheEntry)
		// Skip expired entries
		if c.ttl > 0 && time.Now().After(entry.expiresAt) {
			continue
		}
		result[key] = entry.value
	}
	return result
}

// CleanupExpired removes all expired entries
func (c *LRUCache) CleanupExpired() {
	if c.ttl == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for elem := c.lru.Back(); elem != nil; {
		prev := elem.Prev()
		entry := elem.Value.(*cacheEntry)
		if now.After(entry.expiresAt) {
			c.removeElement(elem)
		}
		elem = prev
	}
}
