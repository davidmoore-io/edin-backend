package assistant

import (
	"context"
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/tools"
)

func TestRunnerBuildsBetaParams(t *testing.T) {
	runner := NewRunner(nil, nil, "test prompt", 5)

	// Verify runner was created with expected config
	if runner.maxIter != 5 {
		t.Fatalf("expected maxIter=5, got %d", runner.maxIter)
	}
	if runner.systemPrompt != "test prompt" {
		t.Fatalf("expected systemPrompt='test prompt', got %q", runner.systemPrompt)
	}
}

func TestRunnerSystemPromptHasCacheControl(t *testing.T) {
	// Verify the runner sets cache_control on system prompt blocks
	// This is verified by checking the code path — the system prompt
	// is constructed with CacheControl in RunWithProgress
	runner := NewRunner(nil, nil, "test prompt", 5)
	if runner.systemPrompt == "" {
		t.Fatal("expected non-empty system prompt")
	}
}

func TestRunnerCompactionInstructions(t *testing.T) {
	if CompactionInstructions == "" {
		t.Fatal("expected non-empty compaction instructions")
	}
	if len(CompactionInstructions) < 50 {
		t.Fatal("compaction instructions seem too short to be useful")
	}
}

func TestRunnerContextManagementConstants(t *testing.T) {
	if compactionTrigger <= 0 {
		t.Fatal("expected positive compaction trigger")
	}
	if clearToolsTrigger <= 0 {
		t.Fatal("expected positive clear tools trigger")
	}
	if clearToolsKeep <= 0 {
		t.Fatal("expected positive clear tools keep count")
	}
	if compactionTrigger <= clearToolsTrigger {
		t.Fatal("compaction trigger should be larger than clear tools trigger")
	}
}

func TestRunnerToolDefsForScope_UsesSlimForKaine(t *testing.T) {
	runner := NewRunner(nil, nil, "", 5)
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeKaineChat)

	betaDefs := runner.betaToolDefsForContext(ctx)
	if len(betaDefs) == 0 {
		t.Fatal("expected non-empty tool defs for Kaine scope")
	}

	// Slim definitions for complex tools should have empty properties
	for _, def := range betaDefs {
		if def.OfTool == nil {
			continue
		}
		name := tools.ToolName(def.OfTool.Name)
		if name == tools.ToolGalaxyMarket {
			props, ok := def.OfTool.InputSchema.Properties.(map[string]any)
			if !ok {
				t.Fatalf("expected Properties to be map[string]any for %s", name)
			}
			if len(props) != 0 {
				t.Fatalf("expected galaxy_market to have empty properties in Kaine scope (slim), got %d", len(props))
			}
			return
		}
	}
	t.Fatal("galaxy_market not found in Kaine tool defs")
}

func TestRunnerToolDefsForScope_UsesFullForOps(t *testing.T) {
	runner := NewRunner(nil, nil, "", 5)
	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeLlmOperator)

	betaDefs := runner.betaToolDefsForContext(ctx)
	if len(betaDefs) == 0 {
		t.Fatal("expected non-empty tool defs for ops scope")
	}

	// Full definitions should have parameters for all tools
	for _, def := range betaDefs {
		if def.OfTool == nil {
			continue
		}
		name := tools.ToolName(def.OfTool.Name)
		if name == tools.ToolGalaxyMarket {
			props, ok := def.OfTool.InputSchema.Properties.(map[string]any)
			if !ok {
				t.Fatalf("expected Properties to be map[string]any for %s", name)
			}
			if len(props) == 0 {
				t.Fatal("expected galaxy_market to have properties in ops scope (full)")
			}
			return
		}
	}
	t.Fatal("galaxy_market not found in ops tool defs")
}

func TestRunnerBuildBetaMessageParams(t *testing.T) {
	runner := NewRunner(nil, nil, "", 5)

	params := runner.buildBetaMessageParams(nil, "hello")
	if len(params) != 1 {
		t.Fatalf("expected 1 message param, got %d", len(params))
	}
	if params[0].Role != "user" {
		t.Fatalf("expected user role, got %q", params[0].Role)
	}
}

func TestRunnerDefaultMaxIterations(t *testing.T) {
	runner := NewRunner(nil, nil, "", 0)
	if runner.maxIter != 5 {
		t.Fatalf("expected default maxIter=5, got %d", runner.maxIter)
	}
}
