package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCommanderTestIssuerValidator creates a fresh RSA-2048 key pair and returns
// a matched issuer and validator for use in tests.
func newCommanderTestIssuerValidator(t *testing.T, rdb auth.RedisRevoker) (*auth.CommanderJWTIssuer, *auth.CommanderJWTValidator) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := auth.NewCommanderJWTIssuer(key, "edin-space", time.Hour)
	validator := auth.NewCommanderJWTValidator(&key.PublicKey, "edin-space", rdb)
	return issuer, validator
}

// newMiddlewareTestMiniredis creates an in-process miniredis and returns a redis.Client.
func newMiddlewareTestMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

// newMiddlewareTestServer constructs a minimal *Server with commanderJWTValidator set.
// Pass nil validator to test the "validator not configured" path.
func newMiddlewareTestServer(t *testing.T, validator *auth.CommanderJWTValidator) *Server {
	t.Helper()
	return &Server{
		cfg: &config.Config{
			CommanderAuth: config.CommanderAuthConfig{
				CookieName: "commander_session",
			},
		},
		logger:                observability.NewLogger("test"),
		commanderJWTValidator: validator,
	}
}

// issueMiddlewareTestJWT signs a token using the given issuer.
// Scopes are nil — tests that exercise scope-injection pass their own via
// issueMiddlewareTestJWTWithScopes.
func issueMiddlewareTestJWT(t *testing.T, issuer *auth.CommanderJWTIssuer, fid, name string) (tokenStr string, jti string) {
	t.Helper()
	tok, jtiVal, err := issuer.Issue(fid, name, nil)
	require.NoError(t, err)
	return tok, jtiVal
}

// issueMiddlewareTestJWTWithScopes signs a token including the given scopes.
func issueMiddlewareTestJWTWithScopes(t *testing.T, issuer *auth.CommanderJWTIssuer, fid, name string, scopes []authz.Scope) (tokenStr string, jti string) {
	t.Helper()
	tok, jtiVal, err := issuer.Issue(fid, name, scopes)
	require.NoError(t, err)
	return tok, jtiVal
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestCommanderAuth_ValidCookie_InjectsClaimsToContext(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	issuer, validator := newCommanderTestIssuerValidator(t, rdb)
	srv := newMiddlewareTestServer(t, validator)

	tokenStr, _ := issueMiddlewareTestJWT(t, issuer, "F1234", "Test Commander")

	var capturedFID string
	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		fid, err := fidFromContext(r.Context())
		require.NoError(t, err)
		capturedFID = fid
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	handler(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "F1234", capturedFID)
}

func TestCommanderAuth_ValidBearerToken_InjectsClaimsToContext(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	issuer, validator := newCommanderTestIssuerValidator(t, rdb)
	srv := newMiddlewareTestServer(t, validator)

	tokenStr, _ := issueMiddlewareTestJWT(t, issuer, "F5678", "Bearer Commander")

	var capturedFID string
	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		fid, err := fidFromContext(r.Context())
		require.NoError(t, err)
		capturedFID = fid
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	rr := httptest.NewRecorder()
	handler(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "F5678", capturedFID)
}

func TestCommanderAuth_NoCookieNoBearer_Returns401(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	_, validator := newCommanderTestIssuerValidator(t, rdb)
	srv := newMiddlewareTestServer(t, validator)

	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCommanderAuth_ExpiredToken_Returns401(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)

	// Issue a token with a -1h expiry (already expired).
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := auth.NewCommanderJWTIssuer(key, "edin-space", -time.Hour)
	validator := auth.NewCommanderJWTValidator(&key.PublicKey, "edin-space", rdb)
	srv := newMiddlewareTestServer(t, validator)

	tokenStr, _ := issueMiddlewareTestJWT(t, issuer, "F9999", "Expired Commander")

	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	handler(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCommanderAuth_RevokedJTI_Returns401(t *testing.T) {
	mr, rdb := newMiddlewareTestMiniredis(t)
	issuer, validator := newCommanderTestIssuerValidator(t, rdb)
	srv := newMiddlewareTestServer(t, validator)

	tokenStr, jti := issueMiddlewareTestJWT(t, issuer, "F4321", "Revoked Commander")

	// Revoke the JTI.
	err := validator.RevokeJTI(context.Background(), jti, time.Now().Add(time.Hour))
	require.NoError(t, err)

	// Ensure miniredis sees the revocation (fast-forward time if needed).
	_ = mr

	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	handler(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "token revoked")
}

func TestCommanderAuth_FIDFromContext_MatchesJWTClaim(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	issuer, validator := newCommanderTestIssuerValidator(t, rdb)
	srv := newMiddlewareTestServer(t, validator)

	tokenStr, _ := issueMiddlewareTestJWT(t, issuer, "F7777", "FID Commander")

	var fidFromCtx string
	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		fid, err := fidFromContext(r.Context())
		require.NoError(t, err)
		fidFromCtx = fid
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	handler(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "F7777", fidFromCtx)
}

// TestCommanderAuth_JWTScopes_InjectedToContext asserts that every scope
// carried in the JWT's "scopes" claim lands in the authz context via
// authz.ScopesFromContext — the middleware must not hardcode a scope list.
func TestCommanderAuth_JWTScopes_InjectedToContext(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	issuer, validator := newCommanderTestIssuerValidator(t, rdb)
	srv := newMiddlewareTestServer(t, validator)

	tokenStr, _ := issueMiddlewareTestJWTWithScopes(t, issuer, "F2222", "Scope Commander", []authz.Scope{
		authz.ScopeCopilotChat,
		authz.ScopeCommanderData,
	})

	var scopes []authz.Scope
	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		scopes = authz.ScopesFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	handler(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, scopes, authz.ScopeCopilotChat)
	assert.Contains(t, scopes, authz.ScopeCommanderData)
	// Scopes not on the JWT must NOT appear in the context.
	assert.NotContains(t, scopes, authz.ScopeGalaxyRead)
}

// TestCommanderAuth_EmptyScopesJWT_LeavesContextEmpty asserts the fail-closed
// behaviour: a legacy JWT issued without a scopes claim does not silently get
// a default scope set from the middleware.
func TestCommanderAuth_EmptyScopesJWT_LeavesContextEmpty(t *testing.T) {
	_, rdb := newMiddlewareTestMiniredis(t)
	issuer, validator := newCommanderTestIssuerValidator(t, rdb)
	srv := newMiddlewareTestServer(t, validator)

	tokenStr, _ := issueMiddlewareTestJWT(t, issuer, "F3333", "Legacy Commander")

	var scopes []authz.Scope
	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		scopes = authz.ScopesFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: tokenStr})
	rr := httptest.NewRecorder()
	handler(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Empty(t, scopes, "legacy JWT without scopes must yield an empty scope context")
}

func TestCommanderAuth_ValidatorNil_Returns503(t *testing.T) {
	// Server with nil commanderJWTValidator.
	srv := newMiddlewareTestServer(t, nil)

	handler := srv.withCommanderAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "commander_session", Value: "anytoken"})
	rr := httptest.NewRecorder()
	handler(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestFIDFromContext_WithoutMiddleware_ReturnsError(t *testing.T) {
	// No middleware applied — bare context.Background().
	fid, err := fidFromContext(context.Background())
	assert.Empty(t, fid)
	assert.ErrorIs(t, err, auth.ErrNoFIDInContext)
}

func TestFIDFromContext_Returns500_NotPanic_WhenMisused(t *testing.T) {
	// A handler that calls fidFromContext without middleware applied.
	// Verifies: no panic, handler returns 500, test does not crash.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := fidFromContext(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	// Must not panic.
	require.NotPanics(t, func() {
		handler.ServeHTTP(rr, req)
	})

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
