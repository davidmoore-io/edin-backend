package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JournalEvent is a single journal entry stored in commander.journal_events.
type JournalEvent struct {
	CommanderID   uuid.UUID
	FID           string
	Timestamp     time.Time
	EventType     string
	EventData     json.RawMessage
	ClientVersion string
	IngestedAt    time.Time
}

// LocationState is extracted from journal events (FSDJump / Location events).
type LocationState struct {
	SystemName string
	StarPos    [3]float64
	UpdatedAt  time.Time
}

// CommanderRepository defines data-access operations for commander journal data.
// All mutating operations are scoped by FID via SET LOCAL app.current_fid so
// the TimescaleDB RLS policy commander_isolation is enforced on every query.
type CommanderRepository interface {
	UpsertCommander(ctx context.Context, fid, name, platform string) (uuid.UUID, error)
	InsertEvents(ctx context.Context, fid string, events []JournalEvent) (inserted, duplicates int, err error)
	RecentEvents(ctx context.Context, fid string, count int) ([]JournalEvent, error)
	EventsByType(ctx context.Context, fid string, types []string, since, until time.Time) ([]JournalEvent, error)
	CurrentLocation(ctx context.Context, fid string) (*LocationState, error)
	DeleteAllEvents(ctx context.Context, fid string) error
}

// pgCommanderRepository is the PostgreSQL/TimescaleDB implementation.
type pgCommanderRepository struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPgCommanderRepository creates a CommanderRepository backed by a pgxpool.Pool.
func NewPgCommanderRepository(pool *pgxpool.Pool, logger *slog.Logger) CommanderRepository {
	return &pgCommanderRepository{pool: pool, logger: logger}
}

// withFIDContext opens a transaction, sets app.current_fid for the duration of
// the transaction (SET LOCAL — expires on commit/rollback), calls fn, then
// commits. Rollback is deferred and any rollback error that occurs after a fn
// error is logged but not returned.
func (r *pgCommanderRepository) withFIDContext(ctx context.Context, fid string,
	fn func(tx pgx.Tx) error) (retErr error) {

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			r.logger.Error("transaction rollback failed", "error", rbErr, "fid", fid)
			if retErr == nil {
				retErr = fmt.Errorf("rollback: %w", rbErr)
			}
		}
	}()

	// set_config(setting, value, is_local) is equivalent to SET LOCAL and
	// accepts a parameterized value, unlike the SET statement which does not
	// support $1 placeholders.
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_fid', $1, true)", fid); err != nil {
		return fmt.Errorf("set fid context: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpsertCommander inserts a new commander row or updates the name and last_seen_at
// on conflict. Returns the commander UUID.
func (r *pgCommanderRepository) UpsertCommander(ctx context.Context, fid, name, platform string) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.withFIDContext(ctx, fid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO commander.commanders (fid, cmdr_name, platform)
			VALUES ($1, $2, $3)
			ON CONFLICT (fid) DO UPDATE
			SET cmdr_name = EXCLUDED.cmdr_name, last_seen_at = now()
			RETURNING id`,
			fid, name, platform,
		).Scan(&id)
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert commander fid=%s: %w", fid, err)
	}
	return id, nil
}

// InsertEvents stores a batch of journal events for fid. Duplicate events
// (same fid + timestamp + event_type) are silently skipped. Returns the count
// of rows actually inserted and the count of duplicates.
func (r *pgCommanderRepository) InsertEvents(ctx context.Context, fid string, events []JournalEvent) (inserted, duplicates int, err error) {
	if len(events) == 0 {
		return 0, 0, nil
	}

	err = r.withFIDContext(ctx, fid, func(tx pgx.Tx) error {
		for _, ev := range events {
			tag, execErr := tx.Exec(ctx, `
				INSERT INTO commander.journal_events
					(commander_id, fid, timestamp, event_type, event_data, client_version)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (fid, timestamp, event_type) DO NOTHING`,
				ev.CommanderID, fid, ev.Timestamp, ev.EventType, []byte(ev.EventData), ev.ClientVersion,
			)
			if execErr != nil {
				return fmt.Errorf("insert event type=%s ts=%s: %w", ev.EventType, ev.Timestamp, execErr)
			}
			if tag.RowsAffected() == 1 {
				inserted++
			} else {
				duplicates++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("insert events fid=%s: %w", fid, err)
	}
	return inserted, duplicates, nil
}

// RecentEvents returns the most recent count events for fid, newest first.
func (r *pgCommanderRepository) RecentEvents(ctx context.Context, fid string, count int) ([]JournalEvent, error) {
	var events []JournalEvent
	err := r.withFIDContext(ctx, fid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT commander_id, fid, timestamp, event_type, event_data, client_version, ingested_at
			FROM commander.journal_events
			WHERE fid = $1
			ORDER BY timestamp DESC
			LIMIT $2`,
			fid, count,
		)
		if err != nil {
			return fmt.Errorf("query recent events: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var ev JournalEvent
			var rawData []byte
			if err := rows.Scan(
				&ev.CommanderID,
				&ev.FID,
				&ev.Timestamp,
				&ev.EventType,
				&rawData,
				&ev.ClientVersion,
				&ev.IngestedAt,
			); err != nil {
				return fmt.Errorf("scan journal event: %w", err)
			}
			ev.EventData = json.RawMessage(rawData)
			events = append(events, ev)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("recent events fid=%s: %w", fid, err)
	}
	return events, nil
}

// EventsByType returns events for fid filtered by event type and time range, newest first.
func (r *pgCommanderRepository) EventsByType(ctx context.Context, fid string, types []string, since, until time.Time) ([]JournalEvent, error) {
	var events []JournalEvent
	err := r.withFIDContext(ctx, fid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT commander_id, fid, timestamp, event_type, event_data, client_version, ingested_at
			FROM commander.journal_events
			WHERE fid = $1
			  AND event_type = ANY($2)
			  AND timestamp >= $3
			  AND timestamp <= $4
			ORDER BY timestamp DESC`,
			fid, types, since, until,
		)
		if err != nil {
			return fmt.Errorf("query events by type: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var ev JournalEvent
			var rawData []byte
			if err := rows.Scan(
				&ev.CommanderID,
				&ev.FID,
				&ev.Timestamp,
				&ev.EventType,
				&rawData,
				&ev.ClientVersion,
				&ev.IngestedAt,
			); err != nil {
				return fmt.Errorf("scan journal event: %w", err)
			}
			ev.EventData = json.RawMessage(rawData)
			events = append(events, ev)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("events by type fid=%s: %w", fid, err)
	}
	return events, nil
}

// CurrentLocation finds the most recent FSDJump or Location event for fid and
// extracts StarSystem and StarPos from the event_data JSON.
// Returns nil if no such event exists.
func (r *pgCommanderRepository) CurrentLocation(ctx context.Context, fid string) (*LocationState, error) {
	var loc *LocationState
	err := r.withFIDContext(ctx, fid, func(tx pgx.Tx) error {
		var rawData []byte
		var ts time.Time
		err := tx.QueryRow(ctx, `
			SELECT event_data, timestamp
			FROM commander.journal_events
			WHERE fid = $1
			  AND event_type IN ('FSDJump', 'Location')
			ORDER BY timestamp DESC
			LIMIT 1`,
			fid,
		).Scan(&rawData, &ts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("query current location: %w", err)
		}

		// Parse the event_data JSON to extract StarSystem and StarPos.
		var payload struct {
			StarSystem string    `json:"StarSystem"`
			StarPos    []float64 `json:"StarPos"`
		}
		if err := json.Unmarshal(rawData, &payload); err != nil {
			return fmt.Errorf("unmarshal location event data: %w", err)
		}

		var starPos [3]float64
		if len(payload.StarPos) == 3 {
			starPos[0] = payload.StarPos[0]
			starPos[1] = payload.StarPos[1]
			starPos[2] = payload.StarPos[2]
		}

		loc = &LocationState{
			SystemName: payload.StarSystem,
			StarPos:    starPos,
			UpdatedAt:  ts,
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("current location fid=%s: %w", fid, err)
	}
	return loc, nil
}

// DeleteAllEvents permanently removes all journal events for fid (GDPR erasure).
//
// TimescaleDB may compress chunks. Because compressed chunks reject DELETE
// statements, this method:
//  1. Finds all compressed chunks for the journal_events hypertable.
//  2. Decompresses each one.
//  3. Executes the DELETE.
//
// This operation runs outside withFIDContext because decompression requires
// separate statements that cannot be interleaved with the RLS-scoped
// transaction. The DELETE is executed directly against the pool using the
// superuser/application connection which bypasses FORCE ROW LEVEL SECURITY.
func (r *pgCommanderRepository) DeleteAllEvents(ctx context.Context, fid string) error {
	// Step 1: Collect compressed chunks.
	rows, err := r.pool.Query(ctx, `
		SELECT chunk_schema, chunk_name
		FROM timescaledb_information.chunks
		WHERE hypertable_schema = 'commander'
		  AND hypertable_name   = 'journal_events'
		  AND is_compressed     = true`)
	if err != nil {
		return fmt.Errorf("list compressed chunks: %w", err)
	}

	type chunk struct{ schema, name string }
	var chunks []chunk
	for rows.Next() {
		var c chunk
		if err := rows.Scan(&c.schema, &c.name); err != nil {
			rows.Close()
			return fmt.Errorf("scan chunk row: %w", err)
		}
		chunks = append(chunks, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate compressed chunks: %w", err)
	}

	// Step 2: Decompress each chunk.
	for _, c := range chunks {
		if _, err := r.pool.Exec(ctx,
			fmt.Sprintf("SELECT decompress_chunk(format('%%I.%%I', $1, $2)::regclass)"),
			c.schema, c.name,
		); err != nil {
			return fmt.Errorf("decompress chunk %s.%s: %w", c.schema, c.name, err)
		}
	}

	// Step 3: Delete all events for this FID.
	// The pool connection is a superuser in the test environment and an
	// application writer role in production. FORCE ROW LEVEL SECURITY applies
	// to non-superusers, so in production the caller must ensure this is run
	// via a privileged connection (e.g., a dedicated GDPR erasure role with
	// BYPASSRLS) or via a superuser.
	if _, err := r.pool.Exec(ctx,
		"DELETE FROM commander.journal_events WHERE fid = $1", fid,
	); err != nil {
		return fmt.Errorf("delete events fid=%s: %w", fid, err)
	}

	return nil
}
