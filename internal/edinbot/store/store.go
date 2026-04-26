package store

import (
	"context"
	"time"
)

// Store is the persistence boundary. Implementations:
//   - PostgresStore (production)
//   - in-memory fakes used in publisher_test.go and scheduler_test.go (defined in those test packages)
//
// Method contracts are tested in postgres_test.go (//go:build integration).
type Store interface {
	// GetPosted returns the current posted_messages rows for one binding,
	// keyed by Identity. Returns an empty map (not nil) if there are no rows.
	GetPosted(ctx context.Context, bindingID string) (map[string]PostedMessage, error)

	// UpsertPosted inserts or updates a single row. last_seen_at is set to
	// m.LastSeenAt; last_edited_at is set to m.LastEditedAt (may be nil for
	// fresh posts). The caller is responsible for setting all timestamp fields
	// from the canonical Snapshot.GeneratedAt time, NOT from time.Now().
	UpsertPosted(ctx context.Context, m PostedMessage) error

	// MarkStruck sets struck_at = at, leaves unstruck_at as-is. Atomic; only
	// updates if the row exists.
	MarkStruck(ctx context.Context, bindingID, identity string, at time.Time) error

	// MarkUnstruck clears struck_at and sets unstruck_at = at. Atomic.
	MarkUnstruck(ctx context.Context, bindingID, identity string, at time.Time) error

	// UpdateLastSeen advances last_seen_at = at for every (bindingID, identity)
	// in identities, in one transaction. Identities not present in the table are
	// silently ignored (publisher should not call for unknown identities).
	UpdateLastSeen(ctx context.Context, bindingID string, identities []string, at time.Time) error

	// DisableBinding sets disabled_at = at on every posted_messages row for the
	// binding. Used when Discord returns 403/404 (channel deleted, bot kicked).
	DisableBinding(ctx context.Context, bindingID string, at time.Time) error

	// IsBindingDisabled returns true if at least one row for this binding has
	// disabled_at != NULL. Used by the scheduler to skip Discord calls for a
	// binding until the next process restart.
	IsBindingDisabled(ctx context.Context, bindingID string) (bool, error)

	// RecordPollCycle inserts one row into discord.poll_cycles. Always called
	// by the scheduler around every Poll() / Subscribe() snapshot, regardless
	// of outcome.
	RecordPollCycle(ctx context.Context, c PollCycle) error

	// RecordDiagnoseReport inserts one row into discord.diagnose_reports.
	// Called by the ops-health-alerts feature after escalation.
	RecordDiagnoseReport(ctx context.Context, r DiagnoseReport) error
}
