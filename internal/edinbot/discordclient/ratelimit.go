package discordclient

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// PerChannelLimiter is a per-channel token bucket. Each channel gets its own
// rate.Limiter sized at (capacity tokens, refill window) — matching Discord's
// per-channel write limit (5 writes per 5 seconds for most channels).
//
// Concurrency: safe for use by multiple goroutines (the underlying
// rate.Limiter is concurrency-safe; the channel→limiter map is mu-guarded).
type PerChannelLimiter struct {
	capacity int
	window   time.Duration
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// NewPerChannelLimiter constructs a limiter where each channel can absorb a
// burst of capacity writes, refilling at capacity tokens per window.
func NewPerChannelLimiter(capacity int, window time.Duration) *PerChannelLimiter {
	return &PerChannelLimiter{
		capacity: capacity,
		window:   window,
		limiters: map[string]*rate.Limiter{},
	}
}

// Wait blocks until a token is available for channelID, or ctx is cancelled.
func (l *PerChannelLimiter) Wait(ctx context.Context, channelID string) error {
	l.mu.Lock()
	lim, ok := l.limiters[channelID]
	if !ok {
		// Refill rate = capacity / window (tokens per second).
		lim = rate.NewLimiter(rate.Limit(float64(l.capacity)/l.window.Seconds()), l.capacity)
		l.limiters[channelID] = lim
	}
	l.mu.Unlock()
	return lim.Wait(ctx)
}
