package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── mock source ─────────────────────────────────────────────────────────────

type mockSource struct {
	commanders []SourceCommander
	events     map[string][]store.JournalEvent // keyed by FID
}

func (m *mockSource) ListCommanders(_ context.Context) ([]SourceCommander, error) {
	return m.commanders, nil
}

func (m *mockSource) ReadEvents(_ context.Context, fid string, offset, limit int) ([]store.JournalEvent, error) {
	all := m.events[fid]
	if offset >= len(all) {
		return nil, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

// ─── mock target ─────────────────────────────────────────────────────────────

// mockTarget implements store.CommanderRepository; only UpsertCommander and
// InsertEvents are exercised by migrate(). The rest are no-ops.
type mockTarget struct {
	upsertedFIDs []string
	events       map[string][]store.JournalEvent // events successfully inserted
}

func newMockTarget() *mockTarget {
	return &mockTarget{events: make(map[string][]store.JournalEvent)}
}

func (m *mockTarget) UpsertCommander(_ context.Context, fid, _, _ string) (uuid.UUID, error) {
	m.upsertedFIDs = append(m.upsertedFIDs, fid)
	return uuid.New(), nil
}

func (m *mockTarget) InsertEvents(_ context.Context, fid string, events []store.JournalEvent) (int, int, error) {
	// Simulate ON CONFLICT DO NOTHING: track by (event_type, timestamp) key.
	seen := make(map[string]struct{})
	for _, ev := range m.events[fid] {
		seen[ev.EventType+ev.Timestamp.String()] = struct{}{}
	}
	var inserted, duplicates int
	for _, ev := range events {
		key := ev.EventType + ev.Timestamp.String()
		if _, ok := seen[key]; ok {
			duplicates++
		} else {
			m.events[fid] = append(m.events[fid], ev)
			seen[key] = struct{}{}
			inserted++
		}
	}
	return inserted, duplicates, nil
}

func (m *mockTarget) RecentEvents(_ context.Context, _ string, _ int) ([]store.JournalEvent, error) {
	return nil, nil
}
func (m *mockTarget) EventsByType(_ context.Context, _ string, _ []string, _, _ time.Time) ([]store.JournalEvent, error) {
	return nil, nil
}
func (m *mockTarget) CurrentLocation(_ context.Context, _ string) (*store.LocationState, error) {
	return nil, nil
}
func (m *mockTarget) DeleteAllEvents(_ context.Context, _ string) error { return nil }
func (m *mockTarget) GetCommander(_ context.Context, _ string) (*store.CommanderRow, error) {
	return nil, nil
}
func (m *mockTarget) GetEventStats(_ context.Context, _ string) (*store.CommanderEventStats, error) {
	return nil, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))

func makeEvent(fid string, ts time.Time, eventType string) store.JournalEvent {
	return store.JournalEvent{
		CommanderID: uuid.New(),
		FID:         fid,
		Timestamp:   ts,
		EventType:   eventType,
		EventData:   json.RawMessage(`{}`),
	}
}

func defaultCfg() Config {
	return Config{BatchSize: 100}
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestMigrate_DryRun_NoDataWritten(t *testing.T) {
	now := time.Now().UTC()
	src := &mockSource{
		commanders: []SourceCommander{{FID: "F001", Name: "CMDR Test", Platform: "PC"}},
		events: map[string][]store.JournalEvent{
			"F001": {makeEvent("F001", now, "FSDJump")},
		},
	}
	dst := newMockTarget()
	cfg := Config{BatchSize: 100, DryRun: true}

	result, err := migrate(context.Background(), cfg, src, dst, testLogger)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Commanders, "commanders counted even in dry-run")
	assert.Equal(t, 0, result.Inserted, "dry-run must not insert events")
	assert.Empty(t, dst.upsertedFIDs, "dry-run must not upsert commanders")
	assert.Empty(t, dst.events["F001"], "dry-run must not write events to target")
}

func TestMigrate_SmallDataset_AllRowsMigrated(t *testing.T) {
	now := time.Now().UTC()
	src := &mockSource{
		commanders: []SourceCommander{
			{FID: "F001", Name: "Alpha", Platform: "PC"},
			{FID: "F002", Name: "Beta", Platform: "Xbox"},
		},
		events: map[string][]store.JournalEvent{
			"F001": {
				makeEvent("F001", now.Add(-2*time.Minute), "FSDJump"),
				makeEvent("F001", now.Add(-1*time.Minute), "Docked"),
			},
			"F002": {
				makeEvent("F002", now, "Location"),
			},
		},
	}
	dst := newMockTarget()

	result, err := migrate(context.Background(), defaultCfg(), src, dst, testLogger)
	require.NoError(t, err)

	assert.Equal(t, 2, result.Commanders)
	assert.Equal(t, 3, result.Inserted)
	assert.Equal(t, 0, result.Duplicates)
	assert.Equal(t, 0, result.Skipped)
	assert.Len(t, dst.events["F001"], 2)
	assert.Len(t, dst.events["F002"], 1)
	assert.ElementsMatch(t, []string{"F001", "F002"}, dst.upsertedFIDs)
}

func TestMigrate_DuplicateRows_Deduplicated(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ev := makeEvent("F001", now, "FSDJump")
	src := &mockSource{
		commanders: []SourceCommander{{FID: "F001", Name: "Cmdr", Platform: "PC"}},
		events: map[string][]store.JournalEvent{
			"F001": {ev, ev}, // same event appears twice in source
		},
	}
	dst := newMockTarget()

	result, err := migrate(context.Background(), defaultCfg(), src, dst, testLogger)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Inserted, "first occurrence inserted")
	assert.Equal(t, 1, result.Duplicates, "second occurrence counted as duplicate")
	assert.Len(t, dst.events["F001"], 1, "only one event stored in target")
}

func TestMigrate_InvalidEventType_SkippedAndLogged(t *testing.T) {
	now := time.Now().UTC()
	src := &mockSource{
		commanders: []SourceCommander{{FID: "F001", Name: "Cmdr", Platform: "PC"}},
		events: map[string][]store.JournalEvent{
			"F001": {
				makeEvent("F001", now, "FSDJump"),
				// event with empty EventType — should be skipped
				{FID: "F001", Timestamp: now.Add(time.Minute), EventType: "", EventData: json.RawMessage(`{}`)},
			},
		},
	}
	dst := newMockTarget()

	result, err := migrate(context.Background(), defaultCfg(), src, dst, testLogger)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Inserted, "valid event inserted")
	assert.Equal(t, 1, result.Skipped, "invalid event counted as skipped")
	assert.Len(t, dst.events["F001"], 1, "only valid event stored")
}

func TestMigrate_ReportsCorrectCounts(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	duplicate := makeEvent("F001", now, "FSDJump")
	src := &mockSource{
		commanders: []SourceCommander{{FID: "F001", Name: "Cmdr", Platform: "PC"}},
		events: map[string][]store.JournalEvent{
			"F001": {
				duplicate,                          // inserted
				duplicate,                          // duplicate
				makeEvent("F001", now.Add(time.Minute), "Docked"), // inserted
				// invalid: empty EventType
				{FID: "F001", Timestamp: now.Add(2 * time.Minute), EventType: "", EventData: json.RawMessage(`{}`)},
			},
		},
	}
	dst := newMockTarget()

	result, err := migrate(context.Background(), defaultCfg(), src, dst, testLogger)
	require.NoError(t, err)

	assert.Equal(t, 2, result.Inserted, "FSDJump + Docked inserted")
	assert.Equal(t, 1, result.Duplicates, "second FSDJump is duplicate")
	assert.Equal(t, 1, result.Skipped, "empty event_type skipped")
	assert.Equal(t, 1, result.Commanders)
}
