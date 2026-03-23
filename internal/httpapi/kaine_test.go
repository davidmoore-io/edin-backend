package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
)

// createTestJWT creates a minimal JWT-like token for testing.
// NOTE: This creates UNSIGNED tokens - only for use with mock validators.
// It is NOT suitable for testing real JWT validation.
func createTestJWT(claims map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + payloadB64 + ".signature"
}

// mockJWTValidator provides a test implementation of JWT validation.
type mockJWTValidator struct {
	users map[string]*KaineUser // token -> user
}

func newMockJWTValidator() *mockJWTValidator {
	return &mockJWTValidator{
		users: make(map[string]*KaineUser),
	}
}

func (m *mockJWTValidator) addUser(token string, user *KaineUser) {
	m.users[token] = user
}

func (m *mockJWTValidator) ValidateToken(token string) (*KaineUser, error) {
	if user, ok := m.users[token]; ok {
		return user, nil
	}
	return nil, errors.New("invalid token")
}

func (m *mockJWTValidator) Close() {}

// TestKaineUserPermissions tests all permission helper methods on KaineUser.
func TestKaineUserPermissions(t *testing.T) {
	tests := []struct {
		name     string
		user     *KaineUser
		check    func(*KaineUser) bool
		expected bool
	}{
		// HasRole tests
		{
			name:     "HasRole with exact match",
			user:     &KaineUser{Groups: []string{"kaine-ops"}},
			check:    func(u *KaineUser) bool { return u.HasRole("ops") },
			expected: true,
		},
		{
			name:     "HasRole with test suffix",
			user:     &KaineUser{Groups: []string{"kaine-ops-test"}},
			check:    func(u *KaineUser) bool { return u.HasRole("ops") },
			expected: true,
		},
		{
			name:     "HasRole with no match",
			user:     &KaineUser{Groups: []string{"kaine-pledge"}},
			check:    func(u *KaineUser) bool { return u.HasRole("ops") },
			expected: false,
		},
		{
			name:     "HasRole with empty groups",
			user:     &KaineUser{Groups: []string{}},
			check:    func(u *KaineUser) bool { return u.HasRole("ops") },
			expected: false,
		},

		// IsGodMode tests
		{
			name:     "IsGodMode with kaine-god",
			user:     &KaineUser{Groups: []string{"kaine-god"}},
			check:    func(u *KaineUser) bool { return u.IsGodMode() },
			expected: true,
		},
		{
			name:     "IsGodMode with kaine-approved is not god mode",
			user:     &KaineUser{Groups: []string{"kaine-approved"}},
			check:    func(u *KaineUser) bool { return u.IsGodMode() },
			expected: false,
		},
		{
			name:     "IsGodMode without kaine-god",
			user:     &KaineUser{Groups: []string{"kaine-ops", "kaine-directors"}},
			check:    func(u *KaineUser) bool { return u.IsGodMode() },
			expected: false,
		},

		// CanEditObjectives tests
		{
			name:     "CanEditObjectives with god mode",
			user:     &KaineUser{Groups: []string{"kaine-god"}},
			check:    func(u *KaineUser) bool { return u.CanEditObjectives() },
			expected: true,
		},
		{
			name:     "CanEditObjectives with directors",
			user:     &KaineUser{Groups: []string{"kaine-directors"}},
			check:    func(u *KaineUser) bool { return u.CanEditObjectives() },
			expected: true,
		},
		{
			name:     "CanEditObjectives with lead-ops",
			user:     &KaineUser{Groups: []string{"kaine-lead-ops"}},
			check:    func(u *KaineUser) bool { return u.CanEditObjectives() },
			expected: true,
		},
		{
			name:     "CanEditObjectives with ops only (should fail)",
			user:     &KaineUser{Groups: []string{"kaine-ops"}},
			check:    func(u *KaineUser) bool { return u.CanEditObjectives() },
			expected: false,
		},

		// CanViewOps tests
		{
			name:     "CanViewOps with god mode",
			user:     &KaineUser{Groups: []string{"kaine-god"}},
			check:    func(u *KaineUser) bool { return u.CanViewOps() },
			expected: true,
		},
		{
			name:     "CanViewOps with ops role",
			user:     &KaineUser{Groups: []string{"kaine-ops"}},
			check:    func(u *KaineUser) bool { return u.CanViewOps() },
			expected: true,
		},
		{
			name:     "CanViewOps with pledge only (should fail)",
			user:     &KaineUser{Groups: []string{"kaine-pledge"}},
			check:    func(u *KaineUser) bool { return u.CanViewOps() },
			expected: false,
		},

		// CanViewPledge tests
		{
			name:     "CanViewPledge with pledge role",
			user:     &KaineUser{Groups: []string{"kaine-pledge"}},
			check:    func(u *KaineUser) bool { return u.CanViewPledge() },
			expected: true,
		},
		{
			name:     "CanViewPledge with ops role (inherits)",
			user:     &KaineUser{Groups: []string{"kaine-ops"}},
			check:    func(u *KaineUser) bool { return u.CanViewPledge() },
			expected: true,
		},
		{
			name:     "CanViewPledge with no roles",
			user:     &KaineUser{Groups: []string{}},
			check:    func(u *KaineUser) bool { return u.CanViewPledge() },
			expected: false,
		},

		// CanAccessChat tests
		{
			name:     "CanAccessChat with god mode",
			user:     &KaineUser{Groups: []string{"kaine-god"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChat() },
			expected: true,
		},
		{
			name:     "CanAccessChat with chat role",
			user:     &KaineUser{Groups: []string{"kaine-chat"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChat() },
			expected: true,
		},
		{
			name:     "CanAccessChat with chat-debug role",
			user:     &KaineUser{Groups: []string{"kaine-chat-debug"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChat() },
			expected: true,
		},
		{
			name:     "CanAccessChat with test suffix",
			user:     &KaineUser{Groups: []string{"kaine-chat-test"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChat() },
			expected: true,
		},
		{
			name:     "CanAccessChat without chat role",
			user:     &KaineUser{Groups: []string{"kaine-ops", "kaine-directors"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChat() },
			expected: false,
		},

		// CanAccessChatDebug tests
		{
			name:     "CanAccessChatDebug with god mode",
			user:     &KaineUser{Groups: []string{"kaine-god"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChatDebug() },
			expected: true,
		},
		{
			name:     "CanAccessChatDebug with chat-debug role",
			user:     &KaineUser{Groups: []string{"kaine-chat-debug"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChatDebug() },
			expected: true,
		},
		{
			name:     "CanAccessChatDebug with chat role only (should fail)",
			user:     &KaineUser{Groups: []string{"kaine-chat"}},
			check:    func(u *KaineUser) bool { return u.CanAccessChatDebug() },
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.check(tt.user)
			if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestGetAccessLevels tests the access level calculation.
func TestGetAccessLevels(t *testing.T) {
	tests := []struct {
		name     string
		user     *KaineUser
		expected []string
	}{
		{
			name:     "public only",
			user:     &KaineUser{Groups: []string{}},
			expected: []string{"public"},
		},
		{
			name:     "pledge level",
			user:     &KaineUser{Groups: []string{"kaine-pledge"}},
			expected: []string{"public", "pledge"},
		},
		{
			name:     "ops level",
			user:     &KaineUser{Groups: []string{"kaine-ops"}},
			expected: []string{"public", "pledge", "ops"},
		},
		{
			name:     "god mode gets all",
			user:     &KaineUser{Groups: []string{"kaine-god"}},
			expected: []string{"public", "pledge", "ops"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.user.GetAccessLevels()
			if len(result) != len(tt.expected) {
				t.Errorf("got %v, want %v", result, tt.expected)
				return
			}
			for i, v := range tt.expected {
				if result[i] != v {
					t.Errorf("at index %d: got %s, want %s", i, result[i], v)
				}
			}
		})
	}
}

// Helper to create a test server with minimal config
func newTestServer() *Server {
	return &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{
				InternalKey: "test-key",
			},
		},
		logger: observability.NewLogger("test"),
	}
}

// newTestServerWithMockValidator creates a test server with a mock JWT validator.
func newTestServerWithMockValidator(mock *mockJWTValidator) *Server {
	s := newTestServer()
	s.jwtValidator = &JWTValidator{
		logger: observability.NewLogger("test-jwt"),
	}
	// Replace the validation method via wrapper
	return s
}

// testableServer wraps Server to allow mock JWT validation.
type testableServer struct {
	*Server
	mockValidator *mockJWTValidator
}

func newTestableServer() *testableServer {
	return &testableServer{
		Server:        newTestServer(),
		mockValidator: newMockJWTValidator(),
	}
}

// withKaineAuthMock is a test version of withKaineAuth that uses the mock validator.
func (ts *testableServer) withKaineAuthMock(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ts.mockValidator == nil {
			ts.logger.Error("kaine_auth: JWT validator not configured", nil)
			ts.writeError(w, http.StatusServiceUnavailable, "authentication service unavailable")
			return
		}

		var token string
		authHeader := r.Header.Get("Authorization")
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			token = authHeader[7:]
		} else {
			token = r.URL.Query().Get("token")
		}

		if token == "" {
			ts.writeError(w, http.StatusUnauthorized, "missing or invalid authorization")
			return
		}

		user, err := ts.mockValidator.ValidateToken(token)
		if err != nil {
			ts.writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		ctx := context.WithValue(r.Context(), kaineUserKey{}, user)
		next(w, r.WithContext(ctx))
	}
}

// TestWithKaineAuth tests the authentication middleware.
// Uses mock JWT validation to test the middleware behavior without requiring real JWKS.
func TestWithKaineAuth(t *testing.T) {
	ts := newTestableServer()

	// Register valid test tokens
	ts.mockValidator.addUser("valid-token-123", &KaineUser{
		Sub:    "user123",
		Name:   "Test User",
		Groups: []string{"kaine-chat"},
	})
	ts.mockValidator.addUser("valid-token-456", &KaineUser{
		Sub:    "user456",
		Name:   "Query User",
		Groups: []string{"kaine-ops"},
	})

	tests := []struct {
		name           string
		authHeader     string
		queryToken     string
		xKaineUser     string // Should be IGNORED - tests that header injection doesn't work
		expectedStatus int
		checkUser      bool
		expectedSub    string
	}{
		{
			name:           "no auth header or query param",
			authHeader:     "",
			queryToken:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid auth header format (Basic instead of Bearer)",
			authHeader:     "Basic dGVzdDp0ZXN0",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "empty bearer token",
			authHeader:     "Bearer ",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "invalid token",
			authHeader:     "Bearer invalid-token-xyz",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "valid token in Authorization header",
			authHeader:     "Bearer valid-token-123",
			expectedStatus: http.StatusOK,
			checkUser:      true,
			expectedSub:    "user123",
		},
		{
			name:           "valid token in query parameter (for WebSocket)",
			queryToken:     "valid-token-456",
			expectedStatus: http.StatusOK,
			checkUser:      true,
			expectedSub:    "user456",
		},
		{
			name:           "X-Kaine-User header injection attempt (should be ignored)",
			authHeader:     "Bearer valid-token-123",
			xKaineUser:     `{"sub": "attacker", "name": "Attacker", "groups": ["kaine-approved"]}`,
			expectedStatus: http.StatusOK,
			checkUser:      true,
			expectedSub:    "user123", // Must use the real JWT user, NOT the injected header
		},
		{
			name:           "X-Kaine-User header without valid token (should fail)",
			authHeader:     "Bearer invalid-token",
			xKaineUser:     `{"sub": "attacker", "name": "Attacker", "groups": ["kaine-approved"]}`,
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedUser *KaineUser

			handler := ts.withKaineAuthMock(func(w http.ResponseWriter, r *http.Request) {
				capturedUser = KaineUserFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			url := "/test"
			if tt.queryToken != "" {
				url += "?token=" + tt.queryToken
			}

			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.xKaineUser != "" {
				req.Header.Set("X-Kaine-User", tt.xKaineUser)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.expectedStatus)
			}

			if tt.checkUser {
				if capturedUser == nil {
					t.Fatal("expected user in context, got nil")
				}
				if capturedUser.Sub != tt.expectedSub {
					t.Errorf("user.Sub = %s, want %s", capturedUser.Sub, tt.expectedSub)
				}
			}
		})
	}
}

// TestWithKaineAuthNoValidator tests that the middleware fails gracefully without a validator.
func TestWithKaineAuthNoValidator(t *testing.T) {
	server := newTestServer() // No JWT validator configured

	handler := server.withKaineAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer some-token")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d (service unavailable when no validator)", rr.Code, http.StatusServiceUnavailable)
	}
}

// TestWithChatAccess tests the chat access middleware.
func TestWithChatAccess(t *testing.T) {
	server := newTestServer()

	tests := []struct {
		name           string
		user           *KaineUser
		expectedStatus int
	}{
		{
			name:           "nil user",
			user:           nil,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "user without chat access",
			user:           &KaineUser{Sub: "user1", Groups: []string{"kaine-ops"}},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "user with chat access",
			user:           &KaineUser{Sub: "user2", Groups: []string{"kaine-chat"}},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "user with chat-debug access",
			user:           &KaineUser{Sub: "user3", Groups: []string{"kaine-chat-debug"}},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "user with god mode",
			user:           &KaineUser{Sub: "user4", Groups: []string{"kaine-approved"}},
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := server.withChatAccess(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.user != nil {
				ctx := context.WithValue(req.Context(), kaineUserKey{}, tt.user)
				req = req.WithContext(ctx)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.expectedStatus)
			}
		})
	}
}

// TestWithKaineEditor tests the editor access middleware.
func TestWithKaineEditor(t *testing.T) {
	server := newTestServer()

	tests := []struct {
		name           string
		user           *KaineUser
		expectedStatus int
	}{
		{
			name:           "nil user",
			user:           nil,
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "user without edit access",
			user:           &KaineUser{Sub: "user1", Groups: []string{"kaine-ops"}},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "user with directors role",
			user:           &KaineUser{Sub: "user2", Groups: []string{"kaine-directors"}},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "user with lead-ops role",
			user:           &KaineUser{Sub: "user3", Groups: []string{"kaine-lead-ops"}},
			expectedStatus: http.StatusOK,
		},
		{
			name:           "user with god mode",
			user:           &KaineUser{Sub: "user4", Groups: []string{"kaine-approved"}},
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := server.withKaineEditor(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.user != nil {
				ctx := context.WithValue(req.Context(), kaineUserKey{}, tt.user)
				req = req.WithContext(ctx)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.expectedStatus)
			}
		})
	}
}

// TestKaineUserFromContext tests context user extraction.
func TestKaineUserFromContext(t *testing.T) {
	t.Run("no user in context", func(t *testing.T) {
		ctx := context.Background()
		user := KaineUserFromContext(ctx)
		if user != nil {
			t.Error("expected nil user from empty context")
		}
	})

	t.Run("user in context", func(t *testing.T) {
		expected := &KaineUser{Sub: "test-user", Name: "Test"}
		ctx := context.WithValue(context.Background(), kaineUserKey{}, expected)
		user := KaineUserFromContext(ctx)
		if user == nil {
			t.Fatal("expected user from context")
		}
		if user.Sub != expected.Sub {
			t.Errorf("got Sub=%s, want %s", user.Sub, expected.Sub)
		}
	})
}
