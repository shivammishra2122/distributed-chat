package ratelimit

import (
	"sync"
	"time"
)

// Limiter implements a per-user token bucket rate limiter
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64 // tokens per second
	burst   int     // max tokens
}

type bucket struct {
	tokens   float64
	lastTime time.Time
}

// NewLimiter creates a rate limiter with the given rate (tokens/sec) and burst size
func NewLimiter(rate float64, burst int) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		rate:    rate,
		burst:   burst,
	}
}

// Allow checks if a user is within rate limits. Returns true if allowed.
func (l *Limiter) Allow(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, exists := l.buckets[user]
	if !exists {
		b = &bucket{
			tokens:   float64(l.burst),
			lastTime: now,
		}
		l.buckets[user] = b
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastTime).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.lastTime = now

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}
	return false
}

// Cleanup removes stale buckets (call periodically)
func (l *Limiter) Cleanup(maxAge time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	for user, b := range l.buckets {
		if now.Sub(b.lastTime) > maxAge {
			delete(l.buckets, user)
		}
	}
}
