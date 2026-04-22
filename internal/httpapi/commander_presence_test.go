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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPresenceTestServer builds a *Server ready to exercise the heartbeat and
// presence endpoints. Reuses the commander-auth test harness (miniredis +
// RSA keypair) and attaches the heartbeat rate limiter.
func newPresenceTestServer(t *testing.T) *Server {
	t.Helper()
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)
	srv.heartbeatRateLimiter = newHeartbeatFIDRateLimiter()
	return srv
}

// bearerReqPost constructs an authenticated POST request for the given fid.
func bearerReqPost(t *testing.T, srv *Server, fid, name, path string, body []byte) *http.Request {
	t.Helper()
	token, _, err := srv.commanderJWTIssuer.Issue(fid, name)
	require.NoError(t, err)

	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(http.MethodPost, path, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// bearerReqGet constructs an authenticated GET request for the given fid.
func bearerReqGet(t *testing.T, srv *Server, fid, name, path string) *http.Request {
	t.Helper()
	token, _, err := srv.commanderJWTIssuer.Issue(fid, name)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// ─── heartbeat ────────────────────────────────────────────────────────────────

func TestCommanderHeartbeat_WritesRedisKeyedOnJWTFID(t *testing.T) {
	srv := newPresenceTestServer(t)

	req := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat",
		[]byte(`{"client_version":"1.2.3"}`))
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)

	// Redis key must be keyed on the JWT's FID. Anything else means FID is
	// being read from somewhere the caller can influence.
	raw, err := srv.redisClient.Get(context.Background(), presenceKey("F2504")).Bytes()
	require.NoError(t, err)

	var rec presenceRecord
	require.NoError(t, json.Unmarshal(raw, &rec))
	assert.Equal(t, "1.2.3", rec.ClientVersion)
	assert.WithinDuration(t, time.Now(), rec.LastSeen, 2*time.Second)
}

func TestCommanderHeartbeat_TTLIsShort(t *testing.T) {
	srv := newPresenceTestServer(t)
	req := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)

	ttl, err := srv.redisClient.TTL(context.Background(), presenceKey("F2504")).Result()
	require.NoError(t, err)
	// Allow a small sliver of clock slop but the TTL must be bounded — if
	// this regressed to "no expiry" the presence signal would be permanently
	// stuck to "live" on backend restart.
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, presenceTTL+time.Second)
}

// TestCommanderHeartbeat_BodyFIDIgnored is the core isolation guarantee: even
// when the request body supplies an FID-looking field, the server must NOT use
// it. The only source of identity is the JWT.
func TestCommanderHeartbeat_BodyFIDIgnored(t *testing.T) {
	srv := newPresenceTestServer(t)

	// JWT is for F2504 but the body tries to impersonate F9999.
	mischief := []byte(`{"client_version":"1.0","fid":"F9999","commander_fid":"F9999","sub":"F9999"}`)
	req := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat", mischief)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)

	// F2504 key must exist, F9999 key must not.
	ctx := context.Background()
	exists, err := srv.redisClient.Exists(ctx, presenceKey("F2504")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists, "heartbeat must write the JWT's FID, not the body's")

	exists, err = srv.redisClient.Exists(ctx, presenceKey("F9999")).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "body FID must never leak into another commander's key")
}

func TestCommanderHeartbeat_MissingAuth_Returns401(t *testing.T) {
	srv := newPresenceTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commander/heartbeat", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCommanderHeartbeat_WrongMethod_Returns405(t *testing.T) {
	srv := newPresenceTestServer(t)
	req := bearerReqGet(t, srv, "F2504", "Pattern State", "/api/v1/commander/heartbeat")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestCommanderHeartbeat_RateLimit_Returns429(t *testing.T) {
	srv := newPresenceTestServer(t)

	// Burn through the bucket.
	for i := 0; i < heartbeatRateLimit; i++ {
		req := bearerReqPost(t, srv, "F2504", "Pattern State",
			"/api/v1/commander/heartbeat", nil)
		rr := httptest.NewRecorder()
		srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
		require.Equal(t, http.StatusNoContent, rr.Code, "request %d should succeed", i)
	}

	// The next one should 429.
	req := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
}

func TestCommanderHeartbeat_MalformedBodyStillSucceeds(t *testing.T) {
	// Body is advisory. A broken body must not deny the heartbeat itself —
	// otherwise a bug in the desktop client's version-string logic could
	// silently flip everyone offline.
	srv := newPresenceTestServer(t)
	req := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat", []byte("{not valid json"))
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNoContent, rr.Code)
}

func TestCommanderHeartbeat_TruncatesOverlongClientVersion(t *testing.T) {
	srv := newPresenceTestServer(t)
	huge := strings.Repeat("A", 10_000)
	body, _ := json.Marshal(map[string]string{"client_version": huge})

	req := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat", body)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(rr, req)
	require.Equal(t, http.StatusNoContent, rr.Code)

	raw, err := srv.redisClient.Get(context.Background(), presenceKey("F2504")).Bytes()
	require.NoError(t, err)
	var rec presenceRecord
	require.NoError(t, json.Unmarshal(raw, &rec))
	assert.LessOrEqual(t, len(rec.ClientVersion), maxClientVersionLen)
}

// ─── presence read ────────────────────────────────────────────────────────────

func TestCommanderPresence_NoHeartbeatYet_ReturnsNotLive(t *testing.T) {
	srv := newPresenceTestServer(t)
	req := bearerReqGet(t, srv, "F2504", "Pattern State", "/api/commander/presence")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderPresence)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var resp presenceResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.IsLive)
	assert.Nil(t, resp.LastSeen)
}

func TestCommanderPresence_AfterHeartbeat_ReturnsLive(t *testing.T) {
	srv := newPresenceTestServer(t)

	// Heartbeat.
	hb := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat", []byte(`{"client_version":"1.2.3"}`))
	hbRR := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(hbRR, hb)
	require.Equal(t, http.StatusNoContent, hbRR.Code)

	// Presence.
	pr := bearerReqGet(t, srv, "F2504", "Pattern State", "/api/commander/presence")
	prRR := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderPresence)).ServeHTTP(prRR, pr)
	require.Equal(t, http.StatusOK, prRR.Code)

	var resp presenceResponse
	require.NoError(t, json.Unmarshal(prRR.Body.Bytes(), &resp))
	assert.True(t, resp.IsLive)
	require.NotNil(t, resp.LastSeen)
	assert.Equal(t, "1.2.3", resp.ClientVersion)
	assert.WithinDuration(t, time.Now(), *resp.LastSeen, 2*time.Second)
}

// TestCommanderPresence_IsolationAcrossCommanders is the central isolation
// test: one commander's heartbeat must never surface in another commander's
// presence reply. Even if they share a process, a Redis instance, a JWT
// signing key.
func TestCommanderPresence_IsolationAcrossCommanders(t *testing.T) {
	srv := newPresenceTestServer(t)

	// Commander A heartbeats.
	hb := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/v1/commander/heartbeat", []byte(`{"client_version":"a-version"}`))
	hbRR := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderHeartbeat)).ServeHTTP(hbRR, hb)
	require.Equal(t, http.StatusNoContent, hbRR.Code)

	// Commander B reads presence — must be not-live. There is no URL param
	// or header for "whose presence" so this is the ONLY way to ask and we
	// rely on the JWT to scope it.
	pr := bearerReqGet(t, srv, "F9999", "Other Commander", "/api/commander/presence")
	prRR := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderPresence)).ServeHTTP(prRR, pr)
	require.Equal(t, http.StatusOK, prRR.Code)

	var resp presenceResponse
	require.NoError(t, json.Unmarshal(prRR.Body.Bytes(), &resp))
	assert.False(t, resp.IsLive, "commander B must not see commander A's heartbeat")
	assert.Empty(t, resp.ClientVersion)
}

func TestCommanderPresence_MissingAuth_Returns401(t *testing.T) {
	srv := newPresenceTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/commander/presence", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderPresence)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestCommanderPresence_WrongMethod_Returns405(t *testing.T) {
	srv := newPresenceTestServer(t)
	req := bearerReqPost(t, srv, "F2504", "Pattern State",
		"/api/commander/presence", nil)
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderPresence)).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestCommanderPresence_CorruptRedisRecord_TreatedAsAbsent(t *testing.T) {
	// Write a garbage value directly. If a future bug (or operator manually
	// fat-fingering) leaves non-JSON in the key, presence must degrade to
	// not-live rather than 500ing the UI.
	srv := newPresenceTestServer(t)
	ctx := context.Background()
	err := srv.redisClient.Set(ctx, presenceKey("F2504"), "this is not json", presenceTTL).Err()
	require.NoError(t, err)

	req := bearerReqGet(t, srv, "F2504", "Pattern State", "/api/commander/presence")
	rr := httptest.NewRecorder()
	srv.withCommanderAuth(http.HandlerFunc(srv.handleCommanderPresence)).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp presenceResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.False(t, resp.IsLive)
}

