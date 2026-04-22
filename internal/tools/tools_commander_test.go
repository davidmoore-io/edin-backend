package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCommanderRepo implements store.CommanderRepository for testing.
type mockCommanderRepo struct {
	recentEvents  []store.JournalEvent
	recentErr     error
	byTypeEvents  []store.JournalEvent
	byTypeErr     error
	location      *store.LocationState
	locationErr   error

	// Captured args from the last EventsByType call — lets tests assert that
	// the tool wrapper is forwarding sensible time bounds.
	lastByTypeTypes []string
	lastByTypeSince time.Time
	lastByTypeUntil time.Time
}

func (m *mockCommanderRepo) UpsertCommander(ctx context.Context, fid, name, platform string) (uuid.UUID, error) {
	return uuid.UUID{}, nil
}
func (m *mockCommanderRepo) InsertEvents(ctx context.Context, fid string, events []store.JournalEvent) (int, int, error) {
	return 0, 0, nil
}
func (m *mockCommanderRepo) RecentEvents(ctx context.Context, fid string, count int) ([]store.JournalEvent, error) {
	if m.recentErr != nil {
		return nil, m.recentErr
	}
	if count > 0 && count < len(m.recentEvents) {
		return m.recentEvents[:count], nil
	}
	return m.recentEvents, nil
}
func (m *mockCommanderRepo) EventsByType(ctx context.Context, fid string, types []string, since, until time.Time) ([]store.JournalEvent, error) {
	m.lastByTypeTypes = types
	m.lastByTypeSince = since
	m.lastByTypeUntil = until
	return m.byTypeEvents, m.byTypeErr
}
func (m *mockCommanderRepo) CurrentLocation(ctx context.Context, fid string) (*store.LocationState, error) {
	return m.location, m.locationErr
}
func (m *mockCommanderRepo) DeleteAllEvents(ctx context.Context, fid string) error {
	return nil
}
func (m *mockCommanderRepo) GetCommander(ctx context.Context, fid string) (*store.CommanderRow, error) {
	return nil, nil
}
func (m *mockCommanderRepo) GetEventStats(ctx context.Context, fid string) (*store.CommanderEventStats, error) {
	return nil, nil
}

// ctxWithFID returns a context with the commander FID set for tool execution.
func ctxWithFID(fid string) context.Context {
	return WithCommanderFID(context.Background(), fid)
}

func makeEvent(ts time.Time, eventType string) store.JournalEvent {
	return store.JournalEvent{
		CommanderID: uuid.New(),
		FID:         "F2504",
		Timestamp:   ts,
		EventType:   eventType,
		EventData:   json.RawMessage(`{}`),
	}
}

// ─── commander_events ────────────────────────────────────────────────────────

func TestCommanderEvents_ReturnsFormattedList(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	repo := &mockCommanderRepo{
		recentEvents: []store.JournalEvent{
			makeEvent(now, "FSDJump"),
			makeEvent(now.Add(-5*time.Minute), "Docked"),
		},
	}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	result, err := exec.commanderEvents(ctxWithFID("F2504"), map[string]any{})
	require.NoError(t, err)
	s := result.(string)
	assert.Contains(t, s, "FSDJump")
	assert.Contains(t, s, "Docked")
	assert.Contains(t, s, "Recent events")
}

func TestCommanderEvents_FiltersByEventType(t *testing.T) {
	now := time.Now().UTC()
	repo := &mockCommanderRepo{
		byTypeEvents: []store.JournalEvent{
			makeEvent(now, "FSDJump"),
		},
	}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	result, err := exec.commanderEvents(ctxWithFID("F2504"), map[string]any{
		"event_types": "FSDJump",
	})
	require.NoError(t, err)
	assert.Contains(t, result.(string), "FSDJump")
}

// TestCommanderEvents_DefaultsUntilToNow is a regression test for a bug where
// passing event_types without an explicit `until` caused the SQL predicate
// `timestamp <= until` to evaluate against Go's zero time (year 1 AD), filtering
// out every real event and returning "No events found for this commander".
// The tool wrapper must default `until` to the current time.
func TestCommanderEvents_DefaultsUntilToNow(t *testing.T) {
	repo := &mockCommanderRepo{byTypeEvents: nil}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)

	before := time.Now().UTC()
	_, err := exec.commanderEvents(ctxWithFID("F2504"), map[string]any{
		"event_types": "Docked",
	})
	after := time.Now().UTC()

	require.NoError(t, err)
	require.False(t, repo.lastByTypeUntil.IsZero(),
		"until must not be zero when caller omits it; otherwise timestamp <= $4 matches nothing")
	assert.True(t, !repo.lastByTypeUntil.Before(before) && !repo.lastByTypeUntil.After(after.Add(time.Second)),
		"until should default to approximately now; got %v", repo.lastByTypeUntil)
}

func TestCommanderEvents_RespectsLimit(t *testing.T) {
	now := time.Now().UTC()
	var events []store.JournalEvent
	for i := 0; i < 10; i++ {
		events = append(events, makeEvent(now.Add(time.Duration(-i)*time.Minute), "FSDJump"))
	}
	repo := &mockCommanderRepo{recentEvents: events}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	result, err := exec.commanderEvents(ctxWithFID("F2504"), map[string]any{
		"limit": float64(3),
	})
	require.NoError(t, err)
	// Should only have 3 event lines (each starts with "- ")
	lines := strings.Split(result.(string), "\n")
	eventLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "- ") {
			eventLines++
		}
	}
	assert.Equal(t, 3, eventLines)
}

func TestCommanderEvents_ReturnsNoEventsMessage(t *testing.T) {
	repo := &mockCommanderRepo{recentEvents: nil}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	result, err := exec.commanderEvents(ctxWithFID("F2504"), map[string]any{})
	require.NoError(t, err)
	assert.Contains(t, result.(string), "No events found")
}

func TestCommanderEvents_ReturnsErrorOnRepoFailure(t *testing.T) {
	repo := &mockCommanderRepo{recentErr: errors.New("db error")}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	_, err := exec.commanderEvents(ctxWithFID("F2504"), map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
}

func TestCommanderEvents_InvalidSinceTimestamp(t *testing.T) {
	repo := &mockCommanderRepo{}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	_, err := exec.commanderEvents(ctxWithFID("F2504"), map[string]any{
		"since": "not-a-timestamp",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "since")
}

// ─── commander_location ──────────────────────────────────────────────────────

func TestCommanderLocation_ReturnsFormattedLocation(t *testing.T) {
	ts := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	repo := &mockCommanderRepo{
		location: &store.LocationState{
			SystemName: "Shinrarta Dezhra",
			UpdatedAt:  ts,
		},
	}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	result, err := exec.commanderLocation(ctxWithFID("F2504"))
	require.NoError(t, err)
	s := result.(string)
	assert.Contains(t, s, "Shinrarta Dezhra")
	assert.Contains(t, s, "2024-01-15 10:30:00")
}

func TestCommanderLocation_ReturnsNoLocationWhenNil(t *testing.T) {
	repo := &mockCommanderRepo{location: nil}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	result, err := exec.commanderLocation(ctxWithFID("F2504"))
	require.NoError(t, err)
	assert.Contains(t, result.(string), "No location data")
}

func TestCommanderLocation_ReturnsErrorOnRepoFailure(t *testing.T) {
	repo := &mockCommanderRepo{locationErr: errors.New("db error")}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	_, err := exec.commanderLocation(ctxWithFID("F2504"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db error")
}

func TestCommanderLocation_NoFIDInContext(t *testing.T) {
	repo := &mockCommanderRepo{}
	exec := NewExecutor(nil, nil, nil, nil).WithCommanderRepository(repo)
	_, err := exec.commanderLocation(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no commander identity")
}
