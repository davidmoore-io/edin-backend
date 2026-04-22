package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateCommanderSchema applies all commander schema migrations from the
// internal/store/migrations/commander/ directory against the provided pool.
// Uses runtime.Caller to locate the migrations directory relative to this source file.
func MigrateCommanderSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "migrations", "commander")

	return applyMigrations(ctx, pool, migrationsDir)
}

// applyMigrations reads all .sql files from dir in sorted (lexicographic) order
// and executes each file's contents as a single Exec call against pool.
// If dir does not exist or contains no .sql files, applyMigrations returns nil.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	// Collect .sql files
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
		return nil
	}

	sort.Strings(files)

	for _, name := range files {
		path := filepath.Join(dir, name)
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", name, err)
		}

		if _, err := pool.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("execute migration %q: %w", name, err)
		}
	}

	return nil
}
