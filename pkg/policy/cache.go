// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

package policy

import (
	"sync"
	"time"
)

// CacheTTL bounds how stale a cached decision may be. Short enough
// that a policy add/remove that races a snapshot Flush still settles
// quickly.
const CacheTTL = 5 * time.Second

// CacheCapacity is a soft cap on entries. The cache is allowed to
// grow up to CacheCapacity before Put performs best-effort eviction:
// expired entries first, then a random subset if still over the cap.
const CacheCapacity = 64 * 1024

type cacheKey struct {
	caller string
	target string
	method string
}

type cacheEntry struct {
	res     Result
	expires time.Time
}

// Cache is the relay-side decision cache. It is goroutine-safe.
//
// Every Engine snapshot rebuild flushes the cache (Flush) — without
// that, a policy add/remove would not take effect until cache
// expiry. The Engine owner calls Flush on PolicyEvent receipt.
type Cache struct {
	mu  sync.RWMutex
	now func() time.Time
	m   map[cacheKey]cacheEntry
}

func NewCache() *Cache {
	return &Cache{
		now: time.Now,
		m:   make(map[cacheKey]cacheEntry),
	}
}

func (c *Cache) Get(caller, target, method string) (Result, bool) {
	c.mu.RLock()
	e, ok := c.m[cacheKey{caller, target, method}]
	c.mu.RUnlock()
	if !ok {
		return Result{}, false
	}
	if !c.now().Before(e.expires) {
		c.mu.Lock()
		delete(c.m, cacheKey{caller, target, method})
		c.mu.Unlock()
		return Result{}, false
	}
	return e.res, true
}

func (c *Cache) Put(caller, target, method string, r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= CacheCapacity {
		// Best-effort eviction: drop expired entries first.
		now := c.now()
		for k, v := range c.m {
			if !now.Before(v.expires) {
				delete(c.m, k)
			}
		}
		// If still over cap, drop a random subset (map iteration order
		// is randomized in Go).
		if len(c.m) >= CacheCapacity {
			i := 0
			for k := range c.m {
				delete(c.m, k)
				i++
				if i >= CacheCapacity/8 {
					break
				}
			}
		}
	}
	c.m[cacheKey{caller, target, method}] = cacheEntry{
		res:     r,
		expires: c.now().Add(CacheTTL),
	}
}

// Flush drops all entries (called on policy snapshot change).
func (c *Cache) Flush() {
	c.mu.Lock()
	c.m = make(map[cacheKey]cacheEntry)
	c.mu.Unlock()
}

// Len reports current entry count for tests / metrics.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}
