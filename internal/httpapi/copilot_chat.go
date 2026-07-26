package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/edin-space/edin-backend/internal/assistant"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/copilot"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/tools"
	"github.com/edin-space/edin-backend/internal/voice"
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
	stateMu    sync.Mutex
	turnActive bool
}

func (cs *copilotChatSession) beginTurn() bool {
	cs.stateMu.Lock()
	defer cs.stateMu.Unlock()
	if cs.turnActive {
		return false
	}
	cs.turnActive = true
	return true
}

func (cs *copilotChatSession) endTurn() {
	cs.stateMu.Lock()
	cs.turnActive = false
	cs.stateMu.Unlock()
}

func (cs *copilotChatSession) hasActiveTurn() bool {
	cs.stateMu.Lock()
	defer cs.stateMu.Unlock()
	return cs.turnActive
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

	// Build a per-session runner. System prompt is assembled from persona×mode templates;
	// voice persona is not yet known at connection time (it arrives per-message), so we
	// use the default system prompt here. handleCopilotMessage overwrites it per-turn.
	sessionRunner := s.copilotRunner.WithSystemPrompt(s.assemblePrompt("the_mind", "standard", user.Name))

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
				if err := session.send(ChatWSMessage{Type: ChatWSTypeHeartbeat}); err != nil {
					return
				}
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
			Type            string       `json:"type"`
			Content         string       `json:"content"`
			ImageData       string       `json:"image_data"`
			SessionID       string       `json:"session_id"`
			ClientMessageID string       `json:"client_message_id"`
			Voice           *VoiceConfig `json:"voice"`
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
			if incoming.Content != "" || incoming.ImageData != "" {
				s.acceptCopilotMessage(session, incoming.ClientMessageID, incoming.Content,
					incoming.ImageData, sessionRunner, incoming.Voice)
			}
		case "new_chat":
			if session.hasActiveTurn() {
				session.send(ChatWSMessage{
					Type:    ChatWSTypeError,
					Content: "wait for the current response before starting a new chat",
					Error:   true,
				})
			} else {
				s.handleCopilotNewChat(session)
			}
		case "switch_session":
			if incoming.SessionID != "" {
				if session.hasActiveTurn() {
					session.send(ChatWSMessage{
						Type:    ChatWSTypeError,
						Content: "wait for the current response before switching chats",
						Error:   true,
					})
				} else {
					s.handleCopilotSwitchSession(session, incoming.SessionID)
				}
			}
		case "sync_session":
			s.handleCopilotSyncSession(session)
		}
	}
}

func (s *Server) acceptCopilotMessage(
	session *copilotChatSession,
	clientMessageID string,
	content string,
	imageData string,
	sessionRunner *assistant.Runner,
	voiceCfg *VoiceConfig,
) {
	if _, err := uuid.Parse(clientMessageID); err != nil {
		session.send(ChatWSMessage{
			Type:            ChatWSTypeError,
			Content:         "invalid or missing client message id",
			ClientMessageID: clientMessageID,
			Error:           true,
		})
		return
	}
	if !session.beginTurn() {
		session.send(ChatWSMessage{
			Type:            ChatWSTypeError,
			Content:         "another message is still being processed",
			ClientMessageID: clientMessageID,
			Error:           true,
		})
		return
	}

	storedContent := content
	if storedContent == "" && imageData != "" {
		storedContent = "[image attached]"
	}
	userMsg := llm.Message{
		Role:            "user",
		Content:         storedContent,
		CreatedAt:       time.Now().UTC(),
		ClientMessageID: clientMessageID,
	}

	store, ok := s.llmStore.(llm.IdempotentSessionBackend)
	if !ok {
		session.endTurn()
		session.send(ChatWSMessage{
			Type:            ChatWSTypeError,
			Content:         "chat persistence does not support reliable delivery",
			ClientMessageID: clientMessageID,
			Error:           true,
		})
		return
	}

	updated, appended, err := store.AppendMessageOnce(session.sessionID, userMsg)
	if err != nil {
		session.endTurn()
		s.logger.Error(fmt.Sprintf("copilot_message_persist_error fid=%s session=%s", session.user.FID, session.sessionID), err)
		session.send(ChatWSMessage{
			Type:            ChatWSTypeError,
			Content:         "message could not be stored",
			ClientMessageID: clientMessageID,
			Error:           true,
		})
		return
	}

	session.stateMu.Lock()
	session.history = updated.Messages
	historyForAPI := append([]llm.Message(nil), updated.Messages...)
	session.stateMu.Unlock()

	session.send(ChatWSMessage{
		Type:            ChatWSTypeMessageAck,
		SessionID:       session.sessionID,
		ClientMessageID: clientMessageID,
		Duplicate:       !appended,
	})

	if !appended {
		session.endTurn()
		session.send(ChatWSMessage{
			Type:      ChatWSTypeChatHistory,
			SessionID: session.sessionID,
			Messages:  updated.Messages,
		})
		return
	}

	if len(historyForAPI) > 0 {
		historyForAPI = historyForAPI[:len(historyForAPI)-1]
	}
	go func() {
		defer session.endTurn()
		s.handleCopilotMessage(session, clientMessageID, content, imageData,
			historyForAPI, sessionRunner, voiceCfg)
	}()
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

// handleCopilotSyncSession returns the latest durable history for the current
// session without changing the commander's active-session pointer. The client
// uses it after reconnecting while an accepted turn may still be finishing on
// the server.
func (s *Server) handleCopilotSyncSession(session *copilotChatSession) {
	current, ok := s.llmStore.Get(session.sessionID)
	if !ok || current == nil || current.UserID != session.user.FID {
		session.send(ChatWSMessage{
			Type:    ChatWSTypeError,
			Content: "current chat session is not available",
			Error:   true,
		})
		return
	}
	session.stateMu.Lock()
	session.history = current.Messages
	session.stateMu.Unlock()
	session.send(ChatWSMessage{
		Type:      ChatWSTypeChatHistory,
		SessionID: session.sessionID,
		Messages:  current.Messages,
	})
}

// handleCopilotMessage processes a user message for the copilot and streams the response.
// splitImageDataURI parses a "data:image/png;base64,XXXX" data URI into the raw
// base64 payload and the media type (e.g. "image/png"). If s is not a data URI,
// it is returned unchanged with a PNG default (the client encodes as PNG).
func splitImageDataURI(s string) (b64 string, mediaType string) {
	mediaType = "image/png"
	if !strings.HasPrefix(s, "data:") {
		return s, mediaType
	}
	if semi := strings.IndexByte(s, ';'); semi > len("data:") {
		mediaType = s[len("data:"):semi]
	}
	if i := strings.Index(s, "base64,"); i >= 0 {
		return s[i+len("base64,"):], mediaType
	}
	return s, mediaType
}

func (s *Server) handleCopilotMessage(
	session *copilotChatSession,
	clientMessageID string,
	content string,
	imageData string,
	historyForAPI []llm.Message,
	sessionRunner *assistant.Runner,
	voiceCfg *VoiceConfig,
) {
	s.logger.Info(fmt.Sprintf("copilot_message fid=%s session=%s message=\"%s\" image=%t", session.user.FID, session.sessionID, truncate(content, 160), imageData != ""))

	// RunWithStreaming appends the current user message itself, so the accepted
	// user turn was removed from this snapshot by acceptCopilotMessage.
	historyLimit := s.cfg.Copilot.MessageHistoryLimit
	if historyLimit <= 0 {
		historyLimit = 20
	}
	if len(historyForAPI) > historyLimit {
		historyForAPI = historyForAPI[len(historyForAPI)-historyLimit:]
	}
	providerContext, contextErr := loadProviderContext(s.llmStore, session.sessionID, "anthropic")
	if contextErr != nil {
		s.logger.Error(fmt.Sprintf("copilot_context_load_error fid=%s session=%s", session.user.FID, session.sessionID), contextErr)
		session.send(ChatWSMessage{
			Type:            ChatWSTypeError,
			Content:         "Conversation context could not be loaded. Please retry.",
			ClientMessageID: clientMessageID,
			Error:           true,
		})
		return
	}

	// Decode the optional attached image (client sends a "data:image/...;base64,"
	// data URI). nil when no image was attached.
	var image *assistant.ImageInput
	if imageData != "" {
		b64, mediaType := splitImageDataURI(imageData)
		image = &assistant.ImageInput{Base64: b64, MediaType: mediaType}
	}

	// Send thinking indicator.
	session.send(ChatWSMessage{
		Type:            ChatWSTypeThinking,
		Content:         "Processing your question...",
		ClientMessageID: clientMessageID,
	})

	// Set up context with copilot authorization scopes and commander FID for
	// tools. FID comes exclusively from the validated JWT (via the nonce →
	// CommanderChatUser pipeline) — never from request body or LLM tool input.
	//
	// Scopes come from the CommanderChatUser attached to the consumed nonce.
	// They were populated at token-issue time in handleCommanderAuthToken;
	// the scopes claim wiring stays unchanged here.
	ctx := assistant.WithContext(context.Background(), session.sessionID, session.user.FID)
	ctx = authz.ContextWithScopes(ctx, session.user.Scopes...)
	ctx = tools.WithCommanderFID(ctx, session.user.FID)

	// Resolve persona and mode from the incoming voice config, with safe defaults.
	persona, mode := "the_mind", "standard"
	if voiceCfg != nil {
		if voiceCfg.Persona != "" {
			persona = voiceCfg.Persona
		}
		if voiceCfg.Mode != "" {
			mode = voiceCfg.Mode
		}
	}

	// Assemble per-turn system prompt from persona×mode templates.
	systemPrompt := s.assemblePrompt(persona, mode, session.user.Name)
	turnRunner := sessionRunner.WithSystemPrompt(systemPrompt)

	// Set up voice session if voice is enabled and ElevenLabs is configured.
	voiceEnabled := voiceCfg != nil && voiceCfg.VoiceEnabled && s.cfg.ElevenLabs.APIKey != ""

	var vs *voice.VoiceSession
	audioCh := make(chan []byte, 100)

	if voiceEnabled {
		voiceID := s.cfg.ElevenLabs.Voices.ForPersona(persona)
		s.logger.Info(fmt.Sprintf("voice_session_start fid=%s persona=%s voice_id=%s key_len=%d key_prefix=%s", session.user.FID, persona, voiceID, len(s.cfg.ElevenLabs.APIKey), s.cfg.ElevenLabs.APIKey[:min(8, len(s.cfg.ElevenLabs.APIKey))]))
		elCfg := voice.ElevenLabsConfig{
			APIKey:  s.cfg.ElevenLabs.APIKey,
			VoiceID: voiceID,
			ModelID: "eleven_flash_v2_5",
			Format:  "mp3_44100_128",
		}
		var vsErr error
		vs, vsErr = voice.NewVoiceSession(ctx, elCfg, audioCh)
		if vsErr != nil {
			s.logger.Warn(fmt.Sprintf("voice_session_start_failed fid=%s: %v — continuing without voice", session.user.FID, vsErr))
			voiceEnabled = false
		} else {
			s.logger.Info(fmt.Sprintf("voice_session_ready fid=%s", session.user.FID))
			defer vs.Dispose()
			// Forward audio chunks to the client under the session write mutex.
			// copilotChatSession.send holds writeMu internally, so concurrent calls
			// from this goroutine and the main handler are safe.
			go func() {
				for chunk := range audioCh {
					session.send(ChatWSMessage{ //nolint:errcheck
						Type:            ChatWSTypeAudioChunk,
						AudioData:       base64.StdEncoding.EncodeToString(chunk),
						ClientMessageID: clientMessageID,
					})
				}
			}()
		}
	}

	// Create progress callback that streams to WebSocket.
	pm := initEdinMetrics()
	onProgress := func(event assistant.ProgressEvent) {
		switch event.Type {
		case assistant.ProgressThinking:
			session.send(ChatWSMessage{
				Type:            ChatWSTypeThinking,
				Content:         event.Message,
				ClientMessageID: clientMessageID,
			})
		case assistant.ProgressToolStart:
			session.send(ChatWSMessage{
				Type:            ChatWSTypeToolStart,
				ToolName:        event.ToolName,
				ToolID:          event.ToolID,
				Content:         event.Message,
				ClientMessageID: clientMessageID,
			})
		case assistant.ProgressToolComplete:
			// Count tool calls on completion so each invocation is counted once.
			pm.copilotToolCallsTotal.WithLabelValues(event.ToolName).Inc()
			session.send(ChatWSMessage{
				Type:            ChatWSTypeToolComplete,
				ToolName:        event.ToolName,
				ToolID:          event.ToolID,
				Content:         event.Message,
				Duration:        event.Message,
				Error:           event.Error,
				ClientMessageID: clientMessageID,
			})
		}
	}

	// Run the copilot with streaming. The voice session is already connected so
	// the ElevenLabs WS is ready the instant the first <speak> segment closes.
	start := time.Now()
	result, runErr := turnRunner.RunWithStreamingContext(ctx, historyForAPI, providerContext, content, image, assistant.StreamingRunnerCallbacks{
		OnTextDelta: func(string) {}, // raw tokens not forwarded; speak/data callbacks handle it
		OnSpeakChunk: func(chunk string) {
			// speak_start signals Flutter to open a fresh bubble for this segment.
			// Ignored by the client when there is no existing content (first bubble).
			session.send(ChatWSMessage{Type: ChatWSTypeSpeakStart, ClientMessageID: clientMessageID}) //nolint:errcheck
			session.send(ChatWSMessage{                                                               //nolint:errcheck
				Type:            ChatWSTypeTextDelta,
				Content:         chunk,
				Channel:         "speak",
				ClientMessageID: clientMessageID,
			})
			if voiceEnabled && vs != nil {
				vs.SendSpeakContent(chunk) //nolint:errcheck
			}
		},
		OnDataChunk: func(chunk string) {
			session.send(ChatWSMessage{ //nolint:errcheck
				Type:            ChatWSTypeTextDelta,
				Content:         chunk,
				Channel:         "data",
				ClientMessageID: clientMessageID,
			})
			// data channel NOT sent to ElevenLabs
		},
		OnProgress: onProgress,
	})
	duration := time.Since(start)

	if runErr != nil {
		s.logger.Error(fmt.Sprintf("copilot_run_error fid=%s session=%s", session.user.FID, session.sessionID), runErr)
		session.send(ChatWSMessage{
			Type:            ChatWSTypeError,
			Content:         "I had trouble answering that.",
			ClientMessageID: clientMessageID,
			Error:           true,
		})
		return
	}
	reply := result.Text

	// Store speak-only content in history — strip channel tags so that history
	// replay never shows raw <speak>/<data> markup. Data tables are transient;
	// they're not reconstructable from the stored string and aren't needed for
	// conversation context passed back to the LLM.
	speakOnly := voice.SpeakContent(voice.ParseTaggedText(reply))
	if speakOnly == "" {
		speakOnly = reply // fallback for untagged responses
	}
	assistantMsg := llm.Message{
		Role:      "assistant",
		Content:   speakOnly,
		CreatedAt: time.Now().UTC(),
		InReplyTo: clientMessageID,
	}
	updated, appendErr := commitAssistantTurn(s.llmStore, session.sessionID, assistantMsg, result.ProviderContext)
	if appendErr != nil {
		s.logger.Error(fmt.Sprintf("copilot_reply_persist_error fid=%s session=%s", session.user.FID, session.sessionID), appendErr)
		session.send(ChatWSMessage{
			Type:            ChatWSTypeError,
			Content:         "The answer was generated but could not be stored. Please retry.",
			ClientMessageID: clientMessageID,
			Error:           true,
		})
		return
	}
	session.stateMu.Lock()
	session.history = updated.Messages
	session.stateMu.Unlock()

	// Send final assembled text frame (for clients that prefer complete responses).
	session.send(ChatWSMessage{
		Type:            ChatWSTypeText,
		Content:         reply,
		Duration:        duration.Round(time.Millisecond).String(),
		ClientMessageID: clientMessageID,
	})

	// Send done signal.
	session.send(ChatWSMessage{
		Type:            ChatWSTypeDone,
		SessionID:       session.sessionID,
		ClientMessageID: clientMessageID,
	})

	s.logger.Info(fmt.Sprintf(
		"copilot_complete fid=%s session=%s duration=%s input_tokens=%d output_tokens=%d compactions=%d reply=\"%s\"",
		session.user.FID,
		session.sessionID,
		duration,
		result.Usage.InputTokens,
		result.Usage.OutputTokens,
		result.Usage.CompactionIterations,
		truncate(reply, 200),
	))
}

// assemblePrompt is a nil-safe wrapper around s.promptAssembler.Assemble.
// If the assembler is nil (e.g., in tests that construct Server directly),
// it falls back to NewDefaultAssembler so no nil-pointer panic occurs.
func (s *Server) assemblePrompt(persona, mode, commanderName string) string {
	if s.promptAssembler != nil {
		return s.promptAssembler.Assemble(persona, mode, commanderName)
	}
	return copilot.NewDefaultAssembler().Assemble(persona, mode, commanderName)
}
