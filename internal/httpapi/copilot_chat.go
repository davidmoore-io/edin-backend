package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/tools"
)

// handleCopilotChatWebSocket handles the WebSocket connection for Copilot chat.
// Auth is performed via a first-message auth frame: the server upgrades freely, then
// waits 5s for {"type":"auth","token":"<nonce>"}. Closes 4401 on timeout, 4403 on
// invalid nonce or insufficient permissions.
//
// The nonce must have been issued by the commander auth flow (Story 7.1) via
// commanderNonceStore. The KaineUser.Sub will be the commander FID (e.g. "F2504").
func (s *Server) handleCopilotChatWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check copilot runner first (before upgrade — cheaper than a WS handshake)
	if s.copilotRunner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "copilot service not available")
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("copilot_ws upgrade failed", err)
		return
	}
	defer conn.Close()

	// --- Auth via first-message frame ---
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, authMsg, err := conn.ReadMessage()
	if err != nil {
		// Timeout or client closed — do not reconnect
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4401, "auth timeout"))
		return
	}

	var authFrame struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(authMsg, &authFrame); err != nil || authFrame.Type != "auth" || authFrame.Token == "" {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4403, "invalid auth frame"))
		return
	}

	// Resolve nonce → user (single-use) — uses commanderNonceStore (set by commander auth flow)
	user := s.commanderNonceStore.Consume(authFrame.Token)
	if user == nil {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4403, "invalid or expired nonce"))
		return
	}

	// Check chat access
	if !user.CanAccessChat() {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4403, "chat access denied"))
		return
	}

	// Reset read deadline for normal operation
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	// --- End auth ---

	// Build a per-session runner with the commander's personalised system prompt.
	// WithSystemPrompt creates a lightweight copy sharing the same client and executor.
	sessionRunner := s.copilotRunner.WithSystemPrompt(CopilotSystemPrompt(user.Name))

	// Load or create session
	sessionID, history := s.loadOrCreateChatSession(user.Sub)

	session := &chatSession{
		conn:       conn,
		user:       user,
		sessionID:  sessionID,
		history:    history,
		debugMode:  user.CanAccessChatDebug(),
		done:       make(chan struct{}),
		lastActive: time.Now(),
	}

	s.logger.Info(fmt.Sprintf("copilot_ws connected user=%s session=%s debug=%t history=%d", user.Sub, sessionID, session.debugMode, len(history)))

	// Send connected message
	session.send(ChatWSMessage{
		Type:      ChatWSTypeConnected,
		SessionID: sessionID,
		DebugMode: session.debugMode,
	})

	// Send chat history if we have any
	if len(history) > 0 {
		session.send(ChatWSMessage{
			Type:      ChatWSTypeChatHistory,
			SessionID: sessionID,
			Messages:  history,
		})
	}

	// Configure connection
	conn.SetReadLimit(64 * 1024) // 64KB max message size
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Start ping ticker
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Ping goroutine
	go func() {
		for {
			select {
			case <-pingTicker.C:
				session.writeMu.Lock()
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					session.writeMu.Unlock()
					return
				}
				session.writeMu.Unlock()
			case <-session.done:
				return
			}
		}
	}()

	// Main read loop
	for {
		select {
		case <-session.done:
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.logger.Warn(fmt.Sprintf("copilot_ws read error user=%s session=%s: %v", user.Sub, sessionID, err))
			}
			close(session.done)
			s.logger.Info(fmt.Sprintf("copilot_ws disconnected user=%s session=%s", user.Sub, sessionID))
			return
		}

		// Reset read deadline
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		session.lastActive = time.Now()

		// Parse incoming message
		var incoming struct {
			Type      string `json:"type"`
			Content   string `json:"content"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(message, &incoming); err != nil {
			session.send(ChatWSMessage{
				Type:    ChatWSTypeError,
				Content: "invalid message format",
				Error:   true,
			})
			continue
		}

		switch incoming.Type {
		case "user_message":
			if incoming.Content != "" {
				s.handleCopilotMessage(session, incoming.Content, sessionRunner)
			}
		case "new_chat":
			s.handleNewChat(session)
		case "switch_session":
			if incoming.SessionID != "" {
				s.handleSwitchSession(session, incoming.SessionID)
			}
		}
	}
}

// handleCopilotMessage processes a user message for the copilot and streams the response.
func (s *Server) handleCopilotMessage(session *chatSession, content string, sessionRunner *assistant.Runner) {
	s.logger.Info(fmt.Sprintf("copilot_message user=%s session=%s message=\"%s\"", session.user.Sub, session.sessionID, truncate(content, 160)))

	// Add user message to history
	userMsg := llm.Message{
		Role:      "user",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	session.history = append(session.history, userMsg)

	// Persist user message to store
	s.llmStore.AppendMessage(session.sessionID, userMsg)

	// Trim in-memory history to last 20 messages for the API call
	historyForAPI := session.history
	if len(historyForAPI) > 20 {
		historyForAPI = historyForAPI[len(historyForAPI)-20:]
	}

	// Send thinking indicator
	session.send(ChatWSMessage{
		Type:    ChatWSTypeThinking,
		Content: "Processing your question...",
	})

	// Set up context with copilot authorization scope and commander FID for tools
	ctx := assistant.WithContext(context.Background(), session.sessionID, session.user.Sub)
	ctx = authz.ContextWithScopes(ctx, authz.ScopeCopilotChat)
	ctx = tools.WithCommanderFID(ctx, session.user.Sub)

	// Create progress callback that streams to WebSocket
	onProgress := func(event assistant.ProgressEvent) {
		switch event.Type {
		case assistant.ProgressThinking:
			session.send(ChatWSMessage{
				Type:    ChatWSTypeThinking,
				Content: event.Message,
			})
		case assistant.ProgressToolStart:
			session.send(ChatWSMessage{
				Type:     ChatWSTypeToolStart,
				ToolName: event.ToolName,
				Content:  event.Message,
			})
		case assistant.ProgressToolComplete:
			session.send(ChatWSMessage{
				Type:     ChatWSTypeToolComplete,
				ToolName: event.ToolName,
				Content:  event.Message,
				Duration: event.Message,
				Error:    event.Error,
			})
		}
	}

	// Run the copilot with per-session runner (includes commander tools)
	start := time.Now()
	reply, err := sessionRunner.RunWithProgress(ctx, historyForAPI, content, onProgress)

	if err != nil {
		s.logger.Error(fmt.Sprintf("copilot_run_error user=%s session=%s", session.user.Sub, session.sessionID), err)
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: fmt.Sprintf("Error processing message: %v", err),
			Error:   true,
		})
		return
	}

	// Add assistant reply to history and persist
	assistantMsg := llm.Message{
		Role:      "assistant",
		Content:   reply,
		CreatedAt: time.Now().UTC(),
	}
	session.history = append(session.history, assistantMsg)
	s.llmStore.AppendMessage(session.sessionID, assistantMsg)

	// Send final response
	session.send(ChatWSMessage{
		Type:     ChatWSTypeText,
		Content:  reply,
		Duration: time.Since(start).Round(time.Millisecond).String(),
	})

	// Send done signal
	session.send(ChatWSMessage{
		Type:      ChatWSTypeDone,
		SessionID: session.sessionID,
	})

	s.logger.Info(fmt.Sprintf("copilot_complete user=%s session=%s duration=%s reply=\"%s\"", session.user.Sub, session.sessionID, time.Since(start), truncate(reply, 200)))
}
