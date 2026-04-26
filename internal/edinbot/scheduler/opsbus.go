// Package scheduler runs the per-binding goroutines that drive PollFeature
// ticks and EventDrivenFeature subscriptions. It also hosts OpsBus — the
// in-process pub/sub channel through which PollFeatures publish OpsEvents
// and the ops-health-alerts feature subscribes to render them.
package scheduler

import (
	"context"
	"sync"
	"time"
)

// OpsEvent is one observation of cycle health. Published by PollFeatures when
// they exhaust their retry budget or recover after a previous failure.
type OpsEvent struct {
	BindingID  string         // canonical YAML id of the failing/recovering binding
	Reason     string         // "poll_exhausted" | "poll_recovered" | "binding_unreachable" | …
	Attempts   int            // 1..N for failures; 1 for recoveries
	Error      error          // nil for recoveries
	Report     map[string]any // structured /admin/diagnose report; nil if diagnose itself failed or wasn't called
	OccurredAt time.Time      // canonical timestamp; ops-health-alerts uses this for outage `started_at`
}

// OpsBus is a fan-out pub/sub. Every subscriber gets every published event,
// asynchronously, with non-blocking delivery (slow subscribers drop events
// rather than blocking the publisher). Suitable for ops alerting where a
// dropped event is acceptable but a stalled hot path is not.
type OpsBus struct {
	mu   sync.Mutex
	subs []chan OpsEvent
}

func NewOpsBus() *OpsBus { return &OpsBus{} }

// Subscribe returns a channel that receives every event published from now
// until ctx is cancelled. On cancel, the channel is closed.
func (b *OpsBus) Subscribe(ctx context.Context) <-chan OpsEvent {
	ch := make(chan OpsEvent, 32) // small buffer; drop-if-full beyond
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, c := range b.subs {
			if c == ch {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}()
	return ch
}

// Publish delivers e to every subscriber. Non-blocking: if a subscriber's
// buffer is full the event is dropped for that subscriber.
func (b *OpsBus) Publish(e OpsEvent) {
	b.mu.Lock()
	subs := make([]chan OpsEvent, len(b.subs))
	copy(subs, b.subs)
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// dropped — slow subscriber
		}
	}
}
