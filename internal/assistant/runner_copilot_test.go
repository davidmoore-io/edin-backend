package assistant

import (
	"context"
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
)

func TestRunner_CopilotScope_ToolListNonEmpty(t *testing.T) {
	runner := NewRunner(nil, nil, "", 5)
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeCopilotChat)

	defs := runner.betaToolDefsForContext(ctx)
	if len(defs) == 0 {
		t.Fatal("expected non-empty tool list for copilot scope")
	}
}

func TestRunner_WithSystemPrompt_IsolatesPrompt(t *testing.T) {
	base := NewRunner(nil, nil, "base prompt", 5)
	derived := base.WithSystemPrompt("commander prompt")

	base.mu.RLock()
	basePrompt := base.systemPrompt
	base.mu.RUnlock()

	derived.mu.RLock()
	derivedPrompt := derived.systemPrompt
	derived.mu.RUnlock()

	if basePrompt != "base prompt" {
		t.Fatalf("base runner prompt changed: got %q", basePrompt)
	}
	if derivedPrompt != "commander prompt" {
		t.Fatalf("derived runner prompt wrong: got %q", derivedPrompt)
	}
}
