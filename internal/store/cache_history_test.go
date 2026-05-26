//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/edin-space/edin-backend/internal/testutil"
	"github.com/stretchr/testify/require"
)

// TestGetSystemHistory groups the three integration tests for GetSystemHistory.
func TestGetSystemHistory(t *testing.T) {
	t.Run("ReceivedAtGuard", func(t *testing.T) {
		pool, _ := testutil.StartEDDNTestDB(t)
		ctx := context.Background()
		now := time.Now().UTC()

		// Row A: live player — received and jumped 1 hour ago. Must be returned.
		rowA := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-1 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Fortified", 1000, 100, now.Add(-1*time.Hour)),
		}

		// Row B: late journal upload — event 1h ago but EDDN only saw it 10h ago.
		// 10h is within the 6-hour-per-hour guard window for a 24h query (cutoff-6h).
		// Wait: cutoff = now-24h; guard = cutoff-6h = now-30h. received_at = now-10h >= now-30h → included.
		rowB := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-10 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Reinforced", 2000, 200, now.Add(-1*time.Hour)),
		}

		// Row C: very late upload — received 35h ago. Guard is now-30h, so this must be excluded.
		rowC := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-35 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Stronghold", 45000, 300, now.Add(-1*time.Hour)),
		}

		// Row D: received_at = now-29h — just inside the 30h guard window.
		// For a 24h query: guard = cutoff - 6h = (now-24h) - 6h = now-30h.
		// now-29h >= now-30h → included. This proves the boundary precision.
		rowD := testutil.EDDNMessageRow{
			ReceivedAt:  now.Add(-29 * time.Hour),
			SystemName:  "Lave",
			EventType:   "FSDJump",
			MessageData: testutil.PowerplayMessageData("Lave", "Nakato Kaine", "Stronghold", 47000, 7500, now.Add(-1*time.Hour)),
		}

		testutil.InsertEDDNMessages(t, pool, []testutil.EDDNMessageRow{rowA, rowB, rowC, rowD})

		cs := store.NewCacheStore(nil)
		cs.SetEDDNClientForTest(pool)

		results, err := cs.GetSystemHistory(ctx, "Lave", 24)
		require.NoError(t, err)

		// Rows A (1000), B (2000), D (47000) should be present.
		// Row C (45000, received_at=now-35h) must be absent — outside the now-30h guard window.
		require.Len(t, results, 3, "expected exactly 3 results (row C excluded by received_at guard)")

		// Verify the reinforcement values — order is ASC by event timestamp.
		// Rows A, B, and D all have event_time = now-1h, so ordering may vary; collect into a set.
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

		testutil.SeedEDDNMessagesFromCSV(t, pool, "feed_messages_alpha_centauri.csv")

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

		testutil.SeedEDDNMessagesFromCSV(t, pool, "feed_messages_alpha_centauri.csv")

		cs := store.NewCacheStore(nil)
		cs.SetEDDNClientForTest(pool)

		results, err := cs.GetSystemHistory(ctx, "Definitely Not A Real System ZZZXXX", 24)
		require.NoError(t, err)
		require.Empty(t, results, "expected empty results for unknown system")
	})
}
