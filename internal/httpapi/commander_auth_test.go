package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/edin-space/edin-backend/internal/authentik"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

func generateTestRSAKeyPair(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key, &key.PublicKey
}

func newTestMiniredis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// frontierTestServer creates a fake Frontier auth+CAPI httptest server.
// tokenResp is returned on POST /token and GET /me.
// profileName is returned on GET /profile (empty string → 500 error).
// profileDelay causes GET /profile to sleep (for timeout testing).
func frontierTestServer(t *testing.T, tokenResp map[string]any, meCustomerID string, profileName string, profileDelay time.Duration, profileFail bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			json.NewEncoder(w).Encode(tokenResp)

		case r.Method == http.MethodGet && r.URL.Path == "/me":
			json.NewEncoder(w).Encode(map[string]string{"customer_id": meCustomerID})

		case r.Method == http.MethodGet && r.URL.Path == "/profile":
			if profileDelay > 0 {
				time.Sleep(profileDelay)
			}
			if profileFail {
				http.Error(w, "capi unavailable", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"commander": map[string]string{"name": profileName},
			})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

// newCommanderAuthTestServer wires up a minimal *Server with commander auth configured.
// frontierURL is used for both auth and CAPI endpoints.
// rdb may be nil (Redis-less mode).
//
// Post-Task-12 (env-var allowlist retired): the helper installs a default
// commanderRepo and authentikUserGroups stub that resolve the access decision
// to "approved + linked + Authentik group=edin-copilot" for any FID. Tests
// that exercise the access-decision matrix override these fields with their
// own fakes (see e.g. TestCallback_LinkedNotApprovedOffAllowlist_*).
func newCommanderAuthTestServer(t *testing.T, frontierURL string, rdb *redis.Client, capiTimeout time.Duration) *Server {
	t.Helper()

	privKey, pubKey := generateTestRSAKeyPair(t)

	issuer := auth.NewCommanderJWTIssuer(privKey, "edin-space-test", 24*time.Hour)
	var validator *auth.CommanderJWTValidator
	if rdb != nil {
		validator = auth.NewCommanderJWTValidator(pubKey, "edin-space-test", rdb)
	}

	if capiTimeout == 0 {
		capiTimeout = 5 * time.Second
	}

	return &Server{
		cfg: &config.Config{
			CommanderAuth: config.CommanderAuthConfig{
				Enabled:              true,
				JWTIssuer:            "edin-space-test",
				JWTExpiry:            24 * time.Hour,
				FrontierClientID:     "test-client",
				FrontierClientSecret: "test-secret",
				FrontierAuthURL:      frontierURL,
				FrontierCAPIURL:      frontierURL, // same server for auth+CAPI in tests
				FrontierScope:        "auth capi",
				FrontierCAPITimeout:  capiTimeout,
				PKCEStateTTL:         10 * time.Minute,
				PKCEMaxPending:       1000,
				CookieName:           "commander_session",
				CookiePath:           "/api/commander",
				CookieSecure:         false,
				CookieMaxAge:         86400,
				NonceExpiry:          10 * time.Second,
				InitiateRateLimit:    5,
				InitiateRateWindow:   time.Minute,
				DesktopRedirectURI:   "https://edin.space/api/commander/auth/callback",
			},
		},
		logger:                observability.NewLogger("test"),
		redisClient:           rdb,
		commanderJWTIssuer:    issuer,
		commanderJWTValidator: validator,
		commanderPKCEStore:    newCommanderPKCEStore(1000),
		commanderNonceStore:   newCommanderChatNonceStore(),
		commanderRepo:         newPermissiveLinkTestRepo(),
		authentikUserGroups:   newPermissiveAuthentikGroups(),
	}
}

// newPermissiveLinkTestRepo returns a *linkTestRepo whose GetCommanderAsAdmin
// returns an approved + linked row for any FID — the post-Task-12 happy path.
// Tests that need the default-deny shape (no row, unapproved row, etc.)
// override srv.commanderRepo with their own newLinkTestRepo() instance.
func newPermissiveLinkTestRepo() *linkTestRepo {
	repo := newLinkTestRepo()
	repo.defaultApproved = true
	return repo
}

// newPermissiveAuthentikGroups returns a fakeAuthentikUserGroups whose
// GetUserByUUID returns a user in the edin-copilot group for any UUID — the
// post-Task-12 happy path. Tests that need a different shape (group missing,
// transient errors, etc.) override srv.authentikUserGroups directly.
func newPermissiveAuthentikGroups() *fakeAuthentikUserGroups {
	return &fakeAuthentikUserGroups{
		defaultGroups: []string{"edin-copilot"},
	}
}

// readLastDeniedLoginAttempt reads the JSON-lines audit file at logPath and
// returns the last (most recent) record. The audit infra writes one JSON
// object per line via recordDeniedLogin (see commander_allowlist.go). Tests
// use this to pin the Reason field for failure-path callbacks.
func readLastDeniedLoginAttempt(t *testing.T, logPath string) deniedLoginAttempt {
	t.Helper()
	data, err := os.ReadFile(logPath)
	require.NoError(t, err, "audit log file must exist after a denied login")
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	require.NotEmpty(t, lines, "audit log must contain at least one line")
	var last deniedLoginAttempt
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last),
		"last audit line must be valid JSON")
	return last
}

// ─── Initiate tests ────────────────────────────────────────────────────────────

func TestAuthInitiate_ReturnsAuthURL(t *testing.T) {
	srv := newCommanderAuthTestServer(t, "https://auth.frontier.test", nil, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/initiate", nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthInitiate(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.NotEmpty(t, body["auth_url"])
	assert.Contains(t, body["auth_url"], "https://auth.frontier.test/auth")
	assert.Contains(t, body["auth_url"], "response_type=code")
	assert.Contains(t, body["auth_url"], "code_challenge_method=S256")
	// Redirect URI must point to the frontend callback page, not the backend endpoint.
	assert.Contains(t, body["auth_url"], "copilot%2Fcallback", "redirect_uri must be /copilot/callback")
}

func TestAuthInitiate_AuthURLContainsCAPIScope(t *testing.T) {
	srv := newCommanderAuthTestServer(t, "https://auth.frontier.test", nil, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/initiate", nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthInitiate(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	authURL := body["auth_url"]

	// The URL should contain "auth+capi" (space encoded as +) or "auth%20capi".
	hasCAPIScope := strings.Contains(authURL, "auth+capi") || strings.Contains(authURL, "auth%20capi")
	assert.True(t, hasCAPIScope, "auth_url should contain 'auth+capi' scope; got: %s", authURL)
}

func TestAuthInitiate_RateLimit_ExceededReturns429(t *testing.T) {
	srv := newCommanderAuthTestServer(t, "https://auth.frontier.test", nil, 0)

	// Use a unique IP to avoid cross-test contamination.
	ip := fmt.Sprintf("10.0.%d.%d", t.Name()[0], t.Name()[1])

	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/initiate", nil)
		req.Header.Set("X-Forwarded-For", ip)
		rr := httptest.NewRecorder()
		srv.handleCommanderAuthInitiate(rr, req)
		return rr
	}

	// First 5 requests must succeed (rate limit = 5/min).
	for i := 0; i < 5; i++ {
		rr := makeReq()
		assert.Equal(t, http.StatusOK, rr.Code, "request %d should succeed", i+1)
	}

	// 6th request must be rate-limited.
	rr := makeReq()
	assert.Equal(t, http.StatusTooManyRequests, rr.Code, "6th request should be rate limited")
}

// ─── Callback tests ────────────────────────────────────────────────────────────

func TestAuthCallback_ValidState_IssuesJWT(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token":  "acc123",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "ref456",
	}
	frontier := frontierTestServer(t, tokenPayload, "2504", "Pattern State", 0, false)
	defer frontier.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontier.URL, rdb, 5*time.Second)

	// Seed a PKCE state entry.
	state := "test-state-uuid"
	srv.commanderPKCEStore.store(state, "test-verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=mycode&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "F2504", body["fid"])
	assert.Equal(t, "Pattern State", body["commander_name"])
	assert.Equal(t, false, body["capi_pending"])
}

func TestAuthCallback_InvalidState_Returns400(t *testing.T) {
	frontier := frontierTestServer(t, nil, "", "", 0, false)
	defer frontier.Close()

	srv := newCommanderAuthTestServer(t, frontier.URL, nil, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=mycode&state=nonexistent-state", nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid or expired state")
}

func TestAuthCallback_FrontierExchangeFails_Returns502(t *testing.T) {
	// Server that always returns 400 on /token.
	badFrontier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer badFrontier.Close()

	srv := newCommanderAuthTestServer(t, badFrontier.URL, nil, 5*time.Second)

	state := "my-state"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=badcode&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	assert.Equal(t, http.StatusBadGateway, rr.Code)
}

func TestAuthCallback_CAPIFails_SucceedsWithPlaceholderName(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token": "acc123",
		"expires_in":   3600,
	}
	// profileFail=true → CAPI returns 503.
	frontier := frontierTestServer(t, tokenPayload, "9999", "irrelevant", 0, true)
	defer frontier.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontier.URL, rdb, 5*time.Second)

	state := "state-capi-fail"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	// ⚠️ Must be 200, not 502.
	require.Equal(t, http.StatusOK, rr.Code, "CAPI failure must not prevent auth; body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "Unknown Commander", body["commander_name"])
	assert.Equal(t, true, body["capi_pending"])
	assert.Equal(t, "F9999", body["fid"])
}

func TestAuthCallback_CAPITimeout_SucceedsWithPlaceholderName(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token": "acc123",
		"expires_in":   3600,
	}
	// profileDelay > capiTimeout → times out.
	frontierSrv := frontierTestServer(t, tokenPayload, "7777", "Slow Commander", 300*time.Millisecond, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	// Set capiTimeout shorter than the server delay.
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 50*time.Millisecond)

	state := "state-timeout"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	// ⚠️ Must be 200 even on timeout.
	require.Equal(t, http.StatusOK, rr.Code, "CAPI timeout must not prevent auth; body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "Unknown Commander", body["commander_name"])
	assert.Equal(t, true, body["capi_pending"])
	assert.Equal(t, "F7777", body["fid"])
}

func TestAuthCallback_SetsHttpOnlyCookieSameSiteLax(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token": "acc123",
		"expires_in":   3600,
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "1111", "Test Commander", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	state := "state-cookie"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "commander_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie, "commander_session cookie must be set")
	assert.True(t, sessionCookie.HttpOnly, "cookie must be HttpOnly")
	assert.Equal(t, http.SameSiteLaxMode, sessionCookie.SameSite, "cookie SameSite must be Lax")
	assert.NotEmpty(t, sessionCookie.Value, "cookie value must not be empty")
}

func TestAuthCallback_ResponseContainsCAPIPendingFlag(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token": "acc123",
		"expires_in":   3600,
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "2222", "Commander Name", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	state := "state-capi-pending-check"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	// When CAPI succeeds, capi_pending should be false.
	capiPending, ok := body["capi_pending"]
	assert.True(t, ok, "response must contain capi_pending field")
	assert.Equal(t, false, capiPending)
}

// parseIssuedSessionJWT extracts CommanderClaims from the cookie set by a
// successful callback. Does not verify the signature — we just want to inspect
// what was claimed.
func parseIssuedSessionJWT(t *testing.T, rr *httptest.ResponseRecorder) *auth.CommanderClaims {
	t.Helper()
	var sessionCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "commander_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie, "commander_session cookie must be set")

	claims := &auth.CommanderClaims{}
	_, _, err := new(jwt.Parser).ParseUnverified(sessionCookie.Value, claims)
	require.NoError(t, err)
	return claims
}

// TestCommanderAuth_IssuedJWTContainsDefaultScopes asserts the JWT minted by
// the browser /callback endpoint carries the base commander scope set —
// {copilot_chat, galaxy_read, commander_data}. Post-Task-6 the scopes are
// derived from Authentik group membership; the test harness pre-seeds an
// approved row + edin-copilot group, which maps to this exact set.
func TestCommanderAuth_IssuedJWTContainsDefaultScopes(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token": "acc123",
		"expires_in":   3600,
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	state := "state-scopes-default"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	claims := parseIssuedSessionJWT(t, rr)
	assert.ElementsMatch(t,
		[]string{"copilot_chat", "galaxy_read", "commander_data"},
		claims.Scopes,
		"JWT must carry the edin-copilot scope set")
}

// TestCommanderAuth_CallbackTracksJTIUnderPerFIDSet verifies that a successful
// browser callback records the minted jti under commander:jtis:{fid} with
// the expected 24h TTL.
func TestCommanderAuth_CallbackTracksJTIUnderPerFIDSet(t *testing.T) {
	tokenPayload := map[string]any{
		"access_token": "acc123",
		"expires_in":   3600,
	}
	frontierSrv := frontierTestServer(t, tokenPayload, "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	state := "state-jti-tracking"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	claims := parseIssuedSessionJWT(t, rr)
	require.NotEmpty(t, claims.ID, "jti must be present")

	members, err := mr.SMembers("commander:jtis:F2504")
	require.NoError(t, err)
	assert.Contains(t, members, claims.ID, "per-FID tracking set must contain the new jti")

	// TTL must be ~24h (allow some slack for test latency).
	ttl := mr.TTL("commander:jtis:F2504")
	assert.True(t, ttl > 23*time.Hour && ttl <= 24*time.Hour,
		"per-FID tracking set TTL must be roughly 24h; got %s", ttl)
}

// TestCommanderAuth_LogoutRemovesJTIFromPerFIDSet verifies that handleCommanderAuthLogout
// SREMs the revoked jti from the per-FID tracking set.
func TestCommanderAuth_LogoutRemovesJTIFromPerFIDSet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr, jti, err := srv.commanderJWTIssuer.Issue("F1234", "Test Commander", nil)
	require.NoError(t, err)

	// Simulate the callback's SAdd so the set has something to remove. Seed two
	// jtis so removing the logout target leaves an observable remainder — this
	// sidesteps miniredis auto-deleting the empty set key.
	otherJTI := "other-active-jti"
	_, err = rdb.SAdd(context.Background(), "commander:jtis:F1234", jti, otherJTI).Result()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthLogout(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	members, err := mr.SMembers("commander:jtis:F1234")
	require.NoError(t, err)
	assert.NotContains(t, members, jti, "logout must SREM the revoked jti from the per-FID tracking set")
	assert.Contains(t, members, otherJTI, "logout must not touch unrelated jtis in the set")
}

// ─── Logout tests ──────────────────────────────────────────────────────────────

func issueTestJWT(t *testing.T, srv *Server, fid, name string) string {
	t.Helper()
	tok, _, err := srv.commanderJWTIssuer.Issue(fid, name, nil)
	require.NoError(t, err)
	return tok
}

func TestAuthLogout_RevokesJTI(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr := issueTestJWT(t, srv, "F1234", "Test Commander")

	req := httptest.NewRequest(http.MethodPost, "/api/commander/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthLogout(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	// After logout, validating the same token must fail (JTI revoked).
	_, err := srv.commanderJWTValidator.Validate(context.Background(), tokenStr)
	assert.ErrorIs(t, err, auth.ErrTokenRevoked)
}

func TestAuthLogout_ClearsCookie(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr := issueTestJWT(t, srv, "F1234", "Test Commander")

	req := httptest.NewRequest(http.MethodPost, "/api/commander/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthLogout(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "commander_session" {
			sessionCookie = c
			break
		}
	}
	require.NotNil(t, sessionCookie, "Set-Cookie header for commander_session must be present on logout")
	assert.Equal(t, -1, sessionCookie.MaxAge, "cookie MaxAge must be -1 to clear it")
}

func TestAuthLogout_NoCookie_Returns401(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodPost, "/api/commander/auth/logout", nil)
	// No cookie set.
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthLogout(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ─── Status tests ──────────────────────────────────────────────────────────────

func TestAuthStatus_ValidCookie_ReturnsAuthenticatedTrue(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr := issueTestJWT(t, srv, "F1234", "Pattern State")

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, true, body["authenticated"])
	assert.Equal(t, "Pattern State", body["commander_name"])
	assert.Equal(t, "F1234", body["fid"])
}

func TestAuthStatus_NoCookie_Returns401(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/status", nil)
	// No cookie set.
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthStatus(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, false, body["authenticated"])
}

func TestAuthStatus_InvalidCookie_Returns401(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/status", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: "not-a-valid-jwt"})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthStatus(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code)

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, false, body["authenticated"])
}

// ─── Token nonce tests ─────────────────────────────────────────────────────────

func TestAuthToken_ValidCookie_WithCsrfHeader_ReturnsNonce(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr := issueTestJWT(t, srv, "F5678", "Elite Commander")

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/token", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthToken(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.NotEmpty(t, body["nonce"])
	assert.Equal(t, float64(10), body["expires_in"])
}

// TestCommanderAuthToken_NoncePayloadMirrorsJWTScopes verifies that the
// CommanderChatUser stashed in the nonce store by handleCommanderAuthToken
// carries exactly the scopes from the JWT's "scopes" claim — not a hardcoded
// default. The JWT issued here deliberately omits commander_data so the test
// would fail if any default-fallback path were re-introduced.
//
// This completes the scope chain for the WS path: the same scopes derived
// from Authentik groups at callback time (Task 6) reach the copilot WS ctx
// via the nonce.
func TestCommanderAuthToken_NoncePayloadMirrorsJWTScopes(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	// Deliberately partial scope set — NOT the default {copilot_chat,
	// galaxy_read, commander_data}. If the handler falls back to a literal
	// default this test fails because the third scope appears.
	jwtScopes := []authz.Scope{authz.ScopeCopilotChat, authz.ScopeGalaxyRead}
	tokenStr, _, err := srv.commanderJWTIssuer.Issue("F9999", "Scope Test Cmdr", jwtScopes)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/token", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthToken(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	nonce, ok := body["nonce"].(string)
	require.True(t, ok, "expected nonce in response")

	user := srv.commanderNonceStore.Consume(nonce)
	require.NotNil(t, user, "nonce consume should return a user")

	assert.Equal(t, "F9999", user.FID)
	assert.Equal(t, "Scope Test Cmdr", user.Name,
		"nonce payload name must mirror JWT claims.Name exactly")
	assert.Equal(t, jwtScopes, user.Scopes,
		"nonce payload scopes must mirror JWT claims.Scopes exactly")
}

// TestCommanderAuthToken_EmptyJWTScopes_NoncePayloadAlsoEmpty verifies the
// fail-closed contract: a JWT with empty scopes (legacy or deny-closed)
// produces a nonce payload with empty scopes. NO default fallback applies —
// such a session reaching the copilot WS will see no tools.
func TestCommanderAuthToken_EmptyJWTScopes_NoncePayloadAlsoEmpty(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	// Issue a JWT with no scopes (issueTestJWT calls Issue(fid, name, nil)).
	tokenStr := issueTestJWT(t, srv, "F0000", "No Scope Cmdr")

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/token", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthToken(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	nonce, ok := body["nonce"].(string)
	require.True(t, ok, "expected nonce in response")

	user := srv.commanderNonceStore.Consume(nonce)
	require.NotNil(t, user, "nonce consume should return a user")

	assert.Equal(t, "F0000", user.FID)
	assert.Empty(t, user.Scopes,
		"empty JWT scopes must produce empty nonce-payload scopes (fail-closed)")
}

func TestAuthToken_ValidCookie_MissingCsrfHeader_Returns403(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr := issueTestJWT(t, srv, "F5678", "Elite Commander")

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/token", nil)
	// No X-Edin-Fetch header.
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthToken(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestAuthToken_NoCookie_Returns401(t *testing.T) {
	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/token", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	// No cookie set.
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthToken(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// ─── Auto-link tests (Task 5) ─────────────────────────────────────────────────

// frontierTokenPayload returns a stock Frontier token payload for tests.
func frontierTokenPayload() map[string]any {
	return map[string]any{
		"access_token":  "acc",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "ref",
	}
}

// TestCallback_FirstLogin_CreatesShadowUserAndLinksFID asserts that the
// browser callback for a commander whose row has authentik_user_id IS NULL
// invokes the shadow-user creator and persists the returned UUID via
// SetAuthentikLink. The login then proceeds (200 OK + JWT cookie).
//
// Post-Task-12 the test pre-approves the row so the access decision
// succeeds after auto-link runs — the auto-link path is what's under
// test here, not the awaiting-approval gate.
func TestCallback_FirstLogin_CreatesShadowUserAndLinksFID(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	repo := newLinkTestRepo()
	repo.seedRow("F2504", nil) // unlinked, but pre-approved so access succeeds.
	repo.rowByFID["F2504"].Approved = true
	srv.commanderRepo = repo

	wantUUID := uuid.MustParse("aaaa1111-bbbb-2222-cccc-333344445555")
	creator := &fakeShadowCreator{wantUUID: wantUUID}
	srv.createShadowUser = creator.fn()

	state := "state-first-login"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	assert.Equal(t, 1, creator.Calls(), "shadow creator must be invoked exactly once")
	assert.Equal(t, 1, repo.upsertCallCount(), "UpsertCommander must run before link")
	assert.Equal(t, 1, repo.setLinkCallCount(), "SetAuthentikLink must run after shadow create")
	assert.Equal(t, wantUUID, repo.linkedUUID("F2504"))
}

// TestCallback_ReturningCommander_DoesNotReCreateShadow asserts that when
// the row already has an authentik_user_id, the shadow creator is NOT
// invoked — preserves the previously-linked UUID.
//
// Post-Task-12 the test pre-approves the row so the access decision
// succeeds; the focus here is the no-op auto-link branch.
func TestCallback_ReturningCommander_DoesNotReCreateShadow(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	existingUUID := uuid.MustParse("ffff1111-eeee-2222-dddd-333344445555")
	repo := newLinkTestRepo()
	repo.seedRow("F2504", &existingUUID)
	repo.rowByFID["F2504"].Approved = true
	srv.commanderRepo = repo

	creator := &fakeShadowCreator{wantUUID: uuid.New()} // Should never be called.
	srv.createShadowUser = creator.fn()

	state := "state-returning"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	assert.Equal(t, 0, creator.Calls(),
		"shadow creator must NOT be invoked when commander row is already linked")
	assert.Equal(t, 0, repo.setLinkCallCount(),
		"SetAuthentikLink must NOT be invoked when commander row is already linked")
	assert.Equal(t, existingUUID, repo.linkedUUID("F2504"),
		"existing link must be preserved")
}

// TestCallback_AuthentikCreateFails_Returns403AndAuditsDenial asserts that
// a transient Authentik failure deny-closes the auth flow with a denial
// audit (reason="authentik_unreachable") and a 403 to the caller. No JWT
// cookie is issued.
func TestCallback_AuthentikCreateFails_Returns403AndAuditsDenial(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	logPath := filepath.Join(t.TempDir(), "attempts.log")
	srv.cfg.CommanderAuth.LoginAttemptLogPath = logPath

	repo := newLinkTestRepo()
	srv.commanderRepo = repo

	creator := &fakeShadowCreator{wantErr: errAuthentikDown}
	srv.createShadowUser = creator.fn()

	state := "state-authentik-down"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	req.Header.Set("User-Agent", "test/1.0")
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code, "Authentik failure must deny-close the auth flow")
	assert.Equal(t, 1, creator.Calls(), "creator must have been attempted")
	assert.Equal(t, 0, repo.setLinkCallCount(),
		"SetAuthentikLink must NOT be invoked when shadow create failed")
	assert.Equal(t, uuid.Nil, repo.linkedUUID("F2504"),
		"row must remain unlinked when shadow create failed")

	// No JWT cookie on a denied login.
	for _, c := range rr.Result().Cookies() {
		assert.NotEqual(t, "commander_session", c.Name,
			"commander_session cookie must NOT be set on denial")
	}

	// The audit record must discriminate Authentik failure from link-persist
	// failure so production diagnostics can tell them apart.
	logged := readLastDeniedLoginAttempt(t, logPath)
	assert.Equal(t, "F2504", logged.FID)
	assert.Equal(t, loginFlowWeb, logged.Flow)
	assert.Equal(t, "authentik_unreachable", logged.Reason,
		"Authentik failure must audit reason=authentik_unreachable")

	// Pin IP / UserAgent / Time so a regression that silently drops these
	// fields from the audit pipeline is caught — the fields are populated
	// in production but were not previously test-covered.
	assert.NotEmpty(t, logged.IP, "audit record must record caller IP")
	assert.Equal(t, "test/1.0", logged.UserAgent,
		"audit record must echo the caller's User-Agent")
	assert.False(t, logged.Time.IsZero(), "audit record must stamp Time")
}

// TestCallback_ShadowCreatedButLinkPersistFails_AuditsAndReturns403 covers
// the seam where Authentik state has gotten ahead of our DB. The next
// retry recovers via duplicate-username; this test verifies the first-
// attempt denial logs reason="link_persist_failed" and returns 403.
func TestCallback_ShadowCreatedButLinkPersistFails_AuditsAndReturns403(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	logPath := filepath.Join(t.TempDir(), "attempts.log")
	srv.cfg.CommanderAuth.LoginAttemptLogPath = logPath

	repo := newLinkTestRepo()
	repo.setLinkErr = errors.New("simulated link persist failure")
	srv.commanderRepo = repo

	wantUUID := uuid.MustParse("11112222-3333-4444-5555-666677778888")
	creator := &fakeShadowCreator{wantUUID: wantUUID}
	srv.createShadowUser = creator.fn()

	state := "state-link-persist-fail"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	req.Header.Set("User-Agent", "test/1.0")
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code,
		"link persist failure must deny-close the auth flow")
	assert.Equal(t, 1, creator.Calls(),
		"shadow creator must have run once even though persist failed")
	assert.Equal(t, 1, repo.setLinkCallCount(),
		"SetAuthentikLink must have been attempted")

	// No cookie on denial.
	for _, c := range rr.Result().Cookies() {
		assert.NotEqual(t, "commander_session", c.Name,
			"commander_session cookie must NOT be set on denial")
	}

	// The audit record must distinguish link-persist failure from a generic
	// Authentik outage — production diagnostics depend on this seam.
	logged := readLastDeniedLoginAttempt(t, logPath)
	assert.Equal(t, "F2504", logged.FID)
	assert.Equal(t, loginFlowWeb, logged.Flow)
	assert.Equal(t, "link_persist_failed", logged.Reason,
		"link-persist failure must audit reason=link_persist_failed")

	// Pin IP / UserAgent / Time so a regression that silently drops these
	// fields from the audit pipeline is caught.
	assert.NotEmpty(t, logged.IP, "audit record must record caller IP")
	assert.Equal(t, "test/1.0", logged.UserAgent,
		"audit record must echo the caller's User-Agent")
	assert.False(t, logged.Time.IsZero(), "audit record must stamp Time")
}

// ─── Task 6 callback integration ──────────────────────────────────────────────

// TestCallback_LinkedApprovedCommander_IssuesJWTWithAuthentikScopes covers the
// happy post-approval path end-to-end: a commander whose row is already
// linked + approved completes the Frontier callback, resolveCommanderAccess
// consults Authentik, derives scopes from the user's "edin-copilot" group,
// and the issued JWT carries those scopes (not the literal default).
func TestCallback_LinkedApprovedCommander_IssuesJWTWithAuthentikScopes(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")
	repo := newLinkTestRepo()
	repo.seedRow("F2504", &authentikUUID)
	repo.rowByFID["F2504"].Approved = true
	srv.commanderRepo = repo

	srv.authentikUserGroups = &fakeAuthentikUserGroups{
		userByUUID: map[uuid.UUID]*authentik.UserWithConnection{
			authentikUUID: {GroupNames: []string{"edin-copilot"}},
		},
	}

	state := "state-approved-authentik-scopes"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	claims := parseIssuedSessionJWT(t, rr)
	// ScopesForGroups([edin-copilot]) sorts lexicographically →
	// {commander_data, copilot_chat, galaxy_read}.
	assert.Equal(t, []string{"commander_data", "copilot_chat", "galaxy_read"}, claims.Scopes,
		"JWT must carry scopes derived from the Authentik group, not the literal default")
}

// TestCallback_LinkedNotApprovedOffAllowlist_AuditsAwaitingApproval covers a
// representative denial path through resolveCommanderAccess. The commander
// is linked (Task 5 ran) but not yet approved → 403 + audit
// reason=awaiting_approval.
func TestCallback_LinkedNotApprovedOffAllowlist_AuditsAwaitingApproval(t *testing.T) {
	frontierSrv := frontierTestServer(t, frontierTokenPayload(), "2504", "Pattern State", 0, false)
	defer frontierSrv.Close()

	rdb := newTestMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	logPath := filepath.Join(t.TempDir(), "attempts.log")
	srv.cfg.CommanderAuth.LoginAttemptLogPath = logPath

	authentikUUID := uuid.MustParse("aaaaaaaa-1111-2222-3333-444455556666")
	repo := newLinkTestRepo()
	repo.seedRow("F2504", &authentikUUID) // Approved defaults to false.
	srv.commanderRepo = repo

	// Authentik must NOT be consulted on the not-approved branch — verify
	// by injecting a fake that records call count.
	groups := &fakeAuthentikUserGroups{}
	srv.authentikUserGroups = groups

	state := "state-awaiting-approval"
	srv.commanderPKCEStore.store(state, "verifier", "http://localhost/copilot/callback", 10*time.Minute)

	req := httptest.NewRequest(http.MethodGet,
		"/api/commander/auth/callback?code=code&state="+state, nil)
	req.Header.Set("User-Agent", "test/1.0")
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthCallback(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code,
		"linked-but-not-approved off-allowlist must deny-close")
	assert.Equal(t, 0, groups.calls,
		"Authentik must NOT be consulted when the row is not yet approved")

	// No JWT cookie on denial.
	for _, c := range rr.Result().Cookies() {
		assert.NotEqual(t, "commander_session", c.Name,
			"commander_session cookie must NOT be set on denial")
	}

	logged := readLastDeniedLoginAttempt(t, logPath)
	assert.Equal(t, "F2504", logged.FID)
	assert.Equal(t, loginFlowWeb, logged.Flow)
	assert.Equal(t, "awaiting_approval", logged.Reason)
}
