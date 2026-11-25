package fga

import (
	"context"
	"strconv"
	"sync"
	"time"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

// Cache wraps an API and caches Read pages for a short time, so repeated
// traces over the same objects (e.g. stepping through "who else has this?")
// don't re-read the same tuples. Reads that ask for higher consistency
// bypass the cache. Check and ListObjects are never cached.
type Cache struct {
	API
	TTL     time.Duration
	MaxSize int

	mu      sync.Mutex
	entries map[string]cacheEntry
	hits    int64
	misses  int64
}

type cacheEntry struct {
	resp *openfgav1.ReadResponse
	at   time.Time
}

// NewCache wraps api with a read cache.
func NewCache(api API, ttl time.Duration) *Cache {
	return &Cache{API: api, TTL: ttl, MaxSize: 50000, entries: map[string]cacheEntry{}}
}

func readCacheKey(r *openfgav1.ReadRequest) string {
	k := r.GetTupleKey()
	return r.GetStoreId() + "\x00" + k.GetObject() + "\x00" + k.GetRelation() + "\x00" + k.GetUser() + "\x00" +
		r.GetContinuationToken() + "\x00" + strconv.Itoa(int(r.GetPageSize().GetValue()))
}

// Read serves a cached page when one is fresh.
func (c *Cache) Read(ctx context.Context, req *openfgav1.ReadRequest) (*openfgav1.ReadResponse, error) {
	if req.GetConsistency() == openfgav1.ConsistencyPreference_HIGHER_CONSISTENCY || c.TTL <= 0 {
		return c.API.Read(ctx, req)
	}
	key := readCacheKey(req)
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Sub(e.at) < c.TTL {
		c.hits++
		c.mu.Unlock()
		return e.resp, nil
	}
	c.misses++
	c.mu.Unlock()
	resp, err := c.API.Read(ctx, req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.entries) >= c.MaxSize {
		// Drop expired entries; if still full, drop arbitrary ones (map order).
		for k, e := range c.entries {
			if now.Sub(e.at) >= c.TTL || len(c.entries) >= c.MaxSize {
				delete(c.entries, k)
			}
			if len(c.entries) < c.MaxSize*9/10 {
				break
			}
		}
	}
	c.entries[key] = cacheEntry{resp: resp, at: now}
	c.mu.Unlock()
	return resp, nil
}

// Stats returns cache hits and misses.
func (c *Cache) Stats() (hits, misses int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// BatchCheck forwards to the wrapped API when it supports BatchCheck.
func (c *Cache) BatchCheck(ctx context.Context, req *openfgav1.BatchCheckRequest) (*openfgav1.BatchCheckResponse, error) {
	if bc, ok := c.API.(BatchChecker); ok {
		return bc.BatchCheck(ctx, req)
	}
	return nil, &Error{Status: 501, Code: "unimplemented", Message: "BatchCheck not supported"}
}
