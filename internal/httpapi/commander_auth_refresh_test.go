package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── refresh test helpers ─────────────────────────────────────────────────────

// newRefreshMiniredis returns a miniredis-backed *redis.Client and the underlying
// *miniredis.Miniredis so tests can manipulate TTLs and keys directly.
func newRefreshMiniredis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

// seedFrontierToken seeds a frontierTokenRecord into miniredis for the given JTI.
// expiresIn controls how far in the future the access token expires (negative = already expired).
// refreshToken may be empty.
// capiPending controls the capi_pending flag.
func seedFrontierToken(t *testing.T, mr *miniredis.Miniredis, jti string, expiresIn time.Duration, refreshToken string, capiPending bool) {
	t.Helper()
	expiresAt := time.Now().Add(expiresIn)
	record := map[string]any{
		"access_token":  "acc-seeded",
		"refresh_token": refreshToken,
		"expires_at":    expiresAt.Format(time.RFC3339),
		"capi_pending":  capiPending,
	}
	data, err := json.Marshal(record)
	require.NoError(t, err)

	// Store with a positive TTL so the key actually persists.
	ttl := expiresIn
	if ttl < 5*time.Minute {
		ttl = 5 * time.Minute // always keep in Redis even if token is "expired"
	}
	err = mr.Set(frontierTokenKey(jti), string(data))
	require.NoError(t, err)
	mr.SetTTL(frontierTokenKey(jti), ttl)
}

// makeRefreshRequest builds a GET /api/commander/auth/refresh request with
// the commander_session cookie set to tokenStr.
func makeRefreshRequest(tokenStr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	return req
}

// ─── Tests ─────────────────────────────────────────────────────────────────────

// TestAuthRefresh_NoCookie_Returns401 verifies that a request without a
// commander_session cookie is rejected with 401.
func TestAuthRefresh_NoCookie_Returns401(t *testing.T) {
	rdb, _ := newRefreshMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/commander/auth/refresh", nil)
	// Deliberately no cookie.
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestAuthRefresh_RedisKeyExpired_Returns401 verifies that when the Redis key
// for the session's JTI is missing (session expired), we return 401.
func TestAuthRefresh_RedisKeyExpired_Returns401(t *testing.T) {
	rdb, _ := newRefreshMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	// Issue a valid EDIN JWT but do NOT seed the Redis key.
	tokenStr := issueTestJWT(t, srv, "F1234", "Test Commander")

	req := makeRefreshRequest(tokenStr)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "session expired")
}

// TestAuthRefresh_FrontierTokenExpiredNoRefreshToken_Returns401 checks that when
// the Frontier access token is expired and there's no refresh token available,
// we return 401.
func TestAuthRefresh_FrontierTokenExpiredNoRefreshToken_Returns401(t *testing.T) {
	rdb, mr := newRefreshMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr, jti, err := srv.commanderJWTIssuer.Issue("F1234", "Test Commander", nil)
	require.NoError(t, err)

	// Token is expired, no refresh token.
	seedFrontierToken(t, mr, jti, -1*time.Hour, "" /* no refresh token */, false)

	req := makeRefreshRequest(tokenStr)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "refresh token not available")
}

// TestAuthRefresh_ValidToken_IssuesNewJWT checks the happy path: valid cookie,
// Frontier token still valid → new EDIN JWT issued and cookie set.
func TestAuthRefresh_ValidToken_IssuesNewJWT(t *testing.T) {
	rdb, mr := newRefreshMiniredis(t)
	// Frontier server not actually called (token not expired).
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr, jti, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	// Frontier token still valid (expires in 1 hour).
	seedFrontierToken(t, mr, jti, 1*time.Hour, "ref-token", false)

	req := makeRefreshRequest(tokenStr)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "Pattern State", body["commander_name"])
	assert.Equal(t, "F2504", body["fid"])
	assert.Equal(t, false, body["capi_pending"])

	// New cookie must be set.
	cookies := rr.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == "commander_session" {
			session = c
			break
		}
	}
	require.NotNil(t, session, "commander_session cookie must be set on refresh")
	assert.True(t, session.HttpOnly)
	assert.NotEmpty(t, session.Value)
	assert.NotEqual(t, tokenStr, session.Value, "new cookie value must differ from the old one")
}

// TestAuthRefresh_OldJTIRevoked verifies that after a successful refresh,
// the old JTI is revoked (old token should no longer be valid).
func TestAuthRefresh_OldJTIRevoked(t *testing.T) {
	rdb, mr := newRefreshMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	tokenStr, jti, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	seedFrontierToken(t, mr, jti, 1*time.Hour, "ref-token", false)

	req := makeRefreshRequest(tokenStr)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	// Validating the OLD token must now fail.
	_, err = srv.commanderJWTValidator.Validate(req.Context(), tokenStr)
	assert.Error(t, err, "old token must be revoked after refresh")
}

// TestAuthRefresh_NewJTIIssuedForNewFrontierTokens verifies that when new Frontier
// tokens are obtained (after a refresh), the new frontier record is stored under
// the new JTI (not the old one).
func TestAuthRefresh_NewJTIIssuedForNewFrontierTokens(t *testing.T) {
	// Stand up a fake Frontier that can handle /token (refresh) requests.
	frontierSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "new-access-token",
				"refresh_token": "new-refresh-token",
				"expires_in":    3600,
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer frontierSrv.Close()

	rdb, mr := newRefreshMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	tokenStr, jti, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	// Frontier access token is expired → will trigger refresh.
	seedFrontierToken(t, mr, jti, -1*time.Hour, "old-refresh-token", false)

	req := makeRefreshRequest(tokenStr)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	// Extract new JTI from the new cookie value.
	var newCookieValue string
	for _, c := range rr.Result().Cookies() {
		if c.Name == "commander_session" {
			newCookieValue = c.Value
			break
		}
	}
	require.NotEmpty(t, newCookieValue)

	newClaims, err := srv.commanderJWTValidator.Validate(req.Context(), newCookieValue)
	require.NoError(t, err)
	newJTI := newClaims.ID
	assert.NotEqual(t, jti, newJTI, "new JTI must differ from old JTI")

	// New JTI key must exist in Redis.
	exists := mr.Exists(frontierTokenKey(newJTI))
	assert.True(t, exists, "frontier token record must be stored under new JTI")

	// Old JTI key may or may not exist (it might have been deleted), but new key is the key invariant.
}

// TestAuthRefresh_FrontierTokenExpired_UsesRefreshToken checks that when the
// Frontier access token is expired, the refresh token is used to obtain new tokens.
func TestAuthRefresh_FrontierTokenExpired_UsesRefreshToken(t *testing.T) {
	refreshCalled := false
	frontierSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/token" {
			refreshCalled = true
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refreshed-access-token",
				"refresh_token": "refreshed-refresh-token",
				"expires_in":    3600,
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer frontierSrv.Close()

	rdb, mr := newRefreshMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	tokenStr, jti, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	// Frontier token is expired, but refresh token is available.
	seedFrontierToken(t, mr, jti, -1*time.Hour, "valid-refresh-token", false)

	req := makeRefreshRequest(tokenStr)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	assert.True(t, refreshCalled, "Frontier /token (refresh grant) must have been called")
}

// TestAuthRefresh_CAPIPending_RetrySucceeds_UpdatesName tests that when the stored
// record has capi_pending=true and GetProfile succeeds, the commander name is updated
// and capi_pending is cleared in the response.
func TestAuthRefresh_CAPIPending_RetrySucceeds_UpdatesName(t *testing.T) {
	frontierSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			// Refresh token call (token is expired in this test to trigger refresh).
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "new-access",
				"refresh_token": "new-refresh",
				"expires_in":    3600,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/profile":
			json.NewEncoder(w).Encode(map[string]any{
				"commander": map[string]string{"name": "Real Commander Name"},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer frontierSrv.Close()

	rdb, mr := newRefreshMiniredis(t)
	srv := newCommanderAuthTestServer(t, frontierSrv.URL, rdb, 5*time.Second)

	// Issue token with placeholder name (capi_pending).
	tokenStr, jti, err := srv.commanderJWTIssuer.Issue("F9999", "Unknown Commander", nil)
	require.NoError(t, err)

	// Frontier token expired, capi_pending=true.
	seedFrontierToken(t, mr, jti, -1*time.Hour, "refresh-tok", true)

	req := makeRefreshRequest(tokenStr)
	rr := httptest.NewRecorder()
	srv.handleCommanderAuthRefresh(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())

	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "Real Commander Name", body["commander_name"])
	assert.Equal(t, false, body["capi_pending"])
	assert.Equal(t, "F9999", body["fid"])
}
