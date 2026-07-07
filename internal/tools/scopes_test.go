package tools

import (
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
)

// allToolNames enumerates every ToolName constant declared across the
// tools package (executor.go and tools_describe.go). This list must
// stay in sync with the ToolName constant block — if a new tool is
// added without appending it here and declaring its scope in
// toolScopes, TestToolScopes_EveryDefinedToolHasAnEntry fails.
var allToolNames = []ToolName{
	ToolStatusService,
	ToolRestart,
	ToolTailLogs,
	ToolRunAnsible,
	ToolListServices,
	ToolSpanshQuery,
	ToolRetrieveRoute,
	ToolSystemProfile,
	ToolGalaxySystem,
	ToolGalaxyStation,
	ToolGalaxyFleetCarrier,
	ToolGalaxyBodies,
	ToolGalaxySignals,
	ToolGalaxySurfaceSites,
	ToolGalaxyPower,
	ToolGalaxyFaction,
	ToolGalaxyStats,
	ToolGalaxyQuery,
	ToolGalaxyMarket,
	ToolGalaxyExpansionCheck,
	ToolGalaxyNearbyPowerplay,
	ToolGalaxyExpansionFrontier,
	ToolGalaxyHistory,
	ToolGalaxyPowerplayCycle,
	ToolGalaxyPlasmiumBuyers,
	ToolGalaxyLTDBuyers,
	ToolGalaxyExpansionTargets,
	ToolGalaxySchema,
	ToolBgsGuideSearch,
	ToolPowerplayGuideSearch,
	ToolCommanderEvents,
	ToolCommanderLocation,
	ToolDescribeTool,
}

func TestToolScopes_EveryDefinedToolHasAnEntry(t *testing.T) {
	for _, name := range allToolNames {
		if _, ok := toolScopes[name]; !ok {
			t.Errorf("tool %q has no entry in toolScopes — every ToolName must declare a scope", name)
		}
	}
	// Reverse check: no stale entries that reference nonexistent tools.
	known := make(map[ToolName]struct{}, len(allToolNames))
	for _, name := range allToolNames {
		known[name] = struct{}{}
	}
	for name := range toolScopes {
		if _, ok := known[name]; !ok {
			t.Errorf("toolScopes contains entry for unknown tool %q", name)
		}
	}
}

func TestToolScopes_OpsTools_RequireLlmOperator(t *testing.T) {
	opsTools := []ToolName{
		ToolStatusService,
		ToolRestart,
		ToolTailLogs,
		ToolRunAnsible,
		ToolListServices,
	}
	for _, name := range opsTools {
		if got := toolScopes[name]; got != authz.ScopeLlmOperator {
			t.Errorf("toolScopes[%q] = %q, want %q", name, got, authz.ScopeLlmOperator)
		}
	}
}

func TestToolScopes_GalaxyReadTools_RequireGalaxyRead(t *testing.T) {
	galaxyReadTools := []ToolName{
		ToolGalaxySystem,
		ToolGalaxyStation,
		ToolGalaxyFleetCarrier,
		ToolGalaxyBodies,
		ToolGalaxySignals,
		ToolGalaxySurfaceSites,
		ToolGalaxyPower,
		ToolGalaxyFaction,
		ToolGalaxyStats,
		ToolGalaxyQuery,
		ToolGalaxyMarket,
		ToolGalaxyExpansionCheck,
		ToolGalaxyNearbyPowerplay,
		ToolGalaxyExpansionFrontier,
		ToolGalaxyHistory,
		ToolGalaxyPowerplayCycle,
		ToolGalaxyExpansionTargets,
		ToolBgsGuideSearch,
		ToolPowerplayGuideSearch,
		ToolSpanshQuery,
		ToolRetrieveRoute,
		ToolSystemProfile,
	}
	for _, name := range galaxyReadTools {
		if got := toolScopes[name]; got != authz.ScopeGalaxyRead {
			t.Errorf("toolScopes[%q] = %q, want %q", name, got, authz.ScopeGalaxyRead)
		}
	}
}

func TestToolScopes_MiningTools_RequireKaineMining(t *testing.T) {
	miningTools := []ToolName{
		ToolGalaxyPlasmiumBuyers,
		ToolGalaxyLTDBuyers,
		ToolGalaxySchema,
	}
	for _, name := range miningTools {
		if got := toolScopes[name]; got != authz.ScopeKaineMining {
			t.Errorf("toolScopes[%q] = %q, want %q", name, got, authz.ScopeKaineMining)
		}
	}
}

func TestToolScopes_CommanderTools_RequireCommanderData(t *testing.T) {
	commanderTools := []ToolName{
		ToolCommanderEvents,
		ToolCommanderLocation,
	}
	for _, name := range commanderTools {
		if got := toolScopes[name]; got != authz.ScopeCommanderData {
			t.Errorf("toolScopes[%q] = %q, want %q", name, got, authz.ScopeCommanderData)
		}
	}
}

func TestToolScopes_DescribeTool_HasEmptyScope(t *testing.T) {
	if got := toolScopes[ToolDescribeTool]; got != authz.Scope("") {
		t.Errorf("toolScopes[%q] = %q, want empty scope", ToolDescribeTool, got)
	}
}
