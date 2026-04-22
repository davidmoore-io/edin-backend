package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/llm"
)

// ─── Test doubles ──────────────────────────────────────────────────────────────

// fakeMultiSessionStore implements llm.MultiSessionBackend for tests. The
// InMemoryStore in the llm package is intentionally single-session, so we
// supply a minimal in-place double to exercise the session endpoints without
// requiring Redis/miniredis.
type fakeMultiSessionStore struct {
	// Preset responses for ListUserSessions, keyed by userID.
	userSessions map[string][]llm.SessionSummary
	listErr      error

	// Record the most recent activation call and optionally fail it.
	lastActivatedFor    string
	lastActivatedTarget string
	activateErr         error
}

// Satisfy the llm.SessionBackend half of the interface. Tests here do not
// exercise these paths, so cheap implementations suffice.
func (s *fakeMultiSessionStore) CreateSession(userID string, _ ...llm.Message) *llm.Session {
	return &llm.Session{ID: "new", Messages: nil}
}
func (s *fakeMultiSessionStore) AppendMessage(string, llm.Message) (*llm.Session, error) {
	return nil, nil
}
func (s *fakeMultiSessionStore) Get(string) (*llm.Session, bool) { return nil, false }
func (s *fakeMultiSessionStore) Delete(string)                   {}
func (s *fakeMultiSessionStore) Cleanup()                        {}

// MultiSessionBackend methods.
func (s *fakeMultiSessionStore) ListUserSessions(userID string) ([]llm.SessionSummary, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.userSessions[userID], nil
}
func (s *fakeMultiSessionStore) GetActiveSession(string) (*llm.Session, error) { return nil, nil }
func (s *fakeMultiSessionStore) SetActiveSession(userID, sessionID string) error {
	s.lastActivatedFor = userID
	s.lastActivatedTarget = sessionID
	return s.activateErr
}

// newCopilotSessionsTestServer builds a Server with everything needed to drive
// the copilot chat session endpoints under withCommanderAuth.
func newCopilotSessionsTestServer(t *testing.T, store llm.SessionBackend) *Server {
	t.Helper()
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)
	srv.llmStore = store
	return srv
}

// ─── handleCopilotChatSessions ────────────────────────────────────────────────

func TestCopilotChatSessions_ReturnsUsersSessions(t *testing.T) {
	store := &fakeMultiSessionStore{
		userSessions: map[string][]llm.SessionSummary{
			"F2504": {
				{ID: "s1", Preview: "hi", MessageCount: 2, UpdatedAt: time.Now()},
				{ID: "s2", Preview: "bye", MessageCount: 1, UpdatedAt: time.Now()},
			},
		},
	}
	srv := newCopilotSessionsTestServer(t, store)

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/chat/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatSessions)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Sessions []llm.SessionSummary `json:"sessions"`
		Count    int                  `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, 2, body.Count)
	assert.Len(t, body.Sessions, 2)
}

func TestCopilotChatSessions_OnlyReturnsCallersSessions(t *testing.T) {
	// Data for another commander MUST NOT leak even if it is in the same
	// process's session store. The handler passes claims.FID to
	// ListUserSessions, so the store itself handles the filtering — this test
	// guards that we never accidentally pass a different identifier (e.g. a
	// request body field) in its place.
	store := &fakeMultiSessionStore{
		userSessions: map[string][]llm.SessionSummary{
			"F2504": {{ID: "mine"}},
			"F9999": {{ID: "theirs"}},
		},
	}
	srv := newCopilotSessionsTestServer(t, store)

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/chat/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatSessions)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Sessions []llm.SessionSummary `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body.Sessions, 1)
	assert.Equal(t, "mine", body.Sessions[0].ID)
}

func TestCopilotChatSessions_MissingAuth_Returns401(t *testing.T) {
	srv := newCopilotSessionsTestServer(t, &fakeMultiSessionStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/commander/chat/sessions", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatSessions)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCopilotChatSessions_WrongMethod_Returns405(t *testing.T) {
	srv := newCopilotSessionsTestServer(t, &fakeMultiSessionStore{})
	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/chat/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatSessions)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestCopilotChatSessions_NonMultiSessionStore_ReturnsEmpty(t *testing.T) {
	// InMemoryStore does not implement MultiSessionBackend. The handler must
	// return 200 with an empty list rather than 500, so the UI can render a
	// valid "no conversations yet" state.
	srv := newCopilotSessionsTestServer(t, llm.NewInMemoryStore(5*time.Minute))

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/api/commander/chat/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatSessions)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, float64(0), body["count"])
}

func TestCopilotChatSessions_StoreError_Returns500(t *testing.T) {
	store := &fakeMultiSessionStore{listErr: errors.New("boom")}
	srv := newCopilotSessionsTestServer(t, store)

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/api/commander/chat/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatSessions)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

// ─── handleCopilotChatActivateSession ─────────────────────────────────────────

func TestCopilotActivateSession_SuccessRecordsFIDAndSessionID(t *testing.T) {
	store := &fakeMultiSessionStore{}
	srv := newCopilotSessionsTestServer(t, store)

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/chat/sessions/abc-123/activate", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatActivateSession)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "F2504", store.lastActivatedFor,
		"handler must pass claims.FID to the store — never a value derived from the request")
	assert.Equal(t, "abc-123", store.lastActivatedTarget)
}

func TestCopilotActivateSession_MissingAuth_Returns401(t *testing.T) {
	srv := newCopilotSessionsTestServer(t, &fakeMultiSessionStore{})
	req := httptest.NewRequest(http.MethodPost, "/api/commander/chat/sessions/abc/activate", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatActivateSession)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCopilotActivateSession_WrongMethod_Returns405(t *testing.T) {
	srv := newCopilotSessionsTestServer(t, &fakeMultiSessionStore{})
	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/chat/sessions/abc/activate", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatActivateSession)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestCopilotActivateSession_EmptyID_Returns400(t *testing.T) {
	srv := newCopilotSessionsTestServer(t, &fakeMultiSessionStore{})
	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	// URL shape /api/commander/chat/sessions//activate → empty ID segment.
	req := httptest.NewRequest(http.MethodPost, "/api/commander/chat/sessions//activate", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatActivateSession)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCopilotActivateSession_PathInjection_Returns400(t *testing.T) {
	// A nested path like `/api/commander/chat/sessions/foo/bar/activate` would
	// extract "foo/bar" as the session ID under the strip/trim approach.
	// We explicitly reject any session ID containing "/" so the trim logic
	// cannot be abused to pass arbitrary path segments to the store.
	srv := newCopilotSessionsTestServer(t, &fakeMultiSessionStore{})
	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/chat/sessions/foo/bar/activate", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatActivateSession)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestCopilotActivateSession_StoreError_Returns500(t *testing.T) {
	store := &fakeMultiSessionStore{activateErr: errors.New("cross-user denial")}
	srv := newCopilotSessionsTestServer(t, store)

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/commander/chat/sessions/abc/activate", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCopilotChatActivateSession)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
