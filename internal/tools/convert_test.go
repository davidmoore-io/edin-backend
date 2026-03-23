package tools

import (
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
)

func TestMCPToAnthropic_SimpleToolConvertsCorrectly(t *testing.T) {
	mcpTools := MCPToolDefinitions()
	// Find galaxy_market which has both required and optional params
	var found bool
	for _, tool := range mcpTools {
		if tool.Name == string(ToolGalaxyMarket) {
			result := MCPToAnthropic(tool)
			if result.OfTool == nil {
				t.Fatal("expected OfTool to be set")
			}
			if result.OfTool.Name != string(ToolGalaxyMarket) {
				t.Fatalf("expected name %q, got %q", ToolGalaxyMarket, result.OfTool.Name)
			}
			if !result.OfTool.Description.Valid() {
				t.Fatal("expected description to be set")
			}
			props, ok := result.OfTool.InputSchema.Properties.(map[string]any)
			if !ok {
				t.Fatalf("expected properties to be map[string]any, got %T", result.OfTool.InputSchema.Properties)
			}
			if _, exists := props["commodity"]; !exists {
				t.Fatal("expected 'commodity' property to exist")
			}
			if _, exists := props["operation"]; !exists {
				t.Fatal("expected 'operation' property to exist")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("galaxy_market tool not found in MCP definitions")
	}
}

func TestMCPToAnthropic_NoParamsToolConvertsCorrectly(t *testing.T) {
	mcpTools := MCPToolDefinitions()
	for _, tool := range mcpTools {
		if tool.Name == string(ToolGalaxyPlasmiumBuyers) {
			result := MCPToAnthropic(tool)
			if result.OfTool == nil {
				t.Fatal("expected OfTool to be set")
			}
			if result.OfTool.Name != string(ToolGalaxyPlasmiumBuyers) {
				t.Fatalf("expected name %q, got %q", ToolGalaxyPlasmiumBuyers, result.OfTool.Name)
			}
			props, _ := result.OfTool.InputSchema.Properties.(map[string]any)
			if len(props) != 0 {
				t.Fatalf("expected no properties for parameterless tool, got %d", len(props))
			}
			if len(result.OfTool.InputSchema.Required) != 0 {
				t.Fatalf("expected no required fields, got %d", len(result.OfTool.InputSchema.Required))
			}
			return
		}
	}
	t.Fatal("galaxy_plasmium_buyers tool not found in MCP definitions")
}

func TestMCPToAnthropic_AllToolsConvertWithoutError(t *testing.T) {
	mcpTools := MCPToolDefinitions()
	if len(mcpTools) == 0 {
		t.Fatal("expected MCP tools to be non-empty")
	}

	results := MCPToAnthropicAll(mcpTools)
	if len(results) != len(mcpTools) {
		t.Fatalf("expected %d results, got %d", len(mcpTools), len(results))
	}

	for i, result := range results {
		if result.OfTool == nil {
			t.Fatalf("tool %d: expected OfTool to be set", i)
		}
		if result.OfTool.Name == "" {
			t.Fatalf("tool %d: expected non-empty name", i)
		}
		if result.OfTool.Name != mcpTools[i].Name {
			t.Fatalf("tool %d: expected name %q, got %q", i, mcpTools[i].Name, result.OfTool.Name)
		}
	}
}

func TestMCPToAnthropic_RoundTrip(t *testing.T) {
	mcpTools := MCPToolDefinitions()
	for _, mcpTool := range mcpTools {
		result := MCPToAnthropic(mcpTool)
		if result.OfTool == nil {
			t.Fatalf("tool %s: OfTool is nil", mcpTool.Name)
		}
		if result.OfTool.Name != mcpTool.Name {
			t.Fatalf("tool %s: name mismatch: got %q", mcpTool.Name, result.OfTool.Name)
		}
		if mcpTool.Description != "" && !result.OfTool.Description.Valid() {
			t.Fatalf("tool %s: description not preserved", mcpTool.Name)
		}

		// Verify property count matches
		mcpProps := mcpTool.InputSchema.Properties
		sdkProps, _ := result.OfTool.InputSchema.Properties.(map[string]any)
		if len(mcpProps) != len(sdkProps) {
			t.Fatalf("tool %s: property count mismatch: MCP=%d SDK=%d", mcpTool.Name, len(mcpProps), len(sdkProps))
		}

		// Verify required fields match
		if len(mcpTool.InputSchema.Required) != len(result.OfTool.InputSchema.Required) {
			t.Fatalf("tool %s: required count mismatch: MCP=%d SDK=%d", mcpTool.Name, len(mcpTool.InputSchema.Required), len(result.OfTool.InputSchema.Required))
		}
	}
}

func TestMCPToBeta_AllToolsConvertWithoutError(t *testing.T) {
	mcpTools := MCPToolDefinitions()
	results := MCPToBetaAll(mcpTools)
	if len(results) != len(mcpTools) {
		t.Fatalf("expected %d beta results, got %d", len(mcpTools), len(results))
	}

	for i, result := range results {
		if result.OfTool == nil {
			t.Fatalf("beta tool %d: expected OfTool to be set", i)
		}
		if result.OfTool.Name != mcpTools[i].Name {
			t.Fatalf("beta tool %d: expected name %q, got %q", i, mcpTools[i].Name, result.OfTool.Name)
		}
	}
}

func TestAnthropicsToolDefinitions_MatchesMCPCount(t *testing.T) {
	mcpCount := len(MCPToolDefinitions())
	anthropicCount := len(AnthropicsToolDefinitions())

	// AnthropicsToolDefinitions adds WebSearch, so count should be MCP + 1
	expected := mcpCount + 1
	if anthropicCount != expected {
		t.Fatalf("expected AnthropicsToolDefinitions to have %d tools (MCP %d + 1 WebSearch), got %d", expected, mcpCount, anthropicCount)
	}
}

func TestAnthropicsToolDefinitionsForScope_KaineFiltersCorrectly(t *testing.T) {
	kaineTools := AnthropicsToolDefinitionsForScope(authz.ScopeKaineChat)
	if len(kaineTools) == 0 {
		t.Fatal("expected Kaine scope to return some tools")
	}

	// Verify no ops tools leak through
	for _, tool := range kaineTools {
		if tool.OfTool != nil {
			name := ToolName(tool.OfTool.Name)
			if opsOnlyTools[name] {
				t.Fatalf("ops tool %q leaked into Kaine scope", name)
			}
			if !kaineAllowedTools[name] {
				t.Fatalf("tool %q is not in Kaine allowed list", name)
			}
		}
	}

	// Verify count matches expected Kaine tools
	if len(kaineTools) != len(kaineAllowedTools) {
		t.Fatalf("expected %d Kaine tools, got %d", len(kaineAllowedTools), len(kaineTools))
	}
}

func TestAnthropicsToolDefinitionsForScope_AdminGetsAll(t *testing.T) {
	adminTools := AnthropicsToolDefinitionsForScope(authz.ScopeAdmin)
	allTools := AnthropicsToolDefinitions()
	if len(adminTools) != len(allTools) {
		t.Fatalf("expected admin to get all %d tools, got %d", len(allTools), len(adminTools))
	}
}

func TestBetaToolDefinitions_MatchesMCPCount(t *testing.T) {
	mcpCount := len(MCPToolDefinitions())
	betaCount := len(BetaToolDefinitions())

	expected := mcpCount + 1 // +1 for WebSearch
	if betaCount != expected {
		t.Fatalf("expected BetaToolDefinitions to have %d tools, got %d", expected, betaCount)
	}
}

func TestBetaToolDefinitionsForScope_KaineFiltersCorrectly(t *testing.T) {
	kaineTools := BetaToolDefinitionsForScope(authz.ScopeKaineChat)
	if len(kaineTools) == 0 {
		t.Fatal("expected Kaine scope to return some beta tools")
	}

	for _, tool := range kaineTools {
		if tool.OfTool != nil {
			name := ToolName(tool.OfTool.Name)
			if opsOnlyTools[name] {
				t.Fatalf("ops tool %q leaked into Kaine beta scope", name)
			}
		}
	}
}
