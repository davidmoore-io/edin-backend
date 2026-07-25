package httpapi

import (
	"context"
	"time"
)

// probeResult is one entry in the /admin/diagnose response.
type probeResult struct {
	OK             bool            `json:"ok"`
	LatencyMs      int             `json:"latency_ms,omitempty"`
	Error          string          `json:"error,omitempty"`
	ContainerState *containerState `json:"container_status,omitempty"`
	LastMessageAt  *time.Time      `json:"last_message_at,omitempty"`
}

type galaxyReaderProber interface {
	ProbeReader(ctx context.Context) error
}

type pgProber interface {
	Ping(ctx context.Context) error
}

type listenerLagProber interface {
	Lag(ctx context.Context) (time.Duration, error)
}

const eddnListenerStaleThreshold = 5 * time.Minute

func probeGalaxyReader(ctx context.Context, p galaxyReaderProber) probeResult {
	start := time.Now()
	err := p.ProbeReader(ctx)
	r := probeResult{LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		r.OK = false
		r.Error = err.Error()
		return r
	}
	r.OK = true
	return r
}

func probePostgres(ctx context.Context, p pgProber) probeResult {
	start := time.Now()
	err := p.Ping(ctx)
	r := probeResult{LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		r.OK = false
		r.Error = err.Error()
		return r
	}
	r.OK = true
	return r
}

func probeEDDNListener(ctx context.Context, p listenerLagProber) probeResult {
	start := time.Now()
	lag, err := p.Lag(ctx)
	r := probeResult{LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		r.OK = false
		r.Error = err.Error()
		return r
	}
	if lag > eddnListenerStaleThreshold {
		r.OK = false
		r.Error = "EDDN listener stale: last message " + lag.String() + " ago"
		return r
	}
	r.OK = true
	return r
}
