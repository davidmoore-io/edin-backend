package tools

import "github.com/edin-space/edin-backend/internal/authz"

// toolScopes declares the scope required to invoke each tool.
//
// An empty scope (authz.Scope("")) means the tool is available to any
// caller who has already passed the product's coarse endpoint gate
// (kaine_chat, copilot_chat, llm_operator, etc.). Non-empty scopes are
// checked by the executor and by the definitions filter before a tool
// is listed to or dispatched for a caller.
//
// This registry is the single source of truth — every ToolName must
// have an entry. A guardrail test in scopes_test.go enforces that
// adding a new ToolName without declaring its scope fails the build.
//
// The mapping is intentionally chosen to preserve the legacy
// kaineAllowedTools / copilotAllowedTools visibility exactly; see
// Task 2 in the Authentik commander access plan for the parity test
// that pins this behaviour.
var toolScopes = map[ToolName]authz.Scope{
	// Ops tools — Discord operators only. opsOnlyTools in executor.go
	// also enforces this at a defense-in-depth layer.
	ToolStatusService: authz.ScopeLlmOperator,
	ToolRestart:       authz.ScopeLlmOperator,
	ToolTailLogs:      authz.ScopeLlmOperator,
	ToolRunAnsible:    authz.ScopeLlmOperator,
	ToolListServices:  authz.ScopeLlmOperator,

	// Galaxy read — the bulk of the Memgraph/Elite query surface.
	// Shared by Kaine and copilot; all callers with galaxy_read can
	// invoke these.
	ToolGalaxySystem:            authz.ScopeGalaxyRead,
	ToolGalaxyStation:           authz.ScopeGalaxyRead,
	ToolGalaxyFleetCarrier:      authz.ScopeGalaxyRead,
	ToolGalaxyBodies:            authz.ScopeGalaxyRead,
	ToolGalaxySignals:           authz.ScopeGalaxyRead,
	ToolGalaxyPower:             authz.ScopeGalaxyRead,
	ToolGalaxyFaction:           authz.ScopeGalaxyRead,
	ToolGalaxyStats:             authz.ScopeGalaxyRead,
	ToolGalaxyQuery:             authz.ScopeGalaxyRead,
	ToolGalaxyMarket:            authz.ScopeGalaxyRead,
	ToolGalaxyExpansionCheck:    authz.ScopeGalaxyRead,
	ToolGalaxyNearbyPowerplay:   authz.ScopeGalaxyRead,
	ToolGalaxyExpansionFrontier: authz.ScopeGalaxyRead,
	ToolGalaxyHistory:           authz.ScopeGalaxyRead,
	ToolGalaxyPowerplayCycle:    authz.ScopeGalaxyRead,
	// expansion_targets is in both legacy allow-maps, so it stays on
	// galaxy_read to preserve copilot visibility even though the plan
	// prose initially grouped it with mining tools.
	ToolGalaxyExpansionTargets: authz.ScopeGalaxyRead,
	ToolBgsGuideSearch:         authz.ScopeGalaxyRead,
	ToolPowerplayGuideSearch:   authz.ScopeGalaxyRead,
	ToolSpanshQuery:            authz.ScopeGalaxyRead,
	ToolRetrieveRoute:          authz.ScopeGalaxyRead,
	ToolSystemProfile:          authz.ScopeGalaxyRead,

	// Kaine-mining specific — tools in legacy kaineAllowedTools but
	// NOT in copilotAllowedTools. Require kaine_mining (granted to
	// kaine-approved, kaine-god, edin-copilot-trusted).
	ToolGalaxyPlasmiumBuyers: authz.ScopeKaineMining,
	ToolGalaxyLTDBuyers:      authz.ScopeKaineMining,
	ToolGalaxySchema:         authz.ScopeKaineMining,

	// Commander-scoped — only copilot callers see these in the legacy
	// set. commander_data is granted to edin-copilot (and by extension
	// edin-copilot-trusted) and to kaine-god.
	ToolCommanderEvents:   authz.ScopeCommanderData,
	ToolCommanderLocation: authz.ScopeCommanderData,

	// Meta-tool — usable by any authenticated caller regardless of
	// fine-grained scopes.
	ToolDescribeTool: authz.Scope(""),
}
