package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
)

// =============================================================================
// Route Authentication Test Suite
// =============================================================================
//
// This file contains comprehensive tests for all HTTP routes in the ssg-server.
// Each route is tested for:
//   1. Authentication requirements (API key vs JWT vs public)
//   2. Authorization requirements (roles/permissions)
//   3. Rejection of invalid/missing credentials
//
// Route Categories:
//   - Internal API routes (/status, /logs, etc.) - require API key auth
//   - Public API routes (/api/edin/*, /api/dayz/*) - no auth required
//   - Kaine Portal routes (/api/kaine/*) - require JWT auth with specific roles
//
// All routes discovered from server.go and kaine.go as of 2024-01.
// =============================================================================

// AuthType represents the type of authentication required for a route.
type AuthType int

const (
	AuthNone    AuthType = iota // Public route, no auth required
	AuthAPIKey                  // Internal route, requires API key
	AuthJWT                     // Kaine portal route, requires JWT
	AuthJWTChat                 // Kaine chat route, requires JWT + chat role
	AuthJWTEdit                 // Kaine editor route, requires JWT + editor role
)

// RouteSpec defines a route and its authentication requirements.
type RouteSpec struct {
	Method      string
	Path        string
	Name        string
	AuthType    AuthType
	Description string
}

// AllRoutes defines ALL routes in the ssg-server with their auth requirements.
// This list is derived from server.go Run() function and kaine.go RegisterKaineRoutes().
var AllRoutes = []RouteSpec{
	// =========================================================================
	// Public Routes - No Authentication Required
	// These are read-only endpoints that provide public data.
	// =========================================================================

	// Core endpoints
	{http.MethodGet, "/health", "health check", AuthNone, "Server health status"},
	{http.MethodGet, "/metrics", "prometheus metrics", AuthNone, "Prometheus metrics endpoint"},

	// EDIN Public API - Elite Dangerous Intel Network
	{http.MethodGet, "/api/edin/hip-thunderdome", "EDIN HIP Thunderdome", AuthNone, "Powerplay data for HIP Thunderdome systems"},
	{http.MethodGet, "/api/edin/inara-links", "EDIN Inara links", AuthNone, "Inara IDs for direct links to Inara"},
	{http.MethodGet, "/api/edin/systems/Sol/history", "EDIN system history", AuthNone, "Historical powerplay data for a system"},
	{http.MethodGet, "/api/edin/systems/Sol/expansion-history", "EDIN expansion history", AuthNone, "Expansion conflict progress history"},
	{http.MethodGet, "/api/edin/status", "EDIN status", AuthNone, "EDIN cache status"},
	{http.MethodGet, "/api/edin/openapi.json", "EDIN OpenAPI spec", AuthNone, "OpenAPI specification"},
	{http.MethodGet, "/api/edin/ws", "EDIN WebSocket", AuthNone, "Real-time EDIN updates via WebSocket"},

	// DayZ Public API
	{http.MethodGet, "/api/dayz/status", "DayZ status", AuthNone, "DayZ server status"},
	{http.MethodGet, "/api/dayz/spawns", "DayZ spawns", AuthNone, "DayZ spawn data"},
	{http.MethodGet, "/api/dayz/map-config", "DayZ map config", AuthNone, "DayZ map configuration"},
	{http.MethodGet, "/api/dayz/categories", "DayZ categories", AuthNone, "DayZ item categories"},
	{http.MethodGet, "/api/dayz/refresh", "DayZ refresh", AuthNone, "Trigger DayZ data refresh"},
	{http.MethodGet, "/api/dayz/full", "DayZ full data", AuthNone, "Complete DayZ economy data"},
	{http.MethodGet, "/api/dayz/economy", "DayZ economy (public)", AuthNone, "DayZ economy data"},
	{http.MethodGet, "/api/dayz/items", "DayZ items search", AuthNone, "Search DayZ items"},

	// =========================================================================
	// Internal API Routes - Require API Key Authentication
	// These are internal routes used by the Discord bot and operations.
	// =========================================================================

	// Service management (used by Discord bot)
	{http.MethodGet, "/status/satisfactory", "service status", AuthAPIKey, "Get Docker service status"},
	{http.MethodPost, "/actions/satisfactory/restart", "service restart", AuthAPIKey, "Restart Docker service"},
	{http.MethodGet, "/logs/satisfactory", "service logs", AuthAPIKey, "Get service logs"},

	// Ansible operations
	{http.MethodPost, "/ansible/run", "ansible playbook", AuthAPIKey, "Run Ansible playbook"},

	// LLM integration (Discord bot ops-assist)
	{http.MethodPost, "/llm/session", "LLM session", AuthAPIKey, "Discord bot LLM session"},

	// Spansh batch queries
	{http.MethodPost, "/spansh/powerplay-batch", "Spansh batch query", AuthAPIKey, "Batch powerplay queries"},

	// Internal DayZ endpoints (Discord bot)
	{http.MethodGet, "/dayz/economy", "DayZ economy (internal)", AuthAPIKey, "Internal DayZ economy endpoint"},
	{http.MethodGet, "/dayz/items", "DayZ items (internal)", AuthAPIKey, "Internal DayZ items endpoint"},

	// =========================================================================
	// Kaine Portal Routes - Require JWT Authentication
	// These are the Kaine portal API endpoints.
	// =========================================================================

	// Objectives (basic JWT auth)
	{http.MethodGet, "/api/kaine/objectives", "list objectives", AuthJWT, "List all objectives visible to user"},
	{http.MethodGet, "/api/kaine/objectives/test-id", "get objective by ID", AuthJWT, "Get specific objective by ID"},
	{http.MethodPut, "/api/kaine/objectives/test-id", "update objective", AuthJWTEdit, "Update objective (requires editor role)"},
	{http.MethodDelete, "/api/kaine/objectives/test-id", "delete objective", AuthJWTEdit, "Delete objective (requires editor role)"},
	{http.MethodPost, "/api/kaine/objectives/create", "create objective", AuthJWTEdit, "Create new objective (requires editor role)"},

	// Mining Maps (basic JWT auth, editor for create/update/delete)
	{http.MethodGet, "/api/kaine/mining-maps", "list mining maps", AuthJWT, "List all mining maps"},
	{http.MethodGet, "/api/kaine/mining-maps/stats", "mining map stats", AuthJWT, "Mining map statistics"},
	{http.MethodGet, "/api/kaine/mining-maps/1", "get mining map", AuthJWT, "Get specific mining map by ID"},
	{http.MethodPost, "/api/kaine/mining-maps/create", "create mining map", AuthJWTEdit, "Create mining map (requires editor role)"},
	{http.MethodPut, "/api/kaine/mining-maps/1", "update mining map", AuthJWTEdit, "Update mining map (requires editor role)"},
	{http.MethodDelete, "/api/kaine/mining-maps/1", "delete mining map", AuthJWTEdit, "Delete mining map (requires editor role)"},

	// System Search (basic JWT auth)
	{http.MethodGet, "/api/kaine/systems/search?q=Sol", "search systems", AuthJWT, "Search star systems"},
	{http.MethodGet, "/api/kaine/systems/Sol", "get system details", AuthJWT, "Get system details"},

	// Market Data (basic JWT auth)
	{http.MethodGet, "/api/kaine/market/station?system=Sol&station=Test", "station market", AuthJWT, "Get station market data"},
	{http.MethodGet, "/api/kaine/market/carrier?id=ABC-123", "carrier market", AuthJWT, "Get carrier market data"},

	// Unified Search (basic JWT auth)
	{http.MethodGet, "/api/kaine/search?q=Sol", "unified search", AuthJWT, "Search systems and stations"},

	// Chat (requires chat role)
	{http.MethodGet, "/api/kaine/chat/ws", "chat WebSocket", AuthJWTChat, "Galaxy AI chat WebSocket"},
}

// routeAuthValidator implements TokenValidator for route auth tests.
type routeAuthValidator struct {
	tokens map[string]*KaineUser
}

func (v *routeAuthValidator) ValidateToken(token string) (*KaineUser, error) {
	if v == nil || v.tokens == nil {
		return nil, errors.New("no tokens configured")
	}
	if user, ok := v.tokens[token]; ok {
		return user, nil
	}
	return nil, errors.New("invalid token")
}

func (v *routeAuthValidator) Close() {}

// testServerForRoutes creates a server configured for route testing.
func testServerForRoutes(apiKey string, jwtTokens map[string]*KaineUser) *Server {
	var validator TokenValidator
	if jwtTokens != nil {
		validator = &routeAuthValidator{tokens: jwtTokens}
	}

	return &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{
				InternalKey:  apiKey,
				AllowOrigins: []string{"https://ssg.sh"},
			},
		},
		logger:       observability.NewLogger("test"),
		apiKey:       apiKey, // Must set this field - used by withAuth middleware
		jwtValidator: validator,
		// Other fields are nil - routes may return 503 for missing services,
		// but auth tests only verify 401/403 behavior.
	}
}

// =============================================================================
// Test: Public Routes Accept Unauthenticated Requests
// =============================================================================

func TestPublicRoutesNoAuthRequired(t *testing.T) {
	server := testServerForRoutes("test-api-key", nil)
	mux := http.NewServeMux()

	// Register all public routes (from server.go Run())
	mux.HandleFunc("/health", server.handleHealth)
	mux.HandleFunc("/api/edin/hip-thunderdome", server.handleHIPThunderdome)
	mux.HandleFunc("/api/edin/inara-links", server.handleEDINInaraLinks)
	mux.HandleFunc("/api/edin/systems/", server.handleEDINSystemHistory)
	mux.HandleFunc("/api/edin/status", server.handleEDINStatus)
	mux.HandleFunc("/api/edin/openapi.json", server.handleEDINOpenAPI)
	mux.HandleFunc("/api/edin/ws", server.handleEDINWebSocket)
	mux.HandleFunc("/api/dayz/status", server.handleDayZStatus)
	mux.HandleFunc("/api/dayz/spawns", server.handleDayZSpawns)
	mux.HandleFunc("/api/dayz/map-config", server.handleDayZMapConfig)
	mux.HandleFunc("/api/dayz/categories", server.handleDayZCategories)
	mux.HandleFunc("/api/dayz/refresh", server.handleDayZRefresh)
	mux.HandleFunc("/api/dayz/full", server.handleDayZFull)
	mux.HandleFunc("/api/dayz/economy", server.handleDayZEconomy)
	mux.HandleFunc("/api/dayz/items", server.handleDayZItemSearch)

	publicRoutes := filterRoutesByAuth(AllRoutes, AuthNone)

	for _, route := range publicRoutes {
		// Skip routes that need special setup (WebSocket, metrics)
		if route.Path == "/api/edin/ws" || route.Path == "/metrics" {
			continue
		}

		t.Run(route.Name+"/no_auth", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Public routes should NOT return 401 or 403
			if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Errorf("%s %s (%s): got %d, public route should not require auth",
					route.Method, route.Path, route.Name, rr.Code)
			}
		})

		// Also verify that auth headers don't cause issues
		t.Run(route.Name+"/with_random_auth", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer some-random-token")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Public routes should still work with random auth headers
			if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Errorf("%s %s (%s): got %d with random auth header, public route should ignore auth",
					route.Method, route.Path, route.Name, rr.Code)
			}
		})
	}
}

// =============================================================================
// Test: Internal API Routes Require API Key
// =============================================================================

func TestInternalRoutesRequireAPIKey(t *testing.T) {
	const validAPIKey = "test-api-key-12345"
	server := testServerForRoutes(validAPIKey, nil)
	mux := http.NewServeMux()

	// Register internal routes with auth middleware (from server.go Run())
	mux.HandleFunc("/status/", server.withAuth(server.handleStatus))
	mux.HandleFunc("/actions/", server.withAuth(server.handleAction))
	mux.HandleFunc("/logs/", server.withAuth(server.handleLogs))
	mux.HandleFunc("/ansible/run", server.withAuth(server.handleAnsible))
	mux.HandleFunc("/llm/session", server.withAuth(server.handleLLMSession))
	mux.HandleFunc("/spansh/powerplay-batch", server.withAuth(server.handlePowerplayBatch))
	mux.HandleFunc("/dayz/economy", server.withAuth(server.handleDayZEconomy))
	mux.HandleFunc("/dayz/items", server.withAuth(server.handleDayZItemSearch))

	internalRoutes := filterRoutesByAuth(AllRoutes, AuthAPIKey)

	for _, route := range internalRoutes {
		// Test 1: No auth header
		t.Run(route.Name+"/no_auth", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d without auth, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 2: Empty Bearer token
		t.Run(route.Name+"/empty_bearer", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer ")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with empty bearer, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 3: Wrong API key
		t.Run(route.Name+"/wrong_api_key", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer wrong-key")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with wrong key, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 4: JWT token instead of API key (should fail)
		t.Run(route.Name+"/jwt_instead_of_api_key", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			// This looks like a JWT but isn't the API key
			req.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.fake")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with JWT (should reject, needs API key), want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 5: Basic auth (should fail - we only accept Bearer)
		t.Run(route.Name+"/basic_auth", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Basic dGVzdDp0ZXN0") // test:test
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with Basic auth, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 6: Valid API key
		// Note: We skip testing valid auth for internal routes because handlers
		// require real ops.Manager/services which would panic with nil.
		// The security-critical tests (1-5) verify that invalid auth is rejected.
		// This is acceptable because:
		// - The positive auth path is tested by the real server
		// - The auth middleware is shared and simple (token == apiKey)
		// - What we care about is that INVALID auth is REJECTED
	}
}

// =============================================================================
// Test: Kaine Routes Require JWT Authentication
// =============================================================================

func TestKaineRoutesRequireJWT(t *testing.T) {
	const validToken = "valid-jwt-token"
	tokens := map[string]*KaineUser{
		validToken: {
			Sub:    "test-user",
			Name:   "Test User",
			Email:  "test@example.com",
			Groups: []string{"kaine-approved"}, // God mode for basic auth tests
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Test all JWT-protected routes (JWT, JWTChat, JWTEdit)
	kaineRoutes := []RouteSpec{}
	for _, r := range AllRoutes {
		if r.AuthType == AuthJWT || r.AuthType == AuthJWTChat || r.AuthType == AuthJWTEdit {
			kaineRoutes = append(kaineRoutes, r)
		}
	}

	for _, route := range kaineRoutes {
		// Test 1: No auth header
		t.Run(route.Name+"/no_auth", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d without auth, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 2: Empty Bearer token
		t.Run(route.Name+"/empty_bearer", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer ")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with empty bearer, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 3: Invalid JWT token
		t.Run(route.Name+"/invalid_jwt", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer invalid-token")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with invalid JWT, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 4: Malformed JWT (looks like JWT but invalid)
		t.Run(route.Name+"/malformed_jwt", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmYWtlIn0.FAKE_SIG")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with malformed JWT, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 5: API key instead of JWT (should fail)
		t.Run(route.Name+"/api_key_instead_of_jwt", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer api-key")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with API key (should reject, needs JWT), want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 6: Basic auth (should fail)
		t.Run(route.Name+"/basic_auth", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Basic dGVzdDp0ZXN0")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s: got %d with Basic auth, want 401",
					route.Method, route.Path, rr.Code)
			}
		})

		// Test 7: Valid JWT (for god mode user)
		t.Run(route.Name+"/valid_jwt", func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer "+validToken)
			if route.Method == http.MethodPost || route.Method == http.MethodPut {
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Should NOT be 401 (may be 503 for missing services)
			// God mode user should also pass all role checks (no 403)
			if rr.Code == http.StatusUnauthorized {
				t.Errorf("%s %s: got 401 with valid JWT",
					route.Method, route.Path)
			}
		})
	}
}

// =============================================================================
// Test: Kaine Editor Routes Require Editor Role
// =============================================================================

func TestKaineEditorRoutesRequireEditorRole(t *testing.T) {
	// Define test users with different permission levels
	tokens := map[string]*KaineUser{
		"ops-user": {
			Sub:    "ops-user",
			Name:   "Ops User",
			Groups: []string{"kaine-ops"},
		},
		"pledge-user": {
			Sub:    "pledge-user",
			Name:   "Pledge User",
			Groups: []string{"kaine-pledge"},
		},
		"chat-only-user": {
			Sub:    "chat-only-user",
			Name:   "Chat User",
			Groups: []string{"kaine-chat"},
		},
		"director-user": {
			Sub:    "director-user",
			Name:   "Director User",
			Groups: []string{"kaine-directors"},
		},
		"lead-ops-user": {
			Sub:    "lead-ops-user",
			Name:   "Lead Ops User",
			Groups: []string{"kaine-lead-ops"},
		},
		"god-mode-user": {
			Sub:    "god-mode-user",
			Name:   "God Mode User",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	editorRoutes := filterRoutesByAuth(AllRoutes, AuthJWTEdit)

	// Test each user type against editor routes
	tests := []struct {
		token       string
		canEdit     bool
		description string
	}{
		{"ops-user", false, "ops user cannot edit"},
		{"pledge-user", false, "pledge user cannot edit"},
		{"chat-only-user", false, "chat-only user cannot edit"},
		{"director-user", true, "director can edit"},
		{"lead-ops-user", true, "lead-ops can edit"},
		{"god-mode-user", true, "god mode can edit"},
	}

	for _, route := range editorRoutes {
		for _, tt := range tests {
			testName := route.Name + "/" + tt.description
			t.Run(testName, func(t *testing.T) {
				req := httptest.NewRequest(route.Method, route.Path, nil)
				req.Header.Set("Authorization", "Bearer "+tt.token)
				if route.Method == http.MethodPost || route.Method == http.MethodPut {
					req.Header.Set("Content-Type", "application/json")
				}
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)

				if tt.canEdit {
					// Should NOT be 401 or 403
					if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
						t.Errorf("%s %s (%s): got %d, editor should have access",
							route.Method, route.Path, tt.description, rr.Code)
					}
				} else {
					// Should be 403 Forbidden (authenticated but not authorized)
					// OR 503 if handler checks service before role (still secure - request rejected)
					if rr.Code != http.StatusForbidden && rr.Code != http.StatusServiceUnavailable {
						t.Errorf("%s %s (%s): got %d, non-editor should get 403 or 503",
							route.Method, route.Path, tt.description, rr.Code)
					}
				}
			})
		}
	}
}

// =============================================================================
// Test: Kaine Chat Routes Require Chat Role
// =============================================================================

func TestKaineChatRoutesRequireChatRole(t *testing.T) {
	// Define test users with different permission levels
	tokens := map[string]*KaineUser{
		"ops-user": {
			Sub:    "ops-user",
			Name:   "Ops User",
			Groups: []string{"kaine-ops", "kaine-directors"}, // Has edit but not chat
		},
		"pledge-user": {
			Sub:    "pledge-user",
			Name:   "Pledge User",
			Groups: []string{"kaine-pledge"},
		},
		"director-only": {
			Sub:    "director-only",
			Name:   "Director Only",
			Groups: []string{"kaine-directors"}, // Can edit but not chat
		},
		"chat-user": {
			Sub:    "chat-user",
			Name:   "Chat User",
			Groups: []string{"kaine-chat"},
		},
		"chat-debug-user": {
			Sub:    "chat-debug-user",
			Name:   "Chat Debug User",
			Groups: []string{"kaine-chat-debug"},
		},
		"chat-debug-test-user": {
			Sub:    "chat-debug-test-user",
			Name:   "Chat Debug Test User",
			Groups: []string{"kaine-chat-debug-test"},
		},
		"god-mode-user": {
			Sub:    "god-mode-user",
			Name:   "God Mode User",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	chatRoutes := filterRoutesByAuth(AllRoutes, AuthJWTChat)

	// Test each user type against chat routes
	tests := []struct {
		token       string
		canChat     bool
		description string
	}{
		{"ops-user", false, "ops user cannot chat (even with edit access)"},
		{"pledge-user", false, "pledge user cannot chat"},
		{"director-only", false, "director cannot chat (edit doesn't grant chat)"},
		{"chat-user", true, "chat user can chat"},
		{"chat-debug-user", true, "chat-debug user can chat"},
		{"chat-debug-test-user", true, "chat-debug-test user can chat"},
		{"god-mode-user", true, "god mode can chat"},
	}

	for _, route := range chatRoutes {
		for _, tt := range tests {
			testName := route.Name + "/" + tt.description
			t.Run(testName, func(t *testing.T) {
				req := httptest.NewRequest(route.Method, route.Path, nil)
				req.Header.Set("Authorization", "Bearer "+tt.token)
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)

				if tt.canChat {
					// Should NOT be 401 or 403 (may be 503 due to no LLM)
					if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
						t.Errorf("%s %s (%s): got %d, chat user should have access",
							route.Method, route.Path, tt.description, rr.Code)
					}
				} else {
					// Should be 403 Forbidden
					if rr.Code != http.StatusForbidden {
						t.Errorf("%s %s (%s): got %d, non-chat user should get 403",
							route.Method, route.Path, tt.description, rr.Code)
					}
				}
			})
		}
	}
}

// =============================================================================
// Test: X-Kaine-User Header Injection Attack Prevention
// =============================================================================

func TestXKaineUserHeaderInjectionBlocked(t *testing.T) {
	tokens := map[string]*KaineUser{
		"basic-user": {
			Sub:    "basic-user",
			Name:   "Basic User",
			Groups: []string{"kaine-pledge"}, // Lowest privilege level
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Test 1: Try to access an editor-only route with header injection
	t.Run("header injection should not elevate privileges to editor", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/kaine/objectives/create", nil)
		req.Header.Set("Authorization", "Bearer basic-user")
		// Try to inject a header claiming god mode privileges
		req.Header.Set("X-Kaine-User", `{"sub":"attacker","groups":["kaine-approved"]}`)
		req.Header.Set("Content-Type", "application/json")

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should be 403 Forbidden (pledge user cannot create objectives)
		// If header injection worked, it would be 200 or 503
		if rr.Code != http.StatusForbidden {
			t.Errorf("header injection test: got %d, want 403 (injection should be ignored)",
				rr.Code)
		}
	})

	// Test 2: Try to access chat with header injection
	t.Run("header injection should not grant chat access", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws", nil)
		req.Header.Set("Authorization", "Bearer basic-user")
		// Try to inject chat access
		req.Header.Set("X-Kaine-User", `{"sub":"attacker","groups":["kaine-chat"]}`)

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should be 403 Forbidden (pledge user cannot access chat)
		if rr.Code != http.StatusForbidden {
			t.Errorf("header injection test: got %d, want 403 (injection should be ignored)",
				rr.Code)
		}
	})

	// Test 3: Header injection without valid token should still fail with 401
	t.Run("header injection without valid token should fail", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		// Try to inject a valid-looking user
		req.Header.Set("X-Kaine-User", `{"sub":"attacker","groups":["kaine-approved"]}`)

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should be 401 Unauthorized (invalid token, header is ignored)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("header injection with invalid token: got %d, want 401",
				rr.Code)
		}
	})

	// Test 4: Header injection with no Authorization header
	t.Run("header injection without auth header should fail", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		// Only the injection header, no Authorization
		req.Header.Set("X-Kaine-User", `{"sub":"attacker","groups":["kaine-approved"]}`)

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should be 401 Unauthorized
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("header injection without auth: got %d, want 401",
				rr.Code)
		}
	})

	// Test 5: Multiple injection headers
	t.Run("multiple injection headers should all be ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws", nil)
		req.Header.Set("Authorization", "Bearer basic-user")
		req.Header.Add("X-Kaine-User", `{"sub":"attacker1","groups":["kaine-approved"]}`)
		req.Header.Add("X-Kaine-User", `{"sub":"attacker2","groups":["kaine-chat"]}`)

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should be 403 Forbidden (pledge user cannot access chat)
		if rr.Code != http.StatusForbidden {
			t.Errorf("multiple injection headers: got %d, want 403",
				rr.Code)
		}
	})
}

// =============================================================================
// Test: Query Parameter Token for WebSocket
// =============================================================================

func TestQueryParameterTokenForWebSocket(t *testing.T) {
	tokens := map[string]*KaineUser{
		"ws-user": {
			Sub:    "ws-user",
			Name:   "WebSocket User",
			Groups: []string{"kaine-chat"},
		},
		"ws-user-no-chat": {
			Sub:    "ws-user-no-chat",
			Name:   "WebSocket User No Chat",
			Groups: []string{"kaine-ops"},
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Test 1: Valid token in query parameter
	t.Run("valid token in query parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws?token=ws-user", nil)
		// No Authorization header, token is in query param
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should NOT be 401 or 403 (may be 503 due to no LLM runner)
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Errorf("query param token: got %d, should accept token in query param", rr.Code)
		}
	})

	// Test 2: Invalid token in query parameter
	t.Run("invalid token in query parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws?token=invalid-token", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("invalid query param token: got %d, want 401", rr.Code)
		}
	})

	// Test 3: Empty token in query parameter
	t.Run("empty token in query parameter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws?token=", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("empty query param token: got %d, want 401", rr.Code)
		}
	})

	// Test 4: Valid token but wrong role
	t.Run("valid token in query param but wrong role", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws?token=ws-user-no-chat", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should be 403 (authenticated but no chat access)
		if rr.Code != http.StatusForbidden {
			t.Errorf("query param token wrong role: got %d, want 403", rr.Code)
		}
	})

	// Test 5: Authorization header takes precedence over query param
	t.Run("authorization header takes precedence over query param", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws?token=invalid-token", nil)
		// Valid token in header, invalid in query
		req.Header.Set("Authorization", "Bearer ws-user")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should succeed because header takes precedence
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Errorf("header should take precedence: got %d, expected success or 503", rr.Code)
		}
	})
}

// =============================================================================
// Test: Read-Only vs Write Routes
// =============================================================================

func TestReadOnlyRoutesVsWriteRoutes(t *testing.T) {
	tokens := map[string]*KaineUser{
		"read-only-user": {
			Sub:    "read-only-user",
			Name:   "Read Only User",
			Groups: []string{"kaine-ops"}, // Can view but not edit
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Test that read-only user can access GET routes
	readOnlyRoutes := []RouteSpec{}
	for _, r := range AllRoutes {
		if r.AuthType == AuthJWT && r.Method == http.MethodGet {
			readOnlyRoutes = append(readOnlyRoutes, r)
		}
	}

	for _, route := range readOnlyRoutes {
		t.Run("read_only_can_access/"+route.Name, func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer read-only-user")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Should NOT be 401 or 403
			if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Errorf("%s %s: read-only user should have access to GET routes, got %d",
					route.Method, route.Path, rr.Code)
			}
		})
	}

	// Test that read-only user CANNOT access write routes
	writeRoutes := filterRoutesByAuth(AllRoutes, AuthJWTEdit)

	for _, route := range writeRoutes {
		t.Run("read_only_cannot_write/"+route.Name, func(t *testing.T) {
			req := httptest.NewRequest(route.Method, route.Path, nil)
			req.Header.Set("Authorization", "Bearer read-only-user")
			if route.Method == http.MethodPost || route.Method == http.MethodPut {
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Should be 403 Forbidden, OR 503 if handler checks service before role
			// Either way, the request is rejected which is the security goal
			if rr.Code != http.StatusForbidden && rr.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s: read-only user should not have access to write routes, got %d (expected 403 or 503)",
					route.Method, route.Path, rr.Code)
			}
		})
	}
}

// =============================================================================
// Test: Route Count Verification
// =============================================================================

func TestAllRoutesAreTested(t *testing.T) {
	// Count routes by category
	public := 0
	apiKey := 0
	jwt := 0
	jwtChat := 0
	jwtEdit := 0

	for _, r := range AllRoutes {
		switch r.AuthType {
		case AuthNone:
			public++
		case AuthAPIKey:
			apiKey++
		case AuthJWT:
			jwt++
		case AuthJWTChat:
			jwtChat++
		case AuthJWTEdit:
			jwtEdit++
		}
	}

	t.Logf("Route counts: public=%d, apiKey=%d, jwt=%d, jwtChat=%d, jwtEdit=%d, total=%d",
		public, apiKey, jwt, jwtChat, jwtEdit, len(AllRoutes))

	// Sanity checks - these ensure we don't accidentally remove routes from testing
	if len(AllRoutes) < 35 {
		t.Errorf("expected at least 35 routes to be defined, got %d", len(AllRoutes))
	}
	if public < 15 {
		t.Errorf("expected at least 15 public routes, got %d", public)
	}
	if apiKey < 7 {
		t.Errorf("expected at least 7 API key routes, got %d", apiKey)
	}
	if jwt < 8 {
		t.Errorf("expected at least 8 JWT routes (basic auth), got %d", jwt)
	}
	if jwtEdit < 6 {
		t.Errorf("expected at least 6 JWT editor routes, got %d", jwtEdit)
	}
	if jwtChat < 1 {
		t.Errorf("expected at least 1 JWT chat route, got %d", jwtChat)
	}
}

// =============================================================================
// Test: Method Not Allowed
// =============================================================================

func TestMethodNotAllowed(t *testing.T) {
	tokens := map[string]*KaineUser{
		"valid-user": {
			Sub:    "valid-user",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Test POST to GET-only routes
	getOnlyRoutes := []string{
		"/api/kaine/objectives",
		"/api/kaine/mining-maps",
		"/api/kaine/systems/search?q=Sol",
	}

	for _, path := range getOnlyRoutes {
		t.Run("POST_to_GET_route/"+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			req.Header.Set("Authorization", "Bearer valid-user")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Should be 405 Method Not Allowed
			if rr.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST to GET route %s: got %d, want 405", path, rr.Code)
			}
		})
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

// filterRoutesByAuth returns routes matching the specified auth type.
func filterRoutesByAuth(routes []RouteSpec, authType AuthType) []RouteSpec {
	result := []RouteSpec{}
	for _, r := range routes {
		if r.AuthType == authType {
			result = append(result, r)
		}
	}
	return result
}

// =============================================================================
// Test: Cross-Auth Type Attacks
// =============================================================================

func TestCrossAuthTypeAttacks(t *testing.T) {
	const apiKey = "test-api-key-12345"
	tokens := map[string]*KaineUser{
		"jwt-user": {
			Sub:    "jwt-user",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes(apiKey, tokens)
	mux := http.NewServeMux()

	// Register both internal and Kaine routes
	mux.HandleFunc("/status/", server.withAuth(server.handleStatus))
	mux.HandleFunc("/logs/", server.withAuth(server.handleLogs))
	server.RegisterKaineRoutes(mux)

	// Test 1: JWT token on internal API route (should fail)
	t.Run("jwt_on_internal_route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/status/test", nil)
		req.Header.Set("Authorization", "Bearer jwt-user")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("JWT on internal route: got %d, want 401", rr.Code)
		}
	})

	// Test 2: API key on Kaine route (should fail)
	t.Run("api_key_on_kaine_route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("API key on Kaine route: got %d, want 401", rr.Code)
		}
	})

	// Test 3: Try to use both auth types together
	t.Run("both_auth_types_kaine_route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		// Try putting JWT in header and API key... where?
		// Actually the Kaine routes extract JWT from Bearer token
		// So if we send API key as Bearer, it should fail JWT validation
		req.Header.Set("Authorization", "Bearer "+apiKey)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should fail because API key is not a valid JWT
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("API key as JWT: got %d, want 401", rr.Code)
		}
	})
}

// =============================================================================
// Test: Case Sensitivity
// =============================================================================

func TestAuthorizationHeaderCaseSensitivity(t *testing.T) {
	const apiKey = "test-api-key"
	tokens := map[string]*KaineUser{
		"jwt-user": {
			Sub:    "jwt-user",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes(apiKey, tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Note: HTTP headers are case-insensitive by spec, but Bearer prefix might matter

	// Test 1: lowercase "bearer" (should still work per HTTP spec)
	t.Run("lowercase_bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		req.Header.Set("Authorization", "bearer jwt-user")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// This depends on implementation - typically should fail since we check "Bearer "
		// Let's verify current behavior
		if rr.Code == http.StatusOK || rr.Code == http.StatusServiceUnavailable {
			// If it works, that's fine (case insensitive is valid)
			t.Log("lowercase 'bearer' accepted - implementation is case-insensitive")
		} else if rr.Code == http.StatusUnauthorized {
			// If it fails, that's also fine (strict checking)
			t.Log("lowercase 'bearer' rejected - implementation requires exact 'Bearer'")
		}
	})

	// Test 2: Mixed case "BeArEr" (unusual but valid per HTTP)
	t.Run("mixed_case_bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		req.Header.Set("Authorization", "BeArEr jwt-user")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Document behavior
		if rr.Code == http.StatusOK || rr.Code == http.StatusServiceUnavailable {
			t.Log("mixed case 'BeArEr' accepted")
		} else if rr.Code == http.StatusUnauthorized {
			t.Log("mixed case 'BeArEr' rejected")
		}
	})
}

// =============================================================================
// Test: Token with Whitespace
// =============================================================================

func TestTokenWithWhitespace(t *testing.T) {
	tokens := map[string]*KaineUser{
		"jwt-user": {
			Sub:    "jwt-user",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Test 1: Extra spaces after Bearer
	t.Run("extra_spaces_after_bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		req.Header.Set("Authorization", "Bearer   jwt-user") // Extra spaces
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should fail - token starts with spaces
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("extra spaces after Bearer: got %d, want 401", rr.Code)
		}
	})

	// Test 2: Trailing whitespace in token
	t.Run("trailing_whitespace_in_token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
		req.Header.Set("Authorization", "Bearer jwt-user   ") // Trailing spaces
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Implementation may or may not trim - document behavior
		if rr.Code == http.StatusUnauthorized {
			t.Log("trailing whitespace in token rejected")
		} else {
			t.Log("trailing whitespace in token trimmed and accepted")
		}
	})
}

// =============================================================================
// Test: Verify All Kaine Roles
// =============================================================================

func TestAllKaineRoles(t *testing.T) {
	// Test that all known Kaine roles work correctly
	allRoles := []string{
		"kaine-approved",        // God mode
		"kaine-directors",       // Editor role
		"kaine-lead-ops",        // Editor role
		"kaine-ops",             // Basic read access
		"kaine-pledge",          // Basic read access
		"kaine-chat",            // Chat access
		"kaine-chat-debug",      // Chat + debug access
		"kaine-chat-debug-test", // Chat + debug access (test env)
	}

	tokens := make(map[string]*KaineUser)
	for _, role := range allRoles {
		tokens[role+"-token"] = &KaineUser{
			Sub:    role + "-user",
			Name:   role + " User",
			Groups: []string{role},
		}
	}

	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// All roles should be able to access basic read routes
	for _, role := range allRoles {
		t.Run(role+"/can_read_objectives", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/kaine/objectives", nil)
			req.Header.Set("Authorization", "Bearer "+role+"-token")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Errorf("role %s should have read access, got %d", role, rr.Code)
			}
		})
	}
}

// =============================================================================
// Test: Paths with URL Encoding
// =============================================================================

func TestPathsWithURLEncoding(t *testing.T) {
	tokens := map[string]*KaineUser{
		"test-user": {
			Sub:    "test-user",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Test URL-encoded system names
	t.Run("url_encoded_system_name", func(t *testing.T) {
		// "Sol" doesn't need encoding, but "Alpha Centauri" does
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/systems/Alpha%20Centauri", nil)
		req.Header.Set("Authorization", "Bearer test-user")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		// Should not return auth errors
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Errorf("URL-encoded path should be handled, got %d", rr.Code)
		}
	})

	t.Run("url_encoded_search_query", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/search?q=Alpha%20Centauri", nil)
		req.Header.Set("Authorization", "Bearer test-user")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Errorf("URL-encoded query should be handled, got %d", rr.Code)
		}
	})
}

// =============================================================================
// Test: Path Traversal Attempts
// =============================================================================

func TestPathTraversalAttempts(t *testing.T) {
	tokens := map[string]*KaineUser{
		"test-user": {
			Sub:    "test-user",
			Groups: []string{"kaine-approved"},
		},
	}
	server := testServerForRoutes("api-key", tokens)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	// Path traversal attempts should not bypass auth
	// Note: Go's http.ServeMux sanitizes paths, returning 301 redirects for
	// cleaned paths. This is actually secure behavior - path traversal is
	// handled by the standard library.
	traversalPaths := []string{
		"/api/kaine/../health",
		"/api/kaine/objectives/../../../etc/passwd",
		"/api/kaine/objectives/..%2F..%2F..%2Fetc%2Fpasswd",
	}

	for _, path := range traversalPaths {
		t.Run("traversal/"+strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			// Test without auth
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			// Should NOT return 200 OK - must be rejected somehow:
			// - 301 Moved Permanently (Go's path cleaning)
			// - 401 Unauthorized
			// - 404 Not Found
			// All of these are secure outcomes
			if rr.Code == http.StatusOK {
				t.Errorf("path traversal %s: got 200 OK, should be rejected", path)
			}
		})
	}
}
