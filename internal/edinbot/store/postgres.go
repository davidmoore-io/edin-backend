package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store against pgxpool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Compile-time check.
var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) GetPosted(ctx context.Context, bindingID string) (map[string]PostedMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT binding_id, identity, guild_id, channel_id, message_id, state_hash,
		       last_render, posted_at, last_edited_at, last_seen_at,
		       struck_at, unstruck_at, disabled_at
		FROM discord.posted_messages
		WHERE binding_id = $1`, bindingID)
	if err != nil {
		return nil, fmt.Errorf("query posted_messages: %w", err)
	}
	defer rows.Close()

	out := map[string]PostedMessage{}
	for rows.Next() {
		var m PostedMessage
		if err := rows.Scan(
			&m.BindingID, &m.Identity, &m.GuildID, &m.ChannelID, &m.MessageID, &m.StateHash,
			&m.LastRender, &m.PostedAt, &m.LastEditedAt, &m.LastSeenAt,
			&m.StruckAt, &m.UnstruckAt, &m.DisabledAt,
		); err != nil {
			return nil, fmt.Errorf("scan posted_message: %w", err)
		}
		out[m.Identity] = m
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertPosted(ctx context.Context, m PostedMessage) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO discord.posted_messages
		    (binding_id, identity, guild_id, channel_id, message_id, state_hash,
		     last_render, posted_at, last_edited_at, last_seen_at,
		     struck_at, unstruck_at, disabled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (binding_id, identity) DO UPDATE SET
		    guild_id       = EXCLUDED.guild_id,
		    channel_id     = EXCLUDED.channel_id,
		    message_id     = EXCLUDED.message_id,
		    state_hash     = EXCLUDED.state_hash,
		    last_render    = EXCLUDED.last_render,
		    last_edited_at = EXCLUDED.last_edited_at,
		    last_seen_at   = EXCLUDED.last_seen_at,
		    struck_at      = EXCLUDED.struck_at,
		    unstruck_at    = EXCLUDED.unstruck_at,
		    disabled_at    = EXCLUDED.disabled_at`,
		m.BindingID, m.Identity, m.GuildID, m.ChannelID, m.MessageID, m.StateHash,
		m.LastRender, m.PostedAt, m.LastEditedAt, m.LastSeenAt,
		m.StruckAt, m.UnstruckAt, m.DisabledAt,
	)
	if err != nil {
		return fmt.Errorf("upsert posted_message: %w", err)
	}
	return nil
}

func (s *PostgresStore) MarkStruck(ctx context.Context, bindingID, identity string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE discord.posted_messages SET struck_at = $1
		 WHERE binding_id = $2 AND identity = $3`,
		at, bindingID, identity)
	if err != nil {
		return fmt.Errorf("mark struck: %w", err)
	}
	return nil
}

func (s *PostgresStore) MarkUnstruck(ctx context.Context, bindingID, identity string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE discord.posted_messages SET struck_at = NULL, unstruck_at = $1
		 WHERE binding_id = $2 AND identity = $3`,
		at, bindingID, identity)
	if err != nil {
		return fmt.Errorf("mark unstruck: %w", err)
	}
	return nil
}

func (s *PostgresStore) UpdateLastSeen(ctx context.Context, bindingID string, identities []string, at time.Time) error {
	if len(identities) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx,
		`UPDATE discord.posted_messages SET last_seen_at = $1
		 WHERE binding_id = $2 AND identity = ANY($3)`,
		at, bindingID, identities,
	); err != nil {
		return fmt.Errorf("update last_seen: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) DisableBinding(ctx context.Context, bindingID string, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("disable binding (begin): %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Mark all existing posted rows so per-row queries see disabled_at too
	// (kept for symmetry with the unit-test memStore behaviour).
	if _, err := tx.Exec(ctx,
		`UPDATE discord.posted_messages SET disabled_at = $1 WHERE binding_id = $2`,
		at, bindingID); err != nil {
		return fmt.Errorf("disable binding (update posted): %w", err)
	}

	// Authoritative tombstone — works even when posted_messages is empty for
	// this binding (e.g. first-cycle ErrChannelGone).
	if _, err := tx.Exec(ctx, `
		INSERT INTO discord.disabled_bindings (binding_id, disabled_at)
		VALUES ($1, $2)
		ON CONFLICT (binding_id) DO UPDATE SET disabled_at = EXCLUDED.disabled_at`,
		bindingID, at); err != nil {
		return fmt.Errorf("disable binding (insert disabled_bindings): %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) DeletePostedForBinding(ctx context.Context, bindingID string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM discord.posted_messages WHERE binding_id = $1`,
		bindingID)
	if err != nil {
		return 0, fmt.Errorf("delete posted_messages for %s: %w", bindingID, err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresStore) EnableBinding(ctx context.Context, bindingID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM discord.disabled_bindings WHERE binding_id = $1`,
		bindingID); err != nil {
		return fmt.Errorf("enable binding %s: %w", bindingID, err)
	}
	return nil
}

func (s *PostgresStore) IsBindingDisabled(ctx context.Context, bindingID string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM discord.disabled_bindings WHERE binding_id = $1`,
		bindingID,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("is_binding_disabled: %w", err)
	}
	return n > 0, nil
}

func (s *PostgresStore) RecordPollCycle(ctx context.Context, c PollCycle) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO discord.poll_cycles
		    (ticked_at, binding_id, status, attempts, item_count, duration_ms, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (ticked_at, binding_id) DO NOTHING`,
		c.TickedAt, c.BindingID, c.Status, c.Attempts, c.ItemCount, c.DurationMs, c.LastError,
	)
	if err != nil {
		return fmt.Errorf("record poll_cycle: %w", err)
	}
	return nil
}

func (s *PostgresStore) LatestSuccessAt(ctx context.Context, bindingID string) (time.Time, error) {
	var ticked time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(ticked_at), TIMESTAMPTZ '1970-01-01')
		FROM discord.poll_cycles
		WHERE binding_id = $1 AND status IN ('success', 'event')`,
		bindingID).Scan(&ticked)
	if err != nil {
		return time.Time{}, fmt.Errorf("query latest_success_at: %w", err)
	}
	if ticked.Year() <= 1970 {
		return time.Time{}, nil
	}
	return ticked, nil
}

func (s *PostgresStore) RecordDiagnoseReport(ctx context.Context, r DiagnoseReport) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO discord.diagnose_reports
		    (triggered_at, binding_id, report, posted_message_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (triggered_at, binding_id) DO NOTHING`,
		r.TriggeredAt, r.BindingID, r.Report, r.PostedMessageID,
	)
	if err != nil {
		return fmt.Errorf("record diagnose_report: %w", err)
	}
	return nil
}

// ---- watched_systems ----

// AddWatch inserts a row. The (channel_id, system_slug) PRIMARY KEY surfaces
// a unique-violation as ErrAlreadyWatched so the /watch handler can offer a
// polite "already watched here" ephemeral.
func (s *PostgresStore) AddWatch(ctx context.Context, w WatchedSystem) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO discord.watched_systems
		    (guild_id, channel_id, system_slug, system_name, message_id,
		     created_by, watched_at, last_updated_at, last_state_hash, last_render)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		w.GuildID, w.ChannelID, w.SystemSlug, w.SystemName, w.MessageID,
		w.CreatedBy, w.WatchedAt, w.LastUpdatedAt, w.LastStateHash, w.LastRender,
	)
	if err != nil {
		// pgx wraps the underlying *pgconn.PgError. We don't import pgconn
		// here to keep the package's deps thin; SQLSTATE 23505 = unique
		// violation is reliably present in the error message text emitted
		// by pgx for INSERT-conflict.
		if isUniqueViolation(err) {
			return ErrAlreadyWatched
		}
		return fmt.Errorf("add watch: %w", err)
	}
	return nil
}

// RemoveWatch deletes the row. Returns (false, nil) when no row matched —
// /unwatch is idempotent.
func (s *PostgresStore) RemoveWatch(ctx context.Context, channelID, systemSlug string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM discord.watched_systems
		WHERE channel_id = $1 AND system_slug = $2`,
		channelID, systemSlug)
	if err != nil {
		return false, fmt.Errorf("remove watch: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetWatch returns the single row for (channel_id, system_slug), or nil.
func (s *PostgresStore) GetWatch(ctx context.Context, channelID, systemSlug string) (*WatchedSystem, error) {
	var w WatchedSystem
	err := s.pool.QueryRow(ctx, `
		SELECT guild_id, channel_id, system_slug, system_name, message_id,
		       created_by, watched_at, last_updated_at, last_state_hash, last_render
		FROM discord.watched_systems
		WHERE channel_id = $1 AND system_slug = $2`,
		channelID, systemSlug,
	).Scan(
		&w.GuildID, &w.ChannelID, &w.SystemSlug, &w.SystemName, &w.MessageID,
		&w.CreatedBy, &w.WatchedAt, &w.LastUpdatedAt, &w.LastStateHash, &w.LastRender,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("get watch: %w", err)
	}
	return &w, nil
}

// ListAllWatches returns every watch row across every channel, sorted by
// system_slug for stable iteration order in the polling loop.
func (s *PostgresStore) ListAllWatches(ctx context.Context) ([]WatchedSystem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT guild_id, channel_id, system_slug, system_name, message_id,
		       created_by, watched_at, last_updated_at, last_state_hash, last_render
		FROM discord.watched_systems
		ORDER BY system_slug`)
	if err != nil {
		return nil, fmt.Errorf("list watches: %w", err)
	}
	defer rows.Close()

	var out []WatchedSystem
	for rows.Next() {
		var w WatchedSystem
		if err := rows.Scan(
			&w.GuildID, &w.ChannelID, &w.SystemSlug, &w.SystemName, &w.MessageID,
			&w.CreatedBy, &w.WatchedAt, &w.LastUpdatedAt, &w.LastStateHash, &w.LastRender,
		); err != nil {
			return nil, fmt.Errorf("scan watch: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CountWatchesInChannel — used by /watch to enforce the 50-per-channel cap.
func (s *PostgresStore) CountWatchesInChannel(ctx context.Context, channelID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM discord.watched_systems WHERE channel_id = $1`,
		channelID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count watches: %w", err)
	}
	return n, nil
}

// UpdateWatchState persists a new state-hash + render after the watcher
// has edited the Discord message. Touch nothing else (created_by,
// watched_at, message_id are append-once fields per the watch lifecycle).
func (s *PostgresStore) UpdateWatchState(ctx context.Context, channelID, systemSlug, hash string, render []byte, updatedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE discord.watched_systems
		SET last_state_hash = $3,
		    last_render     = $4,
		    last_updated_at = $5
		WHERE channel_id = $1 AND system_slug = $2`,
		channelID, systemSlug, hash, render, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("update watch state: %w", err)
	}
	return nil
}

// isUniqueViolation returns true when the error is a Postgres unique-
// constraint violation. We string-match rather than depend on pgconn so
// the store package stays a leaf in the import graph.
func isUniqueViolation(err error) bool {
	return err != nil && (containsAny(err.Error(),
		"SQLSTATE 23505",
		"duplicate key value violates unique constraint"))
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if len(n) <= len(haystack) {
			for i := 0; i+len(n) <= len(haystack); i++ {
				if haystack[i:i+len(n)] == n {
					return true
				}
			}
		}
	}
	return false
}
