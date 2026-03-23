package tools

import (
	"strings"
	"testing"
)

func TestDescribeTool_KnownToolReturnsGuidance(t *testing.T) {
	result, err := DescribeTool("galaxy_market")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify it has the expected fields
	if result["tool_name"] != "galaxy_market" {
		t.Fatalf("expected tool_name 'galaxy_market', got %v", result["tool_name"])
	}
	if result["description"] == nil || result["description"] == "" {
		t.Fatal("expected non-empty description")
	}
	guidance, ok := result["usage_guidance"].(string)
	if !ok || guidance == "" {
		t.Fatal("expected non-empty usage_guidance")
	}
	if !strings.Contains(guidance, "commodity") {
		t.Fatal("expected guidance to mention 'commodity'")
	}
	if !strings.Contains(guidance, "Trading best practices") {
		t.Fatal("expected guidance to contain trading best practices")
	}
}

func TestDescribeTool_UnknownToolReturnsError(t *testing.T) {
	_, err := DescribeTool("nonexistent_tool")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("expected 'unknown tool' in error, got: %v", err)
	}
}

func TestDescribeTool_AllKaineToolsHaveGuidance(t *testing.T) {
	for name := range kaineAllowedTools {
		// Skip simple/parameterless tools and describe_tool itself
		if name == ToolDescribeTool {
			continue
		}
		if !complexTools[name] {
			continue // Simple tools don't need guidance
		}

		_, hasGuidance := ToolGuidance[name]
		if !hasGuidance {
			t.Errorf("complex Kaine tool %q has no guidance entry in ToolGuidance", name)
		}
	}
}

func TestSlimDefinitions_ComplexToolsHaveNoParams(t *testing.T) {
	slimDefs := SlimBetaToolDefinitions()

	for _, def := range slimDefs {
		if def.OfTool == nil {
			continue // WebSearch
		}
		name := ToolName(def.OfTool.Name)
		if !complexTools[name] {
			continue
		}

		props, ok := def.OfTool.InputSchema.Properties.(map[string]any)
		if !ok {
			t.Fatalf("tool %s: expected Properties to be map[string]any, got %T", name, def.OfTool.InputSchema.Properties)
		}
		if len(props) != 0 {
			t.Fatalf("tool %s: expected empty properties in slim definition, got %d properties", name, len(props))
		}
	}
}

func TestSlimDefinitions_SimpleToolsKeepParams(t *testing.T) {
	slimDefs := SlimBetaToolDefinitions()

	for _, def := range slimDefs {
		if def.OfTool == nil {
			continue // WebSearch
		}
		name := ToolName(def.OfTool.Name)
		if name != ToolSystemProfile {
			continue
		}

		props, ok := def.OfTool.InputSchema.Properties.(map[string]any)
		if !ok {
			t.Fatalf("expected Properties to be map[string]any, got %T", def.OfTool.InputSchema.Properties)
		}
		if len(props) == 0 {
			t.Fatal("expected system_profile to retain its parameters in slim definitions")
		}
		if _, exists := props["system_name"]; !exists {
			t.Fatal("expected system_profile to have 'system_name' parameter")
		}
		return
	}
	t.Fatal("system_profile not found in slim definitions")
}

func TestSlimDefinitions_SameToolCount(t *testing.T) {
	fullCount := len(BetaToolDefinitions())
	slimCount := len(SlimBetaToolDefinitions())

	if fullCount != slimCount {
		t.Fatalf("expected slim (%d) and full (%d) to have same tool count", slimCount, fullCount)
	}
}

func TestDescribeToolJSON_ReturnsValidJSON(t *testing.T) {
	jsonStr, err := DescribeToolJSON("galaxy_market")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(jsonStr, "{") {
		t.Fatal("expected JSON output to start with '{'")
	}
	if !strings.Contains(jsonStr, "galaxy_market") {
		t.Fatal("expected JSON to contain tool name")
	}
}
