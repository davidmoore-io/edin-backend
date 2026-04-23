package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
)

func TestBgsGuideSearch_ReturnsChunksWithMatches(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeKaineChat)
	e := &Executor{}

	result, err := e.bgsGuideSearch(ctx, map[string]any{
		"query":      "influence",
		"max_chunks": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	total, _ := m["total_matches"].(int)
	if total == 0 {
		t.Fatalf("expected at least one match for 'influence' in BGS guide, got 0")
	}

	chunks, _ := m["chunks"].([]map[string]any)
	if len(chunks) == 0 || len(chunks) > 2 {
		t.Fatalf("expected 1-2 chunks (capped at max_chunks=2), got %d", len(chunks))
	}

	// Each chunk must contain the search term.
	for i, c := range chunks {
		text, _ := c["text"].(string)
		if !strings.Contains(strings.ToLower(text), "influence") {
			t.Fatalf("chunk %d does not contain the query term", i)
		}
		if len(text) < 1000 || len(text) > bgsChunkChars+500 {
			t.Fatalf("chunk %d has unexpected length %d (want ~%d)", i, len(text), bgsChunkChars)
		}
	}
}

func TestBgsGuideSearch_NoMatches(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeKaineChat)
	e := &Executor{}

	result, err := e.bgsGuideSearch(ctx, map[string]any{
		"query": "zzzznothingmatchesthis",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, _ := result.(map[string]any)
	if total, _ := m["total_matches"].(int); total != 0 {
		t.Fatalf("expected 0 matches, got %d", total)
	}
}

func TestBgsGuideSearch_EmptyQueryFails(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeKaineChat)
	e := &Executor{}

	if _, err := e.bgsGuideSearch(ctx, map[string]any{"query": ""}); err == nil {
		t.Fatal("expected error for empty query")
	}
	if _, err := e.bgsGuideSearch(ctx, map[string]any{"query": "x"}); err == nil {
		t.Fatal("expected error for query shorter than 2 chars")
	}
}

func TestBgsGuideSearch_RequiresScope(t *testing.T) {
	ctx := context.Background() // no scopes
	e := &Executor{}

	if _, err := e.bgsGuideSearch(ctx, map[string]any{"query": "influence"}); err == nil {
		t.Fatal("expected scope error when no scope in context")
	}
}
