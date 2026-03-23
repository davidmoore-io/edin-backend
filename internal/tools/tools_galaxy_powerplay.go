package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxyPower queries powerplay power data from Memgraph.
func (e *Executor) galaxyPower(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (galaxy queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	powerName := strings.TrimSpace(getString(args, "power_name"))
	if powerName == "" {
		powerName = strings.TrimSpace(getString(args, "power"))
	}
	if powerName == "" {
		return nil, errors.New("power_name parameter is required")
	}

	includeSystems := getBool(args, "include_systems", false)
	limit := getInt(args, "limit", 50)

	power, err := e.memgraph.GetPower(ctx, powerName)
	if err != nil {
		return nil, err
	}
	if power == nil {
		return map[string]any{
			"found":   false,
			"message": fmt.Sprintf("Power '%s' not found", powerName),
		}, nil
	}

	response := map[string]any{
		"found":  true,
		"power":  power,
		"source": "memgraph",
	}

	if includeSystems {
		systems, err := e.memgraph.GetPowerSystems(ctx, powerName, limit)
		if err == nil {
			response["systems"] = systems
			response["system_count"] = len(systems)
		}
	}

	return response, nil
}

// galaxyFaction queries minor faction data from Memgraph.
func (e *Executor) galaxyFaction(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (faction queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	factionName := strings.TrimSpace(getString(args, "faction_name"))
	if factionName == "" {
		factionName = strings.TrimSpace(getString(args, "faction"))
	}
	systemName := strings.TrimSpace(getString(args, "system_name"))
	factionState := strings.TrimSpace(getString(args, "faction_state"))

	if factionName != "" {
		// Get faction info and optionally its systems
		faction, err := e.memgraph.GetFaction(ctx, factionName)
		if err != nil {
			return nil, err
		}
		if faction == nil {
			return map[string]any{
				"found":   false,
				"message": fmt.Sprintf("Faction '%s' not found", factionName),
			}, nil
		}

		response := map[string]any{
			"found":   true,
			"faction": faction,
			"source":  "memgraph",
		}

		// If faction_state is also provided, find systems where this faction is in that state
		if factionState != "" {
			limit := getInt(args, "limit", 50)
			results, err := e.memgraph.FindSystemsByFactionState(ctx, factionState, factionName, limit)
			if err == nil {
				response["systems_in_state"] = results
				response["state_count"] = len(results)
				response["state_filter"] = factionState
			}
		} else if getBool(args, "include_systems", false) {
			// Include systems if requested (without state filter)
			limit := getInt(args, "limit", 50)
			presences, err := e.memgraph.GetFactionSystems(ctx, factionName, limit)
			if err == nil {
				response["systems"] = presences
				response["system_count"] = len(presences)
			}
		}

		return response, nil
	}

	if systemName != "" {
		// Get all factions in a system
		factions, err := e.memgraph.GetFactionsInSystem(ctx, systemName)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":    len(factions) > 0,
			"factions": factions,
			"count":    len(factions),
			"system":   systemName,
			"source":   "memgraph",
		}, nil
	}

	if factionState != "" {
		// Find all systems where any faction is in the given state
		limit := getInt(args, "limit", 50)
		results, err := e.memgraph.FindSystemsByFactionState(ctx, factionState, "", limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":         len(results) > 0,
			"systems":       results,
			"count":         len(results),
			"faction_state": factionState,
			"source":        "memgraph",
		}, nil
	}

	return nil, errors.New("faction_name, system_name, or faction_state parameter is required")
}

// galaxyStats returns galaxy database statistics from Memgraph.
func (e *Executor) galaxyStats(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (galaxy queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	stats, err := e.memgraph.GetGalaxyStats(ctx)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"stats":  stats,
		"source": "memgraph",
	}, nil
}

// galaxySchema returns the current Memgraph schema: labels, edge types, indexes, constraints, and counts.
func (e *Executor) galaxySchema(ctx context.Context, args map[string]any) (any, error) {
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	schema, err := e.memgraph.GetSchema(ctx)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"schema": schema,
		"source": "memgraph",
	}, nil
}
