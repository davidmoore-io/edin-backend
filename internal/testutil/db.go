// Package testutil provides integration test helpers for EDIN backend tests.
// It requires real infrastructure (TimescaleDB via testcontainers) and is
// gated behind the "integration" build tag.
package testutil

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	testDBImage    = "timescale/timescaledb:latest-pg16"
	testDBName     = "testdb"
	testDBUser     = "testuser"
	testDBPassword = "testpass"
)

// StartTestDB spins up a TimescaleDB container, runs migrations from
// db/migrations/, and returns a ready pgxpool.Pool plus a cleanup func.
// The cleanup func is also registered with t.Cleanup for automatic teardown.
func StartTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()

	ctx := context.Background()

	ctr, err := postgres.Run(ctx, testDBImage,
		postgres.WithDatabase(testDBName),
		postgres.WithUsername(testDBUser),
		postgres.WithPassword(testDBPassword),
		// Disable timescaledb-tune: it panics when cgroup memory info is unavailable
		// (rootless Podman / container environments without full cgroup v2 delegation).
		// NO_TS_TUNE is the official env var checked by 001_timescaledb_tune.sh.
		testcontainers.WithEnv(map[string]string{
			"NO_TS_TUNE": "1",
		}),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start timescaledb container: %v", err)
	}

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = ctr.Terminate(ctx)
		t.Fatalf("get container connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		_ = ctr.Terminate(ctx)
		t.Fatalf("create pgxpool: %v", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = ctr.Terminate(ctx)
		t.Fatalf("ping database: %v", err)
	}

	// Enable TimescaleDB extension before running migrations.
	// The migration file also contains CREATE EXTENSION IF NOT EXISTS timescaledb,
	// but timescaledb must be pre-loaded via shared_preload_libraries which the
	// official image handles — we call CREATE EXTENSION here to be explicit and
	// ensure it's active before create_hypertable is called.
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		pool.Close()
		_ = ctr.Terminate(ctx)
		t.Fatalf("enable timescaledb extension: %v", err)
	}

	// Locate db/migrations/ relative to this file's source location so the
	// harness works regardless of the working directory when tests are run.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	migrationsDir := filepath.Join(repoRoot, "db", "migrations")

	if err := ApplyMigrations(ctx, pool, migrationsDir); err != nil {
		pool.Close()
		_ = ctr.Terminate(ctx)
		t.Fatalf("apply migrations: %v", err)
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			pool.Close()
			termCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := ctr.Terminate(termCtx); err != nil {
				t.Logf("warning: failed to terminate container: %v", err)
			}
		})
	}

	t.Cleanup(cleanup)

	return pool, cleanup
}

