package testutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyMigrations reads all .sql files from dir in sorted (lexicographic) order
// and executes each file's contents as a single Exec call against pool.
// If dir does not exist or contains no .sql files, ApplyMigrations returns nil.
func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool, dir string) error {
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
