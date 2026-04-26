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
	_, err := s.pool.Exec(ctx,
		`UPDATE discord.posted_messages SET disabled_at = $1 WHERE binding_id = $2`,
		at, bindingID)
	if err != nil {
		return fmt.Errorf("disable binding: %w", err)
	}
	return nil
}

func (s *PostgresStore) IsBindingDisabled(ctx context.Context, bindingID string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM discord.posted_messages
		 WHERE binding_id = $1 AND disabled_at IS NOT NULL`,
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
