package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/tools"
)

// copilotChatSession holds the state for a single copilot WebSocket connection.
//
// It is intentionally a separate type from Kaine's chatSession. Both have similar
// shape — a connection, an identified user, a chat history, a write mutex — but
// they serve different products with different auth models, so a shared type
// would silently couple them (see CommanderChatUser for the full reasoning).
//
// debugMode is currently always false for copilot sessions. A future feature
// flag or per-commander toggle can flip this on for internal debugging UI; for
// now, commanders never see tool-internal JSON.
type copilotChatSession struct {
	conn       *websocket.Conn
	user       *CommanderChatUser
	sessionID  string
	history    []llm.Message
	debugMode  bool
	writeMu    sync.Mutex
	done       chan struct{}
	lastActive time.Time
}

// send serialises a ChatWSMessage and writes it to the WebSocket under the
// session write mutex. Debug-only fields (ToolInput / ToolOutput) are stripped
// for non-debug sessions — redundant today since debugMode is always false, but
// the guard stays so adding the feature flag later is a one-line change.
func (cs *copilotChatSession) send(msg ChatWSMessage) error {
	cs.writeMu.Lock()
	defer cs.writeMu.Unlock()

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	if !cs.debugMode {
		msg.ToolInput = nil
		msg.ToolOutput = nil
	}

	cs.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return cs.conn.WriteJSON(msg)
}

// handleCopilotChatWebSocket handles the WebSocket connection for Copilot chat.
//
// Auth is performed via a first-message auth frame: the server upgrades freely,
// then waits cfg.Copilot.WSAuthTimeout for {"type":"auth","token":"<nonce>"}.
// Closes 4401 on timeout, 4403 on invalid or expired nonce.
//
// There is no secondary role check. The nonce is only issuable by GET
// /api/commander/auth/token, which requires a validated commander JWT — that
// is the authorization point. Any nonce resolvable from commanderNonceStore
// represents a commander who successfully completed Frontier OAuth.
func (s *Server) handleCopilotChatWebSocket(w http.ResponseWriter, r *http.Request) {
	// Check copilot runner first (before upgrade — cheaper than a WS handshake).
	if s.copilotRunner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "copilot service not available")
		return
	}

	// Upgrade to WebSocket.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("copilot_ws upgrade failed", err)
		return
	}
	defer conn.Close()

	// --- Auth via first-message frame ---
	copilotCfg := s.cfg.Copilot
	conn.SetReadDeadline(time.Now().Add(copilotCfg.WSAuthTimeout))
	_, authMsg, err := conn.ReadMessage()
	if err != nil {
		// Timeout or client closed — do not reconnect.
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

	// Resolve nonce → commander (single-use).
	user := s.commanderNonceStore.Consume(authFrame.Token)
	if user == nil {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(4403, "invalid or expired nonce"))
		return
	}

	// Reset read deadline for normal operation.
	conn.SetReadDeadline(time.Now().Add(copilotCfg.WSReadDeadline))
	// --- End auth ---

	// Build a per-session runner with the commander's personalised system prompt.
	// WithSystemPrompt creates a lightweight copy sharing the same client and executor.
	sessionRunner := s.copilotRunner.WithSystemPrompt(CopilotSystemPrompt(user.Name))

	// Load or create session keyed on the commander FID.
	sessionID, history := s.loadOrCreateChatSession(user.FID)

	session := &copilotChatSession{
		conn:       conn,
		user:       user,
		sessionID:  sessionID,
		history:    history,
		debugMode:  false, // commanders never see debug output today
		done:       make(chan struct{}),
		lastActive: time.Now(),
	}

	s.logger.Info(fmt.Sprintf("copilot_ws connected fid=%s session=%s history=%d", user.FID, sessionID, len(history)))

	// Track active session count.
	wsm := initEdinMetrics()
	wsm.copilotChatSessionsActive.Inc()
	defer wsm.copilotChatSessionsActive.Dec()

	// Send connected message.
	session.send(ChatWSMessage{
		Type:      ChatWSTypeConnected,
		SessionID: sessionID,
		DebugMode: session.debugMode,
	})

	// Send chat history if we have any.
	if len(history) > 0 {
		session.send(ChatWSMessage{
			Type:      ChatWSTypeChatHistory,
			SessionID: sessionID,
			Messages:  history,
		})
	}

	// Configure connection.
	conn.SetReadLimit(copilotCfg.WSReadLimitBytes)
	conn.SetReadDeadline(time.Now().Add(copilotCfg.WSReadDeadline))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(copilotCfg.WSReadDeadline))
		return nil
	})

	// Start ping ticker.
	pingTicker := time.NewTicker(copilotCfg.WSPingInterval)
	defer pingTicker.Stop()

	// Ping goroutine.
	go func() {
		for {
			select {
			case <-pingTicker.C:
				session.writeMu.Lock()
				conn.SetWriteDeadline(time.Now().Add(copilotCfg.WSWriteDeadline))
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

	// Main read loop.
	for {
		select {
		case <-session.done:
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.logger.Warn(fmt.Sprintf("copilot_ws read error fid=%s session=%s: %v", user.FID, sessionID, err))
			}
			close(session.done)
			s.logger.Info(fmt.Sprintf("copilot_ws disconnected fid=%s session=%s", user.FID, sessionID))
			return
		}

		// Reset read deadline.
		conn.SetReadDeadline(time.Now().Add(copilotCfg.WSReadDeadline))
		session.lastActive = time.Now()

		// Parse incoming message.
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
			s.handleCopilotNewChat(session)
		case "switch_session":
			if incoming.SessionID != "" {
				s.handleCopilotSwitchSession(session, incoming.SessionID)
			}
		}
	}
}

// handleCopilotNewChat creates a fresh session for the commander and signals
// the client to clear its in-memory history.
func (s *Server) handleCopilotNewChat(session *copilotChatSession) {
	newStoreSession := s.llmStore.CreateSession(session.user.FID)
	session.sessionID = newStoreSession.ID
	session.history = make([]llm.Message, 0)

	s.logger.Info(fmt.Sprintf("copilot_new_chat fid=%s session=%s", session.user.FID, session.sessionID))

	session.send(ChatWSMessage{
		Type:      ChatWSTypeChatCleared,
		SessionID: session.sessionID,
	})
}

// handleCopilotSwitchSession switches the live WebSocket to a different stored
// session belonging to the same commander. SetActiveSession on the store
// enforces ownership — a commander cannot switch to another commander's
// session.
func (s *Server) handleCopilotSwitchSession(session *copilotChatSession, targetSessionID string) {
	ms, ok := s.llmStore.(llm.MultiSessionBackend)
	if !ok {
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "session switching not available",
			Error:   true,
		})
		return
	}

	if err := ms.SetActiveSession(session.user.FID, targetSessionID); err != nil {
		s.logger.Warn(fmt.Sprintf("copilot_switch_session denied fid=%s target=%s: %v", session.user.FID, targetSessionID, err))
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "cannot switch to that session",
			Error:   true,
		})
		return
	}

	targetSession, ok2 := s.llmStore.Get(targetSessionID)
	if !ok2 || targetSession == nil {
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "session not found",
			Error:   true,
		})
		return
	}

	session.sessionID = targetSessionID
	session.history = targetSession.Messages

	s.logger.Info(fmt.Sprintf("copilot_switch_session fid=%s session=%s messages=%d", session.user.FID, targetSessionID, len(targetSession.Messages)))

	session.send(ChatWSMessage{
		Type:      ChatWSTypeChatHistory,
		SessionID: targetSessionID,
		Messages:  targetSession.Messages,
	})
}

// handleCopilotMessage processes a user message for the copilot and streams the response.
func (s *Server) handleCopilotMessage(session *copilotChatSession, content string, sessionRunner *assistant.Runner) {
	s.logger.Info(fmt.Sprintf("copilot_message fid=%s session=%s message=\"%s\"", session.user.FID, session.sessionID, truncate(content, 160)))

	// Add user message to history.
	userMsg := llm.Message{
		Role:      "user",
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	session.history = append(session.history, userMsg)

	// Persist user message to store.
	s.llmStore.AppendMessage(session.sessionID, userMsg)

	// Trim in-memory history for the API call.
	historyLimit := s.cfg.Copilot.MessageHistoryLimit
	if historyLimit <= 0 {
		historyLimit = 20
	}
	historyForAPI := session.history
	if len(historyForAPI) > historyLimit {
		historyForAPI = historyForAPI[len(historyForAPI)-historyLimit:]
	}

	// Send thinking indicator.
	session.send(ChatWSMessage{
		Type:    ChatWSTypeThinking,
		Content: "Processing your question...",
	})

	// Set up context with copilot authorization scope and commander FID for tools.
	// FID comes exclusively from the validated JWT (via the nonce → CommanderChatUser
	// pipeline) — never from request body or LLM tool input.
	ctx := assistant.WithContext(context.Background(), session.sessionID, session.user.FID)
	ctx = authz.ContextWithScopes(ctx, authz.ScopeCopilotChat)
	ctx = tools.WithCommanderFID(ctx, session.user.FID)

	// Create progress callback that streams to WebSocket.
	pm := initEdinMetrics()
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
			// Count tool calls on completion so each invocation is counted once.
			pm.copilotToolCallsTotal.WithLabelValues(event.ToolName).Inc()
			session.send(ChatWSMessage{
				Type:     ChatWSTypeToolComplete,
				ToolName: event.ToolName,
				Content:  event.Message,
				Duration: event.Message,
				Error:    event.Error,
			})
		}
	}

	// Run the copilot with per-session runner (includes commander tools).
	start := time.Now()
	reply, err := sessionRunner.RunWithProgress(ctx, historyForAPI, content, onProgress)

	if err != nil {
		s.logger.Error(fmt.Sprintf("copilot_run_error fid=%s session=%s", session.user.FID, session.sessionID), err)
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: fmt.Sprintf("Error processing message: %v", err),
			Error:   true,
		})
		return
	}

	// Add assistant reply to history and persist.
	assistantMsg := llm.Message{
		Role:      "assistant",
		Content:   reply,
		CreatedAt: time.Now().UTC(),
	}
	session.history = append(session.history, assistantMsg)
	s.llmStore.AppendMessage(session.sessionID, assistantMsg)

	// Send final response.
	session.send(ChatWSMessage{
		Type:     ChatWSTypeText,
		Content:  reply,
		Duration: time.Since(start).Round(time.Millisecond).String(),
	})

	// Send done signal.
	session.send(ChatWSMessage{
		Type:      ChatWSTypeDone,
		SessionID: session.sessionID,
	})

	s.logger.Info(fmt.Sprintf("copilot_complete fid=%s session=%s duration=%s reply=\"%s\"", session.user.FID, session.sessionID, time.Since(start), truncate(reply, 200)))
}
