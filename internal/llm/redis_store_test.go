package llm

import (
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisStoreRoundTrip(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	fallback := NewInMemoryStore(5 * time.Minute)
	store := NewRedisStore(client, time.Minute, 3, fallback, WithRedisPrefix("test"))

	session := store.CreateSession("user-123")
	if session == nil {
		t.Fatal("expected session to be created")
	}

	msgs := []Message{
		{Role: "assistant", Content: "hello there"},
		{Role: "user", Content: "status?"},
		{Role: "assistant", Content: "all good"},
		{Role: "user", Content: "thanks"},
	}
	for _, msg := range msgs {
		if _, err := store.AppendMessage(session.ID, msg); err != nil {
			t.Fatalf("append failed: %v", err)
		}
	}

	loaded, ok := store.Get(session.ID)
	if !ok {
		t.Fatalf("expected session %s to be retrievable", session.ID)
	}
	if got := len(loaded.Messages); got > 3 {
		t.Fatalf("expected max 3 messages, got %d", got)
	}
	last := loaded.Messages[len(loaded.Messages)-1]
	if last.Content != "thanks" {
		t.Fatalf("expected latest message to be preserved, got %q", last.Content)
	}
}

func TestRedisStoreHonoursTTL(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	ttl := time.Second
	fallback := NewInMemoryStore(ttl)
	store := NewRedisStore(client, ttl, 10, fallback, WithRedisPrefix("ttl"))

	session := store.CreateSession("user-ttl")
	if session == nil {
		t.Fatal("expected session to be created")
	}
	if _, err := store.AppendMessage(session.ID, Message{Role: "user", Content: "ping", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("append failed: %v", err)
	}

	time.Sleep(ttl + 150*time.Millisecond)
	mr.FastForward(2 * time.Second)

	if _, ok := store.Get(session.ID); ok {
		t.Fatalf("expected session %s to expire", session.ID)
	}
}

func TestRedisStoreFallbackWithoutClient(t *testing.T) {
	fallback := NewInMemoryStore(time.Minute)
	store := NewRedisStore(nil, time.Minute, 5, fallback, WithRedisPrefix("fallback"))

	session := store.CreateSession("user-fallback")
	if session == nil {
		t.Fatal("expected session from fallback")
	}

	if _, err := store.AppendMessage(session.ID, Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatalf("append failed: %v", err)
	}

	loaded, ok := store.Get(session.ID)
	if !ok {
		t.Fatal("expected fallback session to be retrievable")
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("expected 1 message in fallback store, got %d", len(loaded.Messages))
	}
}
