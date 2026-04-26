package httpapi

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// listenerLagAdapter wraps an *pgxpool.Pool for the eddn_raw database and
// queries `feed.messages` for the most recent received_at timestamp.
// Implements listenerLagProber.
type listenerLagAdapter struct {
	pool *pgxpool.Pool
}

func newListenerLagAdapter(pool *pgxpool.Pool) *listenerLagAdapter {
	return &listenerLagAdapter{pool: pool}
}

func (a *listenerLagAdapter) Lag(ctx context.Context) (time.Duration, error) {
	if a.pool == nil {
		return 0, fmt.Errorf("eddn_raw pool not configured")
	}
	var lastReceived time.Time
	err := a.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(received_at), TIMESTAMPTZ '1970-01-01')
		FROM feed.messages`).Scan(&lastReceived)
	if err != nil {
		return 0, fmt.Errorf("query last received_at: %w", err)
	}
	if lastReceived.IsZero() || lastReceived.Year() <= 1970 {
		return 0, fmt.Errorf("no messages in feed")
	}
	return time.Since(lastReceived), nil
}
