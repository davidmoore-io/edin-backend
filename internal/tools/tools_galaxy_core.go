package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxySystem queries a system with all relationships from Memgraph.
// The optional "include" parameter filters which sections are returned.
func (e *Executor) galaxySystem(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (galaxy queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	if systemName == "" {
		systemName = strings.TrimSpace(getString(args, "system"))
	}
	if systemName == "" {
		return nil, errors.New("system_name parameter is required")
	}

	// Parse include filter — when empty, return all sections
	includeSet := parseIncludeFilter(args)

	// Get full system with all relationships
	full, err := e.memgraph.GetSystemFull(ctx, systemName)
	if err != nil {
		return nil, err
	}
	if full == nil {
		return map[string]any{
			"found":   false,
			"message": fmt.Sprintf("System '%s' not found in galaxy database", systemName),
		}, nil
	}

	// Build response with only requested sections
	response := map[string]any{
		"found":  true,
		"source": "memgraph",
	}

	if includeSet["system"] && full.System != nil {
		response["system"] = full.System
	}
	if includeSet["stations"] && len(full.Stations) > 0 {
		response["stations"] = full.Stations
		response["station_count"] = len(full.Stations)
	}
	if includeSet["bodies"] && len(full.Bodies) > 0 {
		response["bodies"] = full.Bodies
		response["body_count"] = len(full.Bodies)
	}
	if includeSet["factions"] && len(full.Factions) > 0 {
		response["factions"] = full.Factions
		response["faction_count"] = len(full.Factions)
	}
	if includeSet["signals"] && len(full.Signals) > 0 {
		response["signals"] = full.Signals
		response["signal_count"] = len(full.Signals)
	}
	if includeSet["fleet_carriers"] && len(full.FleetCarriers) > 0 {
		response["fleet_carriers"] = full.FleetCarriers
		response["fleet_carrier_count"] = len(full.FleetCarriers)
	}

	return response, nil
}

// parseIncludeFilter extracts the "include" array from args and returns a set
// of section names. When the array is empty or missing, all sections are included.
func parseIncludeFilter(args map[string]any) map[string]bool {
	allSections := map[string]bool{
		"system": true, "stations": true, "bodies": true,
		"factions": true, "signals": true, "fleet_carriers": true,
	}

	raw, ok := args["include"]
	if !ok || raw == nil {
		return allSections
	}

	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return allSections
	}

	set := make(map[string]bool, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(strings.ToLower(s))
			if allSections[s] {
				set[s] = true
			}
		}
	}

	if len(set) == 0 {
		return allSections
	}
	return set
}

// galaxyStation queries station data from Memgraph.
func (e *Executor) galaxyStation(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (galaxy queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	// Support lookup by market_id, name search, or system
	marketID := int64(getInt(args, "market_id", 0))
	stationName := strings.TrimSpace(getString(args, "station_name"))
	systemName := strings.TrimSpace(getString(args, "system_name"))

	if marketID > 0 {
		// Direct lookup by market ID
		station, err := e.memgraph.GetStation(ctx, marketID)
		if err != nil {
			return nil, err
		}
		if station == nil {
			return map[string]any{
				"found":   false,
				"message": fmt.Sprintf("Station with market_id %d not found", marketID),
			}, nil
		}
		return map[string]any{
			"found":   true,
			"station": station,
			"source":  "memgraph",
		}, nil
	}

	if stationName != "" {
		// Search by name prefix
		limit := getInt(args, "limit", 10)
		stations, err := e.memgraph.SearchStations(ctx, stationName, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":    len(stations) > 0,
			"stations": stations,
			"count":    len(stations),
			"query":    stationName,
			"source":   "memgraph",
		}, nil
	}

	if systemName != "" {
		// Get all stations in a system
		stations, err := e.memgraph.GetStationsInSystem(ctx, systemName)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":    len(stations) > 0,
			"stations": stations,
			"count":    len(stations),
			"system":   systemName,
			"source":   "memgraph",
		}, nil
	}

	return nil, errors.New("market_id, station_name, or system_name parameter is required")
}

// galaxyFleetCarrier queries fleet carrier data from Memgraph.
func (e *Executor) galaxyFleetCarrier(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (galaxy queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	carrierID := strings.TrimSpace(getString(args, "carrier_id"))
	systemName := strings.TrimSpace(getString(args, "system_name"))

	if carrierID != "" {
		// Direct lookup by carrier ID (e.g., "VHT-49Z")
		carrier, err := e.memgraph.GetFleetCarrier(ctx, carrierID)
		if err != nil {
			return nil, err
		}
		if carrier == nil {
			return map[string]any{
				"found":   false,
				"message": fmt.Sprintf("Fleet carrier '%s' not found", carrierID),
			}, nil
		}
		return map[string]any{
			"found":   true,
			"carrier": carrier,
			"source":  "memgraph",
		}, nil
	}

	if systemName != "" {
		// Get all fleet carriers in a system
		carriers, err := e.memgraph.GetFleetCarriersInSystem(ctx, systemName)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":    len(carriers) > 0,
			"carriers": carriers,
			"count":    len(carriers),
			"system":   systemName,
			"source":   "memgraph",
		}, nil
	}

	return nil, errors.New("carrier_id or system_name parameter is required")
}

// galaxyBodies queries body data from Memgraph.
func (e *Executor) galaxyBodies(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (galaxy queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	signalType := strings.TrimSpace(getString(args, "signal_type"))
	minSignals := getInt(args, "min_signals", 1)
	limit := getInt(args, "limit", 50)

	if systemName != "" {
		// Get all bodies in a system
		bodies, err := e.memgraph.GetBodiesInSystem(ctx, systemName)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":  len(bodies) > 0,
			"bodies": bodies,
			"count":  len(bodies),
			"system": systemName,
			"source": "memgraph",
		}, nil
	}

	if signalType != "" {
		// Find bodies with specific signal types
		bodies, err := e.memgraph.FindBodiesWithSignals(ctx, signalType, minSignals, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":       len(bodies) > 0,
			"bodies":      bodies,
			"count":       len(bodies),
			"signal_type": signalType,
			"min_signals": minSignals,
			"source":      "memgraph",
		}, nil
	}

	return nil, errors.New("system_name or signal_type parameter is required")
}

// galaxySignals queries system-level signals from Memgraph.
func (e *Executor) galaxySignals(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (galaxy queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	signalType := strings.TrimSpace(getString(args, "signal_type"))

	if systemName == "" {
		return nil, errors.New("system_name parameter is required")
	}

	signals, err := e.memgraph.GetSystemSignals(ctx, systemName, signalType)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"found":       len(signals) > 0,
		"signals":     signals,
		"count":       len(signals),
		"system":      systemName,
		"signal_type": signalType,
		"source":      "memgraph",
	}, nil
}
