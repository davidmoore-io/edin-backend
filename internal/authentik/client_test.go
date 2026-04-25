package authentik

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFakeAuthentikServer returns an httptest.Server that serves a single
// handler. The Client is constructed with a fixed token; the handler can
// assert on the Authorization header.
func newFakeAuthentikServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "test-token")
	return srv, c
}

// TestAuthentikClient_CreateUser_Success exercises the happy path: a 201
// response carrying a UUID is decoded into User.UUID.
func TestAuthentikClient_CreateUser_Success(t *testing.T) {
	wantUUID := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	_, c := newFakeAuthentikServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v3/core/users/", r.URL.Path)
		require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		var got CreateUserRequest
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &got))

		// Caller's payload must round-trip exactly — shadow-user wiring
		// depends on path / email shape.
		assert.Equal(t, "F2504", got.Username)
		assert.Equal(t, "Pattern State", got.Name)
		assert.Equal(t, "F2504@edin-shadow.invalid", got.Email)
		assert.Equal(t, "users/edin-commanders", got.Path)
		require.NotNil(t, got.IsActive)
		assert.True(t, *got.IsActive)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pk":        42,
			"uuid":      wantUUID.String(),
			"username":  got.Username,
			"name":      got.Name,
			"email":     got.Email,
			"is_active": true,
			"path":      got.Path,
		})
	})

	isActive := true
	user, err := c.CreateUser(context.Background(), CreateUserRequest{
		Username: "F2504",
		Name:     "Pattern State",
		Email:    "F2504@edin-shadow.invalid",
		Path:     "users/edin-commanders",
		IsActive: &isActive,
	})
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, wantUUID, user.UUID)
	assert.Equal(t, 42, user.PK)
	assert.Equal(t, "F2504", user.Username)
}

// TestAuthentikClient_CreateUser_Duplicate_ReturnsErrDuplicate exercises the
// idempotency seam: when Authentik rejects the create with a duplicate-
// username error, the client surfaces ErrDuplicateUsername so callers can
// fall back to GetUserByUsername.
func TestAuthentikClient_CreateUser_Duplicate_ReturnsErrDuplicate(t *testing.T) {
	_, c := newFakeAuthentikServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"username":["A user with this username already exists."]}`))
	})

	user, err := c.CreateUser(context.Background(), CreateUserRequest{
		Username: "F2504",
		Name:     "Pattern State",
	})
	assert.Nil(t, user)
	assert.True(t, errors.Is(err, ErrDuplicateUsername),
		"expected ErrDuplicateUsername, got: %v", err)
}

// TestAuthentikClient_CreateUser_OtherError_PropagatesError covers 5xx and
// other 4xx responses — the wrapper must NOT misclassify them as duplicate.
func TestAuthentikClient_CreateUser_OtherError_PropagatesError(t *testing.T) {
	_, c := newFakeAuthentikServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"internal error"}`))
	})

	user, err := c.CreateUser(context.Background(), CreateUserRequest{Username: "F2504"})
	assert.Nil(t, user)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrDuplicateUsername))
	assert.Contains(t, err.Error(), "500")
}

// TestAuthentikClient_GetUserByUsername_Success exercises the happy path:
// the username matches the first paginated result.
func TestAuthentikClient_GetUserByUsername_Success(t *testing.T) {
	wantUUID := uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

	_, c := newFakeAuthentikServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v3/core/users/", r.URL.Path)
		assert.Equal(t, "F2504", r.URL.Query().Get("username"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pagination": map[string]any{"count": 1},
			"results": []map[string]any{
				{
					"pk":        7,
					"uuid":      wantUUID.String(),
					"username":  "F2504",
					"name":      "Pattern State",
					"is_active": true,
				},
			},
		})
	})

	user, err := c.GetUserByUsername(context.Background(), "F2504")
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.Equal(t, wantUUID, user.UUID)
	assert.Equal(t, "F2504", user.Username)
}

// TestAuthentikClient_GetUserByUsername_NoResults_ReturnsErrUserNotFound
// covers the case where the paginated response is empty.
func TestAuthentikClient_GetUserByUsername_NoResults_ReturnsErrUserNotFound(t *testing.T) {
	_, c := newFakeAuthentikServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pagination":{"count":0},"results":[]}`))
	})

	user, err := c.GetUserByUsername(context.Background(), "F-missing")
	assert.Nil(t, user)
	assert.True(t, errors.Is(err, ErrUserNotFound),
		"expected ErrUserNotFound, got: %v", err)
}

// TestAuthentikClient_GetUserByUsername_FilterMismatch_ReturnsErrUserNotFound
// covers the case where Authentik returns rows but none exact-match the
// username (Authentik's ?username= is documented as exact, but defending
// against substring drift is cheap and correct).
func TestAuthentikClient_GetUserByUsername_FilterMismatch_ReturnsErrUserNotFound(t *testing.T) {
	_, c := newFakeAuthentikServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pagination": map[string]any{"count": 1},
			"results": []map[string]any{
				{"pk": 1, "username": "F2504-suffix"},
			},
		})
	})

	user, err := c.GetUserByUsername(context.Background(), "F2504")
	assert.Nil(t, user)
	assert.True(t, errors.Is(err, ErrUserNotFound))
}

// TestAuthentikClient_GetUserByUsername_5xx_PropagatesError covers transient
// upstream failures.
func TestAuthentikClient_GetUserByUsername_5xx_PropagatesError(t *testing.T) {
	_, c := newFakeAuthentikServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`upstream`))
	})

	user, err := c.GetUserByUsername(context.Background(), "F2504")
	assert.Nil(t, user)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrUserNotFound))
	assert.Contains(t, strings.ToLower(err.Error()), "502")
}
