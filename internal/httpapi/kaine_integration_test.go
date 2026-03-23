//go:build integration

// Package httpapi integration tests.
// Run with: go test -tags=integration -v ./internal/httpapi/ -run Integration
//
// These tests require:
// - VPN connection to 10.8.0.x network
// - Memgraph running at 10.8.0.3:7687
// - TimescaleDB running at 10.8.0.3:5432
// - ANTHROPIC_API_KEY environment variable set
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/edin-space/edin-backend/internal/anthropic"
	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/memgraph"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/tools"
)

// TestIntegrationChatWebSocket performs a full integration test of the chat WebSocket.
// This test connects to real services and sends actual queries.
func TestIntegrationChatWebSocket(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set - skipping integration test")
	}

	// Create real clients
	llmClient := anthropic.NewClient(apiKey)

	// Connect to Memgraph (optional - test will work without it)
	var memgraphClient *memgraph.Client
	mgClient, err := memgraph.NewClient(memgraph.Config{
		Host:     "10.8.0.3",
		Port:     7687,
		Username: "memgraph",
		Password: "",
	})
	if err != nil {
		t.Logf("Warning: Could not connect to Memgraph: %v (some tools won't work)", err)
	} else {
		memgraphClient = mgClient
		defer mgClient.Close()
	}

	// Create tool executor with available backends
	toolExec := tools.NewExecutor(nil, nil, nil, nil)
	if memgraphClient != nil {
		toolExec = toolExec.WithMemgraph(memgraphClient)
	}

	// Create assistant runner
	runner := assistant.NewRunner(
		llmClient,
		toolExec,
		"You are a helpful assistant for Elite Dangerous players. Answer questions about the galaxy.",
		10,
	)

	server := &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{
				InternalKey: "test-key",
			},
		},
		logger:    observability.NewLogger("test"),
		llmRunner: runner,
		memgraph:  memgraphClient,
	}

	// Create test HTTP server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject test user with debug access
		user := &KaineUser{
			Sub:    "integration-test-user",
			Name:   "Integration Test",
			Groups: []string{"kaine-chat-debug"},
		}
		ctx := context.WithValue(r.Context(), kaineUserKey{}, user)
		server.handleKaineChatWebSocket(w, r.WithContext(ctx))
	})

	ts := httptest.NewServer(handler)
	defer ts.Close()

	// Convert to WebSocket URL
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	// Connect to WebSocket
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect to WebSocket: %v", err)
	}
	defer ws.Close()

	// Should receive connected message
	var connMsg ChatWSMessage
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := ws.ReadJSON(&connMsg); err != nil {
		t.Fatalf("Failed to read connected message: %v", err)
	}

	if connMsg.Type != ChatWSTypeConnected {
		t.Errorf("Expected connected message, got %s", connMsg.Type)
	}
	if connMsg.SessionID == "" {
		t.Error("Expected session ID")
	}
	t.Logf("Connected with session: %s (debug=%v)", connMsg.SessionID, connMsg.DebugMode)

	// Test queries
	testQueries := []struct {
		name     string
		query    string
		timeout  time.Duration
		validate func(t *testing.T, responses []ChatWSMessage)
	}{
		{
			name:    "simple greeting",
			query:   "Hello! What can you help me with?",
			timeout: 30 * time.Second,
			validate: func(t *testing.T, responses []ChatWSMessage) {
				hasText := false
				for _, r := range responses {
					if r.Type == ChatWSTypeText && r.Content != "" {
						hasText = true
						t.Logf("Response: %s", truncateForLog(r.Content, 200))
					}
				}
				if !hasText {
					t.Error("Expected text response")
				}
			},
		},
	}

	// Only run Memgraph-dependent tests if connected
	if memgraphClient != nil {
		testQueries = append(testQueries, struct {
			name     string
			query    string
			timeout  time.Duration
			validate func(t *testing.T, responses []ChatWSMessage)
		}{
			name:    "system lookup with tool",
			query:   "What can you tell me about the Sol system?",
			timeout: 60 * time.Second,
			validate: func(t *testing.T, responses []ChatWSMessage) {
				hasToolUse := false
				hasText := false
				for _, r := range responses {
					if r.Type == ChatWSTypeToolStart || r.Type == ChatWSTypeToolComplete {
						hasToolUse = true
						t.Logf("Tool used: %s", r.ToolName)
					}
					if r.Type == ChatWSTypeText && r.Content != "" {
						hasText = true
						t.Logf("Response: %s", truncateForLog(r.Content, 200))
					}
				}
				if !hasToolUse {
					t.Log("Warning: Expected tool use for system lookup (may not have used tools)")
				}
				if !hasText {
					t.Error("Expected text response")
				}
			},
		})
	}

	for _, tt := range testQueries {
		t.Run(tt.name, func(t *testing.T) {
			// Send query
			msg := map[string]string{
				"type":    "user_message",
				"content": tt.query,
			}
			if err := ws.WriteJSON(msg); err != nil {
				t.Fatalf("Failed to send message: %v", err)
			}

			// Collect responses until done
			var responses []ChatWSMessage
			deadline := time.Now().Add(tt.timeout)

			for time.Now().Before(deadline) {
				ws.SetReadDeadline(time.Now().Add(5 * time.Second))
				var resp ChatWSMessage
				if err := ws.ReadJSON(&resp); err != nil {
					if strings.Contains(err.Error(), "timeout") {
						continue
					}
					t.Fatalf("Failed to read response: %v", err)
				}

				responses = append(responses, resp)
				t.Logf("Received: type=%s", resp.Type)

				if resp.Type == ChatWSTypeDone {
					break
				}
				if resp.Type == ChatWSTypeError {
					t.Errorf("Received error: %s", resp.Content)
					break
				}
			}

			// Validate responses
			tt.validate(t, responses)
		})
	}
}

// TestIntegrationToolExecution tests that tools execute correctly against real backends.
func TestIntegrationToolExecution(t *testing.T) {
	// Connect to Memgraph
	mgClient, err := memgraph.NewClient(memgraph.Config{
		Host:     "10.8.0.3",
		Port:     7687,
		Username: "memgraph",
		Password: "",
	})
	if err != nil {
		t.Skipf("Could not connect to Memgraph: %v", err)
	}
	defer mgClient.Close()

	ctx := context.Background()

	t.Run("SearchSystems", func(t *testing.T) {
		results, err := mgClient.SearchSystems(ctx, "Sol", 5)
		if err != nil {
			t.Fatalf("SearchSystems failed: %v", err)
		}
		t.Logf("Found %d systems matching 'Sol'", len(results))
		for _, sys := range results {
			t.Logf("  - %v", sys)
		}
	})

	t.Run("GetSystem", func(t *testing.T) {
		system, err := mgClient.GetSystem(ctx, "Sol")
		if err != nil {
			t.Fatalf("GetSystem failed: %v", err)
		}
		if system == nil {
			t.Log("Sol system not found in Memgraph (may not be populated)")
			return
		}
		data, _ := json.MarshalIndent(system, "", "  ")
		t.Logf("Sol system: %s", string(data))
	})
}

// TestIntegrationChatWithHistory tests that conversation history is maintained.
func TestIntegrationChatWithHistory(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	llmClient := anthropic.NewClient(apiKey)
	toolExec := tools.NewExecutor(nil, nil, nil, nil)
	runner := assistant.NewRunner(llmClient, toolExec, "You are a helpful assistant.", 5)

	server := &Server{
		cfg: &config.Config{
			HTTP: config.HTTPConfig{InternalKey: "test-key"},
		},
		logger:    observability.NewLogger("test"),
		llmRunner: runner,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := &KaineUser{
			Sub:    "test-user",
			Groups: []string{"kaine-chat"},
		}
		ctx := context.WithValue(r.Context(), kaineUserKey{}, user)
		server.handleKaineChatWebSocket(w, r.WithContext(ctx))
	})

	ts := httptest.NewServer(handler)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Read connected message
	var connMsg ChatWSMessage
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	ws.ReadJSON(&connMsg)

	// Send first message
	ws.WriteJSON(map[string]string{
		"type":    "user_message",
		"content": "My name is TestBot. Remember that.",
	})

	// Wait for response
	readUntilDone(t, ws, 30*time.Second)

	// Send second message referencing the first
	ws.WriteJSON(map[string]string{
		"type":    "user_message",
		"content": "What is my name?",
	})

	// Collect and verify response mentions the name
	responses := readUntilDone(t, ws, 30*time.Second)

	foundName := false
	for _, r := range responses {
		if r.Type == ChatWSTypeText && strings.Contains(strings.ToLower(r.Content), "testbot") {
			foundName = true
			t.Logf("Response correctly referenced name: %s", truncateForLog(r.Content, 100))
		}
	}

	if !foundName {
		t.Error("Expected response to remember the name 'TestBot'")
	}
}

// Helper to read messages until done signal
func readUntilDone(t *testing.T, ws *websocket.Conn, timeout time.Duration) []ChatWSMessage {
	var responses []ChatWSMessage
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var resp ChatWSMessage
		if err := ws.ReadJSON(&resp); err != nil {
			if strings.Contains(err.Error(), "timeout") {
				continue
			}
			t.Logf("Read error: %v", err)
			break
		}
		responses = append(responses, resp)
		if resp.Type == ChatWSTypeDone || resp.Type == ChatWSTypeError {
			break
		}
	}
	return responses
}

// Helper to truncate strings for logging
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
