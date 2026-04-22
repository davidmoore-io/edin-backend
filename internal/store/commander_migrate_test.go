//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/edin-space/edin-backend/internal/testutil"
	"github.com/stretchr/testify/require"
)

// TestMain sets TESTCONTAINERS_RYUK_DISABLED before any test runs.
// Ryuk (the testcontainers reaper) requires /var/run/docker.sock which is not
// available in rootless Podman environments. Disabling it is safe for tests
// because each test starts its own container and registers a t.Cleanup teardown.
func TestMain(m *testing.M) {
	os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true") //nolint:errcheck
	os.Exit(m.Run())
}

func TestMigrateCommanderSchema_IdempotentOnRerun(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	err := store.MigrateCommanderSchema(ctx, pool)
	require.NoError(t, err, "first run should succeed")

	err = store.MigrateCommanderSchema(ctx, pool)
	require.NoError(t, err, "second run should succeed (idempotent)")
}

func TestMigrateCommanderSchema_CreatesHypertable(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.hypertables
		 WHERE hypertable_schema='commander' AND hypertable_name='journal_events'`,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "journal_events should be a hypertable")
}

func TestMigrateCommanderSchema_HypertableIsSingleDimension(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.dimensions
		 WHERE hypertable_schema='commander' AND hypertable_name='journal_events'`,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count,
		"journal_events hypertable must have exactly 1 dimension (time only, no space partitioning)")
}

func TestMigrateCommanderSchema_CompressionPolicyApplied(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	// TimescaleDB 2.18+ (columnstore/columnar compression) is mutually exclusive with
	// Row Level Security on the same hypertable — both cannot coexist. Since RLS is a
	// hard security requirement for multi-tenant isolation, we use a data-retention
	// policy instead of columnar compression for journal_events.
	//
	// This test verifies that the data-lifecycle policy (retention) is registered as a
	// scheduled job. The policy drops chunks older than 90 days, bounding storage growth.
	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.jobs
		 WHERE hypertable_schema='commander' AND hypertable_name='journal_events'`,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count,
		"a data-lifecycle (retention) policy job should be registered for journal_events")
}

func TestMigrateCommanderSchema_RLSPolicyExists(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	var count int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_policies
		 WHERE schemaname='commander' AND tablename='journal_events' AND policyname='commander_isolation'`,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count, "commander_isolation RLS policy should exist")
}

func TestMigrateCommanderSchema_FORCERLSEnabled(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	var forceRLS bool
	err := pool.QueryRow(ctx,
		`SELECT relforcerowsecurity
		 FROM pg_class
		 JOIN pg_namespace ON pg_namespace.oid = pg_class.relnamespace
		 WHERE nspname='commander' AND relname='journal_events'`,
	).Scan(&forceRLS)
	require.NoError(t, err)
	require.True(t, forceRLS, "FORCE ROW LEVEL SECURITY must be enabled on journal_events")
}

func TestMigrateCommanderSchema_RLSIsolation_SetLocalScoping(t *testing.T) {
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()

	require.NoError(t, store.MigrateCommanderSchema(ctx, pool))

	// Insert a commander for F001
	var cmdID1 string
	err := pool.QueryRow(ctx,
		`INSERT INTO commander.commanders (fid, cmdr_name) VALUES ('F001', 'Commander One')
		 RETURNING id`,
	).Scan(&cmdID1)
	require.NoError(t, err)

	// Insert a commander for F002
	var cmdID2 string
	err = pool.QueryRow(ctx,
		`INSERT INTO commander.commanders (fid, cmdr_name) VALUES ('F002', 'Commander Two')
		 RETURNING id`,
	).Scan(&cmdID2)
	require.NoError(t, err)

	eventData, err := json.Marshal(map[string]string{"event": "test"})
	require.NoError(t, err)

	now := time.Now().UTC()

	// Insert 3 events for F001
	for i := 0; i < 3; i++ {
		ts := now.Add(time.Duration(i) * time.Second)
		_, err = pool.Exec(ctx,
			`INSERT INTO commander.journal_events (commander_id, fid, timestamp, event_type, event_data)
			 VALUES ($1, $2, $3, $4, $5)`,
			cmdID1, "F001", ts, "TestEvent", eventData,
		)
		require.NoError(t, err)
	}

	// Insert 2 events for F002
	for i := 0; i < 2; i++ {
		ts := now.Add(time.Duration(i) * time.Second)
		_, err = pool.Exec(ctx,
			`INSERT INTO commander.journal_events (commander_id, fid, timestamp, event_type, event_data)
			 VALUES ($1, $2, $3, $4, $5)`,
			cmdID2, "F002", ts, "TestEvent", eventData,
		)
		require.NoError(t, err)
	}

	// Create a non-superuser role to test RLS. PostgreSQL superusers bypass RLS even
	// with FORCE ROW LEVEL SECURITY. The testcontainers pool user (testuser) is a
	// superuser, so we must SET LOCAL ROLE to a non-superuser to verify RLS isolation.
	_, err = pool.Exec(ctx, `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'test_rls_checker') THEN
				CREATE ROLE test_rls_checker LOGIN PASSWORD 'test_rls_checker_pw';
			END IF;
		END $$;
		GRANT USAGE ON SCHEMA commander TO test_rls_checker;
		GRANT SELECT ON commander.journal_events TO test_rls_checker;
	`)
	require.NoError(t, err, "should be able to create test_rls_checker role")

	// Step 2: In a transaction with SET LOCAL app.current_fid='F001' — only F001 rows
	var count int
	tx1, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx1.Rollback(ctx) //nolint:errcheck
	// Switch to non-superuser role so RLS is enforced
	_, err = tx1.Exec(ctx, "SET LOCAL ROLE test_rls_checker")
	require.NoError(t, err)
	_, err = tx1.Exec(ctx, "SET LOCAL app.current_fid = 'F001'")
	require.NoError(t, err)
	err = tx1.QueryRow(ctx,
		"SELECT COUNT(*) FROM commander.journal_events",
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 3, count, "F001 transaction should see only F001's 3 events")
	require.NoError(t, tx1.Rollback(ctx))

	// Step 3: In a transaction with SET LOCAL app.current_fid='F002' — only F002 rows
	tx2, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx2.Rollback(ctx) //nolint:errcheck
	_, err = tx2.Exec(ctx, "SET LOCAL ROLE test_rls_checker")
	require.NoError(t, err)
	_, err = tx2.Exec(ctx, "SET LOCAL app.current_fid = 'F002'")
	require.NoError(t, err)
	err = tx2.QueryRow(ctx,
		"SELECT COUNT(*) FROM commander.journal_events",
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 2, count, "F002 transaction should see only F002's 2 events")
	require.NoError(t, tx2.Rollback(ctx))

	// Step 4: Acquire a fresh connection with no current_fid set — must return 0 rows.
	// current_setting('app.current_fid', true) returns '' when unset; no fid equals ''.
	// Use a new transaction with the non-superuser role and NO current_fid SET.
	tx3, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx3.Rollback(ctx) //nolint:errcheck
	_, err = tx3.Exec(ctx, "SET LOCAL ROLE test_rls_checker")
	require.NoError(t, err)
	// Do NOT set app.current_fid — RLS should return 0 rows
	err = tx3.QueryRow(ctx,
		"SELECT COUNT(*) FROM commander.journal_events",
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count,
		"non-superuser transaction with no FID set must return 0 rows (RLS filters all)")
	require.NoError(t, tx3.Rollback(ctx))
}
