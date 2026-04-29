//go:build integration

package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/store"
	"github.com/edin-space/edin-backend/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true") //nolint:errcheck
	os.Exit(m.Run())
}

func TestMigrateDiscordSchema_IdempotentOnRerun(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateDiscordSchema(ctx, pool), "first run should succeed")
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool), "second run should succeed (idempotent)")
}

func TestMigrateDiscordSchema_CreatesPostedMessagesTable(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'discord' AND table_name = 'posted_messages'
		)`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "discord.posted_messages must exist after migration")
}

func TestMigrateDiscordSchema_CreatesPollCyclesHypertable(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	var isHypertable bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM timescaledb_information.hypertables
			WHERE hypertable_schema = 'discord' AND hypertable_name = 'poll_cycles'
		)`).Scan(&isHypertable)
	require.NoError(t, err)
	require.True(t, isHypertable, "discord.poll_cycles must be a hypertable")
}

func TestMigrateDiscordSchema_CreatesDiagnoseReportsHypertable(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	var isHypertable bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM timescaledb_information.hypertables
			WHERE hypertable_schema = 'discord' AND hypertable_name = 'diagnose_reports'
		)`).Scan(&isHypertable)
	require.NoError(t, err)
	require.True(t, isHypertable)
}

func TestMigrateDiscordSchema_PostedMessagesHasExpectedColumns(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	expected := map[string]string{
		"binding_id":     "text",
		"identity":       "text",
		"guild_id":       "text",
		"channel_id":     "text",
		"message_id":     "text",
		"state_hash":     "text",
		"last_render":    "jsonb",
		"posted_at":      "timestamp with time zone",
		"last_edited_at": "timestamp with time zone",
		"last_seen_at":   "timestamp with time zone",
		"struck_at":      "timestamp with time zone",
		"unstruck_at":    "timestamp with time zone",
		"disabled_at":    "timestamp with time zone",
	}

	rows, err := pool.Query(ctx, `
		SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'discord' AND table_name = 'posted_messages'`)
	require.NoError(t, err)
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var col, typ string
		require.NoError(t, rows.Scan(&col, &typ))
		got[col] = typ
	}
	require.Equal(t, expected, got, "posted_messages columns/types must match spec §4 exactly")
}

// ---------------------------------------------------------------------------
// watched_systems coverage (Phase 3 of the system-watch feature)
// ---------------------------------------------------------------------------

// TestMigrateDiscordSchema_CreatesWatchedSystemsTable pins the table's
// existence + the PRIMARY KEY shape that AddWatch's ErrAlreadyWatched
// branch depends on.
func TestMigrateDiscordSchema_CreatesWatchedSystemsTable(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	// Table exists.
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'discord' AND table_name = 'watched_systems'
		)`).Scan(&exists))
	require.True(t, exists, "discord.watched_systems must exist after migration")

	// PRIMARY KEY = (channel_id, system_slug). Without it the AddWatch
	// "already watched" rejection branch is unreachable.
	rows, err := pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
		WHERE i.indrelid = 'discord.watched_systems'::regclass AND i.indisprimary
		ORDER BY array_position(i.indkey::int[], a.attnum)`)
	require.NoError(t, err)
	defer rows.Close()
	var pk []string
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		pk = append(pk, col)
	}
	require.Equal(t, []string{"channel_id", "system_slug"}, pk,
		"watched_systems PRIMARY KEY enforces one-shared-message-per-system")

	// Guild-scan index exists (used by ListAllWatches's boot-recovery sweep).
	var idxCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'discord' AND indexname = 'idx_watched_systems_guild'
	`).Scan(&idxCount))
	require.Equal(t, 1, idxCount, "guild_id index must exist")
}

// TestAddWatch_UniqueViolationReturnsTyped exercises the path the Phase 4
// /watch handler depends on: a duplicate (channel_id, system_slug) insert
// must surface as ErrAlreadyWatched. Locks in the typed-error fix from
// the risk audit (PgError.Code over message-string matching).
func TestAddWatch_UniqueViolationReturnsTyped(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	st := store.NewPostgresStore(pool)
	at := mustParseTimeFor(t, "2026-04-29T12:00:00Z")

	w := store.WatchedSystem{
		GuildID: "g1", ChannelID: "c1",
		SystemSlug: "HIP61332", SystemName: "HIP 61332",
		MessageID: "m1", CreatedBy: "u1",
		WatchedAt: at, LastUpdatedAt: at,
		LastStateHash: "hash1", LastRender: []byte(`{}`),
	}
	require.NoError(t, st.AddWatch(ctx, w))

	// Second insert with the same (channel_id, system_slug). Different
	// message_id / user_id so a silent overwrite would be detectable below.
	w2 := w
	w2.MessageID = "m2"
	w2.CreatedBy = "u2"
	err := st.AddWatch(ctx, w2)
	require.ErrorIs(t, err, store.ErrAlreadyWatched,
		"duplicate insert must surface as ErrAlreadyWatched, not a wrapped pgx error")

	// Original row preserved — no silent overwrite.
	got, err := st.GetWatch(ctx, "c1", "HIP61332")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "m1", got.MessageID, "original row must be preserved")
	require.Equal(t, "u1", got.CreatedBy)
}

// TestRemoveWatch_Idempotent locks in the /unwatch contract: removing a
// non-existent watch returns (false, nil), not an error.
func TestRemoveWatch_Idempotent(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	st := store.NewPostgresStore(pool)

	deleted, err := st.RemoveWatch(ctx, "c1", "HIP61332")
	require.NoError(t, err)
	require.False(t, deleted, "removing a non-existent watch returns deleted=false, no error")

	// Add then remove → deleted=true.
	at := mustParseTimeFor(t, "2026-04-29T12:00:00Z")
	require.NoError(t, st.AddWatch(ctx, store.WatchedSystem{
		GuildID: "g1", ChannelID: "c1", SystemSlug: "HIP61332", SystemName: "HIP 61332",
		MessageID: "m1", CreatedBy: "u1",
		WatchedAt: at, LastUpdatedAt: at, LastStateHash: "h", LastRender: []byte(`{}`),
	}))
	deleted, err = st.RemoveWatch(ctx, "c1", "HIP61332")
	require.NoError(t, err)
	require.True(t, deleted, "removing an existing watch returns deleted=true")

	// And it really is gone — the row no longer exists.
	got, err := st.GetWatch(ctx, "c1", "HIP61332")
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestCountWatchesInChannel_RespectsChannelBoundary ensures the cap-check
// query is scoped to one channel. A bug here would leak a count from one
// channel into another (or count global watches).
func TestCountWatchesInChannel_RespectsChannelBoundary(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	st := store.NewPostgresStore(pool)
	at := mustParseTimeFor(t, "2026-04-29T12:00:00Z")

	mk := func(channel, slug string) store.WatchedSystem {
		return store.WatchedSystem{
			GuildID: "g1", ChannelID: channel, SystemSlug: slug, SystemName: slug,
			MessageID: "m-" + channel + "-" + slug, CreatedBy: "u1",
			WatchedAt: at, LastUpdatedAt: at, LastStateHash: "h", LastRender: []byte(`{}`),
		}
	}
	require.NoError(t, st.AddWatch(ctx, mk("c1", "Sol")))
	require.NoError(t, st.AddWatch(ctx, mk("c1", "HIP61332")))
	require.NoError(t, st.AddWatch(ctx, mk("c2", "Sol")))

	c1, err := st.CountWatchesInChannel(ctx, "c1")
	require.NoError(t, err)
	require.Equal(t, 2, c1)

	c2, err := st.CountWatchesInChannel(ctx, "c2")
	require.NoError(t, err)
	require.Equal(t, 1, c2)

	c3, err := st.CountWatchesInChannel(ctx, "c-nonexistent")
	require.NoError(t, err)
	require.Equal(t, 0, c3)
}

// TestUpdateWatchState_PreservesAppendOnceFields locks in the contract that
// UpdateWatchState only touches state-hash + render + last_updated_at,
// never the append-once watched_at / created_by / message_id.
func TestUpdateWatchState_PreservesAppendOnceFields(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	st := store.NewPostgresStore(pool)
	originalAt := mustParseTimeFor(t, "2026-04-29T12:00:00Z")

	require.NoError(t, st.AddWatch(ctx, store.WatchedSystem{
		GuildID: "g1", ChannelID: "c1", SystemSlug: "HIP61332", SystemName: "HIP 61332",
		MessageID: "m1", CreatedBy: "u1",
		WatchedAt: originalAt, LastUpdatedAt: originalAt,
		LastStateHash: "old-hash", LastRender: []byte(`{"v":1}`),
	}))

	newAt := mustParseTimeFor(t, "2026-04-29T13:00:00Z")
	require.NoError(t, st.UpdateWatchState(ctx, "c1", "HIP61332",
		"new-hash", []byte(`{"v":2}`), newAt))

	got, err := st.GetWatch(ctx, "c1", "HIP61332")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "new-hash", got.LastStateHash)
	// Postgres canonicalises JSONB on storage (adds space after the colon),
	// so compare via JSONEq rather than string equality.
	require.JSONEq(t, `{"v":2}`, string(got.LastRender))
	require.True(t, got.LastUpdatedAt.Equal(newAt))

	// Append-once fields preserved.
	require.Equal(t, "m1", got.MessageID, "message_id must not change on UpdateWatchState")
	require.Equal(t, "u1", got.CreatedBy, "created_by must not change on UpdateWatchState")
	require.True(t, got.WatchedAt.Equal(originalAt), "watched_at must not change on UpdateWatchState")
}

// ListAllWatches must return every row across every channel, sorted by
// system_slug for deterministic stagger ordering in the watcher loop.
func TestListAllWatches_OrdersBySlug(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))

	st := store.NewPostgresStore(pool)
	at := mustParseTimeFor(t, "2026-04-29T12:00:00Z")

	mk := func(channel, slug string) store.WatchedSystem {
		return store.WatchedSystem{
			GuildID: "g1", ChannelID: channel, SystemSlug: slug, SystemName: slug,
			MessageID: "m-" + channel + "-" + slug, CreatedBy: "u1",
			WatchedAt: at, LastUpdatedAt: at, LastStateHash: "h", LastRender: []byte(`{}`),
		}
	}
	// Insert deliberately out of slug order; the ORDER BY clause must
	// re-sort.
	require.NoError(t, st.AddWatch(ctx, mk("c1", "Sol")))
	require.NoError(t, st.AddWatch(ctx, mk("c2", "AntliaeSectorVJ-Rb4-4")))
	require.NoError(t, st.AddWatch(ctx, mk("c1", "HIP61332")))

	all, err := st.ListAllWatches(ctx)
	require.NoError(t, err)
	require.Len(t, all, 3)

	// Slugs should be in alphabetical order regardless of insertion order
	// or channel.
	slugs := []string{all[0].SystemSlug, all[1].SystemSlug, all[2].SystemSlug}
	require.Equal(t, []string{"AntliaeSectorVJ-Rb4-4", "HIP61332", "Sol"}, slugs)
}

func mustParseTimeFor(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return tt
}
