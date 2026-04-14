package testutil

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// CreateTestCommander inserts a commander row. fid must start with "F".
func CreateTestCommander(t *testing.T, pool *pgxpool.Pool, fid, name string) {
	t.Helper()

	require.True(t, len(fid) > 0 && fid[0] == 'F', "fid must start with 'F', got: %q", fid)

	ctx := context.Background()
	_, err := pool.Exec(ctx,
		"INSERT INTO commanders (fid, name) VALUES ($1, $2)",
		fid, name,
	)
	require.NoError(t, err, "CreateTestCommander: insert failed for fid=%q name=%q", fid, name)
}

// CreateTestJournalEvents inserts n events for the given fid with the given event type.
// Returns the inserted timestamps.
func CreateTestJournalEvents(t *testing.T, pool *pgxpool.Pool, fid, eventType string, n int) []time.Time {
	t.Helper()

	ctx := context.Background()
	timestamps := make([]time.Time, 0, n)

	// Space events 1 second apart so TimescaleDB hypertable ordering is deterministic.
	base := time.Now().UTC().Truncate(time.Second)

	payload, err := json.Marshal(map[string]string{
		"event": eventType,
		"fid":   fid,
	})
	require.NoError(t, err, "CreateTestJournalEvents: marshal event_data")

	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		_, err := pool.Exec(ctx,
			`INSERT INTO journal_events (time, fid, event_type, event_data)
			 VALUES ($1, $2, $3, $4)`,
			ts, fid, eventType, payload,
		)
		require.NoError(t, err, "CreateTestJournalEvents: insert event %d for fid=%q", i, fid)
		timestamps = append(timestamps, ts)
	}

	return timestamps
}
