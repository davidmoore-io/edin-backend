package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── mock CommanderRepository ─────────────────────────────────────────────────

type insertCall struct {
	fid    string
	events []store.JournalEvent
}

type mockCommanderRepo struct {
	insertCalls  []insertCall
	insertResult struct {
		inserted int
		dups     int
		err      error
	}
}

func (m *mockCommanderRepo) InsertEvents(_ context.Context, fid string, events []store.JournalEvent) (int, int, error) {
	m.insertCalls = append(m.insertCalls, insertCall{fid: fid, events: events})
	return m.insertResult.inserted, m.insertResult.dups, m.insertResult.err
}

func (m *mockCommanderRepo) UpsertCommander(_ context.Context, _, _, _ string) (uuid.UUID, error) {
	panic("not implemented")
}

func (m *mockCommanderRepo) RecentEvents(_ context.Context, _ string, _ int) ([]store.JournalEvent, error) {
	panic("not implemented")
}

func (m *mockCommanderRepo) EventsByType(_ context.Context, _ string, _ []string, _, _ time.Time) ([]store.JournalEvent, error) {
	panic("not implemented")
}

func (m *mockCommanderRepo) CurrentLocation(_ context.Context, _ string) (*store.LocationState, error) {
	panic("not implemented")
}

func (m *mockCommanderRepo) DeleteAllEvents(_ context.Context, _ string) error {
	panic("not implemented")
}

// ─── test server helper ───────────────────────────────────────────────────────

// newIngestTestServer wires a minimal Server with commander auth and ingest dependencies.
func newIngestTestServer(t *testing.T, repo store.CommanderRepository) *Server {
	t.Helper()
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)
	srv.commanderRepo = repo
	srv.ingestRateLimiter = newIngestFIDRateLimiter()
	srv.logger = observability.NewLogger("test-ingest")
	return srv
}

// makeIngestRequest builds an HTTP request with a valid commander JWT for fid/name.
func makeIngestRequest(t *testing.T, srv *Server, method, path, body, fid, name string) *http.Request {
	t.Helper()
	tok := issueTestJWT(t, srv, fid, name)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// validTimestamp returns a recent valid RFC3339 timestamp.
func validTimestamp() string {
	return time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
}

// singleEventBody returns a JSON body for /api/v1/ingest/event.
func singleEventBody(eventType, ts, fid string) string {
	b, _ := json.Marshal(map[string]any{
		"event": map[string]any{
			"timestamp":      ts,
			"event":          eventType,
			"fid":            fid,
			"commander_name": "Test CMDR",
			"event_data":     map[string]any{"StarSystem": "Sol"},
		},
	})
	return string(b)
}

// batchEventsBody returns a JSON body for /api/v1/ingest/events with n identical events.
func batchEventsBody(eventType, ts string, n int) string {
	events := make([]map[string]any, n)
	for i := range events {
		events[i] = map[string]any{
			"timestamp":      ts,
			"event":          eventType,
			"fid":            "F9999",
			"commander_name": "Test CMDR",
			"event_data":     map[string]any{"StarSystem": "Sol"},
		}
	}
	b, _ := json.Marshal(map[string]any{"events": events})
	return string(b)
}

// ─── Single event tests ───────────────────────────────────────────────────────

func TestIngestSingle_ValidEvent_Returns200(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	body := singleEventBody("FSDJump", validTimestamp(), "F1234")
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var resp ingestResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 1, resp.EventsWritten)
	assert.Equal(t, 0, resp.EventsDuplicated)

	require.Len(t, repo.insertCalls, 1)
	assert.Equal(t, "F1234", repo.insertCalls[0].fid)
}

func TestIngestSingle_MissingAuth_Returns401(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	body := singleEventBody("FSDJump", validTimestamp(), "F1234")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/event", strings.NewReader(body))
	// No Authorization header.
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestIngestSingle_FIDFromJWT_BodyFIDIgnored(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	// JWT is for "F1111", body has "F2222".
	body := singleEventBody("FSDJump", validTimestamp(), "F2222")
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1111", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	// Storage must use JWT FID, not the body FID.
	require.Len(t, repo.insertCalls, 1)
	assert.Equal(t, "F1111", repo.insertCalls[0].fid, "must use JWT FID, not body FID")
	assert.Equal(t, "F1111", repo.insertCalls[0].events[0].FID, "event.FID must be JWT FID")
}

func TestIngestSingle_BodyFIDMismatch_LogsWarningAndContinues(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	// Mismatch: JWT F3333, body F9999.
	body := singleEventBody("FSDJump", validTimestamp(), "F9999")
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F3333", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	// Must still succeed (warning is logged, not returned as error).
	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	require.Len(t, repo.insertCalls, 1)
	assert.Equal(t, "F3333", repo.insertCalls[0].fid)
}

func TestIngestSingle_FutureDatedTimestamp_Returns400(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	futureTS := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)
	body := singleEventBody("FSDJump", futureTS, "F1234")
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "future")
}

func TestIngestSingle_TooOldTimestamp_Returns400(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	oldTS := time.Now().UTC().Add(-400 * 24 * time.Hour).Format(time.RFC3339)
	body := singleEventBody("FSDJump", oldTS, "F1234")
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "too old")
}

func TestIngestSingle_UnknownEventType_Returns400(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	body := singleEventBody("HackedEvent", validTimestamp(), "F1234")
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/event", body, "F1234", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown event type")
}

func TestIngestSingle_OversizedPayload_Returns413(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	// Build a payload larger than 2MB.
	bigData := bytes.Repeat([]byte("x"), 3*1024*1024)
	body, _ := json.Marshal(map[string]any{
		"event": map[string]any{
			"timestamp":      validTimestamp(),
			"event":          "FSDJump",
			"fid":            "F1234",
			"commander_name": "Test CMDR",
			"event_data":     map[string]any{"garbage": string(bigData)},
		},
	})

	tok := issueTestJWT(t, srv, "F1234", "Test CMDR")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/event", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestSingle))(rr, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

// ─── Batch event tests ────────────────────────────────────────────────────────

func TestIngestBatch_ValidBatch_Returns200WithCounts(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 3
	repo.insertResult.dups = 1
	srv := newIngestTestServer(t, repo)

	body := batchEventsBody("FSDJump", validTimestamp(), 4)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", body, "F5678", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var resp ingestResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 3, resp.EventsWritten)
	assert.Equal(t, 1, resp.EventsDuplicated)
}

func TestIngestBatch_ExceedsMaxSize_Returns400(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	body := batchEventsBody("FSDJump", validTimestamp(), 501)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", body, "F5678", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "batch too large")
}

func TestIngestBatch_UnknownEventType_RejectsEntireBatch(t *testing.T) {
	repo := &mockCommanderRepo{}
	srv := newIngestTestServer(t, repo)

	ts := validTimestamp()
	events := []map[string]any{
		{"timestamp": ts, "event": "FSDJump", "fid": "F1234", "commander_name": "Test", "event_data": map[string]any{}},
		{"timestamp": ts, "event": "EvilHackEvent", "fid": "F1234", "commander_name": "Test", "event_data": map[string]any{}},
		{"timestamp": ts, "event": "Docked", "fid": "F1234", "commander_name": "Test", "event_data": map[string]any{}},
	}
	body, _ := json.Marshal(map[string]any{"events": events})

	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", string(body), "F1234", "Test")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "unknown event type")

	// The whole batch must be rejected — no insert calls.
	assert.Len(t, repo.insertCalls, 0, "no events should be inserted when batch contains unknown event type")
}

func TestIngestBatch_Deduplication_DoesNotDoubleInsert(t *testing.T) {
	repo := &mockCommanderRepo{}
	// Simulate 2 inserted, 1 duplicate.
	repo.insertResult.inserted = 2
	repo.insertResult.dups = 1
	srv := newIngestTestServer(t, repo)

	// Send 3 events — the repo mock simulates that 1 is a dup.
	body := batchEventsBody("FSDJump", validTimestamp(), 3)
	req := makeIngestRequest(t, srv, http.MethodPost, "/api/v1/ingest/events", body, "F9999", "Test CMDR")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var resp ingestResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal(t, 2, resp.EventsWritten)
	assert.Equal(t, 1, resp.EventsDuplicated)

	// Only one InsertEvents call — not one per event.
	assert.Len(t, repo.insertCalls, 1)
}

func TestIngestBatch_RateLimit_ByFIDNotAuthHeader(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	// Set a very tight rate limiter: 1 request per minute.
	srv.ingestRateLimiter = &ingestFIDRateLimiter{}

	// Manually seed the bucket at capacity=1 for F-RATELIMIT.
	fid := "F-RATELIMIT"
	tok := issueTestJWT(t, srv, fid, "Rate Test")

	makeReq := func() *httptest.ResponseRecorder {
		body := batchEventsBody("FSDJump", validTimestamp(), 1)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/events", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)
		return rr
	}

	// Drain the bucket (100 tokens).
	for i := 0; i < 100; i++ {
		makeReq()
	}

	// 101st should be rate-limited.
	rr := makeReq()
	assert.Equal(t, http.StatusTooManyRequests, rr.Code, "should be rate-limited after exhausting bucket")
}

func TestIngestBatch_RateLimit_PersistsAcrossTokenRefresh(t *testing.T) {
	repo := &mockCommanderRepo{}
	repo.insertResult.inserted = 1
	srv := newIngestTestServer(t, repo)

	fid := "F-PERSIST"
	// Issue token A and token B — same FID, different JTIs.
	tokA := issueTestJWT(t, srv, fid, "Persist Test")
	tokB := issueTestJWT(t, srv, fid, "Persist Test")

	makeBatchReq := func(tok string) *httptest.ResponseRecorder {
		body := batchEventsBody("FSDJump", validTimestamp(), 1)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/events", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.withCommanderAuth(http.HandlerFunc(srv.handleIngestBatch))(rr, req)
		return rr
	}

	// Drain bucket using token A (100 tokens).
	for i := 0; i < 100; i++ {
		makeBatchReq(tokA)
	}

	// Verify token A is now rate-limited.
	rrA := makeBatchReq(tokA)
	assert.Equal(t, http.StatusTooManyRequests, rrA.Code, "token A should be rate-limited")

	// Token B for the same FID must also be rate-limited (limiter is keyed by FID, not token).
	rrB := makeBatchReq(tokB)
	assert.Equal(t, http.StatusTooManyRequests, rrB.Code, "token B with same FID must share the rate limit bucket")
}
