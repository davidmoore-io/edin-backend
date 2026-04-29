package store

import (
	"context"
	"errors"
	"time"
)

// ErrAlreadyWatched is returned by AddWatch when a row with the same
// (channel_id, system_slug) already exists. The /watch handler turns this
// into a polite ephemeral "this system is already being watched in this
// channel" rather than crashing or silently overwriting.
var ErrAlreadyWatched = errors.New("system already watched in this channel")

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

	// DeletePostedForBinding removes every posted_messages row for one binding.
	// Used by the /admin/clear endpoint AFTER the corresponding Discord
	// messages have been deleted; before this call the rows still represent
	// real Discord state. Returns the number of rows deleted.
	DeletePostedForBinding(ctx context.Context, bindingID string) (int, error)

	// EnableBinding clears any disabled_bindings tombstone for the binding,
	// re-allowing the scheduler to call Poll on the next tick. Idempotent;
	// removing a row that doesn't exist is a no-op.
	EnableBinding(ctx context.Context, bindingID string) error

	// AddWatch inserts a new watched_systems row. Returns ErrAlreadyWatched
	// if a row with the same (channel_id, system_slug) already exists. The
	// uniqueness invariant is enforced by the table's PRIMARY KEY.
	AddWatch(ctx context.Context, w WatchedSystem) error

	// RemoveWatch deletes the (channel_id, system_slug) row. Returns
	// (false, nil) when no row matched (idempotent for /unwatch).
	RemoveWatch(ctx context.Context, channelID, systemSlug string) (deleted bool, err error)

	// GetWatch returns the row for (channel_id, system_slug), if any. Used
	// by /watch's "already watched? show link" branch.
	GetWatch(ctx context.Context, channelID, systemSlug string) (*WatchedSystem, error)

	// ListAllWatches returns every watch across every channel — the boot
	// recovery + 120s polling loop iterates the result. Sorted by slug for
	// stable stagger ordering.
	ListAllWatches(ctx context.Context) ([]WatchedSystem, error)

	// CountWatchesInChannel returns how many watches a channel currently
	// has. Used by /watch to enforce the 50-per-channel cap.
	CountWatchesInChannel(ctx context.Context, channelID string) (int, error)

	// UpdateWatchState persists a fresh state-hash + render after the
	// watcher edits the Discord message. last_updated_at must be the
	// snapshot's freshness timestamp, not the wall clock.
	UpdateWatchState(ctx context.Context, channelID, systemSlug, hash string, render []byte, updatedAt time.Time) error

	// LatestSuccessAt returns the most-recent ticked_at where status='success'
	// or 'event' for the given binding. Returns the zero time if no successful
	// cycle has been recorded yet (the caller distinguishes new-binding from
	// stale-binding via the IsZero check). Used by the bot's /healthz oracle.
	LatestSuccessAt(ctx context.Context, bindingID string) (time.Time, error)
}
