package llm

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Message represents a single conversational turn.
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
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

// SessionBackend defines the behaviour expected from a conversation store backend.
type SessionBackend interface {
	CreateSession(userID string, initialMessages ...Message) *Session
	AppendMessage(sessionID string, msg Message) (*Session, error)
	Get(id string) (*Session, bool)
	Delete(id string)
	Cleanup()
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
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
	maxMsgs  int
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
		sessions: make(map[string]*Session),
		ttl:      ttl,
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
	return cloneSession(session)
}

// AppendMessage appends a message to the session, updating its last-used timestamp.
func (s *InMemoryStore) AppendMessage(sessionID string, msg Message) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok || s.expired(session) {
		delete(s.sessions, sessionID)
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
}

// Cleanup removes expired sessions.
func (s *InMemoryStore) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		if s.expired(session) {
			delete(s.sessions, id)
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

func trimToLimit(messages []Message, limit int) []Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	return append([]Message(nil), messages[len(messages)-limit:]...)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
