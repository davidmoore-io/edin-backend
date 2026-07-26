package llm

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Message represents a single conversational turn.
type Message struct {
	Role            string    `json:"role"`
	Content         string    `json:"content"`
	CreatedAt       time.Time `json:"created_at"`
	ClientMessageID string    `json:"client_message_id,omitempty"`
	InReplyTo       string    `json:"in_reply_to,omitempty"`
}

// Session represents an LLM conversation context.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  []Message `json:"messages"`
}

// SessionSummary provides a lightweight view of a session for listing.
type SessionSummary struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	Preview      string    `json:"preview"` // First user message content (truncated)
}

// ProviderContext is opaque model-facing conversation state. Messages contain
// complete provider message objects, including typed tool and compaction blocks.
// It is persisted separately from the display transcript and is never serialized
// to chat clients.
type ProviderContext struct {
	Provider string            `json:"provider"`
	Version  int               `json:"version"`
	Messages []json.RawMessage `json:"messages"`
}

// SessionBackend defines the behaviour expected from a conversation store backend.
type SessionBackend interface {
	CreateSession(userID string, initialMessages ...Message) *Session
	AppendMessage(sessionID string, msg Message) (*Session, error)
	Get(id string) (*Session, bool)
	Delete(id string)
	Cleanup()
}

// IdempotentSessionBackend atomically appends a client-authored message once.
// The bool is true only when this call appended the message.
type IdempotentSessionBackend interface {
	AppendMessageOnce(sessionID string, msg Message) (*Session, bool, error)
}

// ProviderContextBackend persists model-facing context separately from the
// bounded display transcript.
type ProviderContextBackend interface {
	GetProviderContext(sessionID, provider string) (ProviderContext, bool, error)
	CommitAssistantTurn(sessionID string, msg Message, context ProviderContext) (*Session, error)
}

// MultiSessionBackend extends SessionBackend with multi-session per user support.
type MultiSessionBackend interface {
	SessionBackend
	ListUserSessions(userID string) ([]SessionSummary, error)
	GetActiveSession(userID string) (*Session, error)
	SetActiveSession(userID, sessionID string) error
}

// InMemoryStore maintains sessions in memory with TTL semantics.
type InMemoryStore struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	messageIDs map[string]map[string]struct{}
	contexts   map[string]map[string]ProviderContext
	ttl        time.Duration
	maxMsgs    int
}

// Store is kept as an alias for backwards compatibility.
type Store = InMemoryStore

// NewStore creates a new in-memory store (legacy helper).
func NewStore(ttl time.Duration) *InMemoryStore {
	return NewInMemoryStore(ttl)
}

// NewInMemoryStore creates a new in-memory store with the provided TTL.
func NewInMemoryStore(ttl time.Duration) *InMemoryStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &InMemoryStore{
		sessions:   make(map[string]*Session),
		messageIDs: make(map[string]map[string]struct{}),
		contexts:   make(map[string]map[string]ProviderContext),
		ttl:        ttl,
	}
}

// CreateSession initialises a new session for the provided user.
func (s *InMemoryStore) CreateSession(userID string, initialMessages ...Message) *Session {
	session := newSession(userID, initialMessages...)
	if s.maxMsgs > 0 && len(session.Messages) > s.maxMsgs {
		session.Messages = trimToLimit(session.Messages, s.maxMsgs)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	s.messageIDs[session.ID] = clientMessageIDs(session.Messages)
	return cloneSession(session)
}

// AppendMessage appends a message to the session, updating its last-used timestamp.
func (s *InMemoryStore) AppendMessage(sessionID string, msg Message) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok || s.expired(session) {
		delete(s.sessions, sessionID)
		delete(s.messageIDs, sessionID)
		delete(s.contexts, sessionID)
		return nil, errors.New("session not found or expired")
	}

	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	session.Messages = append(session.Messages, msg)
	session.UpdatedAt = msg.CreatedAt
	if s.maxMsgs > 0 && len(session.Messages) > s.maxMsgs {
		session.Messages = trimToLimit(session.Messages, s.maxMsgs)
	}

	return cloneSession(session), nil
}

// AppendMessageOnce appends a message unless its client message ID is already
// present in the session. Messages without an ID retain normal append behavior.
func (s *InMemoryStore) AppendMessageOnce(sessionID string, msg Message) (*Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok || s.expired(session) {
		delete(s.sessions, sessionID)
		delete(s.messageIDs, sessionID)
		delete(s.contexts, sessionID)
		return nil, false, errors.New("session not found or expired")
	}

	if msg.ClientMessageID != "" {
		ids := s.messageIDs[sessionID]
		if _, exists := ids[msg.ClientMessageID]; exists {
			return cloneSession(session), false, nil
		}
		if ids == nil {
			ids = make(map[string]struct{})
			s.messageIDs[sessionID] = ids
		}
		ids[msg.ClientMessageID] = struct{}{}
	}

	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	session.Messages = append(session.Messages, msg)
	session.UpdatedAt = msg.CreatedAt
	if s.maxMsgs > 0 && len(session.Messages) > s.maxMsgs {
		session.Messages = trimToLimit(session.Messages, s.maxMsgs)
	}

	return cloneSession(session), true, nil
}

// GetProviderContext returns a deep copy of provider-specific model context.
func (s *InMemoryStore) GetProviderContext(sessionID, provider string) (ProviderContext, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok || s.expired(session) {
		return ProviderContext{}, false, nil
	}
	context, ok := s.contexts[sessionID][provider]
	if !ok {
		return ProviderContext{}, false, nil
	}
	return cloneProviderContext(context), true, nil
}

// CommitAssistantTurn atomically appends the display message and replaces the
// corresponding provider context.
func (s *InMemoryStore) CommitAssistantTurn(sessionID string, msg Message, context ProviderContext) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok || s.expired(session) {
		delete(s.sessions, sessionID)
		delete(s.messageIDs, sessionID)
		delete(s.contexts, sessionID)
		return nil, errors.New("session not found or expired")
	}
	if context.Provider == "" || context.Version <= 0 {
		return nil, errors.New("invalid provider context")
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	session.Messages = append(session.Messages, msg)
	session.UpdatedAt = msg.CreatedAt
	if s.maxMsgs > 0 && len(session.Messages) > s.maxMsgs {
		session.Messages = trimToLimit(session.Messages, s.maxMsgs)
	}
	if s.contexts[sessionID] == nil {
		s.contexts[sessionID] = make(map[string]ProviderContext)
	}
	s.contexts[sessionID][context.Provider] = cloneProviderContext(context)
	return cloneSession(session), nil
}

func (s *InMemoryStore) upsertProviderContext(sessionID string, context ProviderContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contexts[sessionID] == nil {
		s.contexts[sessionID] = make(map[string]ProviderContext)
	}
	s.contexts[sessionID][context.Provider] = cloneProviderContext(context)
}

// Get retrieves a session by ID if it exists and is not expired.
func (s *InMemoryStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok || s.expired(session) {
		if ok {
			s.Delete(id)
		}
		return nil, false
	}
	return cloneSession(session), true
}

// Delete removes a session from the store.
func (s *InMemoryStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	delete(s.messageIDs, id)
	delete(s.contexts, id)
}

// Cleanup removes expired sessions.
func (s *InMemoryStore) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		if s.expired(session) {
			delete(s.sessions, id)
			delete(s.messageIDs, id)
			delete(s.contexts, id)
		}
	}
}

// UpsertSession inserts or replaces the provided session snapshot.
func (s *InMemoryStore) UpsertSession(session *Session) {
	if session == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maxMsgs > 0 && len(session.Messages) > s.maxMsgs {
		session.Messages = trimToLimit(session.Messages, s.maxMsgs)
	}
	s.sessions[session.ID] = cloneSession(session)
	ids := s.messageIDs[session.ID]
	if ids == nil {
		ids = make(map[string]struct{})
	}
	for id := range clientMessageIDs(session.Messages) {
		ids[id] = struct{}{}
	}
	s.messageIDs[session.ID] = ids
}

// SetMaxMessages configures the maximum stored messages per session (0 disables the limit).
func (s *InMemoryStore) SetMaxMessages(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxMsgs = limit
	for id, session := range s.sessions {
		if s.maxMsgs > 0 && len(session.Messages) > s.maxMsgs {
			session.Messages = trimToLimit(session.Messages, s.maxMsgs)
			s.sessions[id] = session
		}
	}
}

func (s *InMemoryStore) expired(session *Session) bool {
	if session == nil {
		return true
	}
	expiry := session.UpdatedAt.Add(s.ttl)
	return time.Now().UTC().After(expiry)
}

func newSession(userID string, initialMessages ...Message) *Session {
	now := time.Now().UTC()
	session := &Session{
		ID:        uuid.NewString(),
		UserID:    userID,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  make([]Message, 0, max(len(initialMessages), 4)),
	}

	for _, msg := range initialMessages {
		if msg.CreatedAt.IsZero() {
			msg.CreatedAt = now
		}
		session.Messages = append(session.Messages, msg)
		session.UpdatedAt = msg.CreatedAt
	}
	return session
}

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}
	copySession := *session
	copySession.Messages = append([]Message(nil), session.Messages...)
	return &copySession
}

func cloneProviderContext(context ProviderContext) ProviderContext {
	copyContext := context
	copyContext.Messages = make([]json.RawMessage, len(context.Messages))
	for i, message := range context.Messages {
		copyContext.Messages[i] = append(json.RawMessage(nil), message...)
	}
	return copyContext
}

func trimToLimit(messages []Message, limit int) []Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	return append([]Message(nil), messages[len(messages)-limit:]...)
}

func clientMessageIDs(messages []Message) map[string]struct{} {
	ids := make(map[string]struct{})
	for _, message := range messages {
		if message.ClientMessageID != "" {
			ids[message.ClientMessageID] = struct{}{}
		}
	}
	return ids
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
