package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/store"
	"github.com/edin-space/edin-backend/internal/tools"
	"github.com/gorilla/websocket"
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
	ChatWSTypeAudioChunk   ChatWSMessageType = "audio_chunk"
	ChatWSTypeMessageAck   ChatWSMessageType = "message_ack"
	ChatWSTypeHeartbeat    ChatWSMessageType = "heartbeat"
	// speak_start is sent immediately before each speak text_delta burst.
	// Flutter uses it to start a new chat bubble for each discrete <speak> segment.
	ChatWSTypeSpeakStart ChatWSMessageType = "speak_start"
)

// ChatWSMessage represents a WebSocket message for the chat interface.
type ChatWSMessage struct {
	Type            ChatWSMessageType `json:"type"`
	SessionID       string            `json:"session_id,omitempty"`
	Content         string            `json:"content,omitempty"`
	ToolName        string            `json:"tool_name,omitempty"`
	ToolID          string            `json:"tool_id,omitempty"`
	ToolInput       any               `json:"tool_input,omitempty"`  // Only sent to debug users
	ToolOutput      any               `json:"tool_output,omitempty"` // Only sent to debug users
	Duration        string            `json:"duration,omitempty"`
	Error           bool              `json:"error,omitempty"`
	DebugMode       bool              `json:"debug_mode,omitempty"`
	Timestamp       time.Time         `json:"timestamp"`
	Messages        []llm.Message     `json:"messages,omitempty"`   // For chat_history
	Channel         string            `json:"channel,omitempty"`    // "speak" or "data" on text_delta frames
	AudioData       string            `json:"audio_data,omitempty"` // base64 audio on audio_chunk frames
	ClientMessageID string            `json:"client_message_id,omitempty"`
	Duplicate       bool              `json:"duplicate,omitempty"`
}

// VoiceConfig is sent by the Flutter client with every user_message frame.
type VoiceConfig struct {
	Persona      string `json:"persona"`
	Mode         string `json:"mode"`
	VoiceEnabled bool   `json:"voice_enabled"`
}

// chatSession holds the state for a single chat WebSocket connection.
type chatSession struct {
	conn         *websocket.Conn
	user         *KaineUser
	sessionID    string
	history      []llm.Message
	debugMode    bool
	toolScopes   []authz.Scope
	commanderFID string
	writeMu      sync.Mutex
	done         chan struct{}
	lastActive   time.Time
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
// Auth is performed via a first-message auth frame (Story 1.1): the server upgrades
// freely, then waits 5s for {"type":"auth","token":"<nonce>"}. Closes 4401 on timeout,
// 4403 on invalid nonce or insufficient permissions.
func (s *Server) handleKaineChatWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check LLM runner first (before upgrade — cheaper than a WS handshake)
	if s.kaineRunner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "chat service not available")
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("chat_ws upgrade failed", err)
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

	// Resolve nonce → user (single-use)
	user := s.nonceStore.Consume(authFrame.Token)
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

	// Load or create session
	sessionID, history := s.loadOrCreateChatSession(user.Sub)
	toolScopes, commanderFID, commanderErr := s.resolveKaineChatCapabilities(r.Context(), user)
	if commanderErr != nil {
		s.logger.Warn(fmt.Sprintf(
			"chat_commander_identity_unavailable user=%s: %v",
			user.Sub,
			commanderErr,
		))
	}

	session := &chatSession{
		conn:         conn,
		user:         user,
		sessionID:    sessionID,
		history:      history,
		debugMode:    user.CanAccessChatDebug(),
		toolScopes:   toolScopes,
		commanderFID: commanderFID,
		done:         make(chan struct{}),
		lastActive:   time.Now(),
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

	historyForAPI := append([]llm.Message(nil), session.history...)

	// Add user message to history
	userMsg := llm.Message{
		Role:      "user",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	session.history = append(session.history, userMsg)

	// Persist user message to store
	updated, err := s.llmStore.AppendMessage(session.sessionID, userMsg)
	if err != nil {
		s.logger.Error(fmt.Sprintf("chat_message_persist_error user=%s session=%s", session.user.Sub, session.sessionID), err)
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "Your message could not be stored. Please retry.",
			Error:   true,
		})
		return
	}
	session.history = updated.Messages

	// Legacy display history seeds sessions that predate provider context.
	if len(historyForAPI) > 20 {
		historyForAPI = historyForAPI[len(historyForAPI)-20:]
	}
	providerContext, err := loadProviderContext(s.llmStore, session.sessionID, "anthropic")
	if err != nil {
		s.logger.Error(fmt.Sprintf("chat_context_load_error user=%s session=%s", session.user.Sub, session.sessionID), err)
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "Conversation context could not be loaded. Please retry.",
			Error:   true,
		})
		return
	}

	// Send thinking indicator
	session.send(ChatWSMessage{
		Type:    ChatWSTypeThinking,
		Content: "Processing your question...",
	})

	// Set up context with authorization. Tool capabilities compose from all
	// Authentik groups: Kaine groups provide galaxy/mining tools, kaine-god
	// adds operations tools, and an edin-copilot group adds the commander's
	// own journal tools. ScopeKaineChat is guaranteed here because the
	// endpoint middleware has already admitted the user.
	ctx := assistant.WithContext(context.Background(), session.sessionID, session.user.Sub)
	ctx = authz.ContextWithScopes(ctx, session.toolScopes...)
	if session.commanderFID != "" {
		ctx = tools.WithCommanderFID(ctx, session.commanderFID)
	}

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
	result, err := s.kaineRunner.RunWithProgressContext(ctx, historyForAPI, providerContext, content, onProgress)

	if err != nil {
		s.logger.Error(fmt.Sprintf("chat_run_error user=%s session=%s", session.user.Sub, session.sessionID), err)
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "I had trouble answering that. Please retry.",
			Error:   true,
		})
		return
	}
	reply := result.Text

	// Add assistant reply to history and persist
	assistantMsg := llm.Message{
		Role:      "assistant",
		Content:   reply,
		CreatedAt: time.Now().UTC(),
	}
	updated, err = commitAssistantTurn(s.llmStore, session.sessionID, assistantMsg, result.ProviderContext)
	if err != nil {
		s.logger.Error(fmt.Sprintf("chat_reply_persist_error user=%s session=%s", session.user.Sub, session.sessionID), err)
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "The answer was generated but could not be stored. Please retry.",
			Error:   true,
		})
		return
	}
	session.history = updated.Messages

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

	s.logger.Info(fmt.Sprintf(
		"chat_complete user=%s session=%s duration=%s input_tokens=%d output_tokens=%d compactions=%d reply=\"%s\"",
		session.user.Sub,
		session.sessionID,
		time.Since(start),
		result.Usage.InputTokens,
		result.Usage.OutputTokens,
		result.Usage.CompactionIterations,
		truncate(reply, 200),
	))
}

func kaineChatScopesForGroups(groups []string) []authz.Scope {
	scopes := authz.ScopesForGroups(groups)
	if authz.Allow(scopes, authz.ScopeKaineChat) {
		return scopes
	}
	return append(scopes, authz.ScopeKaineChat)
}

func withoutScope(scopes []authz.Scope, remove authz.Scope) []authz.Scope {
	out := make([]authz.Scope, 0, len(scopes))
	for _, scope := range scopes {
		if scope != remove {
			out = append(out, scope)
		}
	}
	return out
}

func (s *Server) resolveKaineChatCapabilities(ctx context.Context, user *KaineUser) ([]authz.Scope, string, error) {
	scopes := kaineChatScopesForGroups(user.Groups)
	if !authz.Allow(scopes, authz.ScopeCommanderData) {
		return scopes, "", nil
	}

	fid, err := s.resolveKaineCommanderFID(ctx, user)
	if err != nil {
		return withoutScope(scopes, authz.ScopeCommanderData), "", err
	}
	return scopes, fid, nil
}

func (s *Server) resolveKaineCommanderFID(ctx context.Context, user *KaineUser) (string, error) {
	if user == nil || user.Username == "" {
		return "", fmt.Errorf("authenticated username claim missing")
	}
	if s.authentikClient == nil {
		return "", fmt.Errorf("Authentik API unavailable")
	}
	lookup, ok := s.commanderRepo.(store.CommanderAuthentikLookup)
	if !ok {
		return "", fmt.Errorf("commander Authentik lookup unavailable")
	}

	authentikUser, err := s.authentikClient.GetUserByUsername(ctx, user.Username)
	if err != nil {
		return "", fmt.Errorf("resolve Authentik user: %w", err)
	}
	commander, err := lookup.GetCommanderByAuthentikUserID(ctx, authentikUser.UUID)
	if err != nil {
		return "", fmt.Errorf("resolve linked commander: %w", err)
	}
	return commander.FID, nil
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
