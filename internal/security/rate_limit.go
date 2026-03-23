package security

import (
	"sync"
	"time"
)

// TokenBucket implements a basic rate limiter.
type TokenBucket struct {
	mu       sync.Mutex
	capacity int
	tokens   int
	reset    time.Time
	window   time.Duration
}

// NewTokenBucket constructs a new bucket.
func NewTokenBucket(capacity int, window time.Duration) *TokenBucket {
	return &TokenBucket{
		capacity: capacity,
		tokens:   capacity,
		reset:    time.Now().Add(window),
		window:   window,
	}
}

// Allow determines if an action is permitted at the current time.
func (b *TokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	if now.After(b.reset) {
		b.tokens = b.capacity
		b.reset = now.Add(b.window)
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
