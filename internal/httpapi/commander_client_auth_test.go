package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// newClientAuthMiniredis returns a miniredis-backed *redis.Client and the
// underlying *miniredis.Miniredis so tests can inspect and manipulate keys.
func newClientAuthMiniredis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

// seedPendingSession seeds a "pending" desktop auth session into miniredis and
// also writes the state→sessionID reverse-lookup key.
// Returns the sessionID and state values.
func seedPendingSession(t *testing.T, mr *miniredis.Miniredis, rdb *redis.Client, codeVerifier string) (sessionID, state string) {
	t.Helper()
	sessionID = "test-session-id"
	state = "test-state-value"

	session := clientAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		Status:       "pending",
		ExpiresAt:    time.Now().Add(10 * time.Minute).Format(time.RFC3339),
	}
	data, err := json.Marshal(session)
	require.NoError(t, err)

	err = mr.Set(clientAuthSessionKey(sessionID), string(data))
	require.NoError(t, err)
	mr.SetTTL(clientAuthSessionKey(sessionID), 10*time.Minute)

	err = mr.Set(clientAuthStateKey(state), sessionID)
	require.NoError(t, err)
	mr.SetTTL(clientAuthStateKey(state), 10*time.Minute)

	return sessionID, state
}

// seedCompleteSession seeds a "complete" desktop auth session with a token.
func seedCompleteSession(t *testing.T, mr *miniredis.Miniredis, sessionID, token string) {
	t.Helper()
	session := clientAuthSession{
		Status:    "complete",
		Token:     token,
		ExpiresAt: time.Now().Add(5 * time.Minute).Format(time.RFC3339),
	}
	data, err := json.Marshal(session)
	require.NoError(t, err)
	err = mr.Set(clientAuthSessionKey(sessionID), string(data))
	require.NoError(t, err)
	mr.SetTTL(clientAuthSessionKey(sessionID), 5*time.Minute)
}

// ─── POST /api/v1/auth/frontier/initiate ─────────────────────────────────────

func TestClientAuthInitiate_ReturnsAuthURLAndSessionID(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "https://auth.frontier.test", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/frontier/initiate", nil)
	rr := httptest.NewRecorder()
	srv.handleClientAuthInitiate(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))

	assert.NotEmpty(t, body["auth_url"], "auth_url must be present")
	assert.NotEmpty(t, body["session_id"], "session_id must be present")

	// auth_url must point to the frontier auth endpoint.
	assert.Contains(t, body["auth_url"], "https://auth.frontier.test/auth")
	assert.Contains(t, body["auth_url"], "response_type=code")
	assert.Contains(t, body["auth_url"], "code_challenge_method=S256")
	// Must use the fixed registered redirect URI.
	assert.Contains(t, body["auth_url"], urlEncode("https://edin.space/api/commander/auth/callback"))
}

func TestClientAuthInitiate_SessionIDIsCryptoRandom(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "https://auth.frontier.test", rdb, 5*time.Second)

	makeReq := func() string {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/frontier/initiate", nil)
		rr := httptest.NewRecorder()
		srv.handleClientAuthInitiate(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		var body map[string]string
		require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
		return body["session_id"]
	}

	id1 := makeReq()
	id2 := makeReq()

	assert.NotEmpty(t, id1)
	assert.NotEmpty(t, id2)
	assert.NotEqual(t, id1, id2, "two initiate calls must produce different session IDs")
}

func TestClientAuthInitiate_StoresSessionInRedis(t *testing.T) {
	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "https://auth.frontier.test", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/frontier/initiate", nil)
	rr := httptest.NewRecorder()
	srv.handleClientAuthInitiate(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	sessionID := body["session_id"]

	// Session key must exist.
	assert.True(t, mr.Exists(clientAuthSessionKey(sessionID)), "session key must exist in Redis")

	// State reverse-lookup must also exist.
	raw, err := mr.Get(clientAuthSessionKey(sessionID))
	require.NoError(t, err)
	var session clientAuthSession
	require.NoError(t, json.Unmarshal([]byte(raw), &session))
	assert.NotEmpty(t, session.State)
	assert.Equal(t, "pending", session.Status)
	assert.True(t, mr.Exists(clientAuthStateKey(session.State)), "state reverse-lookup key must exist in Redis")
}

// ─── GET /api/v1/auth/frontier/poll ─────────────────────────────────────────

func TestClientAuthPoll_Pending_Returns202(t *testing.T) {
	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	sessionID, _ := seedPendingSession(t, mr, rdb, "verifier")

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/auth/frontier/poll?session_id=%s", sessionID), nil)
	rr := httptest.NewRecorder()
	srv.handleClientAuthPoll(rr, req)

	require.Equal(t, http.StatusAccepted, rr.Code, "pending session must return 202; body: %s", rr.Body.String())

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "pending", body["status"])
}

func TestClientAuthPoll_Complete_ReturnsEDINJWT_AndDeletesSession(t *testing.T) {
	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	sessionID := "complete-session-id"
	edinJWT := issueTestJWT(t, srv, "F1234", "Test Commander")
	seedCompleteSession(t, mr, sessionID, edinJWT)

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/auth/frontier/poll?session_id=%s", sessionID), nil)
	rr := httptest.NewRecorder()
	srv.handleClientAuthPoll(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "complete session must return 200; body: %s", rr.Body.String())

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "complete", body["status"])
	assert.Equal(t, edinJWT, body["token"])

	// Session key must have been deleted (single-use).
	assert.False(t, mr.Exists(clientAuthSessionKey(sessionID)), "session must be deleted after first poll")
}

func TestClientAuthPoll_AfterComplete_Returns410(t *testing.T) {
	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	sessionID := "once-only-session"
	edinJWT := issueTestJWT(t, srv, "F9999", "Pattern State")
	seedCompleteSession(t, mr, sessionID, edinJWT)

	pollURL := fmt.Sprintf("/api/v1/auth/frontier/poll?session_id=%s", sessionID)

	// First poll → 200.
	rr1 := httptest.NewRecorder()
	srv.handleClientAuthPoll(rr1, httptest.NewRequest(http.MethodGet, pollURL, nil))
	require.Equal(t, http.StatusOK, rr1.Code)

	// Second poll → 410 (key is gone).
	rr2 := httptest.NewRecorder()
	srv.handleClientAuthPoll(rr2, httptest.NewRequest(http.MethodGet, pollURL, nil))
	require.Equal(t, http.StatusGone, rr2.Code, "second poll must return 410; body: %s", rr2.Body.String())

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr2.Body).Decode(&body))
	assert.Equal(t, "expired", body["status"])
}

func TestClientAuthPoll_InvalidSessionID_Returns410(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/auth/frontier/poll?session_id=does-not-exist", nil)
	rr := httptest.NewRecorder()
	srv.handleClientAuthPoll(rr, req)

	require.Equal(t, http.StatusGone, rr.Code, "non-existent session must return 410")

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "expired", body["status"])
}

func TestClientAuthPoll_ExpiredSession_Returns410(t *testing.T) {
	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	sessionID := "expiring-session"
	// Seed the session with a very short TTL, then fast-forward miniredis time.
	session := clientAuthSession{
		State:        "s",
		CodeVerifier: "v",
		Status:       "pending",
		ExpiresAt:    time.Now().Add(1 * time.Second).Format(time.RFC3339),
	}
	data, err := json.Marshal(session)
	require.NoError(t, err)
	err = mr.Set(clientAuthSessionKey(sessionID), string(data))
	require.NoError(t, err)
	mr.SetTTL(clientAuthSessionKey(sessionID), 1*time.Second)

	// Fast-forward miniredis clock past the TTL.
	mr.FastForward(2 * time.Second)

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/auth/frontier/poll?session_id=%s", sessionID), nil)
	rr := httptest.NewRecorder()
	srv.handleClientAuthPoll(rr, req)

	require.Equal(t, http.StatusGone, rr.Code, "expired session must return 410; body: %s", rr.Body.String())

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "expired", body["status"])
}

// ─── Desktop callback flow ────────────────────────────────────────────────────

func TestClientAuthCallback_DesktopFlow_StoresTokenForPolling(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token":  "acc-desktop",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "ref-desktop",
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "7777", "Desktop Commander", 0, false)
	defer frontierSrv.Close()

	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	// Seed a pending desktop session in Redis (NOT in the PKCE store).
	sessionID, state := seedPendingSession(t, mr, rdb, "desktop-verifier")

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/commander/auth/callback?code=desktop-code&state=%s", state), nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	// Callback must return the HTML success page.
	require.Equal(t, http.StatusOK, rr.Code, "desktop callback must return 200; body: %s", rr.Body.String())
	assert.Contains(t, rr.Body.String(), "Authentication successful")

	// Session must now be marked complete with a token.
	raw, err := mr.Get(clientAuthSessionKey(sessionID))
	require.NoError(t, err, "session key must still exist (as complete)")
	var session clientAuthSession
	require.NoError(t, json.Unmarshal([]byte(raw), &session))
	assert.Equal(t, "complete", session.Status)
	assert.NotEmpty(t, session.Token, "EDIN JWT must be stored in session")
}

func TestClientAuthCallback_BrowserFlow_StillWorksWithPKCEStore(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token":  "acc-browser",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "ref-browser",
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	// Seed a PKCE state entry in the in-memory PKCE store (browser flow).
	state := "browser-state-uuid"
	srv.commanderPKCEStore.store(state, "browser-verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/commander/auth/callback?code=browser-code&state=%s", state), nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	// Browser flow must still return JSON with commander info and set a cookie.
	require.Equal(t, http.StatusOK, rr.Code, "browser callback must return 200; body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "F2504", body["fid"])
	assert.Equal(t, "Pattern State", body["commander_name"])

	// Cookie must be set.
	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "commander_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie, "commander_session cookie must be set for browser flow")
	assert.True(t, sessionCookie.HttpOnly)
	assert.NotEmpty(t, sessionCookie.Value)
}

// TestCommanderClientAuth_IssuedJWTContainsDefaultScopes asserts the JWT minted
// by the desktop /callback path carries the default commander scope set —
// {copilot_chat, galaxy_read, commander_data}. The desktop flow must receive
// the same scopes as the browser flow so the desktop client sees the same
// tool set in its copilot chat.
func TestCommanderClientAuth_IssuedJWTContainsDefaultScopes(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token":  "acc-desktop",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "ref-desktop",
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "7777", "Desktop Commander", 0, false)
	defer frontierSrv.Close()

	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	sessionID, state := seedPendingSession(t, mr, rdb, "desktop-verifier")

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/commander/auth/callback?code=desktop-code&state=%s", state), nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "desktop callback must return 200; body: %s", rr.Body.String())

	// Read the session back from Redis and parse the stored JWT to inspect scopes.
	raw, err := mr.Get(clientAuthSessionKey(sessionID))
	require.NoError(t, err)
	var session clientAuthSession
	require.NoError(t, json.Unmarshal([]byte(raw), &session))
	require.NotEmpty(t, session.Token)

	claims := &auth.CommanderClaims{}
	_, _, err = new(jwt.Parser).ParseUnverified(session.Token, claims)
	require.NoError(t, err)
	assert.Equal(t, []string{"copilot_chat", "galaxy_read", "commander_data"}, claims.Scopes)
}

// TestCommanderClientAuth_CallbackTracksJTIUnderPerFIDSet asserts that the
// desktop callback records the minted jti under commander:jtis:{fid} — same
// behaviour as the browser callback, so Task 8 admin revocation works against
// sessions minted through either flow.
func TestCommanderClientAuth_CallbackTracksJTIUnderPerFIDSet(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token": "acc-desktop",
		"expires_in":   3600,
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "7777", "Desktop Commander", 0, false)
	defer frontierSrv.Close()

	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	sessionID, state := seedPendingSession(t, mr, rdb, "desktop-verifier-jti")

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/commander/auth/callback?code=desktop-code&state=%s", state), nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	raw, err := mr.Get(clientAuthSessionKey(sessionID))
	require.NoError(t, err)
	var session clientAuthSession
	require.NoError(t, json.Unmarshal([]byte(raw), &session))

	claims := &auth.CommanderClaims{}
	_, _, err = new(jwt.Parser).ParseUnverified(session.Token, claims)
	require.NoError(t, err)
	require.NotEmpty(t, claims.ID, "jti must be present")

	members, err := mr.SMembers("commander:jtis:F7777")
	require.NoError(t, err)
	assert.Contains(t, members, claims.ID, "per-FID tracking set must contain the new jti")

	ttl := mr.TTL("commander:jtis:F7777")
	assert.True(t, ttl > 23*time.Hour && ttl <= 24*time.Hour,
		"per-FID tracking set TTL must be roughly 24h; got %s", ttl)
}

// ─── Desktop auto-link tests (Task 5) ────────────────────────────────────────

// TestClientAuthCallback_FirstLogin_CreatesShadowUserAndLinksFID asserts the
// desktop callback for an unlinked commander row invokes the shadow-user
// creator and persists the returned UUID. The session is marked complete
// for the desktop client to poll.
func TestClientAuthCallback_FirstLogin_CreatesShadowUserAndLinksFID(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "7777", "Desktop Commander", 0, false)
	defer frontierSrv.Close()

	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	repo := newLinkTestRepo()
	srv.commanderRepo = repo

	wantUUID := uuid.MustParse("12345678-1234-1234-1234-123456789abc")
	creator := &fakeShadowCreator{wantUUID: wantUUID}
	srv.createShadowUser = creator.fn()

	sessionID, state := seedPendingSession(t, mr, rdb, "desktop-verifier-link")

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/commander/auth/callback?code=desktop-code&state=%s", state), nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	assert.Equal(t, 1, creator.Calls(), "shadow creator must be invoked exactly once")
	assert.Equal(t, 1, repo.upsertCallCount(), "UpsertCommander must run before link")
	assert.Equal(t, 1, repo.setLinkCallCount(), "SetAuthentikLink must run after shadow create")
	assert.Equal(t, wantUUID, repo.linkedUUID("F7777"))

	// Session must have been completed with a token for the desktop poll.
	raw, err := mr.Get(clientAuthSessionKey(sessionID))
	require.NoError(t, err)
	var session clientAuthSession
	require.NoError(t, json.Unmarshal([]byte(raw), &session))
	assert.Equal(t, "complete", session.Status)
	assert.NotEmpty(t, session.Token)
}

// TestClientAuthCallback_AuthentikCreateFails_Returns403 mirrors the web
// callback's deny-closed contract: a transient Authentik failure on the
// desktop path returns 403, leaves the row unlinked, and does NOT mark the
// pending session complete (so the desktop client's next poll sees pending
// → eventually expired, not a complete-with-bogus-token).
func TestClientAuthCallback_AuthentikCreateFails_Returns403(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "7777", "Desktop Commander", 0, false)
	defer frontierSrv.Close()

	rdb, mr := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	repo := newLinkTestRepo()
	srv.commanderRepo = repo

	creator := &fakeShadowCreator{wantErr: errAuthentikDown}
	srv.createShadowUser = creator.fn()

	sessionID, state := seedPendingSession(t, mr, rdb, "desktop-verifier-deny")

	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/commander/auth/callback?code=desktop-code&state=%s", state), nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code,
		"Authentik failure must deny-close the desktop auth flow")
	assert.Equal(t, 1, creator.Calls())
	assert.Equal(t, 0, repo.setLinkCallCount(),
		"SetAuthentikLink must NOT be invoked when shadow create failed")
	assert.Equal(t, uuid.Nil, repo.linkedUUID("F7777"))

	// The pending session must still be pending (NOT marked complete) — the
	// desktop client will poll, eventually expire, and surface that to the
	// user instead of presenting a complete-but-invalid token.
	raw, err := mr.Get(clientAuthSessionKey(sessionID))
	require.NoError(t, err)
	var session clientAuthSession
	require.NoError(t, json.Unmarshal([]byte(raw), &session))
	assert.Equal(t, "pending", session.Status,
		"session must remain pending when desktop callback is denied")
	assert.Empty(t, session.Token,
		"no JWT must be stored when desktop callback is denied")
}
