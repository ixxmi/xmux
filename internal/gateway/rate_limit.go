package gateway

import (
	"net/http"
	"sync"
	"time"
)

// rateBucket implements a tiny token bucket: at most N events per window per
// key. Backing map is in-memory; not shared across replicas. For multi-replica
// gateways, swap to Redis later — until then this is enough to throttle
// abusive clients on a single node.
type rateBucket struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	events map[string][]time.Time
}

func newRateBucket(limit int, window time.Duration) *rateBucket {
	return &rateBucket{
		limit:  limit,
		window: window,
		events: make(map[string][]time.Time),
	}
}

func (b *rateBucket) Allow(key string) bool {
	if b == nil || b.limit <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-b.window)
	b.mu.Lock()
	defer b.mu.Unlock()
	events := b.events[key]
	filtered := events[:0]
	for _, t := range events {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) >= b.limit {
		b.events[key] = filtered
		return false
	}
	filtered = append(filtered, now)
	b.events[key] = filtered
	return true
}

// rateBuckets holds the buckets the gateway uses for various flows.
type rateBuckets struct {
	register      *rateBucket
	forgot        *rateBucket
	resendVerify  *rateBucket
	oauthCallback *rateBucket
}

func newRateBuckets() *rateBuckets {
	return &rateBuckets{
		register:      newRateBucket(5, time.Minute),
		forgot:        newRateBucket(5, time.Minute),
		resendVerify:  newRateBucket(3, time.Minute),
		oauthCallback: newRateBucket(20, time.Minute),
	}
}

// rateLimit is a small middleware helper: returns 429 if the bucket rejects.
// The key is the client IP; if you need account-keyed limits, hash the
// account into the key before calling.
func rateLimit(bucket *rateBucket, key string, w http.ResponseWriter) bool {
	if bucket == nil {
		return true
	}
	if bucket.Allow(key) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	http.Error(w, "rate limit exceeded; try again later", http.StatusTooManyRequests)
	return false
}
