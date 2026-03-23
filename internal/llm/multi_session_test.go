package llm

import (
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	fallback := NewInMemoryStore(5 * time.Minute)
	store := NewRedisStore(client, 5*time.Minute, 100, fallback, WithRedisPrefix("test"))
	return store, mr
}

func TestListUserSessions_ReturnsOrderedByRecent(t *testing.T) {
	store, _ := newTestRedisStore(t)

	// Create 3 sessions with different timestamps
	s1 := store.CreateSession("user-1")
	time.Sleep(10 * time.Millisecond)
	s2 := store.CreateSession("user-1")
	time.Sleep(10 * time.Millisecond)
	s3 := store.CreateSession("user-1")

	sessions, err := store.ListUserSessions("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// Most recent should be first
	if sessions[0].ID != s3.ID {
		t.Fatalf("expected most recent session %s first, got %s", s3.ID, sessions[0].ID)
	}
	if sessions[1].ID != s2.ID {
		t.Fatalf("expected second session %s, got %s", s2.ID, sessions[1].ID)
	}
	if sessions[2].ID != s1.ID {
		t.Fatalf("expected oldest session %s last, got %s", s1.ID, sessions[2].ID)
	}
}

func TestGetActiveSession_ReturnsMostRecent(t *testing.T) {
	store, _ := newTestRedisStore(t)

	store.CreateSession("user-1")
	time.Sleep(10 * time.Millisecond)
	s2 := store.CreateSession("user-1")

	active, err := store.GetActiveSession("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active == nil {
		t.Fatal("expected active session, got nil")
	}
	// The last created session becomes active
	if active.ID != s2.ID {
		t.Fatalf("expected active session %s, got %s", s2.ID, active.ID)
	}
}

func TestSetActiveSession_SwitchesSession(t *testing.T) {
	store, _ := newTestRedisStore(t)

	s1 := store.CreateSession("user-1")
	time.Sleep(10 * time.Millisecond)
	store.CreateSession("user-1")

	// Switch active to the older session
	if err := store.SetActiveSession("user-1", s1.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	active, err := store.GetActiveSession("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active == nil {
		t.Fatal("expected active session, got nil")
	}
	if active.ID != s1.ID {
		t.Fatalf("expected active session %s after switch, got %s", s1.ID, active.ID)
	}
}

func TestCreateSession_AddsToUserIndex(t *testing.T) {
	store, _ := newTestRedisStore(t)

	session := store.CreateSession("user-1")

	sessions, err := store.ListUserSessions("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session in user index, got %d", len(sessions))
	}
	if sessions[0].ID != session.ID {
		t.Fatalf("expected session %s in index, got %s", session.ID, sessions[0].ID)
	}
}

func TestAppendMessage_RefreshesSessionActivity(t *testing.T) {
	store, _ := newTestRedisStore(t)

	// Create two sessions — s1 first, then s2
	s1 := store.CreateSession("user-1")
	time.Sleep(10 * time.Millisecond)
	store.CreateSession("user-1")

	// Append message to s1 to make it the most recent
	time.Sleep(10 * time.Millisecond)
	_, err := store.AppendMessage(s1.ID, Message{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}

	sessions, err := store.ListUserSessions("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(sessions))
	}

	// s1 should now be the most recently active (first in list)
	if sessions[0].ID != s1.ID {
		t.Fatalf("expected s1 (%s) to be most recent after append, got %s", s1.ID, sessions[0].ID)
	}
}

func TestSessionTTL_ExpiresAfter90Days(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	ttl := time.Second // Short TTL for testing
	fallback := NewInMemoryStore(ttl)
	store := NewRedisStore(client, ttl, 100, fallback, WithRedisPrefix("ttl"))

	session := store.CreateSession("user-ttl")
	_, _ = store.AppendMessage(session.ID, Message{Role: "user", Content: "test"})

	// Advance time
	time.Sleep(ttl + 200*time.Millisecond)
	mr.FastForward(2 * time.Second)

	if _, ok := store.Get(session.ID); ok {
		t.Fatal("expected session to expire")
	}
}

func TestSessionSummary_IncludesPreview(t *testing.T) {
	store, _ := newTestRedisStore(t)

	session := store.CreateSession("user-1")
	_, _ = store.AppendMessage(session.ID, Message{Role: "user", Content: "What is the status of Cubeo?"})
	_, _ = store.AppendMessage(session.ID, Message{Role: "assistant", Content: "Let me check..."})

	sessions, err := store.ListUserSessions("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Preview != "What is the status of Cubeo?" {
		t.Fatalf("expected preview to be first user message, got %q", sessions[0].Preview)
	}
	if sessions[0].MessageCount != 2 {
		t.Fatalf("expected message count 2, got %d", sessions[0].MessageCount)
	}
}

func TestListUserSessions_EmptyForNewUser(t *testing.T) {
	store, _ := newTestRedisStore(t)

	sessions, err := store.ListUserSessions("nonexistent-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions for new user, got %d", len(sessions))
	}
}

func TestSetActiveSession_RejectsCrossUserActivation(t *testing.T) {
	store, _ := newTestRedisStore(t)

	// User A creates a session
	sessionA := store.CreateSession("user-a")

	// User B tries to activate User A's session
	err := store.SetActiveSession("user-b", sessionA.ID)
	if err == nil {
		t.Fatal("expected error when activating another user's session")
	}
}

func TestSetActiveSession_RejectsNonexistentSession(t *testing.T) {
	store, _ := newTestRedisStore(t)

	err := store.SetActiveSession("user-1", "nonexistent-session-id")
	if err == nil {
		t.Fatal("expected error when activating nonexistent session")
	}
}

func TestGetActiveSession_NilForNewUser(t *testing.T) {
	store, _ := newTestRedisStore(t)

	active, err := store.GetActiveSession("nonexistent-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active != nil {
		t.Fatal("expected nil for new user, got a session")
	}
}
