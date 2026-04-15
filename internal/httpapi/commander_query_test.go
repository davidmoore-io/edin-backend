package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── mock for query tests ─────────────────────────────────────────────────────

type mockQueryRepo struct {
	// RecentEvents
	recentEventsResult []store.JournalEvent
	recentEventsErr    error
	recentEventsCalls  []recentEventsCall

	// EventsByType
	eventsByTypeResult []store.JournalEvent
	eventsByTypeErr    error
	eventsByTypeCalls  []eventsByTypeCall

	// CurrentLocation
	currentLocationResult *store.LocationState
	currentLocationErr    error

	// GetCommander
	getCommanderResult *store.CommanderRow
	getCommanderErr    error
}

type recentEventsCall struct {
	fid   string
	count int
}

type eventsByTypeCall struct {
	fid   string
	types []string
	since time.Time
	until time.Time
}

func (m *mockQueryRepo) UpsertCommander(_ context.Context, _, _, _ string) (uuid.UUID, error) {
	panic("not implemented")
}

func (m *mockQueryRepo) InsertEvents(_ context.Context, _ string, _ []store.JournalEvent) (int, int, error) {
	panic("not implemented")
}

func (m *mockQueryRepo) RecentEvents(_ context.Context, fid string, count int) ([]store.JournalEvent, error) {
	m.recentEventsCalls = append(m.recentEventsCalls, recentEventsCall{fid: fid, count: count})
	return m.recentEventsResult, m.recentEventsErr
}

func (m *mockQueryRepo) EventsByType(_ context.Context, fid string, types []string, since, until time.Time) ([]store.JournalEvent, error) {
	m.eventsByTypeCalls = append(m.eventsByTypeCalls, eventsByTypeCall{fid: fid, types: types, since: since, until: until})
	return m.eventsByTypeResult, m.eventsByTypeErr
}

func (m *mockQueryRepo) CurrentLocation(_ context.Context, _ string) (*store.LocationState, error) {
	return m.currentLocationResult, m.currentLocationErr
}

func (m *mockQueryRepo) DeleteAllEvents(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockQueryRepo) GetCommander(_ context.Context, _ string) (*store.CommanderRow, error) {
	return m.getCommanderResult, m.getCommanderErr
}

// ─── test server helper ───────────────────────────────────────────────────────

func newQueryTestServer(t *testing.T, repo store.CommanderRepository) *Server {
	t.Helper()
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)
	srv.commanderRepo = repo
	return srv
}

// makeQueryRequest builds a GET request with a valid commander JWT.
func makeQueryRequest(t *testing.T, srv *Server, path, fid, name string) *http.Request {
	t.Helper()
	tok := issueTestJWT(t, srv, fid, name)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

// ─── GET /api/v1/commander/events — no type filter ────────────────────────────

func TestQueryEvents_NoTypeFilter_ReturnsRecentEvents(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	events := []store.JournalEvent{
		{
			FID:       "F1234",
			Timestamp: now,
			EventType: "FSDJump",
			EventData: json.RawMessage(`{"StarSystem":"Sol"}`),
		},
		{
			FID:       "F1234",
			Timestamp: now.Add(-time.Hour),
			EventType: "Docked",
			EventData: json.RawMessage(`{"StationName":"Jameson Memorial"}`),
		},
	}
	repo := &mockQueryRepo{recentEventsResult: events}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var resp []eventResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(t, resp, 2)
	assert.Equal(t, "FSDJump", resp[0].EventType)
	assert.Equal(t, "Docked", resp[1].EventType)

	// Verify RecentEvents was called with correct FID and default limit.
	require.Len(t, repo.recentEventsCalls, 1)
	assert.Equal(t, "F1234", repo.recentEventsCalls[0].fid)
	assert.Equal(t, 50, repo.recentEventsCalls[0].count) // default limit
}

func TestQueryEvents_NoTypeFilter_DefaultLimit50(t *testing.T) {
	repo := &mockQueryRepo{recentEventsResult: nil}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Len(t, repo.recentEventsCalls, 1)
	assert.Equal(t, 50, repo.recentEventsCalls[0].count)
}

func TestQueryEvents_NoTypeFilter_CustomLimit(t *testing.T) {
	repo := &mockQueryRepo{recentEventsResult: nil}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events?limit=100", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Len(t, repo.recentEventsCalls, 1)
	assert.Equal(t, 100, repo.recentEventsCalls[0].count)
}

// ─── GET /api/v1/commander/events?type=FSDJump — type filter ─────────────────

func TestQueryEvents_WithTypeFilter_CallsEventsByType(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	events := []store.JournalEvent{
		{
			FID:       "F1234",
			Timestamp: now,
			EventType: "FSDJump",
			EventData: json.RawMessage(`{"StarSystem":"Sol"}`),
		},
	}
	repo := &mockQueryRepo{eventsByTypeResult: events}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events?type=FSDJump", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var resp []eventResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	require.Len(t, resp, 1)
	assert.Equal(t, "FSDJump", resp[0].EventType)

	// Verify EventsByType was called with the correct type.
	require.Len(t, repo.eventsByTypeCalls, 1)
	assert.Equal(t, "F1234", repo.eventsByTypeCalls[0].fid)
	assert.Equal(t, []string{"FSDJump"}, repo.eventsByTypeCalls[0].types)
}

func TestQueryEvents_WithBeforeParam_PassedToEventsByType(t *testing.T) {
	before := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	repo := &mockQueryRepo{eventsByTypeResult: nil}
	srv := newQueryTestServer(t, repo)

	path := fmt.Sprintf("/api/v1/commander/events?type=FSDJump&before=%s", before.Format(time.RFC3339))
	req := makeQueryRequest(t, srv, path, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	require.Len(t, repo.eventsByTypeCalls, 1)
	// The until param should match the before timestamp.
	assert.Equal(t, before.Unix(), repo.eventsByTypeCalls[0].until.Unix())
}

// ─── GET /api/v1/commander/events — bad params ────────────────────────────────

func TestQueryEvents_BadLimit_Returns400(t *testing.T) {
	repo := &mockQueryRepo{}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events?limit=abc", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid limit")
}

func TestQueryEvents_LimitExceedsMax_Returns400(t *testing.T) {
	repo := &mockQueryRepo{}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events?limit=501", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "limit")
}

func TestQueryEvents_BadBefore_Returns400(t *testing.T) {
	repo := &mockQueryRepo{}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events?before=notadate", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid before")
}

// ─── GET /api/v1/commander/events — empty result ─────────────────────────────

func TestQueryEvents_EmptyResult_ReturnsEmptyArray(t *testing.T) {
	repo := &mockQueryRepo{recentEventsResult: nil}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/events", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	// Must return [] not null.
	assert.JSONEq(t, "[]", rr.Body.String())
}

// ─── GET /api/v1/commander/events — no auth ──────────────────────────────────

func TestQueryEvents_NoAuth_Returns401(t *testing.T) {
	repo := &mockQueryRepo{}
	srv := newQueryTestServer(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/commander/events", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderEvents))(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ─── GET /api/v1/commander/location ──────────────────────────────────────────

func TestQueryLocation_ReturnsCurrentLocation(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	repo := &mockQueryRepo{
		currentLocationResult: &store.LocationState{
			SystemName: "Sol",
			StarPos:    [3]float64{0, 0, 0},
			UpdatedAt:  ts,
		},
	}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/location", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderLocation))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var resp locationResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "Sol", resp.System)
	assert.Equal(t, ts.Unix(), resp.Timestamp.Unix())
}

func TestQueryLocation_NoLocation_Returns404(t *testing.T) {
	repo := &mockQueryRepo{
		currentLocationResult: nil, // no location
	}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/location", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderLocation))(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestQueryLocation_RepoError_Returns500(t *testing.T) {
	repo := &mockQueryRepo{
		currentLocationErr: errors.New("db error"),
	}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/location", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderLocation))(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestQueryLocation_NoAuth_Returns401(t *testing.T) {
	repo := &mockQueryRepo{}
	srv := newQueryTestServer(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/commander/location", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderLocation))(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ─── GET /api/v1/commander/profile ───────────────────────────────────────────

func TestQueryProfile_ReturnsCommanderProfile(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	repo := &mockQueryRepo{
		getCommanderResult: &store.CommanderRow{
			ID:         uuid.New(),
			FID:        "F1234",
			CmdrName:   "Test CMDR",
			Platform:   "frontier",
			LastSeenAt: ts,
		},
	}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/profile", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderProfile))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var resp profileResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, "F1234", resp.FID)
	assert.Equal(t, "Test CMDR", resp.Name)
	assert.Equal(t, ts.Unix(), resp.LastSeen.Unix())
}

func TestQueryProfile_NotFound_Returns404(t *testing.T) {
	repo := &mockQueryRepo{
		getCommanderResult: nil, // not found
	}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/profile", "F9999", "Unknown")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderProfile))(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestQueryProfile_RepoError_Returns500(t *testing.T) {
	repo := &mockQueryRepo{
		getCommanderErr: errors.New("db error"),
	}
	srv := newQueryTestServer(t, repo)

	req := makeQueryRequest(t, srv, "/api/v1/commander/profile", "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderProfile))(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestQueryProfile_NoAuth_Returns401(t *testing.T) {
	repo := &mockQueryRepo{}
	srv := newQueryTestServer(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/commander/profile", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderProfile))(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
