package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// commanderMigrations embeds the .sql migration files so they travel with the
// compiled binary. The previous runtime.Caller(0) approach was broken outside
// `go test` / `go run` — in a scratch-layered container there is no source tree
// for Caller to point at, and migrations silently didn't run.
//
//go:embed migrations/commander/*.sql
var commanderMigrations embed.FS

const commanderMigrationsDir = "migrations/commander"

// MigrateCommanderSchema applies every embedded .sql file under
// migrations/commander/ against the provided pool in lexicographic order.
//
// Each .sql file is executed as a single Exec call — Postgres processes
// multi-statement input as an implicit transaction per statement (pgx v5), and
// the migrations themselves use IF NOT EXISTS / DROP POLICY IF EXISTS / DO
// blocks so re-running is safe.
//
// The pool must connect as a role with owner privileges (CREATE SCHEMA, CREATE
// TABLE, create_hypertable, GRANT, ALTER DEFAULT PRIVILEGES, ENABLE ROW LEVEL
// SECURITY). Typically the TimescaleDB superuser. Do NOT reuse the runtime
// edin_cmd_writer pool — those grants are deliberately restricted to the point
// where migrations would fail.
func MigrateCommanderSchema(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := fs.ReadDir(commanderMigrations, commanderMigrationsDir)
	if err != nil {
		return fmt.Errorf("read embedded migrations dir %q: %w", commanderMigrationsDir, err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".sql" {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("no .sql migrations found in %q — build embedded incorrectly?", commanderMigrationsDir)
	}

	sort.Strings(files)

	for _, name := range files {
		path := commanderMigrationsDir + "/" + name
		contents, err := commanderMigrations.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded migration %q: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("execute migration %q: %w", name, err)
		}
	}

	return nil
}
