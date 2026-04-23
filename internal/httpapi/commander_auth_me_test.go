package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthMe_ValidJWT_ReturnsFIDAndName(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	token, _, err := srv.commanderJWTIssuer.Issue("F2504", "Pattern State", nil)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()

	srv.withCommanderAuth(http.HandlerFunc(srv.handleAuthMe)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "F2504", body["fid"])
	assert.Equal(t, "Pattern State", body["commander_name"])
}

func TestAuthMe_MissingAuth_Returns401(t *testing.T) {
	rdb, _ := newClientAuthMiniredis(t)
	srv := newCommanderAuthTestServer(t, "http://frontier.invalid", rdb, 5*time.Second)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	rr := httptest.NewRecorder()

	srv.withCommanderAuth(http.HandlerFunc(srv.handleAuthMe)).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
