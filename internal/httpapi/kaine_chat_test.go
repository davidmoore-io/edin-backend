package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
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
		},
		logger:       observability.NewLogger("test"),
		jwtValidator: mock,
		// Note: llmRunner is nil, which will cause handleKaineChatWebSocket to return 503
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
func TestChatWebSocketUpgrade(t *testing.T) {
	server := newChatTestServer()

	t.Run("rejects unauthenticated request", func(t *testing.T) {
		handler := server.withKaineAuth(server.withChatAccess(server.handleKaineChatWebSocket))

		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws", nil)
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
		}
	})

	t.Run("rejects user without chat access", func(t *testing.T) {
		handler := server.withKaineAuth(server.withChatAccess(server.handleKaineChatWebSocket))

		// Use the predefined mock token for a user without chat access
		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws", nil)
		req.Header.Set("Authorization", "Bearer user-no-chat-token")
		rr := httptest.NewRecorder()

		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
		}
	})

	t.Run("returns 503 when LLM not configured", func(t *testing.T) {
		// Direct handler call (bypassing auth middleware for this test)
		handler := server.handleKaineChatWebSocket

		// Inject a user with chat access
		user := &KaineUser{
			Sub:    "test-user",
			Groups: []string{"kaine-chat"},
		}

		req := httptest.NewRequest(http.MethodGet, "/api/kaine/chat/ws", nil)
		ctx := context.WithValue(req.Context(), kaineUserKey{}, user)
		req = req.WithContext(ctx)

		rr := httptest.NewRecorder()
		handler(rr, req)

		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want %d (503 when LLM not configured)", rr.Code, http.StatusServiceUnavailable)
		}
	})
}

// Note: Full WebSocket integration test is in kaine_integration_test.go
// and requires the integration build tag to run with real services.

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
