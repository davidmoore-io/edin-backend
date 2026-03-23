package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/edin-space/edin-backend/internal/observability"
)

const (
	defaultRedisTTL         = 30 * time.Minute
	defaultRedisMaxMessages = 20
	defaultRedisPrefix      = "llm:sessions"
)

// RedisStore persists sessions in Redis, falling back to an in-memory store when Redis is unavailable.
type RedisStore struct {
	client      *redis.Client
	ttl         time.Duration
	maxMessages int
	prefix      string
	fallback    *InMemoryStore
	logger      *observability.Logger
	metrics     *redisMetrics
}

// RedisStoreOption allows customising RedisStore construction.
type RedisStoreOption func(*RedisStore)

// WithRedisPrefix sets a custom key prefix.
func WithRedisPrefix(prefix string) RedisStoreOption {
	return func(rs *RedisStore) {
		if prefix != "" {
			rs.prefix = prefix
		}
	}
}

// WithRedisLogger provides a custom logger.
func WithRedisLogger(logger *observability.Logger) RedisStoreOption {
	return func(rs *RedisStore) {
		if logger != nil {
			rs.logger = logger
		}
	}
}

// NewRedisStore constructs a Redis-backed store with optional in-memory fallback.
func NewRedisStore(client *redis.Client, ttl time.Duration, maxMessages int, fallback *InMemoryStore, opts ...RedisStoreOption) *RedisStore {
	if ttl <= 0 {
		ttl = defaultRedisTTL
	}
	if maxMessages <= 0 {
		maxMessages = defaultRedisMaxMessages
	}
	if fallback == nil {
		fallback = NewInMemoryStore(ttl)
	}
	fallback.SetMaxMessages(maxMessages)

	store := &RedisStore{
		client:      client,
		ttl:         ttl,
		maxMessages: maxMessages,
		prefix:      defaultRedisPrefix,
		fallback:    fallback,
		logger:      observability.NewLogger("llm.redis_store"),
		metrics:     initRedisMetrics(),
	}

	for _, opt := range opts {
		opt(store)
	}

	return store
}

// CreateSession initialises a new session. If Redis operations fail, the fallback store is used.
func (s *RedisStore) CreateSession(userID string, initialMessages ...Message) *Session {
	if s.client == nil {
		newSession := s.fallback.CreateSession(userID, initialMessages...)
		s.metrics.recordFallback("create")
		s.metrics.recordHit("create", len(newSession.Messages), "fallback")
		return newSession
	}

	session := newSession(userID, initialMessages...)
	ctx := context.Background()
	if err := s.persistSession(ctx, session); err != nil {
		s.warn("create_session", session.ID, err)
		newSession := s.fallback.CreateSession(userID, initialMessages...)
		s.metrics.recordFallback("create")
		s.metrics.recordHit("create", len(newSession.Messages), "fallback")
		return newSession
	}

	// Add to user's session index and set as active
	s.addToUserIndex(ctx, userID, session.ID, session.UpdatedAt)
	_ = s.SetActiveSession(userID, session.ID)

	s.fallback.UpsertSession(session)
	s.metrics.recordHit("create", len(session.Messages), "redis")
	return cloneSession(session)
}

// AppendMessage appends a message to the Redis session, falling back on error.
func (s *RedisStore) AppendMessage(sessionID string, msg Message) (*Session, error) {
	if s.client == nil {
		updated, err := s.fallback.AppendMessage(sessionID, msg)
		if err == nil {
			s.metrics.recordFallback("append")
			s.metrics.recordHit("append", len(updated.Messages), "fallback")
		}
		return updated, err
	}

	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	ctx := context.Background()
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}

	metaKey := s.sessionKey(sessionID)
	messagesKey := s.messagesKey(sessionID)

	pipe := s.client.TxPipeline()
	pipe.RPush(ctx, messagesKey, payload)
	if s.maxMessages > 0 {
		pipe.LTrim(ctx, messagesKey, int64(-s.maxMessages), -1)
	}
	pipe.HSet(ctx, metaKey, map[string]any{
		"updated_at": msg.CreatedAt.Format(time.RFC3339Nano),
	})
	pipe.Expire(ctx, metaKey, s.ttl)
	pipe.Expire(ctx, messagesKey, s.ttl)

	if _, err := pipe.Exec(ctx); err != nil {
		s.warn("append_message", sessionID, err)
		updated, fallbackErr := s.fallback.AppendMessage(sessionID, msg)
		if fallbackErr == nil {
			s.metrics.recordFallback("append")
			s.metrics.recordHit("append", len(updated.Messages), "fallback")
		}
		return updated, fallbackErr
	}

	session, ok := s.loadSession(ctx, sessionID)
	if !ok {
		return nil, errors.New("session not found")
	}

	// Refresh the session's position in the user's sorted set
	if session.UserID != "" {
		s.refreshUserIndex(ctx, session.UserID, sessionID, msg.CreatedAt)
	}

	s.fallback.UpsertSession(session)
	s.metrics.recordHit("append", len(session.Messages), "redis")
	return session, nil
}

// Get retrieves a session from Redis or the fallback store.
func (s *RedisStore) Get(id string) (*Session, bool) {
	if s.client == nil {
		session, ok := s.fallback.Get(id)
		if ok {
			s.metrics.recordFallback("get")
			s.metrics.recordHit("get", len(session.Messages), "fallback")
		}
		return session, ok
	}

	session, ok := s.loadSession(context.Background(), id)
	if ok {
		s.fallback.UpsertSession(session)
		s.metrics.recordHit("get", len(session.Messages), "redis")
		return session, true
	}

	session, ok = s.fallback.Get(id)
	if ok {
		s.metrics.recordFallback("get")
		s.metrics.recordHit("get", len(session.Messages), "fallback")
	}
	return session, ok
}

// Delete removes session data from Redis and fallback store.
func (s *RedisStore) Delete(id string) {
	if s.client != nil {
		ctx := context.Background()
		metaKey := s.sessionKey(id)
		messagesKey := s.messagesKey(id)
		if err := s.client.Del(ctx, metaKey, messagesKey).Err(); err != nil {
			s.warn("delete_session", id, err)
		}
	}
	s.fallback.Delete(id)
}

// Cleanup delegates to the fallback (Redis expirations happen server-side).
func (s *RedisStore) Cleanup() {
	s.fallback.Cleanup()
}

// userSessionsKey returns the Redis key for a user's session index (sorted set).
func (s *RedisStore) userSessionsKey(userID string) string {
	return fmt.Sprintf("kaine:user:%s:sessions", userID)
}

// activeSessionKey returns the Redis key for a user's active session pointer.
func (s *RedisStore) activeSessionKey(userID string) string {
	return fmt.Sprintf("kaine:user:%s:active", userID)
}

// ListUserSessions returns sessions for a user, ordered by most recently updated.
func (s *RedisStore) ListUserSessions(userID string) ([]SessionSummary, error) {
	if s.client == nil {
		return nil, nil
	}
	ctx := context.Background()

	// Get session IDs from sorted set (highest score = most recent)
	ids, err := s.client.ZRevRange(ctx, s.userSessionsKey(userID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	summaries := make([]SessionSummary, 0, len(ids))
	for _, sessionID := range ids {
		session, ok := s.loadSession(ctx, sessionID)
		if !ok {
			// Session expired or deleted — clean up the index entry
			s.client.ZRem(ctx, s.userSessionsKey(userID), sessionID)
			continue
		}
		summary := SessionSummary{
			ID:           session.ID,
			CreatedAt:    session.CreatedAt,
			UpdatedAt:    session.UpdatedAt,
			MessageCount: len(session.Messages),
		}
		for _, msg := range session.Messages {
			if msg.Role == "user" && msg.Content != "" {
				preview := msg.Content
				if len(preview) > 100 {
					preview = preview[:100] + "..."
				}
				summary.Preview = preview
				break
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// GetActiveSession returns the user's most recently active session, or nil if none.
func (s *RedisStore) GetActiveSession(userID string) (*Session, error) {
	if s.client == nil {
		return nil, nil
	}
	ctx := context.Background()

	// Check for explicit active session pointer
	activeID, err := s.client.Get(ctx, s.activeSessionKey(userID)).Result()
	if err == nil && activeID != "" {
		session, ok := s.loadSession(ctx, activeID)
		if ok {
			return session, nil
		}
		// Active pointer is stale — clean up
		s.client.Del(ctx, s.activeSessionKey(userID))
	}

	// Fallback: most recently updated session from the sorted set
	ids, err := s.client.ZRevRange(ctx, s.userSessionsKey(userID), 0, 0).Result()
	if err != nil || len(ids) == 0 {
		return nil, nil
	}

	session, ok := s.loadSession(ctx, ids[0])
	if !ok {
		return nil, nil
	}
	return session, nil
}

// SetActiveSession marks a session as active for the user.
// Returns an error if the session does not exist or does not belong to the user.
func (s *RedisStore) SetActiveSession(userID, sessionID string) error {
	if s.client == nil {
		return nil
	}
	ctx := context.Background()

	// Validate that the session exists and belongs to this user
	session, ok := s.loadSession(ctx, sessionID)
	if !ok {
		return fmt.Errorf("session not found")
	}
	if session.UserID != userID {
		return fmt.Errorf("session does not belong to user")
	}

	return s.client.Set(ctx, s.activeSessionKey(userID), sessionID, s.ttl).Err()
}

// addToUserIndex adds a session to the user's session sorted set.
func (s *RedisStore) addToUserIndex(ctx context.Context, userID, sessionID string, updatedAt time.Time) {
	if s.client == nil {
		return
	}
	s.client.ZAdd(ctx, s.userSessionsKey(userID), redis.Z{
		Score:  float64(updatedAt.UnixMilli()),
		Member: sessionID,
	})
	s.client.Expire(ctx, s.userSessionsKey(userID), s.ttl)
}

// refreshUserIndex updates the score for a session in the user's sorted set.
func (s *RedisStore) refreshUserIndex(ctx context.Context, userID, sessionID string, updatedAt time.Time) {
	if s.client == nil {
		return
	}
	s.client.ZAdd(ctx, s.userSessionsKey(userID), redis.Z{
		Score:  float64(updatedAt.UnixMilli()),
		Member: sessionID,
	})
}

func (s *RedisStore) persistSession(ctx context.Context, session *Session) error {
	metaKey := s.sessionKey(session.ID)
	messagesKey := s.messagesKey(session.ID)

	pipe := s.client.TxPipeline()
	pipe.HSet(ctx, metaKey, map[string]any{
		"user_id":    session.UserID,
		"created_at": session.CreatedAt.Format(time.RFC3339Nano),
		"updated_at": session.UpdatedAt.Format(time.RFC3339Nano),
	})

	if len(session.Messages) > 0 {
		payloads := make([]any, 0, len(session.Messages))
		for _, msg := range session.Messages {
			if msg.CreatedAt.IsZero() {
				msg.CreatedAt = session.CreatedAt
			}
			raw, err := json.Marshal(msg)
			if err != nil {
				return fmt.Errorf("marshal message: %w", err)
			}
			payloads = append(payloads, raw)
		}
		pipe.Del(ctx, messagesKey) // ensure clean slate
		pipe.RPush(ctx, messagesKey, payloads...)
		if s.maxMessages > 0 {
			pipe.LTrim(ctx, messagesKey, int64(-s.maxMessages), -1)
		}
	}

	pipe.Expire(ctx, metaKey, s.ttl)
	pipe.Expire(ctx, messagesKey, s.ttl)

	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return nil
}

func (s *RedisStore) loadSession(ctx context.Context, sessionID string) (*Session, bool) {
	if s.client == nil {
		return nil, false
	}

	metaKey := s.sessionKey(sessionID)
	messagesKey := s.messagesKey(sessionID)

	meta, err := s.client.HGetAll(ctx, metaKey).Result()
	if err != nil {
		if err != redis.Nil {
			s.warn("load_session_meta", sessionID, err)
		}
		return nil, false
	}
	if len(meta) == 0 {
		return nil, false
	}

	session := &Session{
		ID: sessionID,
	}

	session.UserID = meta["user_id"]
	if createdAt, err := time.Parse(time.RFC3339Nano, meta["created_at"]); err == nil {
		session.CreatedAt = createdAt
	} else {
		session.CreatedAt = time.Now().UTC()
	}
	if updatedAt, err := time.Parse(time.RFC3339Nano, meta["updated_at"]); err == nil {
		session.UpdatedAt = updatedAt
	} else {
		session.UpdatedAt = session.CreatedAt
	}

	rawMessages, err := s.client.LRange(ctx, messagesKey, 0, -1).Result()
	if err != nil && err != redis.Nil {
		s.warn("load_session_messages", sessionID, err)
		return nil, false
	}

	session.Messages = make([]Message, 0, len(rawMessages))
	for _, raw := range rawMessages {
		var msg Message
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			s.warn("decode_message", sessionID, err)
			continue
		}
		session.Messages = append(session.Messages, msg)
	}

	if s.maxMessages > 0 && len(session.Messages) > s.maxMessages {
		session.Messages = trimToLimit(session.Messages, s.maxMessages)
	}

	return session, true
}

func (s *RedisStore) sessionKey(id string) string {
	return fmt.Sprintf("%s:%s:meta", s.prefix, id)
}

func (s *RedisStore) messagesKey(id string) string {
	return fmt.Sprintf("%s:%s:messages", s.prefix, id)
}

func (s *RedisStore) warn(op, id string, err error) {
	if err == nil || s.logger == nil {
		return
	}
	s.logger.Warn(fmt.Sprintf("%s failed for session=%s: %v", op, id, err))
}

type redisMetrics struct {
	hits      *prometheus.CounterVec
	fallbacks *prometheus.CounterVec
	history   *prometheus.HistogramVec
}

var (
	redisMetricsOnce sync.Once
	redisMetricsInst *redisMetrics
)

func initRedisMetrics() *redisMetrics {
	redisMetricsOnce.Do(func() {
		redisMetricsInst = &redisMetrics{
			hits: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "sleeper",
				Subsystem: "conversation_store",
				Name:      "redis_hits_total",
				Help:      "Successful Redis-backed conversation store operations.",
			}, []string{"operation", "source"}),
			fallbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: "sleeper",
				Subsystem: "conversation_store",
				Name:      "redis_fallback_total",
				Help:      "Operations served by the in-memory fallback store.",
			}, []string{"operation"}),
			history: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Namespace: "sleeper",
				Subsystem: "conversation_store",
				Name:      "history_messages",
				Help:      "Number of messages replayed for a session.",
				Buckets:   []float64{1, 5, 10, 20, 40, 80},
			}, []string{"source"}),
		}
		prometheus.MustRegister(redisMetricsInst.hits, redisMetricsInst.fallbacks, redisMetricsInst.history)
	})
	return redisMetricsInst
}

func (m *redisMetrics) recordHit(operation string, historyLen int, source string) {
	if m == nil {
		return
	}
	m.hits.WithLabelValues(operation, source).Inc()
	if historyLen >= 0 {
		m.history.WithLabelValues(source).Observe(float64(historyLen))
	}
}

func (m *redisMetrics) recordFallback(operation string) {
	if m == nil {
		return
	}
	m.fallbacks.WithLabelValues(operation).Inc()
}
