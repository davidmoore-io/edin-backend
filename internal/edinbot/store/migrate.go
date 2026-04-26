package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrateDiscordSchema runs every SQL file in migrations/ in lexicographic order,
// idempotently. Each file is treated as one transaction. Pattern mirrors
// internal/store/commander_migrate.go.
func MigrateDiscordSchema(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		content, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := runOne(ctx, pool, name, string(content)); err != nil {
			return err
		}
	}
	return nil
}

func runOne(ctx context.Context, pool *pgxpool.Pool, name, sql string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", name, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, sql); err != nil {
		return fmt.Errorf("exec %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}
