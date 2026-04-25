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

// TestCreateShadowUser_HappyPath_ReturnsUUID verifies the shadow-user
// payload (path, email, is_active, no password) and the round-tripped UUID.
func TestCreateShadowUser_HappyPath_ReturnsUUID(t *testing.T) {
	wantUUID := uuid.MustParse("33333333-4444-5555-6666-777777777777")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/api/v3/core/users/", r.URL.Path)

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		// The on-the-wire shape — assert path + email + active + absence
		// of password / groups; the prompt's invariants for shadow users.
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "F2504", got["username"])
		assert.Equal(t, "Pattern State", got["name"])
		assert.Equal(t, "users/edin-commanders", got["path"])
		assert.Equal(t, "F2504@edin-shadow.invalid", got["email"])
		assert.Equal(t, true, got["is_active"])
		_, hasPwd := got["password"]
		assert.False(t, hasPwd, "shadow users must not be created with a password")
		_, hasGroups := got["groups"]
		assert.False(t, hasGroups, "shadow users must not be created with groups")

		emailVal, _ := got["email"].(string)
		assert.True(t, strings.HasSuffix(emailVal, ".invalid"),
			"shadow email must end in .invalid")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pk":        99,
			"uuid":      wantUUID.String(),
			"username":  "F2504",
			"name":      "Pattern State",
			"is_active": true,
		})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")

	gotUUID, err := CreateShadowUser(context.Background(), c, "F2504", "Pattern State")
	require.NoError(t, err)
	assert.Equal(t, wantUUID, gotUUID)
}

// TestCreateShadowUser_DuplicateUsername_FallsBackToLookup covers the
// recovery path: a previous callback created the Authentik user but failed
// to write the link in our DB. The next callback retries; CreateUser
// returns 409 (ErrDuplicateUsername); the wrapper falls back to
// GetUserByUsername and recovers the existing UUID so SetAuthentikLink can
// finish the job.
func TestCreateShadowUser_DuplicateUsername_FallsBackToLookup(t *testing.T) {
	wantUUID := uuid.MustParse("88888888-9999-aaaa-bbbb-cccccccccccc")

	createCalls := 0
	listCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/core/users/":
			createCalls++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"username":["A user with this username already exists."]}`))

		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/core/users/":
			listCalls++
			require.Equal(t, "F2504", r.URL.Query().Get("username"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pagination": map[string]any{"count": 1},
				"results": []map[string]any{
					{
						"pk":       11,
						"uuid":     wantUUID.String(),
						"username": "F2504",
						"name":     "Pattern State",
					},
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")

	gotUUID, err := CreateShadowUser(context.Background(), c, "F2504", "Pattern State")
	require.NoError(t, err)
	assert.Equal(t, wantUUID, gotUUID)
	assert.Equal(t, 1, createCalls, "CreateUser must be called exactly once")
	assert.Equal(t, 1, listCalls, "GetUserByUsername must be called exactly once")
}

// TestCreateShadowUser_5xx_PropagatesError covers the deny-closed path:
// transient Authentik failure must surface as an error so the callback can
// audit + 403.
func TestCreateShadowUser_5xx_PropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`upstream meltdown`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")

	id, err := CreateShadowUser(context.Background(), c, "F2504", "Pattern State")
	assert.Equal(t, uuid.Nil, id)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrDuplicateUsername))
	assert.False(t, errors.Is(err, ErrUserNotFound))
}

// TestCreateShadowUser_DuplicateThenLookupNotFound_ReturnsError covers a
// theoretically-impossible-but-defensive case: CreateUser says duplicate,
// but the follow-up lookup returns no row (race between two concurrent
// callbacks where the other one deleted the user, or transient Authentik
// inconsistency). The wrapper must NOT silently succeed.
func TestCreateShadowUser_DuplicateThenLookupNotFound_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"username":["already exists"]}`))
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pagination":{"count":0},"results":[]}`))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "test-token")

	id, err := CreateShadowUser(context.Background(), c, "F2504", "Pattern State")
	assert.Equal(t, uuid.Nil, id)
	require.Error(t, err)
}

// TestCreateShadowUser_NilClient_ReturnsError covers the wiring guard.
func TestCreateShadowUser_NilClient_ReturnsError(t *testing.T) {
	id, err := CreateShadowUser(context.Background(), nil, "F2504", "Pattern State")
	assert.Equal(t, uuid.Nil, id)
	require.Error(t, err)
}

// TestCreateShadowUser_EmptyFID_ReturnsError covers the wiring guard.
func TestCreateShadowUser_EmptyFID_ReturnsError(t *testing.T) {
	c := NewClient("http://unused", "test-token")
	id, err := CreateShadowUser(context.Background(), c, "", "Pattern State")
	assert.Equal(t, uuid.Nil, id)
	require.Error(t, err)
}
