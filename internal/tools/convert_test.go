package tools

import (
	"sort"
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

	// Pass the full union of tool-declared scopes so every tool passes the
	// fail-closed per-tool scope check — we're asserting the converter
	// produces matching SDK structs here, not the filter behaviour.
	results := MCPToAnthropicAll(mcpTools, allRegisteredToolScopes)
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
	results := MCPToBetaAll(mcpTools, allRegisteredToolScopes)
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

// kaineDefaultScopes is the scope set granted to a "kaine-approved" user: the
// coarse endpoint gate plus galaxy read and mining intel. Used to pin legacy
// kaine visibility in the parity tests.
var kaineDefaultScopes = []authz.Scope{
	authz.ScopeKaineChat,
	authz.ScopeGalaxyRead,
	authz.ScopeKaineMining,
}

// copilotDefaultScopes is the scope set granted to an "edin-copilot" user: the
// coarse endpoint gate plus galaxy read and commander data. Used to pin legacy
// copilot visibility in the parity tests.
var copilotDefaultScopes = []authz.Scope{
	authz.ScopeCopilotChat,
	authz.ScopeGalaxyRead,
	authz.ScopeCommanderData,
}

func TestAnthropicsToolDefinitionsForScopes_KaineFiltersCorrectly(t *testing.T) {
	kaineTools := AnthropicsToolDefinitionsForScopes(kaineDefaultScopes)
	if len(kaineTools) == 0 {
		t.Fatal("expected Kaine scope to return some tools")
	}

	for _, tool := range kaineTools {
		if tool.OfTool == nil {
			continue
		}
		name := ToolName(tool.OfTool.Name)
		if opsOnlyTools[name] {
			t.Fatalf("ops tool %q leaked into Kaine scope", name)
		}
	}
}

func TestAnthropicsToolDefinitionsForScopes_FullScopeUnionGetsAll(t *testing.T) {
	// Pass the union of every scope declared in toolScopes. With the
	// fail-closed filter (no admin/ops bypass), this is the minimal scope
	// set that must yield the full tool list. Holding only ScopeAdmin (the
	// Discord-admin identity marker) is not sufficient and must not be.
	fullScopeTools := AnthropicsToolDefinitionsForScopes(allRegisteredToolScopes)
	allTools := AnthropicsToolDefinitions()
	if len(fullScopeTools) != len(allTools) {
		t.Fatalf("expected full scope union to yield all %d tools, got %d", len(allTools), len(fullScopeTools))
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

func TestBetaToolDefinitionsForScopes_KaineFiltersCorrectly(t *testing.T) {
	kaineTools := BetaToolDefinitionsForScopes(kaineDefaultScopes)
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

// legacyKaineTools pins the exact set of tool names that the deleted
// kaineAllowedTools map used to allow. The list is captured as the source of
// truth for the parity test below so that any drift in scope-driven filtering
// fails the test with a byte-for-byte diff.
//
// These 24 strings match the MCP tool Name field (what reaches the model), not
// Go identifiers. See the "kaine-approved" group in authz/groups.go for the
// scope set that must reproduce this list.
var legacyKaineTools = []string{
	"bgs_guide_search",
	"describe_tool",
	"galaxy_bodies",
	"galaxy_expansion_check",
	"galaxy_expansion_frontier",
	"galaxy_expansion_targets",
	"galaxy_faction",
	"galaxy_fleet_carrier",
	"galaxy_history",
	"galaxy_ltd_buyers",
	"galaxy_market",
	"galaxy_nearby_powerplay",
	"galaxy_plasmium_buyers",
	"galaxy_power",
	"galaxy_powerplay_cycle",
	"galaxy_query",
	"galaxy_schema",
	"galaxy_signals",
	"galaxy_station",
	"galaxy_stats",
	"galaxy_system",
	"powerplay_guide_search",
	"retrieve_carrier_route",
	"spansh_query",
	"system_profile",
}

// legacyCopilotTools pins the exact set of tool names that the deleted
// copilotAllowedTools map used to allow. 23 entries — copilot is kaine minus
// the mining-intel tools (plasmium, LTD, schema) plus the commander tools.
var legacyCopilotTools = []string{
	"bgs_guide_search",
	"commander_events",
	"commander_location",
	"describe_tool",
	"galaxy_bodies",
	"galaxy_expansion_check",
	"galaxy_expansion_frontier",
	"galaxy_expansion_targets",
	"galaxy_faction",
	"galaxy_fleet_carrier",
	"galaxy_history",
	"galaxy_market",
	"galaxy_nearby_powerplay",
	"galaxy_power",
	"galaxy_powerplay_cycle",
	"galaxy_query",
	"galaxy_signals",
	"galaxy_station",
	"galaxy_stats",
	"galaxy_system",
	"powerplay_guide_search",
	"retrieve_carrier_route",
	"spansh_query",
	"system_profile",
}

// TestConvert_FilterByKaineScopes_MatchesLegacyKaineTools pins the derived
// Kaine tool set byte-for-byte against the legacy kaineAllowedTools map. This
// is the safety net for Task 2's scope-driven filter migration — if the
// toolScopes registry drifts from the legacy two-map model, this test fails
// with a sorted diff of the two lists.
//
// Combined scope set is {kaine_chat, galaxy_read, kaine_mining}, matching the
// "kaine-approved" Authentik group.
func TestConvert_FilterByKaineScopes_MatchesLegacyKaineTools(t *testing.T) {
	defs := MCPToAnthropicAll(MCPToolDefinitions(), kaineDefaultScopes)

	var got []string
	for _, tool := range defs {
		if tool.OfTool != nil {
			got = append(got, tool.OfTool.Name)
		}
	}
	sort.Strings(got)

	want := append([]string{}, legacyKaineTools...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("kaine scope filter size mismatch: got %d tools, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kaine scope filter drift at index %d: got %q, want %q\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}

// TestConvert_FilterByCopilotScopes_MatchesLegacyCopilotTools pins the derived
// copilot tool set byte-for-byte against the legacy copilotAllowedTools map.
// Combined scope set is {copilot_chat, galaxy_read, commander_data}, matching
// the "edin-copilot" Authentik group.
func TestConvert_FilterByCopilotScopes_MatchesLegacyCopilotTools(t *testing.T) {
	defs := MCPToAnthropicAll(MCPToolDefinitions(), copilotDefaultScopes)

	var got []string
	for _, tool := range defs {
		if tool.OfTool != nil {
			got = append(got, tool.OfTool.Name)
		}
	}
	sort.Strings(got)

	want := append([]string{}, legacyCopilotTools...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("copilot scope filter size mismatch: got %d tools, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("copilot scope filter drift at index %d: got %q, want %q\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}
