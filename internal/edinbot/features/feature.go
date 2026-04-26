// Package features defines the bot's framework-level capability vocabulary.
// Concrete features (platinum, ltd, ops-health) live in sibling files but are
// registered into Registry by main.go (cmd/edin-bot/main.go).
//
// Every Feature must satisfy EITHER PollFeature (timer-driven; the common
// case) or EventDrivenFeature (channel-driven; ops-health only). The scheduler
// dispatches based on which sub-interface is satisfied; exactly one MUST be.
// The registry test asserts this invariant.
package features

import (
	"context"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Feature is the base interface every capability implements.
type Feature interface {
	Name() string
	DefaultConfig() Config
	Validate(cfg Config) error
}

// PollFeature is for features whose snapshot is produced on a timer (the
// common case: platinum-boom-alerts, ltd-alerts, …).
type PollFeature interface {
	Feature
	// Poll runs one cycle. Internally it handles its own retry+backoff. The
	// scheduler calls this on each tick and skips the next tick if a previous
	// Poll for the same binding is still running.
	Poll(ctx context.Context, cfg Config) (Snapshot, error)
}

// EventDrivenFeature is for features whose snapshot is produced when an
// in-process event arrives (ops-health-alerts subscribes to OpsBus).
//
// Subscribe MUST return a channel the scheduler reads until ctx is
// cancelled. The feature owns the channel and its closing semantics — the
// scheduler will not close it. Snapshots delivered on this channel go through
// the same publisher diff machine — there is no special path for
// "event-driven" beyond scheduling.
type EventDrivenFeature interface {
	Feature
	Subscribe(ctx context.Context, cfg Config) (<-chan Snapshot, error)
}

// Config is the per-binding configuration map. Each feature validates its
// own keys via Validate(); unknown keys MUST be rejected.
type Config map[string]any

// Snapshot is the result of one cycle (poll or event).
type Snapshot struct {
	// Items is the list of items the publisher will diff against persisted
	// state. Order does not matter — diffing is keyed on Identity().
	Items []Item

	// Healthy is the cycle's go/no-go signal. False → publisher MUST NOT act
	// on this snapshot; the scheduler still records the cycle for audit.
	Healthy bool

	// GeneratedAt is the canonical "as-of" timestamp. Every user-visible
	// timestamp the publisher emits (footer "no longer present at HH:MM
	// UTC", "returned at HH:MM UTC") MUST come from here, not from the
	// bot's wall clock. This guarantees consistent timestamps across the
	// emit cycle even if it spans seconds.
	GeneratedAt time.Time

	// SourceMeta carries opaque key/value debugging info (e.g.
	// {"total_maps": 42, "control_api_latency_ms": 180}). Logged but not
	// persisted.
	SourceMeta map[string]any
}

// Item is one logical alert.
type Item interface {
	// Identity is the stable dedup key (e.g. "system:Sol"). Determines which
	// posted_message row this Item maps to. Identities MUST be stable across
	// cycles for the same logical thing — changing an identity means losing
	// continuity with the previously-posted message.
	Identity() string

	// StateHash is sha256 (or any deterministic hash) of the fields the
	// publisher should treat as edit-worthy. If StateHash is identical to the
	// last emitted message's stored hash, the publisher emits Noop.
	StateHash() string

	// Render produces the Discord message body. It MUST embed timestamps
	// from the enclosing Snapshot.GeneratedAt, never from time.Now().
	Render() *discordgo.MessageEmbed
}
