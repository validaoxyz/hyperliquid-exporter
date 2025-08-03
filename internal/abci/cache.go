package abci

import (
	"time"

	"github.com/validaoxyz/hyperliquid-exporter/internal/cache"
)

// stores recently read contexts to avoid re-parsing
type Cache struct {
	lru *cache.LRUCache
}

type cacheItem struct {
	context *ContextInfo
	modTime time.Time
}

// creates a new cache instance with LRU eviction
func NewCache() *Cache {
	// 100 entries max, 1 hour TTL
	return &Cache{
		lru: cache.NewLRUCache(100, 1*time.Hour),
	}
}

// retrieves a context from cache if it's still valid
func (c *Cache) Get(filePath string, modTime time.Time) *ContextInfo {
	value, exists := c.lru.Get(filePath)
	if !exists {
		return nil
	}

	item := value.(*cacheItem)

	// Check if the file has been modified since we cached it
	if !item.modTime.Equal(modTime) {
		// Remove stale entry
		c.lru.Delete(filePath)
		return nil
	}

	// Cache is valid
	return item.context
}

// stores a context in the cache
func (c *Cache) Set(filePath string, context *ContextInfo, modTime time.Time) {
	item := &cacheItem{
		context: context,
		modTime: modTime,
	}
	c.lru.Set(filePath, item)
}

// removes all items from the cache
func (c *Cache) Clear() {
	c.lru.Clear()
}

// removes expired entries
func (c *Cache) CleanupExpired() {
	c.lru.CleanupExpired()
}
