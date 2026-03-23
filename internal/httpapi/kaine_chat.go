package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/llm"
)

// ChatWSMessageType identifies the type of WebSocket message.
type ChatWSMessageType string

const (
	ChatWSTypeUserMessage  ChatWSMessageType = "user_message"
	ChatWSTypeThinking     ChatWSMessageType = "thinking"
	ChatWSTypeToolStart    ChatWSMessageType = "tool_start"
	ChatWSTypeToolComplete ChatWSMessageType = "tool_complete"
	ChatWSTypeTextDelta    ChatWSMessageType = "text_delta"
	ChatWSTypeText         ChatWSMessageType = "text"
	ChatWSTypeError        ChatWSMessageType = "error"
	ChatWSTypeDone         ChatWSMessageType = "done"
	ChatWSTypeConnected    ChatWSMessageType = "connected"
	ChatWSTypeChatHistory  ChatWSMessageType = "chat_history"
	ChatWSTypeChatCleared  ChatWSMessageType = "chat_cleared"
)

// ChatWSMessage represents a WebSocket message for the chat interface.
type ChatWSMessage struct {
	Type       ChatWSMessageType `json:"type"`
	SessionID  string            `json:"session_id,omitempty"`
	Content    string            `json:"content,omitempty"`
	ToolName   string            `json:"tool_name,omitempty"`
	ToolInput  any               `json:"tool_input,omitempty"`  // Only sent to debug users
	ToolOutput any               `json:"tool_output,omitempty"` // Only sent to debug users
	Duration   string            `json:"duration,omitempty"`
	Error      bool              `json:"error,omitempty"`
	DebugMode  bool              `json:"debug_mode,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
	Messages   []llm.Message     `json:"messages,omitempty"` // For chat_history
}

// chatSession holds the state for a single chat WebSocket connection.
type chatSession struct {
	conn       *websocket.Conn
	user       *KaineUser
	sessionID  string
	history    []llm.Message
	debugMode  bool
	writeMu    sync.Mutex
	done       chan struct{}
	lastActive time.Time
}

func (cs *chatSession) send(msg ChatWSMessage) error {
	cs.writeMu.Lock()
	defer cs.writeMu.Unlock()

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	// Strip debug info for non-debug users
	if !cs.debugMode {
		msg.ToolInput = nil
		msg.ToolOutput = nil
	}

	cs.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return cs.conn.WriteJSON(msg)
}

// handleKaineChatWebSocket handles the WebSocket connection for galaxy chat.
func (s *Server) handleKaineChatWebSocket(w http.ResponseWriter, r *http.Request) {
	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Check if Kaine LLM runner is available
	if s.kaineRunner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "chat service not available")
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error(fmt.Sprintf("chat_ws upgrade failed user=%s", user.Sub), err)
		return
	}
	defer conn.Close()

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

	s.logger.Info(fmt.Sprintf("chat_ws connected user=%s session=%s debug=%t history=%d", user.Sub, sessionID, session.debugMode, len(history)))

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
				s.logger.Warn(fmt.Sprintf("chat_ws read error user=%s session=%s: %v", user.Sub, sessionID, err))
			}
			close(session.done)
			s.logger.Info(fmt.Sprintf("chat_ws disconnected user=%s session=%s", user.Sub, sessionID))
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
				s.handleChatMessage(session, incoming.Content)
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

// loadOrCreateChatSession loads the user's active session from the store, or creates a new one.
func (s *Server) loadOrCreateChatSession(userID string) (string, []llm.Message) {
	// Try multi-session store first
	if ms, ok := s.llmStore.(llm.MultiSessionBackend); ok {
		existing, err := ms.GetActiveSession(userID)
		if err == nil && existing != nil {
			return existing.ID, existing.Messages
		}
	}

	// Create a new session
	newSession := s.llmStore.CreateSession(userID)
	return newSession.ID, newSession.Messages
}

// handleNewChat creates a fresh session for the user.
func (s *Server) handleNewChat(session *chatSession) {
	newStoreSession := s.llmStore.CreateSession(session.user.Sub)
	session.sessionID = newStoreSession.ID
	session.history = make([]llm.Message, 0)

	s.logger.Info(fmt.Sprintf("new_chat user=%s session=%s", session.user.Sub, session.sessionID))

	session.send(ChatWSMessage{
		Type:      ChatWSTypeChatCleared,
		SessionID: session.sessionID,
	})
}

// handleSwitchSession switches to a different session for the user.
// Validates that the session belongs to the authenticated user via the store.
func (s *Server) handleSwitchSession(session *chatSession, targetSessionID string) {
	ms, ok := s.llmStore.(llm.MultiSessionBackend)
	if !ok {
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "session switching not available",
			Error:   true,
		})
		return
	}

	if err := ms.SetActiveSession(session.user.Sub, targetSessionID); err != nil {
		s.logger.Warn(fmt.Sprintf("switch_session denied user=%s target=%s: %v", session.user.Sub, targetSessionID, err))
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "cannot switch to that session",
			Error:   true,
		})
		return
	}

	// Load the target session's messages
	targetSession, ok2 := s.llmStore.Get(targetSessionID)
	if !ok2 || targetSession == nil {
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "session not found",
			Error:   true,
		})
		return
	}

	// Update the live connection to use the new session
	session.sessionID = targetSessionID
	session.history = targetSession.Messages

	s.logger.Info(fmt.Sprintf("switch_session user=%s session=%s messages=%d", session.user.Sub, targetSessionID, len(targetSession.Messages)))

	// Send history to the client
	session.send(ChatWSMessage{
		Type:      ChatWSTypeChatHistory,
		SessionID: targetSessionID,
		Messages:  targetSession.Messages,
	})
}

// handleChatMessage processes a user message and streams the response.
func (s *Server) handleChatMessage(session *chatSession, content string) {
	s.logger.Info(fmt.Sprintf("chat_message user=%s session=%s message=\"%s\"", session.user.Sub, session.sessionID, truncate(content, 160)))

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

	// Set up context with authorization - Kaine chat gets limited tool access
	ctx := assistant.WithContext(context.Background(), session.sessionID, session.user.Sub)
	ctx = authz.ContextWithScopes(ctx, authz.ScopeKaineChat)

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

	// Run the assistant with Kaine-specific runner (Elite Dangerous tools only)
	start := time.Now()
	reply, err := s.kaineRunner.RunWithProgress(ctx, historyForAPI, content, onProgress)

	if err != nil {
		s.logger.Error(fmt.Sprintf("chat_run_error user=%s session=%s", session.user.Sub, session.sessionID), err)
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

	s.logger.Info(fmt.Sprintf("chat_complete user=%s session=%s duration=%s reply=\"%s\"", session.user.Sub, session.sessionID, time.Since(start), truncate(reply, 200)))
}

// handleKaineChatSessions returns the list of sessions for the authenticated user.
func (s *Server) handleKaineChatSessions(w http.ResponseWriter, r *http.Request) {
	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	ms, ok := s.llmStore.(llm.MultiSessionBackend)
	if !ok {
		s.writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}, "count": 0})
		return
	}

	sessions, err := ms.ListUserSessions(user.Sub)
	if err != nil {
		s.logger.Error(fmt.Sprintf("list_sessions_error user=%s", user.Sub), err)
		s.writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"sessions": sessions,
		"count":    len(sessions),
	})
}

// handleKaineChatActivateSession sets a session as the active one for the user.
func (s *Server) handleKaineChatActivateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Extract session ID from path: /api/kaine/chat/sessions/{id}/activate
	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/chat/sessions/")
	sessionID := strings.TrimSuffix(path, "/activate")
	if sessionID == "" {
		s.writeError(w, http.StatusBadRequest, "session ID required")
		return
	}

	ms, ok := s.llmStore.(llm.MultiSessionBackend)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "multi-session not available")
		return
	}

	if err := ms.SetActiveSession(user.Sub, sessionID); err != nil {
		s.logger.Error(fmt.Sprintf("activate_session_error user=%s session=%s", user.Sub, sessionID), err)
		s.writeError(w, http.StatusInternalServerError, "failed to activate session")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "session_id": sessionID})
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
