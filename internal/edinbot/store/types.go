// Package store is the persistence boundary for the edin-bot's discord schema.
// All previous-state reads, message-id writes, and strike/unstrike state changes
// go through the Store interface. The bot is the sole writer to discord.* tables.
package store

import (
	"encoding/json"
	"time"
)

// PostedMessage is one row in discord.posted_messages.
type PostedMessage struct {
	BindingID    string          // canonical YAML id, e.g. "kaine-platinum-boom"
	Identity     string          // feature-defined dedup key, e.g. "system:Sol"
	GuildID      string          // Discord snowflake
	ChannelID    string          // Discord snowflake
	MessageID    string          // Discord snowflake
	StateHash    string          // sha256 of the rendered fields
	LastRender   json.RawMessage // serialized embed used for the last edit (for un-strike re-render)
	PostedAt     time.Time
	LastEditedAt *time.Time // nil = never edited since post
	LastSeenAt   time.Time
	StruckAt     *time.Time // non-nil = currently struck-through
	UnstruckAt   *time.Time // non-nil = most-recent return after a strike
	DisabledAt   *time.Time // non-nil = binding became unreachable; scheduler skips
}

// PollCycle is one row in discord.poll_cycles.
type PollCycle struct {
	TickedAt   time.Time
	BindingID  string
	Status     string  // "success" | "failed" | "skipped" | "event"
	Attempts   int     // 1 for events; 1..4 for polls
	ItemCount  int
	DurationMs int
	LastError  *string // nil when Status == "success" or "event"
}

// DiagnoseReport is one row in discord.diagnose_reports.
type DiagnoseReport struct {
	TriggeredAt     time.Time
	BindingID       string
	Report          json.RawMessage
	PostedMessageID *string // Discord message id of the ops-channel announcement
}
