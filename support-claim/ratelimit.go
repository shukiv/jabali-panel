package main

import (
	"sync"
	"time"
)

// rateLimiter is a tiny per-key (per-IP) token bucket: `perMin` tokens that
// refill continuously, capped at `perMin`. Enough to blunt claim-issue spam
// without a dependency. Not a security boundary — redemption auth is.
type rateLimiter struct {
	mu     sync.Mutex
	perMin int
	rate   float64 // tokens per second
	bucket map[string]*tokenBucket
	now    func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMin int) *rateLimiter {
	if perMin <= 0 {
		perMin = 30
	}
	return &rateLimiter{
		perMin: perMin,
		rate:   float64(perMin) / 60.0,
		bucket: map[string]*tokenBucket{},
		now:    time.Now,
	}
}

// allow consumes one token for key, returning false when the bucket is empty.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	b, ok := rl.bucket[key]
	if !ok {
		rl.bucket[key] = &tokenBucket{tokens: float64(rl.perMin) - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > float64(rl.perMin) {
		b.tokens = float64(rl.perMin)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
