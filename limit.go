package main

import (
	"sync"
	"time"
)

// bucket is one fixed window counter.
type bucket struct {
	count int
	until time.Time
}

// Limiter counts events for each key inside a fixed time window. The program
// is one process, so an in-memory map is sufficient.
type Limiter struct {
	mtx   sync.Mutex
	items map[string]*bucket
}

// NewLimiter makes a limiter and starts its cleanup loop.
func NewLimiter() *Limiter {
	lim := &Limiter{items: make(map[string]*bucket)}
	go lim.cleanup()
	return lim
}

// Allow reports whether the event is inside the limit, and counts the event.
func (lim *Limiter) Allow(key string, limit int, window time.Duration) bool {
	now := time.Now()
	lim.mtx.Lock()
	defer lim.mtx.Unlock()

	buk, found := lim.items[key]
	if !found || now.After(buk.until) {
		lim.items[key] = &bucket{count: 1, until: now.Add(window)}
		return true
	}
	if buk.count >= limit {
		return false
	}
	buk.count++
	return true
}

func (lim *Limiter) cleanup() {
	for range time.Tick(5 * time.Minute) {
		now := time.Now()
		lim.mtx.Lock()
		for key, buk := range lim.items {
			if now.After(buk.until) {
				delete(lim.items, key)
			}
		}
		lim.mtx.Unlock()
	}
}

// SeenCache limits how often the program writes the last-seen time of a
// session. Without this, every request causes a database write.
type SeenCache struct {
	mtx    sync.Mutex
	items  map[int64]time.Time
	window time.Duration
}

// NewSeenCache makes a cache with the given minimum interval between writes.
func NewSeenCache(window time.Duration) *SeenCache {
	cache := &SeenCache{
		items:  make(map[int64]time.Time),
		window: window,
	}
	go cache.cleanup()
	return cache
}

// Should reports whether the caller must write the last-seen time now.
func (cache *SeenCache) Should(sid int64) bool {
	now := time.Now()
	cache.mtx.Lock()
	defer cache.mtx.Unlock()

	last, found := cache.items[sid]
	if found && now.Sub(last) < cache.window {
		return false
	}
	cache.items[sid] = now
	return true
}

// Forget removes a session from the cache after a logout.
func (cache *SeenCache) Forget(sid int64) {
	cache.mtx.Lock()
	delete(cache.items, sid)
	cache.mtx.Unlock()
}

func (cache *SeenCache) cleanup() {
	for range time.Tick(30 * time.Minute) {
		limit := time.Now().Add(-2 * time.Hour)
		cache.mtx.Lock()
		for sid, last := range cache.items {
			if last.Before(limit) {
				delete(cache.items, sid)
			}
		}
		cache.mtx.Unlock()
	}
}

