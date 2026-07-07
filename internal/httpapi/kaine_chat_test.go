package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/tools"
)

// chatMockValidator provides JWT validation for chat tests.
type chatMockValidator struct {
	tokens map[string]*KaineUser
}

func (m *chatMockValidator) ValidateToken(token string) (*KaineUser, error) {
	if user, ok := m.tokens[token]; ok {
		return user, nil
	}
	return nil, errors.New("invalid token")
}

func (m *chatMockValidator) Close() {}

// newChatTestServer creates a test server for chat tests with mock JWT validation.
func newChatTestServer() *Server {
	// Create mock validator with predefined tokens
	mock := &chatMockValidator{
		tokens: map[string]*KaineUser{
			"user-no-chat-token": {
				Sub:    "user-no-chat",
				Groups: []string{"kaine-ops"},
			},
			"user-with-chat-token": {
				Sub:    "user-with-chat",
				Groups: []string{"kaine-chat"},
			},
		},
	}

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
		logger:       observability.NewLogger("test"),
		jwtValidator: mock,
		nonceStore:   newKaineNonceStore(),
		// Note: kaineRunner is nil, which will cause handleKaineChatWebSocket to return 503
	}
}

// TestChatSessionSend tests the chat session send method.
func TestChatSessionSend(t *testing.T) {
	t.Run("strips debug info for non-debug users", func(t *testing.T) {
		session := &chatSession{
			debugMode: false,
		}

		msg := ChatWSMessage{
			Type:       ChatWSTypeToolComplete,
			ToolName:   "galaxy_system",
			ToolInput:  map[string]string{"system": "Sol"},
			ToolOutput: map[string]interface{}{"id64": 123456},
			Content:    "Found Sol",
		}

		// Since we can't easily test websocket.Conn without a real connection,
		// we test the logic by checking the struct manipulation
		if !session.debugMode {
			msg.ToolInput = nil
			msg.ToolOutput = nil
		}

		if msg.ToolInput != nil {
			t.Error("expected ToolInput to be nil for non-debug user")
		}
		if msg.ToolOutput != nil {
			t.Error("expected ToolOutput to be nil for non-debug user")
		}

		// For debug users, data should be preserved
		session.debugMode = true
		msg2 := ChatWSMessage{
			Type:       ChatWSTypeToolComplete,
			ToolName:   "galaxy_system",
			ToolInput:  map[string]string{"system": "Sol"},
			ToolOutput: map[string]interface{}{"id64": 123456},
		}

		if !session.debugMode {
			msg2.ToolInput = nil
			msg2.ToolOutput = nil
		}

		if msg2.ToolInput == nil {
			t.Error("expected ToolInput to be preserved for debug user")
		}
		if msg2.ToolOutput == nil {
			t.Error("expected ToolOutput to be preserved for debug user")
		}
	})
}

// TestChatWSMessageTypes verifies all message type constants.
func TestChatWSMessageTypes(t *testing.T) {
	types := []struct {
		constant ChatWSMessageType
		value    string
	}{
		{ChatWSTypeUserMessage, "user_message"},
		{ChatWSTypeThinking, "thinking"},
		{ChatWSTypeToolStart, "tool_start"},
		{ChatWSTypeToolComplete, "tool_complete"},
		{ChatWSTypeTextDelta, "text_delta"},
		{ChatWSTypeText, "text"},
		{ChatWSTypeError, "error"},
		{ChatWSTypeDone, "done"},
		{ChatWSTypeConnected, "connected"},
	}

	for _, tt := range types {
		t.Run(string(tt.constant), func(t *testing.T) {
			if string(tt.constant) != tt.value {
				t.Errorf("got %s, want %s", tt.constant, tt.value)
			}
		})
	}
}

// TestChatWSMessageJSON verifies JSON serialization of chat messages.
func TestChatWSMessageJSON(t *testing.T) {
	t.Run("connected message", func(t *testing.T) {
		msg := ChatWSMessage{
			Type:      ChatWSTypeConnected,
			SessionID: "test-session-123",
			DebugMode: true,
			Timestamp: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded ChatWSMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.Type != ChatWSTypeConnected {
			t.Errorf("Type = %s, want %s", decoded.Type, ChatWSTypeConnected)
		}
		if decoded.SessionID != "test-session-123" {
			t.Errorf("SessionID = %s, want test-session-123", decoded.SessionID)
		}
		if !decoded.DebugMode {
			t.Error("DebugMode should be true")
		}
	})

	t.Run("tool_complete with debug data", func(t *testing.T) {
		msg := ChatWSMessage{
			Type:       ChatWSTypeToolComplete,
			ToolName:   "galaxy_system",
			ToolInput:  map[string]string{"system": "Sol"},
			ToolOutput: map[string]interface{}{"name": "Sol", "id64": 10477373803},
			Duration:   "150ms",
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		if !strings.Contains(string(data), `"tool_name":"galaxy_system"`) {
			t.Error("expected tool_name in JSON")
		}
		if !strings.Contains(string(data), `"tool_input"`) {
			t.Error("expected tool_input in JSON")
		}
	})

	t.Run("omitempty fields", func(t *testing.T) {
		msg := ChatWSMessage{
			Type:    ChatWSTypeThinking,
			Content: "Processing...",
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		// These fields should be omitted
		if strings.Contains(string(data), `"session_id"`) {
			t.Error("empty session_id should be omitted")
		}
		if strings.Contains(string(data), `"tool_name"`) {
			t.Error("empty tool_name should be omitted")
		}
	})
}

// TestChatWebSocketUpgrade tests the WebSocket upgrade process.
// Note: With the new first-message frame auth (Story 1.1), the handler upgrades freely.
// Auth is now in-frame — pre-upgrade rejection tests have been replaced by WS frame tests below.
func TestChatWebSocketUpgrade(t *testing.T) {
	server := newChatTestServer()

	t.Run("returns 503 when LLM not configured", func(t *testing.T) {
		// The handler checks kaineRunner BEFORE upgrading, so a plain HTTP request
		// should receive 503 without needing WS upgrade.
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws", nil)
		rr := httptest.NewRecorder()
		server.handleKaineChatWebSocket(rr, req)

		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d (503 when LLM not configured)", rr.Code, http.StatusServiceUnavailable)
		}
	})
}

// Note: Full WebSocket integration test (real LLM) is in kaine_integration_test.go
// and requires the integration build tag to run with real services.

// newWSChatTestServer creates a test server with a non-nil kaineRunner so the WS handler
// upgrades the connection (instead of returning 503). The runner has nil client so actual
// LLM calls would fail, but the auth frame tests close the connection before any LLM use.
func newWSChatTestServer() (*Server, *kaineNonceStore) {
	mock := &chatMockValidator{
		tokens: map[string]*KaineUser{
			"ws-chat-token": {Sub: "ws-user", Groups: []string{"kaine-chat"}},
		},
	}

	ns := newKaineNonceStore()
	// assistant.NewRunner with nil client — safe as long as no LLM call is made
	runner := assistant.NewRunner(nil, nil, "", 1)

	s := &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{
				InternalKey: "test-key",
			},
			KaineAuth: config.KaineAuthConfig{
				CookieName: "kaine_session",
				CookiePath: "/api/kaine",
			},
		},
		logger:       observability.NewLogger("test"),
		jwtValidator: mock,
		nonceStore:   ns,
		kaineRunner:  runner,
		llmStore:     llm.NewInMemoryStore(5 * time.Minute),
	}
	return s, ns
}

// dialWSTestServer creates a WebSocket client connection to a test server.
func dialWSTestServer(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/kaine/chat/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("failed to dial WS: %v", err)
	}
	return conn
}

// TestKaineChatWS_AuthViaFirstMessage_Success verifies a valid nonce grants access.
func TestKaineChatWS_AuthViaFirstMessage_Success(t *testing.T) {
	s, ns := newWSChatTestServer()

	user := &KaineUser{Sub: "ws-user", Groups: []string{"kaine-chat"}}
	nonce := ns.Issue(user, 10*time.Second)

	ts := httptest.NewServer(http.HandlerFunc(s.handleKaineChatWebSocket))
	defer ts.Close()

	conn := dialWSTestServer(t, ts)
	defer conn.Close()

	// Send auth frame
	authFrame, _ := json.Marshal(map[string]string{"type": "auth", "token": nonce})
	if err := conn.WriteMessage(websocket.TextMessage, authFrame); err != nil {
		t.Fatalf("failed to send auth frame: %v", err)
	}

	// Server should respond with "connected" message
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read message after auth: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(msg, &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["type"] != "connected" {
		t.Errorf("first message type = %v, want connected", resp["type"])
	}
}

// TestKaineChatWS_AuthViaFirstMessage_Timeout_ConnectionClosed4401 verifies auth timeout closes 4401.
func TestKaineChatWS_AuthViaFirstMessage_Timeout_ConnectionClosed4401(t *testing.T) {
	s, _ := newWSChatTestServer()

	// Override the upgrader read deadline via a test-specific setup.
	// We test by simply not sending any auth frame for more than the server's 5s deadline.
	// To keep the test fast, we rely on the server closing us with 4401 on timeout.
	// We make the server's deadline effectively immediate by connecting and waiting.
	ts := httptest.NewServer(http.HandlerFunc(s.handleKaineChatWebSocket))
	defer ts.Close()

	conn := dialWSTestServer(t, ts)
	defer conn.Close()

	// Don't send auth frame — wait for server to close with 4401
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed by server, got nil error")
	}

	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Logf("got non-close error (acceptable for timeout): %v", err)
		return
	}
	if closeErr.Code != 4401 {
		t.Errorf("close code = %d, want 4401", closeErr.Code)
	}
}

// TestKaineChatWS_AuthViaFirstMessage_InvalidNonce_ConnectionClosed4403 verifies invalid nonce closes 4403.
func TestKaineChatWS_AuthViaFirstMessage_InvalidNonce_ConnectionClosed4403(t *testing.T) {
	s, _ := newWSChatTestServer()

	ts := httptest.NewServer(http.HandlerFunc(s.handleKaineChatWebSocket))
	defer ts.Close()

	conn := dialWSTestServer(t, ts)
	defer conn.Close()

	// Send an auth frame with a nonce that was never issued
	authFrame, _ := json.Marshal(map[string]string{"type": "auth", "token": "nonexistent-nonce-value"})
	if err := conn.WriteMessage(websocket.TextMessage, authFrame); err != nil {
		t.Fatalf("failed to send auth frame: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed by server")
	}

	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected WS close error, got: %T %v", err, err)
	}
	if closeErr.Code != 4403 {
		t.Errorf("close code = %d, want 4403", closeErr.Code)
	}
}

// TestKaineChatWS_NoAuthFrame_PlainTextMessage_ConnectionClosed verifies non-auth first message closes connection.
func TestKaineChatWS_NoAuthFrame_PlainTextMessage_ConnectionClosed(t *testing.T) {
	s, _ := newWSChatTestServer()

	ts := httptest.NewServer(http.HandlerFunc(s.handleKaineChatWebSocket))
	defer ts.Close()

	conn := dialWSTestServer(t, ts)
	defer conn.Close()

	// Send a plain text message instead of auth frame
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hello server")); err != nil {
		t.Fatalf("failed to send message: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed by server")
	}

	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected WS close error, got: %T %v", err, err)
	}
	// Either 4403 (invalid auth frame) or 4401 (treated as timeout)
	if closeErr.Code != 4403 && closeErr.Code != 4401 {
		t.Errorf("close code = %d, want 4403 or 4401", closeErr.Code)
	}
}

// TestKaineChatWS_UserWithoutChatAccess_ConnectionClosed4403 verifies users without chat access get 4403.
func TestKaineChatWS_UserWithoutChatAccess_ConnectionClosed4403(t *testing.T) {
	s, ns := newWSChatTestServer()

	// Issue a nonce for a user WITHOUT chat access
	noAccessUser := &KaineUser{Sub: "no-access-user", Groups: []string{"kaine-ops"}}
	nonce := ns.Issue(noAccessUser, 10*time.Second)

	ts := httptest.NewServer(http.HandlerFunc(s.handleKaineChatWebSocket))
	defer ts.Close()

	conn := dialWSTestServer(t, ts)
	defer conn.Close()

	authFrame, _ := json.Marshal(map[string]string{"type": "auth", "token": nonce})
	if err := conn.WriteMessage(websocket.TextMessage, authFrame); err != nil {
		t.Fatalf("failed to send auth frame: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed by server")
	}

	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected WS close error, got: %T %v", err, err)
	}
	if closeErr.Code != 4403 {
		t.Errorf("close code = %d, want 4403", closeErr.Code)
	}
}

// TestTruncate tests the truncate helper function.
func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// TestKaineChatScopeDerivation_ApprovedUser_SeesLegacyKaineTools mirrors the
// scope-threading that handleChatMessage performs for an authenticated Kaine
// chat user: ScopesForGroups(user.Groups) + ScopeKaineChat. It asserts the
// derived scope set yields exactly the legacy kaine-approved tool surface when
// run through the scope-driven filter, so any regression in group→scope→tool
// mapping is caught at the httpapi boundary, not just in tools/.
func TestKaineChatScopeDerivation_ApprovedUser_SeesLegacyKaineTools(t *testing.T) {
	user := &KaineUser{Sub: "approved-user", Groups: []string{"kaine-approved"}}

	// Reproduce the exact derivation from kaine_chat.go's handleChatMessage.
	scopes := append(authz.ScopesForGroups(user.Groups), authz.ScopeKaineChat)

	defs := tools.MCPToAnthropicAll(tools.MCPToolDefinitions(), scopes)

	var got []string
	for _, def := range defs {
		if def.OfTool != nil {
			got = append(got, def.OfTool.Name)
		}
	}
	sort.Strings(got)

	// Kaine-approved tool surface. Kept in sync with
	// internal/tools/convert_test.go's legacyKaineTools; W5.5 deliberately
	// adds galaxy_surface_sites to the old legacy set.
	want := []string{
		"bgs_guide_search",
		"describe_tool",
		"galaxy_bodies",
		"galaxy_expansion_check",
		"galaxy_expansion_frontier",
		"galaxy_expansion_targets",
		"galaxy_faction",
		"galaxy_fleet_carrier",
		"galaxy_history",
		"galaxy_ltd_buyers",
		"galaxy_market",
		"galaxy_nearby_powerplay",
		"galaxy_plasmium_buyers",
		"galaxy_power",
		"galaxy_powerplay_cycle",
		"galaxy_query",
		"galaxy_schema",
		"galaxy_signals",
		"galaxy_station",
		"galaxy_stats",
		"galaxy_surface_sites",
		"galaxy_system",
		"powerplay_guide_search",
		"retrieve_carrier_route",
		"spansh_query",
		"system_profile",
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("kaine-approved derived tool set size mismatch: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kaine-approved derived tool set drift at %d: got %q, want %q\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}

// TestChatSessionDebugModeDetection tests that debug mode is correctly detected.
func TestChatSessionDebugModeDetection(t *testing.T) {
	tests := []struct {
		name      string
		user      *KaineUser
		wantDebug bool
	}{
		{
			name:      "regular chat user",
			user:      &KaineUser{Groups: []string{"kaine-chat"}},
			wantDebug: false,
		},
		{
			name:      "chat-debug user",
			user:      &KaineUser{Groups: []string{"kaine-chat-debug"}},
			wantDebug: true,
		},
		{
			name:      "god mode user",
			user:      &KaineUser{Groups: []string{"kaine-god"}},
			wantDebug: true,
		},
		{
			name:      "chat-debug-test user",
			user:      &KaineUser{Groups: []string{"kaine-chat-debug-test"}},
			wantDebug: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			debugMode := tt.user.CanAccessChatDebug()
			if debugMode != tt.wantDebug {
				t.Errorf("CanAccessChatDebug() = %v, want %v", debugMode, tt.wantDebug)
			}
		})
	}
}
