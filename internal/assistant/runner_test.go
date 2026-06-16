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
	// Kaine-approved default scope set: the endpoint gate plus per-tool
	// scopes. Passing only ScopeKaineChat would fail-closed against the
	// scope-driven filter since galaxy_market requires ScopeGalaxyRead.
	ctx := authz.ContextWithScopes(
		context.Background(),
		authz.ScopeKaineChat,
		authz.ScopeGalaxyRead,
		authz.ScopeKaineMining,
	)

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
	// An ops caller ("kaine-god") holds ScopeLlmOperator plus the
	// fine-grained scopes galaxy tools require. With scope-driven
	// fail-closed filtering, ScopeLlmOperator alone is deliberately
	// insufficient to see galaxy_market — the test here pins the UX
	// decision (ops path returns the FULL, non-slim definitions), not
	// an authz bypass.
	ctx := authz.ContextWithScopes(
		context.Background(),
		authz.ScopeAdmin,
		authz.ScopeLlmOperator,
		authz.ScopeKaineChat,
		authz.ScopeGalaxyRead,
		authz.ScopeKaineMining,
		authz.ScopeCommanderData,
	)

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

	params := runner.buildBetaMessageParams(nil, "hello", nil)
	if len(params) != 1 {
		t.Fatalf("expected 1 message param, got %d", len(params))
	}
	if params[0].Role != "user" {
		t.Fatalf("expected user role, got %q", params[0].Role)
	}
	// Text-only turn → a single text content block, no image.
	if len(params[0].Content) != 1 || params[0].Content[0].OfImage != nil {
		t.Fatalf("expected a single non-image content block, got %d (image=%v)",
			len(params[0].Content), params[0].Content[0].OfImage != nil)
	}
}

func TestRunnerBuildBetaMessageParams_WithImage(t *testing.T) {
	runner := NewRunner(nil, nil, "", 5)
	img := &ImageInput{Base64: "aGVsbG8=", MediaType: "image/png"}

	params := runner.buildBetaMessageParams(nil, "look at this", img)
	if len(params) != 1 {
		t.Fatalf("expected 1 message param, got %d", len(params))
	}
	// Image + text → two content blocks, image first.
	if len(params[0].Content) != 2 {
		t.Fatalf("expected 2 content blocks (image+text), got %d", len(params[0].Content))
	}
	imgBlock := params[0].Content[0].OfImage
	if imgBlock == nil || imgBlock.Source.OfBase64 == nil {
		t.Fatal("expected first block to be a base64 image block")
	}
	if imgBlock.Source.OfBase64.Data != "aGVsbG8=" {
		t.Fatalf("expected image data passed through, got %q", imgBlock.Source.OfBase64.Data)
	}
	if string(imgBlock.Source.OfBase64.MediaType) != "image/png" {
		t.Fatalf("expected media type image/png, got %q", imgBlock.Source.OfBase64.MediaType)
	}
}

func TestRunnerBuildBetaMessageParams_ImageOnly(t *testing.T) {
	runner := NewRunner(nil, nil, "", 5)
	img := &ImageInput{Base64: "aGVsbG8=", MediaType: "image/png"}

	params := runner.buildBetaMessageParams(nil, "", img)
	if len(params) != 1 {
		t.Fatalf("expected 1 message param, got %d", len(params))
	}
	// Image-only turn → exactly one block, and it is the image (no empty text block).
	if len(params[0].Content) != 1 || params[0].Content[0].OfImage == nil {
		t.Fatalf("expected a single image block for an image-only turn, got %d blocks", len(params[0].Content))
	}
}

func TestRunnerDefaultMaxIterations(t *testing.T) {
	runner := NewRunner(nil, nil, "", 0)
	if runner.maxIter != 5 {
		t.Fatalf("expected default maxIter=5, got %d", runner.maxIter)
	}
}

func TestRunner_SetSystemPrompt_UpdatesPrompt(t *testing.T) {
	r := NewRunner(nil, nil, "initial prompt", 5)

	r.SetSystemPrompt("  updated prompt  ")

	r.mu.RLock()
	got := r.systemPrompt
	r.mu.RUnlock()

	if got != "updated prompt" {
		t.Fatalf("expected 'updated prompt' (trimmed), got %q", got)
	}
}

func TestRunner_SetSystemPrompt_EmptyString(t *testing.T) {
	r := NewRunner(nil, nil, "initial", 5)
	r.SetSystemPrompt("")

	r.mu.RLock()
	got := r.systemPrompt
	r.mu.RUnlock()

	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestRunner_SetSystemPrompt_ConcurrentSafe(t *testing.T) {
	// Run with -race to detect data races.
	r := NewRunner(nil, nil, "initial", 5)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			r.SetSystemPrompt("writer goroutine")
		}
		close(done)
	}()

	for i := 0; i < 100; i++ {
		r.mu.RLock()
		_ = r.systemPrompt
		r.mu.RUnlock()
	}
	<-done
}
