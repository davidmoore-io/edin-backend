//go:build integration

package store_test

import (
	"context"
	"os"
	"testing"

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
