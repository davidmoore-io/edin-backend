//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/store"
	"github.com/edin-space/edin-backend/internal/testutil"
	"github.com/stretchr/testify/require"
)

// helper: fresh DB + migrated schema + PostgresStore
func newStore(t *testing.T) (store.Store, context.Context) {
	t.Helper()
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))
	return store.NewPostgresStore(pool), ctx
}

// ---------- Phase 1.4: GetPosted + UpsertPosted ----------

func TestPostgresStore_GetPosted_EmptyReturnsEmptyMap(t *testing.T) {
	s, ctx := newStore(t)

	got, err := s.GetPosted(ctx, "kaine-platinum-boom")
	require.NoError(t, err)
	require.NotNil(t, got, "GetPosted must return a non-nil empty map")
	require.Len(t, got, 0)
}

func TestPostgresStore_UpsertPosted_RoundTrip(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	m := store.PostedMessage{
		BindingID:  "kaine-platinum-boom",
		Identity:   "system:Sol",
		GuildID:    "1334858214533103646",
		ChannelID:  "1487248197582852321",
		MessageID:  "9999999999999999",
		StateHash:  "abc123",
		LastRender: json.RawMessage(`{"title":"Sol"}`),
		PostedAt:   now,
		LastSeenAt: now,
	}
	require.NoError(t, s.UpsertPosted(ctx, m))

	got, err := s.GetPosted(ctx, "kaine-platinum-boom")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Contains(t, got, "system:Sol")
	require.Equal(t, "9999999999999999", got["system:Sol"].MessageID)
	require.Equal(t, "abc123", got["system:Sol"].StateHash)
	require.Nil(t, got["system:Sol"].LastEditedAt, "fresh post: last_edited_at must be nil")
}

func TestPostgresStore_UpsertPosted_UpdatesExistingRow(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	later := now.Add(15 * time.Minute)

	first := store.PostedMessage{
		BindingID: "b1", Identity: "i1", GuildID: "g", ChannelID: "c",
		MessageID: "m1", StateHash: "h1", LastRender: json.RawMessage(`{}`),
		PostedAt: now, LastSeenAt: now,
	}
	require.NoError(t, s.UpsertPosted(ctx, first))

	second := first
	second.StateHash = "h2"
	second.LastEditedAt = &later
	second.LastSeenAt = later
	require.NoError(t, s.UpsertPosted(ctx, second))

	got, err := s.GetPosted(ctx, "b1")
	require.NoError(t, err)
	require.Equal(t, "h2", got["i1"].StateHash)
	require.NotNil(t, got["i1"].LastEditedAt)
	require.True(t, later.Equal(*got["i1"].LastEditedAt), "last_edited_at instant mismatch (expected %v, got %v)", later, *got["i1"].LastEditedAt)
}

func TestPostgresStore_GetPosted_OnlyReturnsRowsForRequestedBinding(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	for _, bid := range []string{"b1", "b2", "b3"} {
		m := store.PostedMessage{
			BindingID: bid, Identity: "x", GuildID: "g", ChannelID: "c",
			MessageID: "m", StateHash: "h", LastRender: json.RawMessage(`{}`),
			PostedAt: now, LastSeenAt: now,
		}
		require.NoError(t, s.UpsertPosted(ctx, m))
	}

	got, err := s.GetPosted(ctx, "b2")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Contains(t, got, "x")
}

// ---------- Phase 1.5: Mark/Update/Disable/Record methods ----------

func TestPostgresStore_MarkStruck_SetsStruckAt(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, s.UpsertPosted(ctx, store.PostedMessage{
		BindingID: "b", Identity: "i", GuildID: "g", ChannelID: "c",
		MessageID: "m", StateHash: "h", LastRender: json.RawMessage(`{}`),
		PostedAt: now, LastSeenAt: now,
	}))

	struck := now.Add(time.Hour)
	require.NoError(t, s.MarkStruck(ctx, "b", "i", struck))

	got, _ := s.GetPosted(ctx, "b")
	require.NotNil(t, got["i"].StruckAt)
	require.True(t, struck.Equal(*got["i"].StruckAt), "struck_at instant mismatch")
}

func TestPostgresStore_MarkUnstruck_ClearsStruckAndSetsUnstruck(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	struck := now.Add(time.Hour)
	unstruck := now.Add(2 * time.Hour)

	require.NoError(t, s.UpsertPosted(ctx, store.PostedMessage{
		BindingID: "b", Identity: "i", GuildID: "g", ChannelID: "c",
		MessageID: "m", StateHash: "h", LastRender: json.RawMessage(`{}`),
		PostedAt: now, LastSeenAt: now,
	}))
	require.NoError(t, s.MarkStruck(ctx, "b", "i", struck))
	require.NoError(t, s.MarkUnstruck(ctx, "b", "i", unstruck))

	got, _ := s.GetPosted(ctx, "b")
	require.Nil(t, got["i"].StruckAt, "struck_at must be cleared")
	require.NotNil(t, got["i"].UnstruckAt)
	require.True(t, unstruck.Equal(*got["i"].UnstruckAt), "unstruck_at instant mismatch")
}

func TestPostgresStore_UpdateLastSeen_BatchesIdentities(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, s.UpsertPosted(ctx, store.PostedMessage{
			BindingID: "bid", Identity: id, GuildID: "g", ChannelID: "c",
			MessageID: "m" + id, StateHash: "h", LastRender: json.RawMessage(`{}`),
			PostedAt: now, LastSeenAt: now,
		}))
	}

	later := now.Add(time.Hour)
	require.NoError(t, s.UpdateLastSeen(ctx, "bid", []string{"a", "c"}, later))

	got, _ := s.GetPosted(ctx, "bid")
	require.True(t, later.Equal(got["a"].LastSeenAt), "a updated")
	require.True(t, now.Equal(got["b"].LastSeenAt), "b unchanged")
	require.True(t, later.Equal(got["c"].LastSeenAt), "c updated")
}

func TestPostgresStore_UpdateLastSeen_EmptyIdentitiesIsNoop(t *testing.T) {
	s, ctx := newStore(t)
	require.NoError(t, s.UpdateLastSeen(ctx, "anything", nil, time.Now()))
	require.NoError(t, s.UpdateLastSeen(ctx, "anything", []string{}, time.Now()))
}

func TestPostgresStore_DisableBinding_SetsAllRows(t *testing.T) {
	s, ctx := newStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, id := range []string{"x", "y"} {
		require.NoError(t, s.UpsertPosted(ctx, store.PostedMessage{
			BindingID: "b", Identity: id, GuildID: "g", ChannelID: "c",
			MessageID: "m" + id, StateHash: "h", LastRender: json.RawMessage(`{}`),
			PostedAt: now, LastSeenAt: now,
		}))
	}

	at := now.Add(time.Hour)
	require.NoError(t, s.DisableBinding(ctx, "b", at))

	got, _ := s.GetPosted(ctx, "b")
	for _, id := range []string{"x", "y"} {
		require.NotNil(t, got[id].DisabledAt, id+" must have disabled_at set")
		require.True(t, at.Equal(*got[id].DisabledAt), id+" disabled_at instant mismatch")
	}

	disabled, err := s.IsBindingDisabled(ctx, "b")
	require.NoError(t, err)
	require.True(t, disabled)

	disabled, err = s.IsBindingDisabled(ctx, "other-binding")
	require.NoError(t, err)
	require.False(t, disabled)
}

func TestPostgresStore_RecordPollCycle_RoundTrip(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))
	s := store.NewPostgresStore(pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, s.RecordPollCycle(ctx, store.PollCycle{
		TickedAt:   now,
		BindingID:  "b",
		Status:     "success",
		Attempts:   1,
		ItemCount:  42,
		DurationMs: 180,
	}))

	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM discord.poll_cycles WHERE binding_id = 'b'`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestPostgresStore_RecordDiagnoseReport_RoundTrip(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))
	s := store.NewPostgresStore(pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	mid := "9999"
	require.NoError(t, s.RecordDiagnoseReport(ctx, store.DiagnoseReport{
		TriggeredAt:     now,
		BindingID:       "b",
		Report:          json.RawMessage(`{"memgraph":{"ok":false}}`),
		PostedMessageID: &mid,
	}))

	var report []byte
	var posted *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT report, posted_message_id FROM discord.diagnose_reports WHERE binding_id='b'`).
		Scan(&report, &posted))
	require.JSONEq(t, `{"memgraph":{"ok":false}}`, string(report))
	require.NotNil(t, posted)
	require.Equal(t, "9999", *posted)
}

func TestPostgresStore_LatestSuccessAt_ReturnsZeroWhenNoCycles(t *testing.T) {
	s, ctx := newStore(t)
	got, err := s.LatestSuccessAt(ctx, "never-polled")
	require.NoError(t, err)
	require.True(t, got.IsZero(), "no cycles → zero time")
}

func TestPostgresStore_LatestSuccessAt_IgnoresFailedCycles(t *testing.T) {
	s, ctx := newStore(t)
	t1 := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 26, 12, 15, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 26, 12, 30, 0, 0, time.UTC)

	require.NoError(t, s.RecordPollCycle(ctx, store.PollCycle{
		TickedAt: t1, BindingID: "b", Status: "success", Attempts: 1, ItemCount: 5, DurationMs: 100,
	}))
	require.NoError(t, s.RecordPollCycle(ctx, store.PollCycle{
		TickedAt: t2, BindingID: "b", Status: "failed", Attempts: 4, ItemCount: 0, DurationMs: 30000,
	}))
	require.NoError(t, s.RecordPollCycle(ctx, store.PollCycle{
		TickedAt: t3, BindingID: "b", Status: "success", Attempts: 1, ItemCount: 7, DurationMs: 120,
	}))

	got, err := s.LatestSuccessAt(ctx, "b")
	require.NoError(t, err)
	require.True(t, t3.Equal(got), "must return the latest success, ignoring intervening failure")
}

func TestPostgresStore_LatestSuccessAt_AcceptsEventStatus(t *testing.T) {
	s, ctx := newStore(t)
	t1 := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.RecordPollCycle(ctx, store.PollCycle{
		TickedAt: t1, BindingID: "b", Status: "event", Attempts: 1, ItemCount: 1, DurationMs: 0,
	}))

	got, err := s.LatestSuccessAt(ctx, "b")
	require.NoError(t, err)
	require.True(t, t1.Equal(got), "'event' status counts as success for healthcheck purposes")
}

func TestPostgresStore_LatestSuccessAt_BindingIsolation(t *testing.T) {
	s, ctx := newStore(t)
	t1 := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	require.NoError(t, s.RecordPollCycle(ctx, store.PollCycle{
		TickedAt: t1, BindingID: "a", Status: "success", Attempts: 1, ItemCount: 1, DurationMs: 100,
	}))

	got, err := s.LatestSuccessAt(ctx, "b")
	require.NoError(t, err)
	require.True(t, got.IsZero(), "binding b has no cycles → zero")
}
