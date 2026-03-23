package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
)

// mockValidator implements JWT validation for tests.
type mockValidator struct {
	validTokens map[string]*KaineUser
}

func (m *mockValidator) ValidateToken(token string) (*KaineUser, error) {
	if user, ok := m.validTokens[token]; ok {
		return user, nil
	}
	return nil, errors.New("invalid token")
}

func (m *mockValidator) Close() {}

// newServerWithMockValidator creates a test server with a mock JWT validator.
func newServerWithMockValidator(tokens map[string]*KaineUser) *Server {
	mock := &mockValidator{validTokens: tokens}
	return &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{
				InternalKey: "test-key",
			},
		},
		logger:       observability.NewLogger("test"),
		jwtValidator: mock,
	}
}

// TestKaineRoutesRequireAuth verifies that all Kaine routes require authentication.
// This is a critical security test - no Kaine route should be accessible without auth.
func TestKaineRoutesRequireAuth(t *testing.T) {
	// Create server with a validator that has NO valid tokens
	// This way requests without tokens will be rejected with 401
	server := newServerWithMockValidator(map[string]*KaineUser{})

	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// All Kaine routes that should require authentication
	protectedRoutes := []struct {
		method string
		path   string
		name   string
	}{
		{http.MethodGet, "/api/kaine/objectives", "list objectives"},
		{http.MethodGet, "/api/kaine/objectives/test-id", "get objective by ID"},
		{http.MethodPut, "/api/kaine/objectives/test-id", "update objective"},
		{http.MethodDelete, "/api/kaine/objectives/test-id", "delete objective"},
		{http.MethodPost, "/api/kaine/objectives/create", "create objective"},
		{http.MethodGet, "/api/kaine/systems/search?q=Sol", "search systems"},
		{http.MethodGet, "/api/kaine/systems/Sol", "get system details"},
		{http.MethodGet, "/api/kaine/chat/ws", "chat WebSocket"},
	}

	for _, route := range protectedRoutes {
		t.Run(route.name, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			// No Authorization header - should be rejected
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got status %d, want %d (unauthorized)",
					route.method, route.path, rr.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestKaineRoutesWithAuth verifies routes respond correctly with valid auth.
func TestKaineRoutesWithAuth(t *testing.T) {
	// Use a fixed token that the mock validator will accept
	const validToken = "valid-test-token"
	server := newServerWithMockValidator(map[string]*KaineUser{
		validToken: {
			Sub:    "test-user",
			Name:   "Test User",
			Groups: []string{"kaine-approved"}, // God mode = all permissions
		},
	})
	// Note: kaineStore is nil, so most routes will return 503 or empty data

	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Use the fixed token
	token := validToken

	tests := []struct {
		method       string
		path         string
		name         string
		expectedCode int // Expected code with auth but no backend services
	}{
		// These should return 200 with empty/demo data when store is nil
		{http.MethodGet, "/api/kaine/objectives", "list objectives", http.StatusOK},

		// These require the kaineStore to be configured
		{http.MethodGet, "/api/kaine/objectives/test-id", "get objective by ID", http.StatusServiceUnavailable},
		{http.MethodPost, "/api/kaine/objectives/create", "create objective", http.StatusServiceUnavailable},

		// These require memgraph to be configured
		{http.MethodGet, "/api/kaine/systems/search?q=Sol", "search systems", http.StatusServiceUnavailable},
		{http.MethodGet, "/api/kaine/systems/Sol", "get system details", http.StatusServiceUnavailable},

		// Chat requires LLM runner
		{http.MethodGet, "/api/kaine/chat/ws", "chat WebSocket", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tt.expectedCode {
				t.Errorf("%s %s: got status %d, want %d",
					tt.method, tt.path, rr.Code, tt.expectedCode)
			}
		})
	}
}

// TestChatRouteRequiresChatAccess verifies the chat route requires specific chat access.
func TestChatRouteRequiresChatAccess(t *testing.T) {
	tests := []struct {
		name         string
		groups       []string
		token        string
		expectedCode int
	}{
		{
			name:         "ops user cannot access chat",
			groups:       []string{"kaine-ops", "kaine-directors"},
			token:        "ops-user-token",
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "pledge user cannot access chat",
			groups:       []string{"kaine-pledge"},
			token:        "pledge-user-token",
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "chat user can access (but 503 due to no LLM)",
			groups:       []string{"kaine-chat"},
			token:        "chat-user-token",
			expectedCode: http.StatusServiceUnavailable,
		},
		{
			name:         "chat-debug user can access (but 503 due to no LLM)",
			groups:       []string{"kaine-chat-debug"},
			token:        "chat-debug-token",
			expectedCode: http.StatusServiceUnavailable,
		},
		{
			name:         "god mode user can access (but 503 due to no LLM)",
			groups:       []string{"kaine-approved"},
			token:        "god-mode-token",
			expectedCode: http.StatusServiceUnavailable,
		},
	}

	// Build mock validator with all test tokens
	tokens := make(map[string]*KaineUser)
	for _, tt := range tests {
		tokens[tt.token] = &KaineUser{
			Sub:    "test-user",
			Groups: tt.groups,
		}
	}
	server := newServerWithMockValidator(tokens)

	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tt.expectedCode {
				t.Errorf("groups=%v: got status %d, want %d",
					tt.groups, rr.Code, tt.expectedCode)
			}
		})
	}
}

// TestEditorRoutesRequireEditorRole verifies editor routes require proper permissions.
func TestEditorRoutesRequireEditorRole(t *testing.T) {
	tests := []struct {
		name         string
		groups       []string
		token        string
		expectedCode int
	}{
		{
			name:         "ops user cannot create objectives",
			groups:       []string{"kaine-ops"},
			token:        "ops-token",
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "pledge user cannot create objectives",
			groups:       []string{"kaine-pledge"},
			token:        "pledge-token",
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "directors can create (but 503 due to no store)",
			groups:       []string{"kaine-directors"},
			token:        "directors-token",
			expectedCode: http.StatusServiceUnavailable,
		},
		{
			name:         "lead-ops can create (but 503 due to no store)",
			groups:       []string{"kaine-lead-ops"},
			token:        "lead-ops-token",
			expectedCode: http.StatusServiceUnavailable,
		},
		{
			name:         "god mode can create (but 503 due to no store)",
			groups:       []string{"kaine-approved"},
			token:        "god-mode-token",
			expectedCode: http.StatusServiceUnavailable,
		},
	}

	// Build mock validator with all test tokens
	tokens := make(map[string]*KaineUser)
	for _, tt := range tests {
		tokens[tt.token] = &KaineUser{
			Sub:    "test-user",
			Groups: tt.groups,
		}
	}
	server := newServerWithMockValidator(tokens)

	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Routes that require editor access
	editorRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/kaine/objectives/create"},
	}

	for _, route := range editorRoutes {
		for _, tt := range tests {
			testName := route.path + "/" + tt.name
			t.Run(testName, func(t *testing.T) {
				req := httptest.NewRequest(route.method, route.path, nil)
				req.Header.Set("Authorization", "Bearer "+tt.token)
				req.Header.Set("Content-Type", "application/json")
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)

				if rr.Code != tt.expectedCode {
					t.Errorf("%s %s groups=%v: got status %d, want %d",
						route.method, route.path, tt.groups, rr.Code, tt.expectedCode)
				}
			})
		}
	}
}

// TestKaineRouteRegistration verifies all expected routes are registered.
func TestKaineRouteRegistration(t *testing.T) {
	server := &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{
				InternalKey: "test-key",
			},
		},
		logger: observability.NewLogger("test"),
	}

	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Token for authenticated requests
	token := createTestJWT(map[string]interface{}{
		"sub":    "test-user",
		"groups": []string{"kaine-approved"},
	})

	// Expected routes (we check they don't return 404)
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/kaine/objectives"},
		{http.MethodGet, "/api/kaine/objectives/any-id"},
		{http.MethodPost, "/api/kaine/objectives/create"},
		{http.MethodGet, "/api/kaine/systems/search"},
		{http.MethodGet, "/api/kaine/systems/any-name"},
		{http.MethodGet, "/api/kaine/chat/ws"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Should not be 404 (route not found)
			if rr.Code == http.StatusNotFound {
				t.Errorf("route %s %s not registered (got 404)", route.method, route.path)
			}
		})
	}
}
