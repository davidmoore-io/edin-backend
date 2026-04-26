package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edin-space/edin-backend/internal/authentik"
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
			KaineAuth: config.KaineAuthConfig{
				CookieName: "kaine_session",
				CookiePath: "/api/kaine",
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
// Mirrors the real withKaineAuth: Authorization header > kaine_session cookie, no query string.
func (ts *testableServer) withKaineAuthMock(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ts.mockValidator == nil {
			ts.logger.Error("kaine_auth: JWT validator not configured", nil)
			ts.writeError(w, http.StatusServiceUnavailable, "authentication service unavailable")
			return
		}

		var token string
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		} else if cookie, err := r.Cookie("kaine_session"); err == nil {
			token = cookie.Value
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

		// Mirror withKaineAuth's bot:edin handling so tests cover the same path.
		if user != nil && hasGroup(user.Groups, botEdinGroup) {
			if r.Method != http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/kaine/") {
				ts.logger.Warn(fmt.Sprintf("m2m write rejected: sub=%s method=%s path=%s",
					user.Sub, r.Method, r.URL.Path))
				ts.writeError(w, http.StatusForbidden, "bot:edin identities are read-only on /api/kaine/")
				return
			}
			ts.logger.Info(fmt.Sprintf("m2m call: sub=%s method=%s path=%s",
				user.Sub, r.Method, r.URL.Path))
			ctx := context.WithValue(r.Context(), kaineUserKey{}, user)
			ctx = context.WithValue(ctx, kaineBotIdentityKey{}, user.Sub)
			next(w, r.WithContext(ctx))
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
	ts.mockValidator.addUser("valid-token-cookie", &KaineUser{
		Sub:    "user456",
		Name:   "Cookie User",
		Groups: []string{"kaine-ops"},
	})

	tests := []struct {
		name           string
		authHeader     string
		cookie         string
		xKaineUser     string // Should be IGNORED - tests that header injection doesn't work
		expectedStatus int
		checkUser      bool
		expectedSub    string
	}{
		{
			name:           "no auth header or cookie",
			authHeader:     "",
			cookie:         "",
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
			name:           "valid token in kaine_session cookie",
			cookie:         "kaine_session=valid-token-cookie",
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

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.cookie != "" {
				req.Header.Set("Cookie", tt.cookie)
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

// newTestServerForHandlers creates a test server with mock validator and nonce store for handler tests.
func newTestServerForHandlers(mock *mockJWTValidator) *Server {
	s := &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{
				InternalKey: "test-key",
			},
			KaineAuth: config.KaineAuthConfig{
				CookieName:   "kaine_session",
				CookiePath:   "/api/kaine",
				CookieMaxAge: 3600,
				CookieSecure: false,
				ClientID:     "kaine-portal",
			},
		},
		logger:       observability.NewLogger("test"),
		jwtValidator: mock,
		nonceStore:   newKaineNonceStore(),
	}
	return s
}

// TestKaineToken_ValidCookie_WithCsrfHeader_ReturnsSingleUseNonce verifies the happy path.
func TestKaineToken_ValidCookie_WithCsrfHeader_ReturnsSingleUseNonce(t *testing.T) {
	mock := newMockJWTValidator()
	user := &KaineUser{Sub: "user123", Groups: []string{"kaine-chat"}}
	mock.addUser("valid-jwt", user)

	s := newTestServerForHandlers(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/token", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	req.Header.Set("Cookie", "kaine_session=valid-jwt")
	rr := httptest.NewRecorder()

	s.handleKaineToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	nonce, ok := resp["nonce"].(string)
	if !ok || nonce == "" {
		t.Errorf("expected non-empty nonce in response, got: %v", resp)
	}

	expiresIn, ok := resp["expires_in"].(float64)
	if !ok || expiresIn != 10 {
		t.Errorf("expected expires_in=10, got: %v", resp["expires_in"])
	}

	// Nonce must be usable exactly once
	if got := s.nonceStore.Consume(nonce); got == nil {
		t.Error("nonce was not stored or already consumed")
	}
	if got := s.nonceStore.Consume(nonce); got != nil {
		t.Error("nonce was reusable — expected single-use")
	}
}

// TestKaineToken_ValidCookie_MissingCsrfHeader_Returns400 verifies CSRF
// guard works. Task 8 refactored handleKaineToken to use the shared
// requireFetchHeader helper which returns 400 (was 403); the request
// is well-formed but missing required metadata, so 400 is the right
// signal to the client.
func TestKaineToken_ValidCookie_MissingCsrfHeader_Returns400(t *testing.T) {
	mock := newMockJWTValidator()
	mock.addUser("valid-jwt", &KaineUser{Sub: "user123"})

	s := newTestServerForHandlers(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/token", nil)
	// Intentionally no X-Edin-Fetch header
	req.Header.Set("Cookie", "kaine_session=valid-jwt")
	rr := httptest.NewRecorder()

	s.handleKaineToken(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// TestKaineToken_NoCookie_Returns401 verifies missing cookie returns 401.
func TestKaineToken_NoCookie_Returns401(t *testing.T) {
	mock := newMockJWTValidator()
	s := newTestServerForHandlers(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/token", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	// No cookie
	rr := httptest.NewRecorder()

	s.handleKaineToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// TestKaineToken_InvalidJWTInCookie_Returns401 verifies invalid JWT in cookie returns 401.
func TestKaineToken_InvalidJWTInCookie_Returns401(t *testing.T) {
	mock := newMockJWTValidator()
	// mock has no tokens — all JWTs are invalid
	s := newTestServerForHandlers(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/token", nil)
	req.Header.Set("X-Edin-Fetch", "1")
	req.Header.Set("Cookie", "kaine_session=invalid-jwt-value")
	rr := httptest.NewRecorder()

	s.handleKaineToken(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// TestKaineAuthExchange_SetsHttpOnlyCookie_SameSiteLax verifies the cookie is set correctly.
func TestKaineAuthExchange_SetsHttpOnlyCookie_SameSiteLax(t *testing.T) {
	// Mock Authentik token endpoint
	idToken := "valid-id-token"
	authentikServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"id_token": idToken})
	}))
	defer authentikServer.Close()

	mock := newMockJWTValidator()
	mock.addUser(idToken, &KaineUser{Sub: "user123", Name: "Test User", Groups: []string{"kaine-chat"}})

	s := newTestServerForHandlers(mock)
	s.cfg.KaineAuth.TokenURL = authentikServer.URL

	body := `{"code":"auth-code-123","redirect_uri":"https://example.com/callback"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/auth/exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleKaineAuthExchange(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Check cookie is set
	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "kaine_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected kaine_session cookie, not found")
	}
	if !sessionCookie.HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite = %v, want Lax", sessionCookie.SameSite)
	}
	if sessionCookie.MaxAge != 3600 {
		t.Errorf("cookie MaxAge = %d, want 3600", sessionCookie.MaxAge)
	}
	if sessionCookie.Value != idToken {
		t.Errorf("cookie Value = %s, want %s", sessionCookie.Value, idToken)
	}
}

// TestKaineAuthExchange_DoesNotReturnTokenInBody verifies no token is leaked in the response body.
func TestKaineAuthExchange_DoesNotReturnTokenInBody(t *testing.T) {
	idToken := "secret-id-token"
	authentikServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"id_token": idToken})
	}))
	defer authentikServer.Close()

	mock := newMockJWTValidator()
	mock.addUser(idToken, &KaineUser{Sub: "user123", Groups: []string{"kaine-chat"}})

	s := newTestServerForHandlers(mock)
	s.cfg.KaineAuth.TokenURL = authentikServer.URL

	body := `{"code":"code","redirect_uri":"https://example.com/callback"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/auth/exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	s.handleKaineAuthExchange(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	responseBody := rr.Body.String()
	if strings.Contains(responseBody, idToken) {
		t.Errorf("response body contains id_token — must not expose token: %s", responseBody)
	}
	if strings.Contains(responseBody, "id_token") {
		t.Errorf("response body contains 'id_token' key — must not expose token: %s", responseBody)
	}

	// Body must contain user info
	var resp map[string]any
	if err := json.Unmarshal([]byte(responseBody), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if _, ok := resp["user"]; !ok {
		t.Error("response body should contain 'user' key")
	}
}

// TestKaineAuthLogout_ClearsCookie verifies logout clears the session cookie.
func TestKaineAuthLogout_ClearsCookie(t *testing.T) {
	s := newTestServerForHandlers(newMockJWTValidator())

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/auth/logout", nil)
	rr := httptest.NewRecorder()

	s.handleKaineAuthLogout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	cookies := rr.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "kaine_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected kaine_session cookie in response (to clear it)")
	}
	if sessionCookie.MaxAge != -1 {
		t.Errorf("cookie MaxAge = %d, want -1 (clear cookie)", sessionCookie.MaxAge)
	}
}

// TestKaineAuthMe_ValidCookie_ReturnsUser verifies /api/kaine/auth/me returns user info.
func TestKaineAuthMe_ValidCookie_ReturnsUser(t *testing.T) {
	mock := newMockJWTValidator()
	mock.addUser("valid-jwt", &KaineUser{Sub: "user123", Name: "Test", Email: "test@example.com", Groups: []string{"kaine-chat"}})

	s := newTestServerForHandlers(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/auth/me", nil)
	req.Header.Set("Cookie", "kaine_session=valid-jwt")
	rr := httptest.NewRecorder()

	s.handleKaineAuthMe(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	user, ok := resp["user"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'user' object in response, got: %v", resp)
	}
	if user["sub"] != "user123" {
		t.Errorf("user.sub = %v, want user123", user["sub"])
	}
}

// TestKaineAuthMe_NoCookie_Returns401 verifies missing cookie returns 401.
func TestKaineAuthMe_NoCookie_Returns401(t *testing.T) {
	s := newTestServerForHandlers(newMockJWTValidator())

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/auth/me", nil)
	rr := httptest.NewRecorder()

	s.handleKaineAuthMe(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// ─── Task 8: regression guards on existing /admin/users/{id} endpoint ────────

// stubAuthentikServerForUserGroups returns an httptest.Server that
// satisfies the minimal API surface AddUserToGroup / RemoveUserFromGroup
// exercise (GetGroupByName, GetUser by PK, PATCH user). It returns
// canned responses so the handler can reach the prefix-validation path
// AND, when a valid prefix is supplied, complete without 5xx.
func stubAuthentikServerForUserGroups(t *testing.T) *httptest.Server {
	t.Helper()
	groupPK := "00000000-0000-0000-0000-000000000001"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3/core/groups/"):
			// GetGroupByName — return one group.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"pk":"` + groupPK + `","name":"x","is_superuser":false}]}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3/core/users/"):
			// GetUser by PK — return a user with no current groups.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pk":7,"uuid":"` + groupPK + `","username":"u","name":"U","email":"u@example.com","is_active":true,"type":"external","groups_obj":[]}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v3/core/users/"):
			// Patch user groups — return 200.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newAdminUsersTestServer(t *testing.T, fakeAuthentik *httptest.Server) *Server {
	t.Helper()
	srv := newTestServerForHandlers(newMockJWTValidator())
	srv.authentikClient = authentik.NewClient(fakeAuthentik.URL, "test-token")
	return srv
}

// TestKaineAdminUserByID_AddEdinGroup_Allowed exercises the Task 8
// extension: the user-group endpoint accepts edin-* groups in addition
// to kaine-*.
func TestKaineAdminUserByID_AddEdinGroup_Allowed(t *testing.T) {
	az := stubAuthentikServerForUserGroups(t)
	defer az.Close()
	srv := newAdminUsersTestServer(t, az)

	body := strings.NewReader(`{"group":"edin-copilot"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/users/7", body)
	req.Header.Set("X-Edin-Fetch", "1")
	rr := httptest.NewRecorder()
	srv.handleKaineAdminUserByID(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestKaineAdminUserByID_RemoveEdinGroup_Allowed exercises the same
// extension for DELETE.
func TestKaineAdminUserByID_RemoveEdinGroup_Allowed(t *testing.T) {
	az := stubAuthentikServerForUserGroups(t)
	defer az.Close()
	srv := newAdminUsersTestServer(t, az)

	body := strings.NewReader(`{"group":"edin-copilot"}`)
	req := httptest.NewRequest(http.MethodDelete, "/api/kaine/admin/users/7", body)
	req.Header.Set("X-Edin-Fetch", "1")
	rr := httptest.NewRecorder()
	srv.handleKaineAdminUserByID(rr, req)

	// 200 on success, or 200 on no-op (user not in group). Either is
	// acceptable for this prefix-acceptance check.
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestKaineAdminUserByID_AddArbitraryGroup_StillRejected is the
// regression guard: only kaine-* / edin-* groups are accepted. A
// "wheel" or "admin" group must still be rejected with 400.
func TestKaineAdminUserByID_AddArbitraryGroup_StillRejected(t *testing.T) {
	az := stubAuthentikServerForUserGroups(t)
	defer az.Close()
	srv := newAdminUsersTestServer(t, az)

	body := strings.NewReader(`{"group":"wheel"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/users/7", body)
	req.Header.Set("X-Edin-Fetch", "1")
	rr := httptest.NewRecorder()
	srv.handleKaineAdminUserByID(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "kaine-") {
		t.Errorf("expected error to mention valid prefixes, got: %s", rr.Body.String())
	}
}

// TestKaineAdminUserByID_AddGroup_MissingFetchHeader_400 verifies the
// X-Edin-Fetch CSRF guard added in Task 8.
func TestKaineAdminUserByID_AddGroup_MissingFetchHeader_400(t *testing.T) {
	az := stubAuthentikServerForUserGroups(t)
	defer az.Close()
	srv := newAdminUsersTestServer(t, az)

	body := strings.NewReader(`{"group":"kaine-chat"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/kaine/admin/users/7", body)
	// No X-Edin-Fetch header.
	rr := httptest.NewRecorder()
	srv.handleKaineAdminUserByID(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// =============================================================================
// bot:edin service identity tests (Phase 4 of edin-bot plan)
// =============================================================================

// botEdinFixture wires a testable server with the mock validator + a bot:edin
// user mapped to a fixed token, and a director user for regression baseline.
type botEdinFixture struct {
	ts             *testableServer
	botToken       string
	directorToken  string
}

func newBotEdinFixture(t *testing.T) *botEdinFixture {
	t.Helper()
	ts := newTestableServer()
	ts.mockValidator.addUser("bot-token", &KaineUser{
		Sub:    "svc-edin-bot",
		Groups: []string{botEdinGroup},
	})
	ts.mockValidator.addUser("director-token", &KaineUser{
		Sub:    "user-director",
		Groups: []string{"kaine-directors"},
	})
	return &botEdinFixture{ts: ts, botToken: "bot-token", directorToken: "director-token"}
}

// fixedHandler returns a handler that records whether it was reached.
func fixedHandler() (http.HandlerFunc, *bool) {
	reached := false
	return func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}, &reached
}

func TestWithKaineAuth_BotEdin_AllowsGET(t *testing.T) {
	f := newBotEdinFixture(t)
	h, reached := fixedHandler()
	wrapped := f.ts.withKaineAuthMock(f.ts.withKaineEditor(h))

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/mining/plasmium-buyers", nil)
	req.Header.Set("Authorization", "Bearer "+f.botToken)
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !*reached {
		t.Error("handler must be invoked for bot:edin GET")
	}
}

func TestWithKaineAuth_BotEdin_RejectsPOST(t *testing.T) {
	f := newBotEdinFixture(t)
	h, reached := fixedHandler()
	wrapped := f.ts.withKaineAuthMock(h)

	req := httptest.NewRequest(http.MethodPost, "/api/kaine/objectives", nil)
	req.Header.Set("Authorization", "Bearer "+f.botToken)
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for bot:edin POST, got %d", rr.Code)
	}
	if *reached {
		t.Error("handler MUST NOT be invoked for bot:edin POST")
	}
}

func TestWithKaineAuth_BotEdin_RejectsPUT(t *testing.T) {
	f := newBotEdinFixture(t)
	h, _ := fixedHandler()
	wrapped := f.ts.withKaineAuthMock(h)

	req := httptest.NewRequest(http.MethodPut, "/api/kaine/objectives/abc", nil)
	req.Header.Set("Authorization", "Bearer "+f.botToken)
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for bot:edin PUT, got %d", rr.Code)
	}
}

func TestWithKaineAuth_BotEdin_RejectsDELETE(t *testing.T) {
	f := newBotEdinFixture(t)
	h, _ := fixedHandler()
	wrapped := f.ts.withKaineAuthMock(h)

	req := httptest.NewRequest(http.MethodDelete, "/api/kaine/objectives/abc", nil)
	req.Header.Set("Authorization", "Bearer "+f.botToken)
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for bot:edin DELETE, got %d", rr.Code)
	}
}

func TestWithKaineAuth_DirectorJWT_StillWorksUnchanged(t *testing.T) {
	f := newBotEdinFixture(t)
	h, reached := fixedHandler()
	wrapped := f.ts.withKaineAuthMock(f.ts.withKaineEditor(h))

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/mining/plasmium-buyers", nil)
	req.Header.Set("Authorization", "Bearer "+f.directorToken)
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("director regression: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !*reached {
		t.Error("director handler must still be invoked")
	}
}

func TestWithKaineEditor_BotEdinBypassesEditorCheck(t *testing.T) {
	// Sanity: even though bot:edin user does NOT satisfy CanEditObjectives,
	// withKaineEditor lets it through because withKaineAuth set the
	// bot-identity context sentinel.
	f := newBotEdinFixture(t)
	h, reached := fixedHandler()
	wrapped := f.ts.withKaineAuthMock(f.ts.withKaineEditor(h))

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/mining/plasmium-buyers", nil)
	req.Header.Set("Authorization", "Bearer "+f.botToken)
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("bot:edin must bypass editor check; got %d", rr.Code)
	}
	if !*reached {
		t.Error("handler must be reached")
	}
}

func TestWithKaineEditor_NonBotNonDirector_StillRejected(t *testing.T) {
	// A user with no editor group AND no bot:edin group is still rejected.
	ts := newTestableServer()
	ts.mockValidator.addUser("plain-token", &KaineUser{
		Sub:    "user-pleb",
		Groups: []string{"kaine-pledge"},
	})
	h, reached := fixedHandler()
	wrapped := ts.withKaineAuthMock(ts.withKaineEditor(h))

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/mining/plasmium-buyers", nil)
	req.Header.Set("Authorization", "Bearer plain-token")
	rr := httptest.NewRecorder()
	wrapped(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("non-bot non-editor: expected 403, got %d", rr.Code)
	}
	if *reached {
		t.Error("handler MUST NOT be invoked")
	}
}

// hasGroup helper coverage.
func TestHasGroup(t *testing.T) {
	cases := []struct {
		groups []string
		want   string
		expect bool
	}{
		{[]string{"a", "b"}, "a", true},
		{[]string{"a", "b"}, "c", false},
		{nil, "a", false},
		{[]string{}, "a", false},
		{[]string{"bot:edin", "kaine-director"}, "bot:edin", true},
		{[]string{"bot:edin-fake"}, "bot:edin", false},
	}
	for _, c := range cases {
		if got := hasGroup(c.groups, c.want); got != c.expect {
			t.Errorf("hasGroup(%v, %q) = %v, want %v", c.groups, c.want, got, c.expect)
		}
	}
}
