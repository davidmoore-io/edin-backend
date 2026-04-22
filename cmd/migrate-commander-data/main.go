// Command migrate-commander-data migrates commander journal data from a source
// PostgreSQL database to a target database configured with the new
// commander.journal_events schema and RLS roles.
//
// The source connection should use the edin_migrator role (BYPASSRLS) so it
// can read all rows regardless of the RLS policy. The target connection uses
// edin_cmd_writer (enforced RLS) for writes.
//
// Usage:
//
//	go run ./cmd/migrate-commander-data \
//	  --source-dsn "postgres://edin_migrator:pw@10.8.0.6:5432/edin?sslmode=disable" \
//	  --target-dsn "postgres://edin_cmd_writer:pw@10.8.0.6:5432/edin?sslmode=disable" \
//	  [--batch-size 1000] [--dry-run]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SourceCommander holds the identifiers for a commander in the source database.
type SourceCommander struct {
	FID      string
	Name     string
	Platform string
}

// SourceReader reads commander data from the migration source.
type SourceReader interface {
	// ListCommanders returns all commanders in the source, ordered by FID.
	ListCommanders(ctx context.Context) ([]SourceCommander, error)
	// ReadEvents returns up to limit events for fid in ascending timestamp order,
	// starting at row offset. Returns an empty slice when exhausted.
	ReadEvents(ctx context.Context, fid string, offset, limit int) ([]store.JournalEvent, error)
}

// Config holds migration run parameters.
type Config struct {
	BatchSize int
	DryRun    bool
}

// MigrateResult holds aggregate statistics from a completed migration run.
type MigrateResult struct {
	Commanders int // number of commanders processed
	Inserted   int // events successfully written to target
	Duplicates int // events already present in target (ON CONFLICT DO NOTHING)
	Skipped    int // events skipped due to validation failure (e.g. empty EventType)
}

// migrate reads all commander data from src and writes it to dst.
//
// Processing order:
//  1. List all commanders from source.
//  2. For each commander, upsert the commander record in target.
//  3. For each commander, stream events from source in batches of cfg.BatchSize,
//     filtering out invalid events, and insert into target.
//
// When cfg.DryRun is true the source is fully read but no writes occur.
func migrate(ctx context.Context, cfg Config, src SourceReader, dst store.CommanderRepository, logger *slog.Logger) (MigrateResult, error) {
	commanders, err := src.ListCommanders(ctx)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("list commanders: %w", err)
	}

	var result MigrateResult
	result.Commanders = len(commanders)

	for _, cmdr := range commanders {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		if !cfg.DryRun {
			if _, err := dst.UpsertCommander(ctx, cmdr.FID, cmdr.Name, cmdr.Platform); err != nil {
				return result, fmt.Errorf("upsert commander fid=%s: %w", cmdr.FID, err)
			}
		}

		offset := 0
		for {
			events, err := src.ReadEvents(ctx, cmdr.FID, offset, cfg.BatchSize)
			if err != nil {
				return result, fmt.Errorf("read events fid=%s offset=%d: %w", cmdr.FID, offset, err)
			}
			if len(events) == 0 {
				break
			}

			var valid []store.JournalEvent
			for _, ev := range events {
				if ev.EventType == "" {
					logger.Warn("skipping event with empty event_type",
						"fid", cmdr.FID, "timestamp", ev.Timestamp)
					result.Skipped++
					continue
				}
				valid = append(valid, ev)
			}

			if !cfg.DryRun && len(valid) > 0 {
				ins, dups, err := dst.InsertEvents(ctx, cmdr.FID, valid)
				if err != nil {
					return result, fmt.Errorf("insert events fid=%s: %w", cmdr.FID, err)
				}
				result.Inserted += ins
				result.Duplicates += dups
			}

			offset += len(events)
			if len(events) < cfg.BatchSize {
				break
			}
		}
	}

	return result, nil
}

// pgSourceReader implements SourceReader against a PostgreSQL source using
// the edin_migrator role (BYPASSRLS) to read all rows regardless of RLS.
type pgSourceReader struct {
	pool *pgxpool.Pool
}

func (r *pgSourceReader) ListCommanders(ctx context.Context) ([]SourceCommander, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT fid, cmdr_name, platform
		FROM commander.commanders
		ORDER BY fid`)
	if err != nil {
		return nil, fmt.Errorf("query commanders: %w", err)
	}
	defer rows.Close()

	var commanders []SourceCommander
	for rows.Next() {
		var c SourceCommander
		if err := rows.Scan(&c.FID, &c.Name, &c.Platform); err != nil {
			return nil, fmt.Errorf("scan commander: %w", err)
		}
		commanders = append(commanders, c)
	}
	return commanders, rows.Err()
}

func (r *pgSourceReader) ReadEvents(ctx context.Context, fid string, offset, limit int) ([]store.JournalEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT commander_id, fid, timestamp, event_type, event_data, client_version, ingested_at
		FROM commander.journal_events
		WHERE fid = $1
		ORDER BY timestamp ASC
		OFFSET $2 LIMIT $3`,
		fid, offset, limit)
	if err != nil {
		return nil, fmt.Errorf("query events fid=%s: %w", fid, err)
	}
	defer rows.Close()

	var events []store.JournalEvent
	for rows.Next() {
		var ev store.JournalEvent
		var rawData []byte
		if err := rows.Scan(
			&ev.CommanderID, &ev.FID, &ev.Timestamp, &ev.EventType,
			&rawData, &ev.ClientVersion, &ev.IngestedAt,
		); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		ev.EventData = json.RawMessage(rawData)
		events = append(events, ev)
	}
	return events, rows.Err()
}

func main() {
	sourceDSN := flag.String("source-dsn", "", "PostgreSQL DSN for the migration source (edin_migrator role)")
	targetDSN := flag.String("target-dsn", "", "PostgreSQL DSN for the migration target (edin_cmd_writer role)")
	batchSize := flag.Int("batch-size", 1000, "number of events to read and write per batch")
	dryRun := flag.Bool("dry-run", false, "read source but write nothing to target")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *sourceDSN == "" {
		fmt.Fprintln(os.Stderr, "error: --source-dsn is required")
		os.Exit(1)
	}
	if !*dryRun && *targetDSN == "" {
		fmt.Fprintln(os.Stderr, "error: --target-dsn is required unless --dry-run is set")
		os.Exit(1)
	}

	ctx := context.Background()

	srcPool, err := pgxpool.New(ctx, *sourceDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connect to source: %v\n", err)
		os.Exit(1)
	}
	defer srcPool.Close()

	cfg := Config{
		BatchSize: *batchSize,
		DryRun:    *dryRun,
	}

	src := &pgSourceReader{pool: srcPool}

	var dst store.CommanderRepository
	if !*dryRun {
		dstPool, err := pgxpool.New(ctx, *targetDSN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: connect to target: %v\n", err)
			os.Exit(1)
		}
		defer dstPool.Close()
		dst = store.NewPgCommanderRepository(dstPool, logger)
	}

	start := time.Now()
	result, err := migrate(ctx, cfg, src, dst, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}

	mode := "live"
	if *dryRun {
		mode = "dry-run"
	}
	fmt.Printf("migration complete [%s] duration=%s commanders=%d inserted=%d duplicates=%d skipped=%d\n",
		mode, time.Since(start).Round(time.Millisecond),
		result.Commanders, result.Inserted, result.Duplicates, result.Skipped)
}
