package tools

import (
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
)

// copilotTestScopes mirrors the default commander scope set (edin-copilot
// group) used by the WS handler when populating the tool context.
var copilotTestScopes = []authz.Scope{
	authz.ScopeCopilotChat,
	authz.ScopeGalaxyRead,
	authz.ScopeCommanderData,
}

func TestBetaToolDefs_CopilotScope_ReturnsNonEmptyToolList(t *testing.T) {
	defs := SlimBetaToolDefinitionsForScopes(copilotTestScopes)
	if len(defs) == 0 {
		t.Fatal("expected non-empty tool list for copilot scope")
	}
}

func TestBetaToolDefs_CopilotScope_HasCommanderEvents(t *testing.T) {
	defs := SlimBetaToolDefinitionsForScopes(copilotTestScopes)
	for _, d := range defs {
		if d.OfTool != nil && ToolName(d.OfTool.Name) == ToolCommanderEvents {
			return
		}
	}
	t.Fatalf("expected %s in copilot tool list", ToolCommanderEvents)
}

func TestBetaToolDefs_CopilotScope_HasGalaxySystem(t *testing.T) {
	defs := SlimBetaToolDefinitionsForScopes(copilotTestScopes)
	for _, d := range defs {
		if d.OfTool != nil && ToolName(d.OfTool.Name) == ToolGalaxySystem {
			return
		}
	}
	t.Fatalf("expected %s in copilot tool list", ToolGalaxySystem)
}

func TestBetaToolDefs_CopilotScope_DoesNotHavePlasmiumBuyers(t *testing.T) {
	defs := SlimBetaToolDefinitionsForScopes(copilotTestScopes)
	for _, d := range defs {
		if d.OfTool != nil && ToolName(d.OfTool.Name) == ToolGalaxyPlasmiumBuyers {
			t.Fatalf("unexpected %s in copilot tool list (should be excluded)", ToolGalaxyPlasmiumBuyers)
		}
	}
}

func TestBetaToolDefs_CopilotScope_DoesNotHaveLTDBuyers(t *testing.T) {
	defs := SlimBetaToolDefinitionsForScopes(copilotTestScopes)
	for _, d := range defs {
		if d.OfTool != nil && ToolName(d.OfTool.Name) == ToolGalaxyLTDBuyers {
			t.Fatalf("unexpected %s in copilot tool list (should be excluded)", ToolGalaxyLTDBuyers)
		}
	}
}
