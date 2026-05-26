//go:build integration

package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/edin-space/edin-backend/internal/testutil"
	"github.com/stretchr/testify/require"
)

// aggregateDDL creates the feed.powerplay_hourly continuous aggregate in the test DB.
// Must match production migration 003_powerplay_hourly_aggregate.sql exactly.
const aggregateDDL = `
	CREATE MATERIALIZED VIEW IF NOT EXISTS feed.powerplay_hourly
	WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
	SELECT
		time_bucket('1 hour', received_at)                                           AS bucket,
		system_name,
		MAX((message_data->>'PowerplayStateReinforcement')::bigint)                  AS reinforcement,
		MAX((message_data->>'PowerplayStateUndermining')::bigint)                    AS undermining,
		last(message_data->>'PowerplayState',          received_at)                  AS powerplay_state,
		last(message_data->>'ControllingPower',         received_at)                 AS controlling_power,
		last(message_data->'PowerplayConflictProgress', received_at)                 AS conflict_progress_json,
		last(software_name,                             received_at)                 AS source,
		COUNT(*)                                                                      AS observations
	FROM feed.messages
	WHERE event_type = 'FSDJump'
	  AND system_name IS NOT NULL
	  AND message_data->>'PowerplayState' IS NOT NULL
	GROUP BY time_bucket('1 hour', received_at), system_name
	WITH NO DATA
`

// TestGetSystemHistory groups the three integration tests for GetSystemHistory.
func TestGetSystemHistory(t *testing.T) {
	t.Run("ReceivedAtGuard", func(t *testing.T) {
		pool, _ := testutil.StartEDDNTestDB(t)
		ctx := context.Background()
		now := time.Now().UTC()

		_, err := pool.Exec(ctx, aggregateDDL)
		require.NoError(t, err, "create aggregate view")

		// Row A: live player — received and jumped 1 hour ago. Must be returned.
		rowA := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-1 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Fortified", 1000, 100, now.Add(-1*time.Hour)),
		}

		// Row B: received 10h ago — within the 24h window, different hourly bucket from A.
		rowB := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-10 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Reinforced", 2000, 200, now.Add(-10*time.Hour)),
		}

		// Row C: received 35h ago — outside the 24h bucket window; excluded by aggregate query.
		rowC := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-35 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Stronghold", 45000, 300, now.Add(-35*time.Hour)),
		}

		// Row D: received 29h ago — within the 24h... wait, 29h > 24h.
		// The aggregate filters bucket >= NOW() - 24h, so a bucket at now-29h is outside.
		// Use 23h instead to keep it inside the window.
		rowD := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-23 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Stronghold", 47000, 7500, now.Add(-23*time.Hour)),
		}

		testutil.InsertEDDNMessages(t, pool, []testutil.EDDNMessageRow{rowA, rowB, rowC, rowD})

		_, err = pool.Exec(ctx,
			`CALL refresh_continuous_aggregate('feed.powerplay_hourly', $1::timestamptz, $2::timestamptz)`,
			now.Add(-40*time.Hour), now.Add(time.Hour),
		)
		require.NoError(t, err, "refresh aggregate")

		cs := store.NewCacheStore(nil)
		cs.SetEDDNClientForTest(pool)

		results, err := cs.GetSystemHistory(ctx, "Lave", 24)
		require.NoError(t, err)

		// Rows A (1000), B (2000), D (47000) should be present (all within 24h bucket window).
		// Row C (45000, received_at=now-35h) must be absent — bucket is outside 24h window.
		require.Len(t, results, 3, "expected exactly 3 results (row C excluded by 24h bucket window)")

		reinforcements := make(map[int64]bool)
		for _, r := range results {
			reinforcements[r.Reinforcement] = true
		}
		require.True(t, reinforcements[1000], "row A reinforcement (1000) must be present")
		require.True(t, reinforcements[2000], "row B reinforcement (2000) must be present")
		require.True(t, reinforcements[47000], "row D reinforcement (47000) must be present")
		require.False(t, reinforcements[45000], "row C reinforcement (45000) must be absent")
	})

	t.Run("ProductionFixture", func(t *testing.T) {
		pool, _ := testutil.StartEDDNTestDB(t)
		ctx := context.Background()

		_, err := pool.Exec(ctx, aggregateDDL)
		require.NoError(t, err, "create aggregate view")

		testutil.SeedEDDNMessagesFromCSV(t, pool, "feed_messages_alpha_centauri.csv")

		// Refresh the aggregate over a wide window to capture all fixture data.
		_, err = pool.Exec(ctx,
			`CALL refresh_continuous_aggregate('feed.powerplay_hourly', $1::timestamptz, $2::timestamptz)`,
			time.Now().UTC().Add(-30*24*time.Hour), time.Now().UTC().Add(time.Hour),
		)
		require.NoError(t, err, "refresh aggregate")

		cs := store.NewCacheStore(nil)
		cs.SetEDDNClientForTest(pool)

		results, err := cs.GetSystemHistory(ctx, "Alpha Centauri", 168)
		require.NoError(t, err)
		require.NotEmpty(t, results, "expected non-empty results for Alpha Centauri from production fixture")

		for i, r := range results {
			require.False(t, r.Timestamp.IsZero(), "result %d: Timestamp must not be zero", i)
			require.NotEmpty(t, r.PowerplayState, "result %d: PowerplayState must not be empty", i)
			require.NotEmpty(t, r.ControllingPower, "result %d: ControllingPower must not be empty", i)
		}
	})

	t.Run("EmptyForUnknownSystem", func(t *testing.T) {
		pool, _ := testutil.StartEDDNTestDB(t)
		ctx := context.Background()

		_, err := pool.Exec(ctx, aggregateDDL)
		require.NoError(t, err, "create aggregate view")

		testutil.SeedEDDNMessagesFromCSV(t, pool, "feed_messages_alpha_centauri.csv")

		_, err = pool.Exec(ctx,
			`CALL refresh_continuous_aggregate('feed.powerplay_hourly', $1::timestamptz, $2::timestamptz)`,
			time.Now().UTC().Add(-30*24*time.Hour), time.Now().UTC().Add(time.Hour),
		)
		require.NoError(t, err, "refresh aggregate")

		cs := store.NewCacheStore(nil)
		cs.SetEDDNClientForTest(pool)

		results, err := cs.GetSystemHistory(ctx, "Definitely Not A Real System ZZZXXX", 24)
		require.NoError(t, err)
		require.Empty(t, results, "expected empty results for unknown system")
	})
}

func TestGetSystemHistory_UsesAggregate(t *testing.T) {
	pool, _ := testutil.StartEDDNTestDB(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, aggregateDDL)
	require.NoError(t, err, "create aggregate view")

	now := time.Now().UTC().Truncate(time.Hour)
	systemName := "Lave"

	// Two rows in the same hour — aggregate must return MAX(reinforcement) = 99000
	testutil.InsertEDDNMessages(t, pool, []testutil.EDDNMessageRow{
		{
			ReceivedAt:  now.Add(-30 * time.Minute),
			SystemName:  systemName,
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData(systemName, "Nakato Kaine", "Stronghold", 75000, 5000, now.Add(-30*time.Minute)),
		},
		{
			ReceivedAt:  now.Add(-10 * time.Minute),
			SystemName:  systemName,
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData(systemName, "Nakato Kaine", "Stronghold", 99000, 8000, now.Add(-10*time.Minute)),
		},
	})

	_, err = pool.Exec(ctx,
		`CALL refresh_continuous_aggregate('feed.powerplay_hourly', $1::timestamptz, $2::timestamptz)`,
		now.Add(-2*time.Hour), now.Add(time.Hour),
	)
	require.NoError(t, err, "refresh aggregate")

	s := store.NewCacheStore(nil)
	s.SetEDDNClientForTest(pool)

	results, err := s.GetSystemHistory(ctx, systemName, 24)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	var found *store.SystemHistoryEntry
	for i := range results {
		if results[i].Reinforcement == 99000 {
			found = &results[i]
			break
		}
	}
	require.NotNil(t, found, "should find bucket with reinforcement=99000 (MAX of 75000 and 99000)")
	require.Equal(t, "Stronghold", found.PowerplayState)
	require.Equal(t, "Nakato Kaine", found.ControllingPower)
}

func TestGetExpansionHistory_UsesAggregate(t *testing.T) {
	pool, _ := testutil.StartEDDNTestDB(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, aggregateDDL)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Hour)
	systemName := "Lembava"

	conflictData := `[{"Power":"Nakato Kaine","ConflictProgress":0.55},{"Power":"Jerome Archer","ConflictProgress":0.45}]`
	testutil.InsertEDDNMessages(t, pool, []testutil.EDDNMessageRow{
		{
			ReceivedAt: now.Add(-20 * time.Minute),
			SystemName: systemName,
			EventType:  "FSDJump",
			MessageData: fmt.Sprintf(
				`{"event":"FSDJump","timestamp":%q,"StarSystem":%q,"PowerplayState":"Expansion","PowerplayConflictProgress":%s}`,
				now.Add(-20*time.Minute).UTC().Format(time.RFC3339), systemName, conflictData,
			),
		},
	})

	_, err = pool.Exec(ctx,
		`CALL refresh_continuous_aggregate('feed.powerplay_hourly', $1::timestamptz, $2::timestamptz)`,
		now.Add(-2*time.Hour), now.Add(time.Hour),
	)
	require.NoError(t, err)

	s := store.NewCacheStore(nil)
	s.SetEDDNClientForTest(pool)

	results, err := s.GetExpansionHistory(ctx, systemName, 24)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.InDelta(t, 0.55, results[0].ConflictProgress["Nakato Kaine"], 0.001)
	require.InDelta(t, 0.45, results[0].ConflictProgress["Jerome Archer"], 0.001)
}

func TestGetPowerplayHistory_UsesAggregate(t *testing.T) {
	pool, _ := testutil.StartEDDNTestDB(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, aggregateDDL)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Hour)
	systems := []string{"Sol", "Lave"}

	for _, sys := range systems {
		testutil.InsertEDDNMessages(t, pool, []testutil.EDDNMessageRow{
			{
				ReceivedAt:  now.Add(-2 * time.Hour),
				SystemName:  sys,
				EventType:   "FSDJump",
				MessageData: testutil.PowerplayMessageData(sys, "Nakato Kaine", "Stronghold", 100000, 5000, now.Add(-2*time.Hour)),
			},
		})
	}

	_, err = pool.Exec(ctx,
		`CALL refresh_continuous_aggregate('feed.powerplay_hourly', $1::timestamptz, $2::timestamptz)`,
		now.Add(-24*time.Hour), now.Add(time.Hour),
	)
	require.NoError(t, err)

	s := store.NewCacheStore(nil)
	s.SetEDDNClientForTest(pool)

	results, err := s.GetPowerplayHistory(ctx, systems, 7)
	require.NoError(t, err)
	require.Len(t, results, 2, "one entry per requested system")

	for _, r := range results {
		require.NotEmpty(t, r.History, "each system should have at least one day of history")
		require.Equal(t, int64(100000), r.History[0].Reinforcement)
	}
}
