package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
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
			},
		},
		logger:                observability.NewLogger("test"),
		redisClient:           rdb,
		commanderJWTIssuer:    issuer,
		commanderJWTValidator: validator,
		commanderPKCEStore:    newCommanderPKCEStore(1000),
		commanderNonceStore:   newKaineNonceStore(),
	}
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

// ─── Logout tests ──────────────────────────────────────────────────────────────

func issueTestJWT(t *testing.T, srv *Server, fid, name string) string {
	t.Helper()
	tok, _, err := srv.commanderJWTIssuer.Issue(fid, name)
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
