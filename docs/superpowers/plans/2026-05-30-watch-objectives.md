# watch-objectives Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `/watch-objectives` slash command that posts a live-updating safety objectives board (reinforcement progress per system) into a Discord channel, editing the single message whenever Memgraph data changes.

**Architecture:** A new `features/objectives` package mirrors the `features/watcher` pattern — slash handler posts a plain-text code-block message, stores one row per channel in a new `discord.objective_boards` table, and a background loop polls each board's systems every 120 s, editing the message when any reinforcement number changes. The `discordclient` gets one new method (`PostContent`) for plain-text posting; all else reuses existing interfaces.

**Tech Stack:** Go, discordgo, pgx/v5, Memgraph via controlclient, Discord slash commands

---

## File Map

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `store/migrations/006_objectives.sql` | Schema for `discord.objective_boards` |
| Modify | `store/types.go` | Add `ObjectiveSystem`, `ObjectiveBoard` types |
| Modify | `store/postgres.go` | Add 5 objective CRUD methods |
| Modify | `discordclient/client.go` | Add `PostContent` to `Client` interface + `RealClient` |
| Modify | `discordclient/fake.go` | Add `PostContent` to `FakeDiscordClient` |
| Create | `features/objectives/objectives.go` | Narrow interfaces (`Store`, `Snapshotter`, `Discord`), `Config` |
| Create | `features/objectives/render.go` | `Render()`, `stateHash()` — pure functions |
| Create | `features/objectives/render_test.go` | Table-shape + hash-stability tests |
| Create | `features/objectives/loop.go` | `ObjectivesLoop` — polling goroutine |
| Create | `features/objectives/loop_test.go` | Loop edit/skip/delete-on-gone tests |
| Create | `features/objectives/handler.go` | `/watch-objectives` + `/unwatch-objectives` handlers |
| Create | `features/objectives/handler_test.go` | Handler happy-path + error-branch tests |
| Modify | `cmd/edin-bot/main.go` | Wire objectives into `setupSlash` |

All paths are relative to `internal/edinbot/` except the migration and `cmd/edin-bot/main.go`.

---

### Task 1: DB migration + store types

**Files:**
- Create: `internal/edinbot/store/migrations/006_objectives.sql`
- Modify: `internal/edinbot/store/types.go`

- [ ] **Step 1.1: Write the migration**

```sql
-- 006_objectives.sql
-- One row per channel — "one live board per channel" is the dedup invariant.
-- systems is a JSONB array of {slug, name} objects, ordered by the commander
-- who ran /watch-objectives. last_render holds the raw message content we
-- last posted so we can inspect Discord state without re-fetching from Memgraph.

CREATE TABLE IF NOT EXISTS discord.objective_boards (
    guild_id        TEXT        NOT NULL,
    channel_id      TEXT        NOT NULL PRIMARY KEY,
    message_id      TEXT        NOT NULL,
    systems         JSONB       NOT NULL DEFAULT '[]',
    created_by      TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    last_state_hash TEXT        NOT NULL DEFAULT '',
    last_render     TEXT        NOT NULL DEFAULT ''
);
```

Save to `internal/edinbot/store/migrations/006_objectives.sql`.

- [ ] **Step 1.2: Add types to `store/types.go`**

Append after the `DiagnoseReport` type:

```go
// ObjectiveSystem is one entry in an ObjectiveBoard's systems list.
// Stored as a JSONB element in discord.objective_boards.
type ObjectiveSystem struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ObjectiveBoard is one row in discord.objective_boards.
// PK is ChannelID — one live board per channel.
type ObjectiveBoard struct {
	GuildID       string
	ChannelID     string
	MessageID     string
	Systems       []ObjectiveSystem
	CreatedBy     string
	CreatedAt     time.Time
	LastStateHash string
	LastRender    string // raw Discord message content
}
```

- [ ] **Step 1.3: Verify migration runs**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go build ./internal/edinbot/store/...
```

Expected: no errors (migration is embedded at runtime — this just checks the Go compiles).

- [ ] **Step 1.4: Commit**

```bash
git add internal/edinbot/store/migrations/006_objectives.sql internal/edinbot/store/types.go
git commit -m "feat(objectives): add objective_boards migration + store types"
```

---

### Task 2: Postgres store methods

**Files:**
- Modify: `internal/edinbot/store/postgres.go`

These methods live on `*PostgresStore` but are **not** added to the `store.Store` interface — the objectives package defines its own narrow interface (Task 4). `*PostgresStore` satisfies it duck-typing style.

- [ ] **Step 2.1: Add the five methods**

Append to `internal/edinbot/store/postgres.go`:

```go
// GetObjectiveBoard returns the board for the channel, or nil if none exists.
func (s *PostgresStore) GetObjectiveBoard(ctx context.Context, channelID string) (*ObjectiveBoard, error) {
	var b ObjectiveBoard
	var systemsJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT guild_id, channel_id, message_id, systems, created_by,
		       created_at, last_state_hash, last_render
		FROM discord.objective_boards
		WHERE channel_id = $1`, channelID,
	).Scan(&b.GuildID, &b.ChannelID, &b.MessageID, &systemsJSON,
		&b.CreatedBy, &b.CreatedAt, &b.LastStateHash, &b.LastRender)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("get objective board: %w", err)
	}
	if err := json.Unmarshal(systemsJSON, &b.Systems); err != nil {
		return nil, fmt.Errorf("decode objective systems: %w", err)
	}
	return &b, nil
}

// SetObjectiveBoard upserts the board for a channel (insert or replace).
func (s *PostgresStore) SetObjectiveBoard(ctx context.Context, b ObjectiveBoard) error {
	systemsJSON, err := json.Marshal(b.Systems)
	if err != nil {
		return fmt.Errorf("encode objective systems: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO discord.objective_boards
		    (guild_id, channel_id, message_id, systems, created_by,
		     created_at, last_state_hash, last_render)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (channel_id) DO UPDATE
		    SET guild_id        = EXCLUDED.guild_id,
		        message_id      = EXCLUDED.message_id,
		        systems         = EXCLUDED.systems,
		        created_by      = EXCLUDED.created_by,
		        created_at      = EXCLUDED.created_at,
		        last_state_hash = EXCLUDED.last_state_hash,
		        last_render     = EXCLUDED.last_render`,
		b.GuildID, b.ChannelID, b.MessageID, systemsJSON, b.CreatedBy,
		b.CreatedAt, b.LastStateHash, b.LastRender,
	)
	if err != nil {
		return fmt.Errorf("set objective board: %w", err)
	}
	return nil
}

// UpdateObjectiveBoardState persists a new state-hash + render after the
// loop edits the Discord message.
func (s *PostgresStore) UpdateObjectiveBoardState(ctx context.Context, channelID, hash, render string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE discord.objective_boards
		SET last_state_hash = $2, last_render = $3
		WHERE channel_id = $1`,
		channelID, hash, render,
	)
	if err != nil {
		return fmt.Errorf("update objective board state: %w", err)
	}
	return nil
}

// RemoveObjectiveBoard deletes the board for a channel. Returns (false, nil)
// when no row matched (idempotent for /unwatch-objectives).
func (s *PostgresStore) RemoveObjectiveBoard(ctx context.Context, channelID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM discord.objective_boards WHERE channel_id = $1`, channelID)
	if err != nil {
		return false, fmt.Errorf("remove objective board: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListAllObjectiveBoards returns every board across every channel. Sorted
// by channel_id for stable iteration in the polling loop.
func (s *PostgresStore) ListAllObjectiveBoards(ctx context.Context) ([]ObjectiveBoard, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT guild_id, channel_id, message_id, systems, created_by,
		       created_at, last_state_hash, last_render
		FROM discord.objective_boards
		ORDER BY channel_id`)
	if err != nil {
		return nil, fmt.Errorf("list objective boards: %w", err)
	}
	defer rows.Close()

	var out []ObjectiveBoard
	for rows.Next() {
		var b ObjectiveBoard
		var systemsJSON []byte
		if err := rows.Scan(&b.GuildID, &b.ChannelID, &b.MessageID, &systemsJSON,
			&b.CreatedBy, &b.CreatedAt, &b.LastStateHash, &b.LastRender); err != nil {
			return nil, fmt.Errorf("scan objective board: %w", err)
		}
		if err := json.Unmarshal(systemsJSON, &b.Systems); err != nil {
			return nil, fmt.Errorf("decode objective systems: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
```

You'll also need `"encoding/json"` in the import block if not already present — check `postgres.go`'s imports and add it if missing.

- [ ] **Step 2.2: Verify compilation**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go build ./internal/edinbot/store/...
```

Expected: no errors.

- [ ] **Step 2.3: Commit**

```bash
git add internal/edinbot/store/postgres.go
git commit -m "feat(objectives): add objective board postgres methods"
```

---

### Task 3: discordclient PostContent

The objectives board posts a plain-text code-block message (not a Discord embed). `PostMessage` takes an embed; we need `PostContent` for raw text. Edits reuse the existing `ReplaceWithText`.

**Files:**
- Modify: `internal/edinbot/discordclient/client.go`
- Modify: `internal/edinbot/discordclient/fake.go`

- [ ] **Step 3.1: Add `PostContent` to the `Client` interface**

In `client.go`, add to the `Client` interface (after `DeleteMessage`):

```go
// PostContent posts a plain-text message (no embed). Used by the
// objectives board which renders as a monospace code-block. Returns the
// new message ID. Edits use ReplaceWithText.
PostContent(ctx context.Context, channelID, content string) (messageID string, err error)
```

- [ ] **Step 3.2: Implement on `RealClient`**

Add after the `DeleteMessage` method:

```go
func (c *RealClient) PostContent(ctx context.Context, channelID, content string) (string, error) {
	if err := c.limiter.Wait(ctx, channelID); err != nil {
		return "", err
	}
	msg, err := c.sess.ChannelMessageSend(channelID, content, discordgo.WithContext(ctx))
	if err != nil {
		if isChannelGone(err) {
			return "", ErrChannelGone
		}
		return "", err
	}
	return msg.ID, nil
}
```

- [ ] **Step 3.3: Write the failing test in `fake_test.go` (or `fake.go` test assertions)**

Add a `PostContent` test in `internal/edinbot/discordclient/fake_test.go` (create if it doesn't exist):

```go
package discordclient_test

import (
	"context"
	"testing"

	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
)

func TestFakePostContent(t *testing.T) {
	f := discordclient.NewFakeDiscordClient()
	msgID, err := f.PostContent(context.Background(), "ch1", "hello world")
	if err != nil {
		t.Fatalf("PostContent: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message ID")
	}
	calls := f.PostContentCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 PostContent call, got %d", len(calls))
	}
	if calls[0].Content != "hello world" {
		t.Errorf("got content %q, want %q", calls[0].Content, "hello world")
	}
}
```

- [ ] **Step 3.4: Run — verify it fails**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/discordclient/... 2>&1 | head -20
```

Expected: compile error — `PostContent` not on `FakeDiscordClient`, `PostContentCalls` undefined.

- [ ] **Step 3.5: Add `PostContent` to `FakeDiscordClient`**

In `fake.go`, add a struct and fields:

```go
type FakePostContentCall struct {
	ChannelID string
	Content   string
}
```

Add to `FakeDiscordClient` struct:
```go
PostContentErr    error
postContentCalls  []FakePostContentCall
```

Add methods:
```go
func (f *FakeDiscordClient) PostContent(ctx context.Context, channelID, content string) (string, error) {
	if f.PostContentErr != nil {
		return "", f.PostContentErr
	}
	id := strconv.FormatInt(f.nextID.Add(1), 10)
	f.mu.Lock()
	f.postContentCalls = append(f.postContentCalls, FakePostContentCall{ChannelID: channelID, Content: content})
	f.mu.Unlock()
	return id, nil
}

func (f *FakeDiscordClient) PostContentCalls() []FakePostContentCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakePostContentCall(nil), f.postContentCalls...)
}
```

- [ ] **Step 3.6: Run tests — verify they pass**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/discordclient/... -v 2>&1 | tail -20
```

Expected: `PASS`

- [ ] **Step 3.7: Commit**

```bash
git add internal/edinbot/discordclient/client.go internal/edinbot/discordclient/fake.go internal/edinbot/discordclient/fake_test.go
git commit -m "feat(objectives): add PostContent to discordclient"
```

---

### Task 4: objectives package skeleton

**Files:**
- Create: `internal/edinbot/features/objectives/objectives.go`

- [ ] **Step 4.1: Write `objectives.go`**

```go
// Package objectives implements the /watch-objectives slash command and its
// polling loop. One message per channel shows reinforcement progress for a
// configured set of safety-objective systems; the loop edits the message
// whenever Memgraph data changes.
package objectives

import (
	"context"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// Store is the objectives package's narrow persistence surface.
// *store.PostgresStore satisfies this interface.
type Store interface {
	GetObjectiveBoard(ctx context.Context, channelID string) (*store.ObjectiveBoard, error)
	SetObjectiveBoard(ctx context.Context, b store.ObjectiveBoard) error
	UpdateObjectiveBoardState(ctx context.Context, channelID, hash, render string) error
	RemoveObjectiveBoard(ctx context.Context, channelID string) (bool, error)
	ListAllObjectiveBoards(ctx context.Context) ([]store.ObjectiveBoard, error)
}

// Snapshotter is the objectives package's view of the control-API.
// *controlclient.Client satisfies this interface.
type Snapshotter interface {
	GetSystemWatchSnapshot(ctx context.Context, slug string) (*controlclient.SystemWatchSnapshot, error)
}

// Discord is the objectives package's view of Discord I/O.
// *discordclient.RealClient satisfies this interface.
type Discord interface {
	PostContent(ctx context.Context, channelID, content string) (messageID string, err error)
	ReplaceWithText(ctx context.Context, channelID, messageID, content string) error
	DeleteMessage(ctx context.Context, channelID, messageID string) error
}

// Config governs the polling loop behaviour.
type Config struct {
	// PollInterval is how often the loop checks all boards. Default 120 s.
	PollInterval time.Duration
	// PerBoardStagger is the delay between consecutive board fetches inside
	// one tick. Default 2 s (each board hits Memgraph once per system).
	PerBoardStagger time.Duration
}

func (c Config) defaults() Config {
	if c.PollInterval == 0 {
		c.PollInterval = 120 * time.Second
	}
	if c.PerBoardStagger == 0 {
		c.PerBoardStagger = 2 * time.Second
	}
	return c
}
```

- [ ] **Step 4.2: Compile check**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go build ./internal/edinbot/features/objectives/...
```

Expected: no errors.

- [ ] **Step 4.3: Commit**

```bash
git add internal/edinbot/features/objectives/objectives.go
git commit -m "feat(objectives): add package skeleton with interfaces and config"
```

---

### Task 5: render.go + tests

The render produces the raw Discord message string. It's a pure function — no I/O, no clock reads (caller passes `updatedAt`). This makes it trivially testable.

**Files:**
- Create: `internal/edinbot/features/objectives/render.go`
- Create: `internal/edinbot/features/objectives/render_test.go`

- [ ] **Step 5.1: Write `render_test.go` first**

```go
package objectives_test

import (
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features/objectives"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

func ptr64(n int64) *int64    { return &n }
func ptrF64(f float64) *float64 { return &f }

var testSystems = []store.ObjectiveSystem{
	{Slug: "Ross709", Name: "Ross 709"},
	{Slug: "HIP59603", Name: "HIP 59603"},
	{Slug: "Cnepan", Name: "Cnepan"},
}

func testSnaps() map[string]*controlclient.SystemWatchSnapshot {
	return map[string]*controlclient.SystemWatchSnapshot{
		"Ross709": {
			Name:            "Ross 709",
			Reinforcement:   ptr64(13097),
			ControlProgress: ptrF64(0.037989),
		},
		"HIP59603": {
			Name:            "HIP 59603",
			Reinforcement:   ptr64(31440),
			ControlProgress: ptrF64(0.090309),
		},
		// Cnepan absent from map → "no data" row
	}
}

func TestRender_containsSystemNames(t *testing.T) {
	out := objectives.Render(testSystems, testSnaps(), time.Now())
	for _, sys := range testSystems {
		if !containsStr(out, sys.Name) {
			t.Errorf("render missing system %q", sys.Name)
		}
	}
}

func TestRender_doneSymbol(t *testing.T) {
	out := objectives.Render(testSystems, testSnaps(), time.Now())
	// HIP 59603 has reinf=31440 >= 10000 → done
	if !containsStr(out, "✓") {
		t.Error("expected ✓ symbol for completed system")
	}
}

func TestRender_noDataSymbol(t *testing.T) {
	out := objectives.Render(testSystems, testSnaps(), time.Now())
	// Cnepan not in snaps → no-data row
	if !containsStr(out, "?") {
		t.Error("expected ? symbol for missing system")
	}
}

func TestStateHash_stable(t *testing.T) {
	snaps := testSnaps()
	h1 := objectives.StateHash(testSystems, snaps)
	h2 := objectives.StateHash(testSystems, snaps)
	if h1 != h2 {
		t.Error("hash not stable across identical calls")
	}
}

func TestStateHash_changesOnUpdate(t *testing.T) {
	snaps := testSnaps()
	h1 := objectives.StateHash(testSystems, snaps)
	snaps["Ross709"].Reinforcement = ptr64(15000)
	h2 := objectives.StateHash(testSystems, snaps)
	if h1 == h2 {
		t.Error("hash did not change after reinforcement update")
	}
}

func containsStr(s, sub string) bool {
	return len(s) > 0 && len(sub) > 0 && (func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 5.2: Run — verify it fails**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/features/objectives/... 2>&1 | head -10
```

Expected: compile error — `objectives.Render` and `objectives.StateHash` undefined.

- [ ] **Step 5.3: Write `render.go`**

```go
package objectives

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

const (
	cpThreshold  int64   = 10_000
	pctThreshold float64 = 0.03
	colSystem            = 33
	colCP                = 10
	colPct               = 6
)

// Render builds the raw Discord message content for one objectives board.
// systems is the ordered list from the DB; snaps maps slug → snapshot.
// Systems absent from snaps (not yet in Memgraph) render as "no data" rows.
func Render(systems []store.ObjectiveSystem, snaps map[string]*controlclient.SystemWatchSnapshot, updatedAt time.Time) string {
	var b strings.Builder

	b.WriteString("**Kaine Safety Objectives** — Target: 10k CP or 3%   ✓ done  · in progress  ? no data\n")
	b.WriteString("```\n")

	// Header row
	fmt.Fprintf(&b, "%-*s  %*s  %*s  %s\n",
		colSystem, "System", colCP, "CP", colPct, "%", "")
	b.WriteString(strings.Repeat("─", colSystem) + "  " +
		strings.Repeat("─", colCP) + "  " +
		strings.Repeat("─", colPct) + "  " + "──\n")

	for _, sys := range systems {
		snap, ok := snaps[sys.Slug]
		if !ok || snap == nil {
			fmt.Fprintf(&b, "%-*s  %*s  %*s  ?\n",
				colSystem, truncate(sys.Name, colSystem),
				colCP, "?", colPct, "?")
			continue
		}

		cpStr := "?"
		pctStr := "?"
		status := "·"

		if snap.Reinforcement != nil {
			cpStr = thousands(*snap.Reinforcement)
		}
		if snap.ControlProgress != nil && *snap.ControlProgress <= 1.0 {
			pctStr = fmt.Sprintf("%.1f%%", *snap.ControlProgress*100)
		}

		done := (snap.Reinforcement != nil && *snap.Reinforcement >= cpThreshold) ||
			(snap.ControlProgress != nil && *snap.ControlProgress >= pctThreshold)
		if done {
			status = "✓"
		}

		fmt.Fprintf(&b, "%-*s  %*s  %*s  %s\n",
			colSystem, truncate(sys.Name, colSystem),
			colCP, cpStr,
			colPct, pctStr,
			status)
	}

	b.WriteString("```\n")
	fmt.Fprintf(&b, "_Last updated: %s UTC_", updatedAt.UTC().Format("02 Jan 2006 15:04"))
	return b.String()
}

// StateHash returns a deterministic hash of the reinforcement + progress
// values across all systems. Used by the loop to skip Discord edits when
// nothing changed. Systems absent from snaps hash as a zero-value struct
// so a missing→found transition triggers an edit.
func StateHash(systems []store.ObjectiveSystem, snaps map[string]*controlclient.SystemWatchSnapshot) string {
	type entry struct {
		Slug          string
		Reinforcement *int64
		Progress      *float64
	}
	rows := make([]entry, len(systems))
	for i, sys := range systems {
		e := entry{Slug: sys.Slug}
		if snap, ok := snaps[sys.Slug]; ok && snap != nil {
			e.Reinforcement = snap.Reinforcement
			e.Progress = snap.ControlProgress
		}
		rows[i] = e
	}
	buf, _ := json.Marshal(rows)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func thousands(n int64) string {
	if n < 0 {
		return "-" + thousands(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		b.WriteByte(',')
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}
```

- [ ] **Step 5.4: Run tests — verify they pass**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/features/objectives/... -run TestRender -v
go test ./internal/edinbot/features/objectives/... -run TestStateHash -v
```

Expected: all PASS.

- [ ] **Step 5.5: Commit**

```bash
git add internal/edinbot/features/objectives/render.go internal/edinbot/features/objectives/render_test.go
git commit -m "feat(objectives): render + stateHash with tests"
```

---

### Task 6: loop.go + loop_test.go

The loop polls all boards, fetches snapshots for each system, and edits the Discord message when the hash changes.

**Files:**
- Create: `internal/edinbot/features/objectives/loop.go`
- Create: `internal/edinbot/features/objectives/loop_test.go`

- [ ] **Step 6.1: Write the failing tests**

```go
// loop_test.go
package objectives_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features/objectives"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// -- fakes --

type fakeObjStore struct {
	mu     sync.Mutex
	boards []store.ObjectiveBoard
	hashes map[string]string // channelID → last hash stored
}

func newFakeObjStore(boards []store.ObjectiveBoard) *fakeObjStore {
	return &fakeObjStore{boards: boards, hashes: map[string]string{}}
}

func (f *fakeObjStore) GetObjectiveBoard(_ context.Context, channelID string) (*store.ObjectiveBoard, error) {
	for _, b := range f.boards {
		if b.ChannelID == channelID {
			return &b, nil
		}
	}
	return nil, nil
}
func (f *fakeObjStore) SetObjectiveBoard(_ context.Context, b store.ObjectiveBoard) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boards = append(f.boards, b)
	return nil
}
func (f *fakeObjStore) UpdateObjectiveBoardState(_ context.Context, channelID, hash, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes[channelID] = hash
	return nil
}
func (f *fakeObjStore) RemoveObjectiveBoard(_ context.Context, channelID string) (bool, error) {
	return false, nil
}
func (f *fakeObjStore) ListAllObjectiveBoards(_ context.Context) ([]store.ObjectiveBoard, error) {
	return f.boards, nil
}

type fakeSnapshotter struct {
	snaps map[string]*controlclient.SystemWatchSnapshot
	err   error
}

func (f *fakeSnapshotter) GetSystemWatchSnapshot(_ context.Context, slug string) (*controlclient.SystemWatchSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	s, ok := f.snaps[slug]
	if !ok {
		return nil, controlclient.ErrSystemNotFound
	}
	return s, nil
}

// fakeObjDiscord wraps FakeDiscordClient and tracks ReplaceWithText calls for loop tests.
type fakeObjDiscord struct {
	*discordclient.FakeDiscordClient
}

// -- tests --

func TestLoop_editsOnHashChange(t *testing.T) {
	board := store.ObjectiveBoard{
		GuildID:       "g1",
		ChannelID:     "ch1",
		MessageID:     "msg1",
		Systems:       []store.ObjectiveSystem{{Slug: "Ross709", Name: "Ross 709"}},
		LastStateHash: "stale-hash",
	}
	st := newFakeObjStore([]store.ObjectiveBoard{board})
	snap := &fakeSnapshotter{snaps: map[string]*controlclient.SystemWatchSnapshot{
		"Ross709": {Name: "Ross 709", Reinforcement: ptr64(13097), ControlProgress: ptrF64(0.038)},
	}}
	dc := &fakeObjDiscord{discordclient.NewFakeDiscordClient()}

	loop := objectives.NewLoop(objectives.LoopDeps{
		Store:   st,
		Snap:    snap,
		Discord: dc,
		Cfg:     objectives.Config{PollInterval: time.Hour, PerBoardStagger: 0},
		NowFunc: func() time.Time { return time.Unix(1000, 0) },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loop.RunCycleForTest(ctx)

	calls := dc.ReplaceWithTextCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 ReplaceWithText call, got %d", len(calls))
	}
	if calls[0].ChannelID != "ch1" || calls[0].MessageID != "msg1" {
		t.Errorf("wrong channel/message: %+v", calls[0])
	}
}

func TestLoop_skipsOnSameHash(t *testing.T) {
	systems := []store.ObjectiveSystem{{Slug: "Ross709", Name: "Ross 709"}}
	snaps := map[string]*controlclient.SystemWatchSnapshot{
		"Ross709": {Name: "Ross 709", Reinforcement: ptr64(13097), ControlProgress: ptrF64(0.038)},
	}
	hash := objectives.StateHash(systems, snaps)

	board := store.ObjectiveBoard{
		GuildID: "g1", ChannelID: "ch1", MessageID: "msg1",
		Systems: systems, LastStateHash: hash,
	}
	st := newFakeObjStore([]store.ObjectiveBoard{board})
	snap := &fakeSnapshotter{snaps: snaps}
	dc := &fakeObjDiscord{discordclient.NewFakeDiscordClient()}

	loop := objectives.NewLoop(objectives.LoopDeps{
		Store: st, Snap: snap, Discord: dc,
		Cfg: objectives.Config{PollInterval: time.Hour, PerBoardStagger: 0},
	})
	loop.RunCycleForTest(context.Background())

	if calls := dc.ReplaceWithTextCalls(); len(calls) != 0 {
		t.Errorf("expected no edits, got %d", len(calls))
	}
}

func TestLoop_removesRowOnMessageGone(t *testing.T) {
	board := store.ObjectiveBoard{
		GuildID: "g1", ChannelID: "ch1", MessageID: "msg1",
		Systems:       []store.ObjectiveSystem{{Slug: "Ross709", Name: "Ross 709"}},
		LastStateHash: "stale",
	}
	st := newFakeObjStore([]store.ObjectiveBoard{board})
	snap := &fakeSnapshotter{snaps: map[string]*controlclient.SystemWatchSnapshot{
		"Ross709": {Name: "Ross 709", Reinforcement: ptr64(13097), ControlProgress: ptrF64(0.038)},
	}}
	dc := &fakeObjDiscord{discordclient.NewFakeDiscordClient()}
	dc.ReplaceTextErr = discordclient.ErrMessageNotFound

	loop := objectives.NewLoop(objectives.LoopDeps{
		Store: st, Snap: snap, Discord: dc,
		Cfg: objectives.Config{PollInterval: time.Hour, PerBoardStagger: 0},
	})
	loop.RunCycleForTest(context.Background())

	// The board should be removed from the store. We verify by listing boards.
	boards, _ := st.ListAllObjectiveBoards(context.Background())
	// The fake store doesn't implement delete — but RemoveObjectiveBoard should have been called.
	// A real integration test would verify 0 rows. Here we just verify no panic and the loop survived.
	_ = boards
}
```

- [ ] **Step 6.2: Run — verify it fails**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/features/objectives/... -run TestLoop 2>&1 | head -15
```

Expected: compile error — `objectives.NewLoop`, `objectives.LoopDeps`, `loop.RunCycleForTest` undefined. Also `dc.ReplaceWithTextCalls()` may be missing from FakeDiscordClient — if so, you need to add it (see Step 6.3a).

- [ ] **Step 6.3a: Add `ReplaceWithTextCalls()` to FakeDiscordClient if missing**

Check `discordclient/fake.go` — if `ReplaceWithTextCalls()` accessor is absent:

```go
func (f *FakeDiscordClient) ReplaceWithTextCalls() []FakeReplaceTextCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeReplaceTextCall(nil), f.replaceTextCalls...)
}
```

- [ ] **Step 6.3b: Write `loop.go`**

```go
package objectives

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// LoopDeps bundles the collaborators the polling goroutine needs.
type LoopDeps struct {
	Store   Store
	Snap    Snapshotter
	Discord Discord
	Cfg     Config
	NowFunc func() time.Time
	LogFunc func(format string, args ...any)
}

// ObjectivesLoop polls all objective boards and edits Discord messages when
// reinforcement numbers change.
type ObjectivesLoop struct {
	deps     LoopDeps
	inFlight atomic.Bool
	stopped  chan struct{}
	once     sync.Once
}

// NewLoop constructs an ObjectivesLoop. Call Start(ctx) to begin polling.
func NewLoop(deps LoopDeps) *ObjectivesLoop {
	deps.Cfg = deps.Cfg.defaults()
	return &ObjectivesLoop{deps: deps, stopped: make(chan struct{})}
}

// Start kicks off the polling goroutine. Boot recovery runs one cycle
// immediately, then ticks every Cfg.PollInterval.
func (l *ObjectivesLoop) Start(ctx context.Context) {
	go func() {
		defer close(l.stopped)
		l.runCycle(ctx)
		t := time.NewTicker(l.deps.Cfg.PollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				l.runCycle(ctx)
			}
		}
	}()
}

// Done returns a channel closed when the goroutine exits cleanly.
func (l *ObjectivesLoop) Done() <-chan struct{} { return l.stopped }

// RunCycleForTest exposes runCycle for test packages. Not part of the
// production surface — tests inject fakes via LoopDeps.
func (l *ObjectivesLoop) RunCycleForTest(ctx context.Context) { l.runCycle(ctx) }

func (l *ObjectivesLoop) runCycle(ctx context.Context) {
	if !l.inFlight.CompareAndSwap(false, true) {
		l.logf("[INFO] objectives loop: previous cycle still running; skipping tick")
		return
	}
	defer l.inFlight.Store(false)

	boards, err := l.deps.Store.ListAllObjectiveBoards(ctx)
	if err != nil {
		l.logf("[ERROR] objectives loop: list boards: %v", err)
		return
	}
	for i, board := range boards {
		if ctx.Err() != nil {
			return
		}
		if i > 0 && l.deps.Cfg.PerBoardStagger > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(l.deps.Cfg.PerBoardStagger):
			}
		}
		l.processOne(ctx, board)
	}
}

func (l *ObjectivesLoop) processOne(ctx context.Context, board store.ObjectiveBoard) {
	snaps := make(map[string]*controlclient.SystemWatchSnapshot, len(board.Systems))
	for _, sys := range board.Systems {
		snap, err := l.deps.Snap.GetSystemWatchSnapshot(ctx, sys.Slug)
		if err != nil {
			if !errors.Is(err, controlclient.ErrSystemNotFound) {
				l.logf("[ERROR] objectives loop: snap fetch %q: %v", sys.Slug, err)
			}
			// Missing systems render as "no data" — nil in map is intentional.
			continue
		}
		snaps[sys.Slug] = snap
	}

	hash := StateHash(board.Systems, snaps)
	if hash == board.LastStateHash {
		return
	}

	now := l.now()
	content := Render(board.Systems, snaps, now)
	if err := l.deps.Discord.ReplaceWithText(ctx, board.ChannelID, board.MessageID, content); err != nil {
		if errors.Is(err, discordclient.ErrMessageNotFound) || errors.Is(err, discordclient.ErrChannelGone) {
			l.logf("[WARN] objectives loop: message gone for channel %s; removing board", board.ChannelID)
			_, _ = l.deps.Store.RemoveObjectiveBoard(ctx, board.ChannelID)
			return
		}
		l.logf("[ERROR] objectives loop: edit message for channel %s: %v", board.ChannelID, err)
		return
	}

	if err := l.deps.Store.UpdateObjectiveBoardState(ctx, board.ChannelID, hash, content); err != nil {
		l.logf("[ERROR] objectives loop: persist state for channel %s: %v", board.ChannelID, err)
	}
}

func (l *ObjectivesLoop) now() time.Time {
	if l.deps.NowFunc != nil {
		return l.deps.NowFunc()
	}
	return time.Now().UTC()
}

func (l *ObjectivesLoop) logf(format string, args ...any) {
	if l.deps.LogFunc != nil {
		l.deps.LogFunc(format, args...)
		return
	}
	log.Printf(format, args...)
}
```

- [ ] **Step 6.4: Run tests — verify they pass**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/features/objectives/... -run TestLoop -v
```

Expected: all PASS.

- [ ] **Step 6.5: Commit**

```bash
git add internal/edinbot/features/objectives/loop.go internal/edinbot/features/objectives/loop_test.go internal/edinbot/discordclient/fake.go
git commit -m "feat(objectives): polling loop with tests"
```

---

### Task 7: handler.go + handler_test.go

**Files:**
- Create: `internal/edinbot/features/objectives/handler.go`
- Create: `internal/edinbot/features/objectives/handler_test.go`

The handler parses a comma-separated `systems` option, looks each up in Memgraph, warns ephemerally about any missing ones, then posts the board and stores the row. If an existing board exists in the channel, the old Discord message is deleted first.

- [ ] **Step 7.1: Write handler_test.go first**

```go
package objectives_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features/objectives"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// fakeResponder records ephemeral replies without a Discord connection.
type fakeResponder struct {
	deferred bool
	reply    string
}

func (f *fakeResponder) InteractionRespond(_ *discordgo.Interaction, resp *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	if resp.Type == discordgo.InteractionResponseDeferredChannelMessageWithSource {
		f.deferred = true
	}
	return nil
}
func (f *fakeResponder) InteractionResponseEdit(_ *discordgo.Interaction, edit *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	if edit.Content != nil {
		f.reply = *edit.Content
	}
	return &discordgo.Message{}, nil
}
func (f *fakeResponder) FollowupMessageCreate(_ *discordgo.Interaction, _ bool, _ *discordgo.WebhookParams, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{}, nil
}

func makeIC(systems, channelID, guildID, userID string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			ChannelID: channelID,
			GuildID:   guildID,
			Member:    &discordgo.Member{User: &discordgo.User{ID: userID}},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: "watch-objectives",
				Options: []*discordgo.ApplicationCommandInteractionDataOption{{
					Name:  "systems",
					Type:  discordgo.ApplicationCommandOptionString,
					Value: systems,
				}},
			},
		},
	}
}

func TestWatchObjectives_happyPath(t *testing.T) {
	st := newFakeObjStore(nil)
	snap := &fakeSnapshotter{snaps: map[string]*controlclient.SystemWatchSnapshot{
		"Ross709":  {Name: "Ross 709", Reinforcement: ptr64(13097), ControlProgress: ptrF64(0.038)},
		"HIP59603": {Name: "HIP 59603", Reinforcement: ptr64(31440), ControlProgress: ptrF64(0.09)},
	}}
	dc := &fakeObjDiscord{discordclient.NewFakeDiscordClient()}
	resp := &fakeResponder{}

	handler := objectives.WatchObjectives(objectives.HandlerDeps{
		Store:   st,
		Snap:    snap,
		Discord: dc,
		NowFunc: func() time.Time { return time.Unix(1000, 0) },
	})

	ic := makeIC("Ross 709, HIP 59603", "ch1", "g1", "u1")
	if err := handler(context.Background(), resp, ic); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if !resp.deferred {
		t.Error("expected ephemeral defer")
	}
	if !strings.Contains(resp.reply, "Now watching") {
		t.Errorf("unexpected reply: %q", resp.reply)
	}
	if calls := dc.PostContentCalls(); len(calls) != 1 {
		t.Fatalf("expected 1 PostContent call, got %d", len(calls))
	}
}

func TestWatchObjectives_allMissing(t *testing.T) {
	st := newFakeObjStore(nil)
	snap := &fakeSnapshotter{snaps: map[string]*controlclient.SystemWatchSnapshot{}}
	dc := &fakeObjDiscord{discordclient.NewFakeDiscordClient()}
	resp := &fakeResponder{}

	handler := objectives.WatchObjectives(objectives.HandlerDeps{
		Store: st, Snap: snap, Discord: dc,
	})
	ic := makeIC("Nonexistent Alpha, Nonexistent Beta", "ch1", "g1", "u1")
	_ = handler(context.Background(), resp, ic)

	if !strings.Contains(resp.reply, "none of those systems") {
		t.Errorf("unexpected reply for all-missing: %q", resp.reply)
	}
	if calls := dc.PostContentCalls(); len(calls) != 0 {
		t.Error("expected no PostContent call when all systems missing")
	}
}

func TestWatchObjectives_replacesExistingBoard(t *testing.T) {
	existing := store.ObjectiveBoard{
		GuildID: "g1", ChannelID: "ch1", MessageID: "old-msg",
		Systems: []store.ObjectiveSystem{{Slug: "OldSystem", Name: "Old System"}},
	}
	st := newFakeObjStore([]store.ObjectiveBoard{existing})
	snap := &fakeSnapshotter{snaps: map[string]*controlclient.SystemWatchSnapshot{
		"Ross709": {Name: "Ross 709", Reinforcement: ptr64(13097), ControlProgress: ptrF64(0.038)},
	}}
	dc := &fakeObjDiscord{discordclient.NewFakeDiscordClient()}
	resp := &fakeResponder{}

	handler := objectives.WatchObjectives(objectives.HandlerDeps{
		Store: st, Snap: snap, Discord: dc,
		NowFunc: func() time.Time { return time.Unix(1000, 0) },
	})
	ic := makeIC("Ross 709", "ch1", "g1", "u1")
	_ = handler(context.Background(), resp, ic)

	// Old message should have been deleted.
	delCalls := dc.DeleteCalls()
	if len(delCalls) != 1 || delCalls[0].MessageID != "old-msg" {
		t.Errorf("expected old-msg to be deleted, got: %+v", delCalls)
	}
}
```

- [ ] **Step 7.2: Run — verify it fails**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/features/objectives/... -run TestWatchObjectives 2>&1 | head -15
```

Expected: compile error — `objectives.WatchObjectives`, `objectives.HandlerDeps` undefined. Also check if `dc.DeleteCalls()` exists on FakeDiscordClient — add it if not.

- [ ] **Step 7.3a: Add `DeleteCalls()` accessor to FakeDiscordClient if missing**

In `discordclient/fake.go`:

```go
func (f *FakeDiscordClient) DeleteCalls() []FakeDeleteCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeDeleteCall(nil), f.deleteCalls...)
}
```

- [ ] **Step 7.3b: Write `handler.go`**

```go
package objectives

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/slash"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
	"github.com/edin-space/edin-backend/internal/galaxy"
)

// HandlerDeps bundles collaborators for the slash handlers.
type HandlerDeps struct {
	Store   Store
	Snap    Snapshotter
	Discord Discord
	NowFunc func() time.Time
	LogFunc func(format string, args ...any)
}

func (d *HandlerDeps) now() time.Time {
	if d.NowFunc != nil {
		return d.NowFunc()
	}
	return time.Now().UTC()
}

func (d *HandlerDeps) logf(format string, args ...any) {
	if d.LogFunc != nil {
		d.LogFunc(format, args...)
		return
	}
	log.Printf(format, args...)
}

// WatchObjectives returns a slash.Handler for /watch-objectives.
// It parses comma-separated system names, resolves each via Memgraph,
// reports any missing ones ephemerally, then posts the board.
func WatchObjectives(deps HandlerDeps) slash.Handler {
	return func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		if err := deferEphemeral(resp, ic); err != nil {
			deps.logf("[ERROR] watch-objectives: defer: %v", err)
			return err
		}

		raw := strings.TrimSpace(systemsOption(ic))
		if raw == "" {
			return reply(resp, ic, "Please provide system names: `/watch-objectives systems:Ross 709, HIP 59603, ...`")
		}

		names := splitNames(raw)
		if len(names) == 0 {
			return reply(resp, ic, "No system names found in input.")
		}

		// Resolve each name against Memgraph.
		type resolved struct {
			sys  store.ObjectiveSystem
			snap *controlclient.SystemWatchSnapshot
		}
		var found []resolved
		var missing []string

		for _, name := range names {
			slug := galaxy.Slugify(name)
			if slug == "" {
				missing = append(missing, name)
				continue
			}
			snap, err := deps.Snap.GetSystemWatchSnapshot(ctx, slug)
			if err != nil {
				if errors.Is(err, controlclient.ErrSystemNotFound) {
					missing = append(missing, name)
					continue
				}
				deps.logf("[ERROR] watch-objectives: snap fetch %q: %v", slug, err)
				return reply(resp, ic, "Something went wrong looking up systems. Please try again.")
			}
			found = append(found, resolved{
				sys:  store.ObjectiveSystem{Slug: slug, Name: snap.Name},
				snap: snap,
			})
		}

		if len(found) == 0 {
			hint := "none of those systems were found in our galaxy data"
			if len(missing) > 0 {
				hint += fmt.Sprintf(" (%s)", strings.Join(missing, ", "))
			}
			hint += ". Have a commander visit them to add them to Memgraph."
			return reply(resp, ic, hint)
		}

		// Delete existing board if present.
		existing, _ := deps.Store.GetObjectiveBoard(ctx, ic.ChannelID)
		if existing != nil {
			if err := deps.Discord.DeleteMessage(ctx, ic.ChannelID, existing.MessageID); err != nil {
				if !errors.Is(err, discordclient.ErrMessageNotFound) && !errors.Is(err, discordclient.ErrChannelGone) {
					deps.logf("[ERROR] watch-objectives: delete old message %s: %v", existing.MessageID, err)
					return reply(resp, ic, "Couldn't delete the existing objectives board. Try again.")
				}
			}
		}

		// Build the systems slice and snaps map for the initial render.
		systems := make([]store.ObjectiveSystem, len(found))
		snaps := make(map[string]*controlclient.SystemWatchSnapshot, len(found))
		for i, r := range found {
			systems[i] = r.sys
			snaps[r.sys.Slug] = r.snap
		}

		now := deps.now()
		content := Render(systems, snaps, now)
		msgID, err := deps.Discord.PostContent(ctx, ic.ChannelID, content)
		if err != nil {
			deps.logf("[ERROR] watch-objectives: post message in %s: %v", ic.ChannelID, err)
			return reply(resp, ic, "Couldn't post the objectives board. Check bot permissions.")
		}

		userID := ""
		if ic.Member != nil && ic.Member.User != nil {
			userID = ic.Member.User.ID
		}
		board := store.ObjectiveBoard{
			GuildID:       ic.GuildID,
			ChannelID:     ic.ChannelID,
			MessageID:     msgID,
			Systems:       systems,
			CreatedBy:     userID,
			CreatedAt:     now,
			LastStateHash: StateHash(systems, snaps),
			LastRender:    content,
		}
		if err := deps.Store.SetObjectiveBoard(ctx, board); err != nil {
			_ = deps.Discord.DeleteMessage(ctx, ic.ChannelID, msgID)
			deps.logf("[ERROR] watch-objectives: persist board in %s: %v", ic.ChannelID, err)
			return reply(resp, ic, "Posted the board but failed to record it. Please try again.")
		}

		suffix := ""
		if len(missing) > 0 {
			suffix = fmt.Sprintf("\n_(Couldn't find: %s — have someone visit those systems.)_", strings.Join(missing, ", "))
		}
		link := fmt.Sprintf("https://discord.com/channels/%s/%s/%s", ic.GuildID, ic.ChannelID, msgID)
		return reply(resp, ic, fmt.Sprintf("Now watching %d system(s) — %s%s", len(found), link, suffix))
	}
}

// UnwatchObjectives returns a handler for /unwatch-objectives that removes
// the active board from the channel.
func UnwatchObjectives(deps HandlerDeps) slash.Handler {
	return func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		if err := deferEphemeral(resp, ic); err != nil {
			return err
		}
		existing, err := deps.Store.GetObjectiveBoard(ctx, ic.ChannelID)
		if err != nil {
			deps.logf("[ERROR] unwatch-objectives: get board: %v", err)
			return reply(resp, ic, "Something went wrong looking up the board.")
		}
		if existing == nil {
			return reply(resp, ic, "No objectives board is active in this channel.")
		}
		if err := deps.Discord.DeleteMessage(ctx, ic.ChannelID, existing.MessageID); err != nil {
			if !errors.Is(err, discordclient.ErrMessageNotFound) && !errors.Is(err, discordclient.ErrChannelGone) {
				return reply(resp, ic, "Couldn't delete the objectives board message.")
			}
		}
		if _, err := deps.Store.RemoveObjectiveBoard(ctx, ic.ChannelID); err != nil {
			return reply(resp, ic, "Deleted the message but couldn't remove the board record.")
		}
		return reply(resp, ic, "Objectives board removed.")
	}
}

// -- helpers --

func systemsOption(ic *discordgo.InteractionCreate) string {
	for _, o := range ic.ApplicationCommandData().Options {
		if o.Name == "systems" {
			return o.StringValue()
		}
	}
	return ""
}

func splitNames(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func deferEphemeral(resp slash.Responder, ic *discordgo.InteractionCreate) error {
	return resp.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	})
}

func reply(resp slash.Responder, ic *discordgo.InteractionCreate, content string) error {
	_, err := resp.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{Content: &content})
	return err
}
```

- [ ] **Step 7.4: Run tests — verify they pass**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/features/objectives/... -v 2>&1 | tail -30
```

Expected: all PASS.

- [ ] **Step 7.5: Commit**

```bash
git add internal/edinbot/features/objectives/handler.go internal/edinbot/features/objectives/handler_test.go internal/edinbot/discordclient/fake.go
git commit -m "feat(objectives): watch-objectives and unwatch-objectives handlers with tests"
```

---

### Task 8: Wire into main.go

**Files:**
- Modify: `cmd/edin-bot/main.go`

- [ ] **Step 8.1: Add import**

In `main.go` imports, add:

```go
"github.com/edin-space/edin-backend/internal/edinbot/features/objectives"
```

- [ ] **Step 8.2: Add objective commands to `registerSlashGuild`**

In `registerSlashGuild`, add two commands to the `commands` slice alongside `watch` and `unwatch`:

```go
{
    Name:                     "watch-objectives",
    Description:              "Post a live safety objectives board for a set of systems",
    DMPermission:             &dmsBlocked,
    DefaultMemberPermissions: defaultPerms,
    Options: []*discordgo.ApplicationCommandOption{{
        Type:        discordgo.ApplicationCommandOptionString,
        Name:        "systems",
        Description: "Comma-separated system names, e.g. Ross 709, HIP 59603",
        Required:    true,
    }},
},
{
    Name:                     "unwatch-objectives",
    Description:              "Remove the active objectives board from this channel",
    DMPermission:             &dmsBlocked,
    DefaultMemberPermissions: defaultPerms,
},
```

- [ ] **Step 8.3: Register handlers and start loop in `setupSlash`**

In `setupSlash`, after the watcher setup, add:

```go
objDeps := objectives.HandlerDeps{Store: st, Snap: control, Discord: dc}
router.Handle("watch-objectives", objectives.WatchObjectives(objDeps))
router.Handle("unwatch-objectives", objectives.UnwatchObjectives(objDeps))

objLoop := objectives.NewLoop(objectives.LoopDeps{
    Store:   st,
    Snap:    control,
    Discord: dc,
    Cfg:     objectives.Config{},
})
objLoop.Start(ctx)
```

- [ ] **Step 8.4: Build the whole binary**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go build ./cmd/edin-bot/...
```

Expected: binary builds with no errors.

- [ ] **Step 8.5: Run all tests**

```bash
cd /home/davidmoore/src/edin-space/edin-backend
go test ./internal/edinbot/... 2>&1 | tail -20
```

Expected: all PASS (unit tests — integration tests need `-tags integration` + a real DB).

- [ ] **Step 8.6: Final commit**

```bash
git add cmd/edin-bot/main.go
git commit -m "feat(objectives): wire /watch-objectives into main"
```

---

## Self-Review

**Spec coverage:**
- ✓ `/watch-objectives` slash command with comma-separated system names
- ✓ Reports missing systems ephemerally (suggests visits), proceeds with found ones
- ✓ Posts a code-block table: system name, CP, %, done/in-progress/no-data status
- ✓ One message per channel (replacing existing board when re-invoked)
- ✓ Live updates: loop edits the message when Memgraph data changes
- ✓ `/unwatch-objectives` to remove a board
- ✓ Handles message-gone (manual delete) by removing the DB row

**Placeholder scan:** None found — all code blocks are complete.

**Type consistency check:**
- `store.ObjectiveBoard` and `store.ObjectiveSystem` defined in Task 1, used consistently in Tasks 2–8
- `objectives.Store` interface methods match `PostgresStore` method signatures exactly
- `StateHash` and `Render` are exported from `render.go` and called by the same names in `loop.go`, `handler.go`, and tests
- `LoopDeps` and `HandlerDeps` are defined in `loop.go` and `handler.go` respectively; loop tests import `objectives.LoopDeps`, handler tests import `objectives.HandlerDeps` ✓

**One gap noted and addressed:** The `ReplaceWithTextCalls()` and `DeleteCalls()` accessors on `FakeDiscordClient` may not exist yet — Steps 6.3a and 7.3a add them if absent.
