package llm

import (
	"encoding/json"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInMemoryProviderContextSurvivesDisplayHistoryTrim(t *testing.T) {
	store := NewInMemoryStore(time.Minute)
	store.SetMaxMessages(2)
	session := store.CreateSession("user")
	_, _ = store.AppendMessage(session.ID, Message{Role: "user", Content: "first"})
	_, _ = store.AppendMessage(session.ID, Message{Role: "user", Content: "second"})

	context := testProviderContext()
	updated, err := store.CommitAssistantTurn(
		session.ID,
		Message{Role: "assistant", Content: "answer"},
		context,
	)
	if err != nil {
		t.Fatalf("commit assistant turn: %v", err)
	}
	if len(updated.Messages) != 2 {
		t.Fatalf("display history = %d, want bounded length 2", len(updated.Messages))
	}

	got, ok, err := store.GetProviderContext(session.ID, "anthropic")
	if err != nil || !ok {
		t.Fatalf("get provider context = (%v, %v)", ok, err)
	}
	if len(got.Messages) != len(context.Messages) {
		t.Fatalf("provider messages = %d, want %d", len(got.Messages), len(context.Messages))
	}
	got.Messages[0][0] = 'X'
	again, _, _ := store.GetProviderContext(session.ID, "anthropic")
	if string(again.Messages[0]) != string(context.Messages[0]) {
		t.Fatal("provider context was not deep-copied")
	}
}

func TestRedisCommitAssistantTurnStoresDisplayAndProviderContext(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, time.Minute, 2, NewInMemoryStore(time.Minute), WithRedisPrefix("provider"))
	session := store.CreateSession("user")
	_, _ = store.AppendMessage(session.ID, Message{Role: "user", Content: "question"})

	context := testProviderContext()
	updated, err := store.CommitAssistantTurn(
		session.ID,
		Message{Role: "assistant", Content: "answer"},
		context,
	)
	if err != nil {
		t.Fatalf("commit assistant turn: %v", err)
	}
	if len(updated.Messages) != 2 || updated.Messages[1].Content != "answer" {
		t.Fatalf("unexpected display transcript: %#v", updated.Messages)
	}
	got, ok, err := store.GetProviderContext(session.ID, "anthropic")
	if err != nil || !ok {
		t.Fatalf("get provider context = (%v, %v)", ok, err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("provider context was trimmed with display history: %d messages", len(got.Messages))
	}

	store.Delete(session.ID)
	if mr.Exists(store.providerContextKey(session.ID, "anthropic")) {
		t.Fatal("delete left provider context behind")
	}
}

func TestRedisCommitAssistantTurnRequiresExistingSession(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, time.Minute, 2, NewInMemoryStore(time.Minute), WithRedisPrefix("missing"))

	_, err = store.CommitAssistantTurn(
		"does-not-exist",
		Message{Role: "assistant", Content: "answer"},
		testProviderContext(),
	)
	if err == nil {
		t.Fatal("commit unexpectedly succeeded for a missing session")
	}
	if mr.Exists(store.providerContextKey("does-not-exist", "anthropic")) {
		t.Fatal("failed commit wrote provider context")
	}
}

func testProviderContext() ProviderContext {
	return ProviderContext{
		Provider: "anthropic",
		Version:  1,
		Messages: []json.RawMessage{
			json.RawMessage(`{"role":"user","content":[{"type":"text","text":"one"}]}`),
			json.RawMessage(`{"role":"assistant","content":[{"type":"compaction","content":"summary"}]}`),
			json.RawMessage(`{"role":"user","content":[{"type":"text","text":"two"}]}`),
		},
	}
}
