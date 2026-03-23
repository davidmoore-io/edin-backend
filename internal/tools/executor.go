package tools

import (
	"context"
	"fmt"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/edsm"
	"github.com/edin-space/edin-backend/internal/kaine"
	"github.com/edin-space/edin-backend/internal/memgraph"
	"github.com/edin-space/edin-backend/internal/ops"
	"github.com/edin-space/edin-backend/internal/spansh"
	"github.com/edin-space/edin-backend/internal/store"
)

// HistoryQuerier provides historical powerplay data from EDDN raw feed.
// Implemented by *store.CacheStore when EDDN raw database is configured.
type HistoryQuerier interface {
	GetPowerplayHistory(ctx context.Context, systemNames []string, days int) ([]store.SystemPowerplayHistory, error)
	GetPowerplayCycleData(ctx context.Context, systemNames []string, cycleOffset int) ([]store.CycleSystemData, error)
}

// ToolName enumerates MCP/Anthropic tool identifiers.
type ToolName string

const (
	ToolStatusService ToolName = "status_service"
	ToolRestart       ToolName = "restart_service"
	ToolTailLogs      ToolName = "tail_logs"
	ToolRunAnsible    ToolName = "run_ansible"
	ToolListServices  ToolName = "list_services"
	ToolSpanshQuery   ToolName = "spansh_query"
	ToolRetrieveRoute ToolName = "retrieve_carrier_route"
	ToolSystemProfile ToolName = "system_profile"

	// Galaxy database tools (Memgraph)
	ToolGalaxySystem            ToolName = "galaxy_system"
	ToolGalaxyStation           ToolName = "galaxy_station"
	ToolGalaxyFleetCarrier      ToolName = "galaxy_fleet_carrier"
	ToolGalaxyBodies            ToolName = "galaxy_bodies"
	ToolGalaxySignals           ToolName = "galaxy_signals"
	ToolGalaxyPower             ToolName = "galaxy_power"
	ToolGalaxyFaction           ToolName = "galaxy_faction"
	ToolGalaxyStats             ToolName = "galaxy_stats"
	ToolGalaxyQuery             ToolName = "galaxy_query"
	ToolGalaxyMarket            ToolName = "galaxy_market"
	ToolGalaxyExpansionCheck    ToolName = "galaxy_expansion_check"
	ToolGalaxyNearbyPowerplay   ToolName = "galaxy_nearby_powerplay"
	ToolGalaxyExpansionFrontier ToolName = "galaxy_expansion_frontier"
	ToolGalaxyHistory           ToolName = "galaxy_history"
	ToolGalaxyPowerplayCycle    ToolName = "galaxy_powerplay_cycle"
	ToolGalaxyPlasmiumBuyers    ToolName = "galaxy_plasmium_buyers"
	ToolGalaxyLTDBuyers         ToolName = "galaxy_ltd_buyers"
	ToolGalaxyExpansionTargets  ToolName = "galaxy_expansion_targets"
	ToolGalaxySchema            ToolName = "galaxy_schema"
)

// opsOnlyTools are restricted to ScopeLlmOperator (Discord operators only).
// These tools manage server infrastructure and should not be exposed to public users.
var opsOnlyTools = map[ToolName]bool{
	ToolStatusService: true,
	ToolRestart:       true,
	ToolTailLogs:      true,
	ToolRunAnsible:    true,
	ToolListServices:  true,
}

// kaineAllowedTools are available to Kaine chat users (ScopeKaineChat).
// These are Elite Dangerous query tools that are safe for public access.
var kaineAllowedTools = map[ToolName]bool{
	// Galaxy database tools (EDIN - Elite Dangerous Intel Network)
	ToolGalaxySystem:            true,
	ToolGalaxyStation:           true,
	ToolGalaxyFleetCarrier:      true,
	ToolGalaxyBodies:            true,
	ToolGalaxySignals:           true,
	ToolGalaxyPower:             true,
	ToolGalaxyFaction:           true,
	ToolGalaxyStats:             true,
	ToolGalaxyQuery:             true,
	ToolGalaxyMarket:            true,
	ToolGalaxyExpansionCheck:    true,
	ToolGalaxyNearbyPowerplay:   true,
	ToolGalaxyExpansionFrontier: true,
	ToolGalaxyHistory:           true,
	ToolGalaxyPowerplayCycle:    true,
	ToolGalaxyPlasmiumBuyers:    true,
	ToolGalaxyLTDBuyers:         true,
	ToolGalaxyExpansionTargets:  true,
	ToolGalaxySchema:            true,
	// Elite intelligence tools
	ToolSystemProfile: true,
	// Carrier route planning
	ToolSpanshQuery:   true,
	ToolRetrieveRoute: true,
	// Meta-tool
	ToolDescribeTool: true,
}

// UpdateBroadcaster is an interface for broadcasting system updates via WebSocket.
type UpdateBroadcaster interface {
	BroadcastSystemUpdate(systemName, source string)
	BroadcastFullRefresh(source string)
}

// Executor wires low-level operations to tool invocations.
type Executor struct {
	ops           *ops.Manager
	spansh        *spansh.Client
	edsm          *edsm.Client
	cacheStore    *store.CacheStore
	memgraph      *memgraph.Client
	kaineStore    *kaine.Store
	historyClient HistoryQuerier
	broadcaster   UpdateBroadcaster
	logger        func(msg string)
}

// NewExecutor constructs a tool executor.
func NewExecutor(opsManager *ops.Manager, spanshClient *spansh.Client, edsmClient *edsm.Client, cacheStore *store.CacheStore) *Executor {
	return &Executor{
		ops:        opsManager,
		spansh:     spanshClient,
		edsm:       edsmClient,
		cacheStore: cacheStore,
	}
}

// WithBroadcaster sets a broadcaster for real-time WebSocket updates.
func (e *Executor) WithBroadcaster(broadcaster UpdateBroadcaster) *Executor {
	e.broadcaster = broadcaster
	return e
}

// WithMemgraph sets the Memgraph client for real-time galaxy data.
func (e *Executor) WithMemgraph(client *memgraph.Client) *Executor {
	e.memgraph = client
	return e
}

// WithKaineStore sets the Kaine store for accessing mining maps and objectives.
func (e *Executor) WithKaineStore(store *kaine.Store) *Executor {
	e.kaineStore = store
	return e
}

// WithHistoryClient sets the EDDN raw client for historical powerplay queries.
func (e *Executor) WithHistoryClient(client HistoryQuerier) *Executor {
	e.historyClient = client
	return e
}

func requireScope(ctx context.Context, scope authz.Scope) error {
	if scope == "" {
		return nil
	}
	if authz.Allow(authz.ScopesFromContext(ctx), scope) {
		return nil
	}
	return fmt.Errorf("unauthorized: requires %s scope", scope)
}

// Invoke executes the named tool with arguments originating from MCP or Anthropics tool calls.
func (e *Executor) Invoke(ctx context.Context, name string, args map[string]any) (any, error) {
	toolName := ToolName(name)

	// Defense in depth: Block ops tools for Kaine chat users even if Claude tries to call them.
	// Tool definitions are filtered by scope, but this catches any edge cases.
	if opsOnlyTools[toolName] {
		scopes := authz.ScopesFromContext(ctx)
		hasOpsScope := false
		for _, s := range scopes {
			if s == authz.ScopeLlmOperator || s == authz.ScopeAdmin {
				hasOpsScope = true
				break
			}
		}
		if !hasOpsScope {
			return nil, fmt.Errorf("tool %q is not available in this context", name)
		}
	}

	switch toolName {
	// Operations tools
	case ToolStatusService:
		return e.status(ctx, args)
	case ToolRestart:
		return e.restart(ctx, args)
	case ToolTailLogs:
		return e.tailLogs(ctx, args)
	case ToolRunAnsible:
		return e.runAnsible(ctx, args)
	case ToolListServices:
		return e.listServices(ctx)

	// Spansh tools
	case ToolSpanshQuery:
		return e.spanshQuery(ctx, args)
	case ToolRetrieveRoute:
		return e.retrieveCarrierRoute(ctx, args)

	// System profile
	case ToolSystemProfile:
		return e.systemProfile(ctx, args)

	// Galaxy core tools
	case ToolGalaxySystem:
		return e.galaxySystem(ctx, args)
	case ToolGalaxyStation:
		return e.galaxyStation(ctx, args)
	case ToolGalaxyFleetCarrier:
		return e.galaxyFleetCarrier(ctx, args)
	case ToolGalaxyBodies:
		return e.galaxyBodies(ctx, args)
	case ToolGalaxySignals:
		return e.galaxySignals(ctx, args)

	// Galaxy powerplay tools
	case ToolGalaxyPower:
		return e.galaxyPower(ctx, args)
	case ToolGalaxyFaction:
		return e.galaxyFaction(ctx, args)
	case ToolGalaxyStats:
		return e.galaxyStats(ctx, args)

	// Galaxy query tools
	case ToolGalaxyQuery:
		return e.galaxyQuery(ctx, args)

	// Galaxy market tools
	case ToolGalaxyMarket:
		return e.galaxyMarket(ctx, args)

	// Galaxy expansion tools
	case ToolGalaxyExpansionCheck:
		return e.galaxyExpansionCheck(ctx, args)
	case ToolGalaxyNearbyPowerplay:
		return e.galaxyNearbyPowerplay(ctx, args)
	case ToolGalaxyExpansionFrontier:
		return e.galaxyExpansionFrontier(ctx, args)

	// Historical data
	case ToolGalaxyHistory:
		return e.galaxyHistory(ctx, args)
	case ToolGalaxyPowerplayCycle:
		return e.galaxyPowerplayCycle(ctx, args)

	// Meta-tools
	case ToolDescribeTool:
		return e.describeTool(args)

	// Mining intel tools (Orok's processes)
	case ToolGalaxyPlasmiumBuyers:
		return e.galaxyPlasmiumBuyers(ctx, args)
	case ToolGalaxyLTDBuyers:
		return e.galaxyLTDBuyers(ctx, args)
	case ToolGalaxyExpansionTargets:
		return e.galaxyExpansionTargets(ctx, args)
	case ToolGalaxySchema:
		return e.galaxySchema(ctx, args)

	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func (e *Executor) describeTool(args map[string]any) (any, error) {
	toolName, _ := args["tool_name"].(string)
	if toolName == "" {
		return nil, fmt.Errorf("tool_name is required")
	}
	return DescribeTool(toolName)
}
