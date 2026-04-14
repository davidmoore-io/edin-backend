//go:build integration

package testutil_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/testutil"
)

func TestStartTestDB_StartsAndMigrates(t *testing.T) {
	pool, cleanup := testutil.StartTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Pool should be pingable
	err := pool.Ping(ctx)
	require.NoError(t, err, "pool.Ping should succeed")

	// commanders table should exist (created by migration)
	var tableName string
	err = pool.QueryRow(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name='commanders'",
	).Scan(&tableName)
	require.NoError(t, err, "commanders table should exist after migrations")
	require.Equal(t, "commanders", tableName)
}

func TestStartTestDB_TimescaleDBExtensionEnabled(t *testing.T) {
	pool, cleanup := testutil.StartTestDB(t)
	defer cleanup()

	ctx := context.Background()

	var extName string
	err := pool.QueryRow(ctx,
		"SELECT extname FROM pg_extension WHERE extname='timescaledb'",
	).Scan(&extName)
	require.NoError(t, err, "timescaledb extension should be present")
	require.Equal(t, "timescaledb", extName)
}

func TestStartTestDB_CleanupDropsData(t *testing.T) {
	var connString string

	func() {
		pool, cleanup := testutil.StartTestDB(t)
		defer cleanup()

		ctx := context.Background()

		// Insert a commander row
		_, err := pool.Exec(ctx,
			"INSERT INTO commanders (fid, name) VALUES ($1, $2)",
			"F9999999", "TestPilot",
		)
		require.NoError(t, err, "insert should succeed")

		// Capture connection string for later reconnect attempt
		connString = pool.Config().ConnString()
	}()

	// After cleanup, attempting to connect to the (now-terminated) container should fail.
	// pgxpool.New is lazy and doesn't connect immediately; use Ping to force a real dial.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, connString)
	if err == nil {
		// Pool created (lazy); now force an actual connection attempt
		err = pool.Ping(ctx)
		pool.Close()
	}
	require.Error(t, err, "should not be able to connect after cleanup")
}

func TestFixtures_CreateCommander(t *testing.T) {
	pool, cleanup := testutil.StartTestDB(t)
	defer cleanup()

	ctx := context.Background()

	testutil.CreateTestCommander(t, pool, "F1234567", "Jameson")

	var name string
	err := pool.QueryRow(ctx,
		"SELECT name FROM commanders WHERE fid=$1", "F1234567",
	).Scan(&name)
	require.NoError(t, err, "should be able to read back the inserted commander")
	require.Equal(t, "Jameson", name)
}

func TestFixtures_CreateJournalEvents(t *testing.T) {
	pool, cleanup := testutil.StartTestDB(t)
	defer cleanup()

	ctx := context.Background()

	testutil.CreateTestCommander(t, pool, "F7654321", "Sidewinder")
	timestamps := testutil.CreateTestJournalEvents(t, pool, "F7654321", "FSDJump", 5)

	require.Len(t, timestamps, 5, "should return 5 timestamps")

	var count int
	err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM journal_events WHERE fid=$1 AND event_type=$2",
		"F7654321", "FSDJump",
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 5, count, "should have 5 journal events in DB")
}
