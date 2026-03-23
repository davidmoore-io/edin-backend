package tools

import (
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/edin-space/edin-backend/internal/authz"
)

// MCPToolDefinitions exposes the tool definitions for MCP clients.
func MCPToolDefinitions() []mcp.Tool {
	return []mcp.Tool{
		mcp.NewTool(string(ToolStatusService),
			mcp.WithDescription("Fetch container status for a managed service"),
			mcp.WithString("service", mcp.Required(), mcp.Description("Service identifier, e.g. dayz or web")),
		),
		mcp.NewTool(string(ToolRestart),
			mcp.WithDescription("Restart a managed container"),
			mcp.WithString("service", mcp.Required(), mcp.Description("Service identifier")),
		),
		mcp.NewTool(string(ToolTailLogs),
			mcp.WithDescription("Retrieve recent logs for a service"),
			mcp.WithString("service", mcp.Required(), mcp.Description("Service identifier")),
			mcp.WithNumber("limit", mcp.Description("Number of log lines to return (default 200)")),
		),
		mcp.NewTool(string(ToolRunAnsible),
			mcp.WithDescription("Execute an allow-listed Ansible playbook"),
			mcp.WithString("playbook", mcp.Required(), mcp.Description("Playbook name")),
			mcp.WithObject("extra_vars", mcp.Description("Additional variables for the playbook")),
		),
		mcp.NewTool(string(ToolListServices),
			mcp.WithDescription("List all managed services with their container names and labels"),
		),
		mcp.NewTool(string(ToolSpanshQuery),
			mcp.WithDescription("Plan fleet carrier routes. Use galaxy_* tools for system/station/powerplay queries instead."),
			mcp.WithString("operation", mcp.Required(), mcp.Description("Operation: fleet_carrier_route (tritium-efficient routes) or health (API status)")),
			mcp.WithObject("parameters", mcp.Description("Operation-specific parameters (source, destination, capacity_used for routes)")),
		),
		mcp.NewTool(string(ToolRetrieveRoute),
			mcp.WithDescription("Retrieve completed fleet carrier route details"),
			mcp.WithString("job_id", mcp.Required(), mcp.Description("Job identifier returned by fleet_carrier_route")),
		),
		mcp.NewTool(string(ToolSystemProfile),
			mcp.WithDescription("Generate a comprehensive system dossier from EDIN"),
			mcp.WithString("system_name", mcp.Description("Elite Dangerous system name (e.g. 'Sol').")),
			mcp.WithString("system_id", mcp.Description("Optional numeric system id for disambiguation.")),
		),
		// Galaxy database tools (EDIN)
		mcp.NewTool(string(ToolGalaxySystem),
			mcp.WithDescription("Query a star system from the galaxy database. Use 'include' to request only what you need — omitting it returns everything which can be very large. For coordinate-only lookups use include=[\"system\"]."),
			mcp.WithString("system_name", mcp.Required(), mcp.Description("Star system name (e.g. 'Sol', 'Cubeo')")),
			mcp.WithArray("include", mcp.Description("Sections to return: 'system' (coords, government, powerplay), 'stations', 'bodies', 'factions', 'signals', 'fleet_carriers'. Omit for all.")),
		),
		mcp.NewTool(string(ToolGalaxyStation),
			mcp.WithDescription("Query station data from the galaxy database"),
			mcp.WithNumber("market_id", mcp.Description("Station market ID for direct lookup")),
			mcp.WithString("station_name", mcp.Description("Station name prefix for search")),
			mcp.WithString("system_name", mcp.Description("System name to get all stations in that system")),
			mcp.WithNumber("limit", mcp.Description("Max results to return (default 10)")),
		),
		mcp.NewTool(string(ToolGalaxyFleetCarrier),
			mcp.WithDescription("Query fleet carrier data from the galaxy database"),
			mcp.WithString("carrier_id", mcp.Description("Fleet carrier ID (e.g. 'VHT-49Z') for direct lookup")),
			mcp.WithString("system_name", mcp.Description("System name to find fleet carriers in that system")),
		),
		mcp.NewTool(string(ToolGalaxyBodies),
			mcp.WithDescription("Query celestial body data from the galaxy database"),
			mcp.WithString("system_name", mcp.Description("System name to get all bodies in that system")),
			mcp.WithString("signal_type", mcp.Description("Signal type to find bodies with signals (bio, geo)")),
			mcp.WithNumber("min_signals", mcp.Description("Minimum signal count (default 1)")),
			mcp.WithNumber("limit", mcp.Description("Max results to return (default 50)")),
		),
		mcp.NewTool(string(ToolGalaxySignals),
			mcp.WithDescription("Query system-level signals from the galaxy database (Combat Zones, RES sites, USS, Titans)"),
			mcp.WithString("system_name", mcp.Required(), mcp.Description("System name to query signals")),
			mcp.WithString("signal_type", mcp.Description("Filter by signal type (Combat, ResourceExtraction, USS, Titan)")),
		),
		mcp.NewTool(string(ToolGalaxyPower),
			mcp.WithDescription("Query powerplay power data from the galaxy database"),
			mcp.WithString("power_name", mcp.Required(), mcp.Description("Power name (e.g. 'Nakato Kaine', 'Aisling Duval')")),
			mcp.WithBoolean("include_systems", mcp.Description("Include list of controlled systems (default false)")),
			mcp.WithNumber("limit", mcp.Description("Max systems to return if include_systems=true (default 50)")),
		),
		mcp.NewTool(string(ToolGalaxyFaction),
			mcp.WithDescription("Query minor faction data from the galaxy database. Can search by faction name, system name, or faction state."),
			mcp.WithString("faction_name", mcp.Description("Faction name for direct lookup")),
			mcp.WithString("system_name", mcp.Description("System name to get all factions in that system")),
			mcp.WithString("faction_state", mcp.Description("Find systems where factions are in this state (e.g. 'War', 'Civil War', 'Expansion', 'Boom', 'Bust', 'Famine', 'Outbreak', 'Lockdown')")),
			mcp.WithBoolean("include_systems", mcp.Description("Include faction's systems when querying by name (default false)")),
			mcp.WithNumber("limit", mcp.Description("Max results to return (default 50)")),
		),
		mcp.NewTool(string(ToolGalaxyStats),
			mcp.WithDescription("Get galaxy database statistics (node counts, relationship counts)"),
		),
		mcp.NewTool(string(ToolGalaxyQuery),
			mcp.WithDescription("Execute ad-hoc Cypher query against Memgraph galaxy database. Read-only operations only (no CREATE/DELETE/SET). LIMIT auto-appended if missing."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Cypher query to execute")),
			mcp.WithObject("parameters", mcp.Description("Query parameters for $var substitution")),
		),
		mcp.NewTool(string(ToolGalaxyMarket),
			mcp.WithDescription("Query commodity market data from Memgraph. Find places to buy/sell commodities, check market prices at stations/systems. Commodity names are normalized automatically (spaces removed, lowercased). Returns station_type and distance_ls for each result."),
			mcp.WithString("commodity", mcp.Description("Commodity name (e.g. 'power generators', 'tritium', 'platinum') - spaces/case normalized automatically")),
			mcp.WithString("operation", mcp.Description("Operation type: 'buy' to find where to buy (lowest prices), 'sell' to find where to sell (highest prices)")),
			mcp.WithString("system_name", mcp.Description("Get all market data for stations in this system")),
			mcp.WithString("station_name", mcp.Description("Search for market data by station name (partial match)")),
			mcp.WithString("reference_system", mcp.Description("Calculate distances from this system (default: Sol)")),
			mcp.WithNumber("max_distance", mcp.Description("Maximum distance in Ly from reference_system (default: 100)")),
			mcp.WithString("station_type", mcp.Description("Filter by station type: 'orbital' (Coriolis/Orbis/Ocellus), 'outpost', 'planetary', or 'any' (default)")),
			mcp.WithNumber("max_distance_ls", mcp.Description("Maximum station distance from star in light-seconds (e.g., 500 for reasonable supercruise)")),
			mcp.WithString("min_pad", mcp.Description("Minimum landing pad size: 'L' (large only), 'M' (medium+), 'S' (any)")),
			mcp.WithNumber("min_price", mcp.Description("Minimum price filter (for sell operations)")),
			mcp.WithNumber("max_price", mcp.Description("Maximum price filter (for buy operations)")),
			mcp.WithNumber("min_demand", mcp.Description("Minimum demand filter (for sell operations)")),
			mcp.WithNumber("min_stock", mcp.Description("Minimum stock filter (for buy operations)")),
			mcp.WithNumber("limit", mcp.Description("Max results to return (default: 20, max: 100)")),
			mcp.WithBoolean("exclude_carriers", mcp.Description("Exclude fleet carriers from results (default: true). Set false only when user explicitly asks for fleet carrier prices")),
		),
		mcp.NewTool(string(ToolGalaxyExpansionCheck),
			mcp.WithDescription("Check if a system is a valid expansion target for a power. Validates distances to nearest Fortified (20 Ly range) and Stronghold (30 Ly range) systems."),
			mcp.WithString("system_name", mcp.Required(), mcp.Description("Name of the system to check")),
			mcp.WithString("power_name", mcp.Description("Power name to check expansion for (default: 'Nakato Kaine')")),
		),
		mcp.NewTool(string(ToolGalaxyNearbyPowerplay),
			mcp.WithDescription("Find powerplay activity near a system. Returns nearby controlled systems (Fortified/Stronghold) and acquisition systems (Expansion/Contested) for a specific power."),
			mcp.WithString("system_name", mcp.Required(), mcp.Description("Name of the reference system")),
			mcp.WithString("power_name", mcp.Description("Power name to filter for (default: 'Nakato Kaine')")),
			mcp.WithNumber("max_distance", mcp.Description("Maximum search radius in Ly (default: 50, max: 100)")),
		),
		mcp.NewTool(string(ToolGalaxyExpansionFrontier),
			mcp.WithDescription("Find systems on the edge of a power's control bubble around a specific Fortified or Stronghold system. Shows valid targets just inside range and potential targets just outside."),
			mcp.WithString("control_system", mcp.Required(), mcp.Description("Name of a Fortified or Stronghold system to check frontier around")),
			mcp.WithString("power_name", mcp.Description("Power name (default: 'Nakato Kaine')")),
			mcp.WithString("show", mcp.Description("What to show: 'inside' (valid targets), 'outside' (future targets), or 'both' (default: 'both')")),
		),
		mcp.NewTool(string(ToolGalaxyHistory),
			mcp.WithDescription("Query historical powerplay data for systems. Returns daily reinforcement/undermining values, controlling power changes, and observation counts. Data comes from EDDN raw feed (up to 30 days). Use for trend analysis, cycle comparisons, or investigating historical activity."),
			mcp.WithString("system_name", mcp.Description("Single system name to query (use system_names for multiple)")),
			mcp.WithArray("system_names", mcp.Description("Array of system names to query (max 10)")),
			mcp.WithNumber("days", mcp.Description("Number of days of history to retrieve (default 14, max 30)")),
		),
		mcp.NewTool(string(ToolGalaxyPowerplayCycle),
			mcp.WithDescription("Query powerplay data with cycle/tick awareness. The powerplay tick occurs every Thursday 07:00 UTC. Use cycle=0 for current week (since last Thursday), cycle=-1 for previous week, etc. Returns start/end reinforcement, undermining values, and optionally week-over-week comparison. Ideal for 'how are we doing this week' or 'compare to last week' queries."),
			mcp.WithString("system_name", mcp.Description("Single system name to query (use system_names for multiple)")),
			mcp.WithArray("system_names", mcp.Description("Array of system names to query (max 10)")),
			mcp.WithNumber("cycle", mcp.Description("Cycle offset: 0=current week, -1=last week, -2=two weeks ago, etc. (default 0, min -8)")),
			mcp.WithBoolean("compare", mcp.Description("If true and cycle=0, also returns previous cycle data and week-over-week deltas")),
		),
		mcp.NewTool(string(ToolGalaxyPlasmiumBuyers),
			mcp.WithDescription("Find stations in Boom state that buy Platinum/Osmium near Kaine mining maps. Implements Orok's mining intel process for Plasmium buyers. Returns maps grouped by mining system with nearby Boom stations scored by Platinum/Osmium demand. Scoring: >=1288t demand = optimal (100 pts Platinum, 80 pts Osmium), sub-threshold = linear scale, Military/Colony economy = 40 pts (hidden demand)."),
		),
		mcp.NewTool(string(ToolGalaxyLTDBuyers),
			mcp.WithDescription("Find stations that buy Low Temperature Diamonds near Kaine mining maps. Returns maps grouped by mining system with nearby stations scored by LTD demand and price. Use when players ask about LTD sell locations or diamond mining intel."),
		),
		mcp.NewTool(string(ToolGalaxyExpansionTargets),
			mcp.WithDescription("Find optimal expansion targets for Kaine powerplay. Returns ranked systems within the control bubble (20 Ly of Fortified, 30 Ly of Stronghold) that are valid acquisition targets, scored by strategic value. Use when players ask 'where should we expand?' or 'best acquire targets'."),
		),
		mcp.NewTool(string(ToolGalaxySchema),
			mcp.WithDescription("Return the current Memgraph database schema: node labels with counts, edge types with counts, indexes, and constraints. Use before writing ad-hoc Cypher queries to understand available data structures."),
		),
		DescribeToolMCPDefinition(),
	}
}

// AnthropicsToolDefinitions returns tool definitions for Anthropic Messages API.
// Generated from MCPToolDefinitions() via the converter, plus the WebSearch tool.
func AnthropicsToolDefinitions() []sdk.ToolUnionParam {
	defs := MCPToAnthropicAll(MCPToolDefinitions())
	defs = append(defs, sdk.ToolUnionParam{
		OfWebSearchTool20250305: &sdk.WebSearchTool20250305Param{
			Name: constant.ValueOf[constant.WebSearch](),
			Type: constant.ValueOf[constant.WebSearch20250305](),
		},
	})
	return defs
}

// AnthropicsToolDefinitionsForScope returns tool definitions filtered by authorization scope.
// ScopeLlmOperator and ScopeAdmin get all tools; ScopeKaineChat gets only Elite Dangerous query tools.
func AnthropicsToolDefinitionsForScope(scope authz.Scope) []sdk.ToolUnionParam {
	allTools := AnthropicsToolDefinitions()

	// Full access for ops and admin
	if scope == authz.ScopeLlmOperator || scope == authz.ScopeAdmin {
		return allTools
	}

	// Filter for Kaine context - only include allowed tools
	var filtered []sdk.ToolUnionParam
	for _, tool := range allTools {
		if tool.OfTool != nil {
			name := ToolName(tool.OfTool.Name)
			if kaineAllowedTools[name] {
				filtered = append(filtered, tool)
			}
		}
	}
	return filtered
}

// complexTools are tools that have detailed usage guidance in ToolGuidance.
// When generating slim definitions, these tools get 1-line descriptions and no parameter schemas.
var complexTools = map[ToolName]bool{
	ToolGalaxyMarket:            true,
	ToolGalaxyQuery:             true,
	ToolGalaxyFaction:           true,
	ToolGalaxyHistory:           true,
	ToolGalaxyPowerplayCycle:    true,
	ToolGalaxyExpansionCheck:    true,
	ToolGalaxyNearbyPowerplay:   true,
	ToolGalaxyExpansionFrontier: true,
}

// slimDescriptions provides 1-line descriptions for complex tools used in slim definitions.
var slimDescriptions = map[ToolName]string{
	ToolGalaxyMarket:            "Query commodity market data. Use describe_tool for parameters.",
	ToolGalaxyQuery:             "Execute ad-hoc Cypher query against galaxy database. Use describe_tool for schema.",
	ToolGalaxyFaction:           "Query minor faction data. Use describe_tool for parameters.",
	ToolGalaxyHistory:           "Query historical powerplay data. Use describe_tool for parameters.",
	ToolGalaxyPowerplayCycle:    "Query cycle-aware powerplay data. Use describe_tool for parameters.",
	ToolGalaxyExpansionCheck:    "Check if system is a valid expansion target. Use describe_tool for parameters.",
	ToolGalaxyNearbyPowerplay:   "Find powerplay activity near a system. Use describe_tool for parameters.",
	ToolGalaxyExpansionFrontier: "Find systems on the edge of a power's control bubble. Use describe_tool for parameters.",
}

// SlimBetaToolDefinitions returns beta tool definitions with slim descriptions for complex tools.
// Simple/parameterless tools keep full definitions; complex tools get 1-line descriptions
// with no parameter schemas (model should use describe_tool first).
func SlimBetaToolDefinitions() []sdk.BetaToolUnionParam {
	fullDefs := MCPToolDefinitions()
	result := make([]sdk.BetaToolUnionParam, 0, len(fullDefs)+1)

	for _, tool := range fullDefs {
		name := ToolName(tool.Name)
		if complexTools[name] {
			// Slim: short description, no params
			desc := slimDescriptions[name]
			result = append(result, sdk.BetaToolUnionParam{
				OfTool: &sdk.BetaToolParam{
					Name:        tool.Name,
					Description: param.NewOpt(desc),
					Type:        sdk.BetaToolTypeCustom,
					InputSchema: sdk.BetaToolInputSchemaParam{
						Properties: map[string]any{},
					},
				},
			})
		} else {
			result = append(result, MCPToBeta(tool))
		}
	}

	// Append WebSearch
	result = append(result, sdk.BetaToolUnionParam{
		OfWebSearchTool20250305: &sdk.BetaWebSearchTool20250305Param{
			Name: constant.ValueOf[constant.WebSearch](),
			Type: constant.ValueOf[constant.WebSearch20250305](),
		},
	})

	return result
}

// SlimBetaToolDefinitionsForScope returns slim beta tool definitions filtered by scope.
func SlimBetaToolDefinitionsForScope(scope authz.Scope) []sdk.BetaToolUnionParam {
	allTools := SlimBetaToolDefinitions()

	if scope == authz.ScopeLlmOperator || scope == authz.ScopeAdmin {
		return allTools
	}

	var filtered []sdk.BetaToolUnionParam
	for _, tool := range allTools {
		if tool.OfTool != nil {
			name := ToolName(tool.OfTool.Name)
			if kaineAllowedTools[name] {
				filtered = append(filtered, tool)
			}
		}
	}
	return filtered
}

// BetaToolDefinitions returns tool definitions for the Anthropic Beta Messages API.
// Generated from MCPToolDefinitions() via the converter, plus the WebSearch tool.
func BetaToolDefinitions() []sdk.BetaToolUnionParam {
	defs := MCPToBetaAll(MCPToolDefinitions())
	defs = append(defs, sdk.BetaToolUnionParam{
		OfWebSearchTool20250305: &sdk.BetaWebSearchTool20250305Param{
			Name: constant.ValueOf[constant.WebSearch](),
			Type: constant.ValueOf[constant.WebSearch20250305](),
		},
	})
	return defs
}

// BetaToolDefinitionsForScope returns beta tool definitions filtered by authorization scope.
func BetaToolDefinitionsForScope(scope authz.Scope) []sdk.BetaToolUnionParam {
	allTools := BetaToolDefinitions()

	if scope == authz.ScopeLlmOperator || scope == authz.ScopeAdmin {
		return allTools
	}

	var filtered []sdk.BetaToolUnionParam
	for _, tool := range allTools {
		if tool.OfTool != nil {
			name := ToolName(tool.OfTool.Name)
			if kaineAllowedTools[name] {
				filtered = append(filtered, tool)
			}
		}
	}
	return filtered
}
