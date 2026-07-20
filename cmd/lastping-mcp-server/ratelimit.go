package main

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// tokenRateLimiter is a bounded LRU sliding-window limiter keyed by a hash of
// the caller's bearer token. It mirrors the ingest hot-path limiter
// (ingest/ratelimit.go): in-process state is correct for a single Fargate task
// and degrades gracefully (per-task windows → effective cap × taskCount) if the
// mcp service ever scales beyond one task — acceptable for abuse prevention.
type tokenRateLimiter struct {
	mu       sync.Mutex
	maxSlots int
	cap      int
	window   time.Duration
	items    map[string]*list.Element
	lru      *list.List
}

type rlEntry struct {
	key         string
	windowStart time.Time
	count       int
}

func newTokenRateLimiter(maxSlots, cap int, window time.Duration) *tokenRateLimiter {
	return &tokenRateLimiter{
		maxSlots: maxSlots,
		cap:      cap,
		window:   window,
		items:    make(map[string]*list.Element, maxSlots),
		lru:      list.New(),
	}
}

// Allow returns true when the request for key is within the cap for the current
// window; false (without counting) when the cap is exceeded. Windows reset
// automatically when they expire.
func (r *tokenRateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if el, ok := r.items[key]; ok {
		entry := el.Value.(*rlEntry)
		if now.Before(entry.windowStart.Add(r.window)) {
			entry.count++
			r.lru.MoveToFront(el)
			return entry.count <= r.cap
		}
		// Window expired: start fresh; this request is the first.
		entry.windowStart = now
		entry.count = 1
		r.lru.MoveToFront(el)
		return true
	}

	// New key: evict the LRU entry if at capacity.
	if len(r.items) >= r.maxSlots {
		if back := r.lru.Back(); back != nil {
			evicted := r.lru.Remove(back).(*rlEntry)
			delete(r.items, evicted.key)
		}
	}
	entry := &rlEntry{key: key, windowStart: now, count: 1}
	r.items[key] = r.lru.PushFront(entry)
	return true
}

// hashToken maps a bearer token to a fixed-width limiter key so raw tokens are
// never retained as map keys. A 64-bit SHA-256 prefix is collision-safe enough
// for rate-limit buckets.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}
