package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
)

func TestPowerplayGuideSearch_ReturnsChunksWithMatches(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeGalaxyRead)
	e := &Executor{}

	result, err := e.powerplayGuideSearch(ctx, map[string]any{
		"query":      "exobiology",
		"max_chunks": 1,
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
		t.Fatalf("expected at least one match for 'exobiology' in Powerplay refcard, got 0")
	}

	chunks, _ := m["chunks"].([]map[string]any)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (max_chunks=1), got %d", len(chunks))
	}

	text, _ := chunks[0]["text"].(string)
	if !strings.Contains(strings.ToLower(text), "exobiology") {
		t.Fatalf("chunk does not contain the query term")
	}
	if len(text) > powerplayChunkChars+500 {
		t.Fatalf("chunk length %d exceeds expected ~%d", len(text), powerplayChunkChars)
	}
}

func TestPowerplayGuideSearch_NoMatches(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeGalaxyRead)
	e := &Executor{}

	result, err := e.powerplayGuideSearch(ctx, map[string]any{
		"query": "totallyfakekeywordthatwillnotmatch",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	total, _ := m["total_matches"].(int)
	if total != 0 {
		t.Fatalf("expected 0 matches for nonsense query, got %d", total)
	}
}

func TestPowerplayGuideSearch_RejectsShortQuery(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeGalaxyRead)
	e := &Executor{}
	if _, err := e.powerplayGuideSearch(ctx, map[string]any{"query": "a"}); err == nil {
		t.Fatalf("expected error for 1-char query")
	}
	if _, err := e.powerplayGuideSearch(ctx, map[string]any{"query": ""}); err == nil {
		t.Fatalf("expected error for empty query")
	}
}

func TestPowerplayGuideSearch_GroundingRulePresent(t *testing.T) {
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeGalaxyRead)
	e := &Executor{}

	result, _ := e.powerplayGuideSearch(ctx, map[string]any{"query": "fortified"})
	m := result.(map[string]any)
	rule, ok := m["!IMPORTANT"].(string)
	if !ok || !strings.Contains(rule, "STRICT GROUNDING RULE") {
		t.Fatalf("expected grounding rule in response, got %q", rule)
	}
}
