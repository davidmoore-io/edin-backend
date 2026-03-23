package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxyExpansionCheck validates if a system is a valid expansion target for a power.
// It checks distances to nearest Fortified (20 Ly range) and Stronghold (30 Ly range) systems.
func (e *Executor) galaxyExpansionCheck(ctx context.Context, args map[string]any) (any, error) {
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
		return nil, errors.New("system_name is required")
	}
	powerName := strings.TrimSpace(getString(args, "power_name"))
	if powerName == "" {
		powerName = "Nakato Kaine"
	}

	query := `
		MATCH (target:System {name: $system_name})

		// Find nearest Fortified for this power
		OPTIONAL MATCH (p:Power {name: $power_name})-[:CONTROLS]->(fort:System)
		WHERE fort.powerplay_state = 'Fortified'
		WITH target, fort,
		     sqrt((target.x - fort.x)*(target.x - fort.x) + (target.y - fort.y)*(target.y - fort.y) + (target.z - fort.z)*(target.z - fort.z)) AS fort_dist
		ORDER BY fort_dist
		WITH target, collect({system: fort.name, distance: fort_dist})[0] AS nearest_fortified

		// Find nearest Stronghold for this power
		OPTIONAL MATCH (p2:Power {name: $power_name})-[:CONTROLS]->(strong:System)
		WHERE strong.powerplay_state = 'Stronghold'
		WITH target, nearest_fortified, strong,
		     sqrt((target.x - strong.x)*(target.x - strong.x) + (target.y - strong.y)*(target.y - strong.y) + (target.z - strong.z)*(target.z - strong.z)) AS strong_dist
		ORDER BY strong_dist
		WITH target, nearest_fortified, collect({system: strong.name, distance: strong_dist})[0] AS nearest_stronghold

		RETURN
		    target.name AS system,
		    target.powerplay_state AS current_state,
		    target.powers AS powers_active,
		    target.x AS x, target.y AS y, target.z AS z,
		    nearest_fortified.system AS nearest_fortified_system,
		    nearest_fortified.distance AS fortified_distance_ly,
		    nearest_fortified.distance <= 20 AS fortified_in_range,
		    nearest_stronghold.system AS nearest_stronghold_system,
		    nearest_stronghold.distance AS stronghold_distance_ly,
		    nearest_stronghold.distance <= 30 AS stronghold_in_range,
		    (nearest_fortified.distance <= 20 OR nearest_stronghold.distance <= 30) AS is_valid_target
	`

	params := map[string]any{
		"system_name": systemName,
		"power_name":  powerName,
	}

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to query expansion check: %w", err)
	}

	if len(results) == 0 {
		return map[string]any{
			"error":       "system not found",
			"system_name": systemName,
			"power_name":  powerName,
			"source":      "memgraph",
		}, nil
	}

	row := results[0]
	isValidTarget, _ := row["is_valid_target"].(bool)
	fortInRange, _ := row["fortified_in_range"].(bool)
	strongInRange, _ := row["stronghold_in_range"].(bool)
	fortDist, _ := row["fortified_distance_ly"].(float64)
	strongDist, _ := row["stronghold_distance_ly"].(float64)

	// Build reason string
	var reason string
	if isValidTarget {
		if strongInRange {
			reason = fmt.Sprintf("Within Stronghold range: %.1f Ly from %v (max 30 Ly)", strongDist, row["nearest_stronghold_system"])
		} else if fortInRange {
			reason = fmt.Sprintf("Within Fortified range: %.1f Ly from %v (max 20 Ly)", fortDist, row["nearest_fortified_system"])
		}
	} else {
		reason = fmt.Sprintf("Outside control bubble - nearest Stronghold is %.1f Ly (max 30 Ly), nearest Fortified is %.1f Ly (max 20 Ly)",
			strongDist, fortDist)
	}

	// Parse powers_active
	var powersActive []string
	if pw, ok := row["powers_active"].([]any); ok {
		for _, p := range pw {
			if pname, ok := p.(string); ok && pname != "" {
				powersActive = append(powersActive, pname)
			}
		}
	}

	return map[string]any{
		"system":          row["system"],
		"power":           powerName,
		"is_valid_target": isValidTarget,
		"current_state":   row["current_state"],
		"powers_active":   powersActive,
		"coordinates":     map[string]any{"x": row["x"], "y": row["y"], "z": row["z"]},
		"nearest_fortified": map[string]any{
			"system":       row["nearest_fortified_system"],
			"distance_ly":  fortDist,
			"within_range": fortInRange,
			"max_range":    20,
		},
		"nearest_stronghold": map[string]any{
			"system":       row["nearest_stronghold_system"],
			"distance_ly":  strongDist,
			"within_range": strongInRange,
			"max_range":    30,
		},
		"reason": reason,
		"source": "memgraph",
	}, nil
}

// galaxyNearbyPowerplay finds powerplay activity near a system for a specific power.
func (e *Executor) galaxyNearbyPowerplay(ctx context.Context, args map[string]any) (any, error) {
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
		return nil, errors.New("system_name is required")
	}
	powerName := strings.TrimSpace(getString(args, "power_name"))
	if powerName == "" {
		powerName = "Nakato Kaine"
	}
	maxDistance := float64(getInt(args, "max_distance", 50))
	if maxDistance > 100 {
		maxDistance = 100
	}

	query := `
		MATCH (target:System {name: $system_name})
		WITH target

		// Subquery for nearby controlled systems
		CALL {
		    WITH target
		    MATCH (p:Power {name: $power_name})-[:CONTROLS]->(controlled:System)
		    WITH target, controlled,
		         sqrt((target.x - controlled.x)*(target.x - controlled.x) + (target.y - controlled.y)*(target.y - controlled.y) + (target.z - controlled.z)*(target.z - controlled.z)) AS dist
		    WHERE dist <= $max_distance
		    RETURN controlled.name AS c_name, controlled.powerplay_state AS c_state, dist AS c_dist
		    ORDER BY c_dist
		    LIMIT 10
		}
		WITH target, collect({system: c_name, state: c_state, distance: c_dist}) AS nearby_controlled

		// Subquery for nearby acquisition systems
		CALL {
		    WITH target
		    MATCH (acq:System)
		    WHERE acq.powerplay_state IN ['Expansion', 'Contested']
		      AND $power_name IN acq.powers
		    WITH target, acq,
		         sqrt((target.x - acq.x)*(target.x - acq.x) + (target.y - acq.y)*(target.y - acq.y) + (target.z - acq.z)*(target.z - acq.z)) AS dist
		    WHERE dist <= $max_distance
		    RETURN acq.name AS a_name, acq.powerplay_state AS a_state, acq.powers AS a_powers, dist AS a_dist
		    ORDER BY a_dist
		    LIMIT 10
		}
		WITH target, nearby_controlled, collect({system: a_name, state: a_state, powers: a_powers, distance: a_dist}) AS nearby_acquisition

		RETURN
		    target.name AS reference_system,
		    target.x AS x, target.y AS y, target.z AS z,
		    target.powerplay_state AS reference_state,
		    nearby_controlled,
		    nearby_acquisition
	`

	params := map[string]any{
		"system_name":  systemName,
		"power_name":   powerName,
		"max_distance": maxDistance,
	}

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to query nearby powerplay: %w", err)
	}

	if len(results) == 0 {
		return map[string]any{
			"error":       "system not found",
			"system_name": systemName,
			"power_name":  powerName,
			"source":      "memgraph",
		}, nil
	}

	row := results[0]

	// Process nearby_controlled
	var nearbyControlled []map[string]any
	if nc, ok := row["nearby_controlled"].([]any); ok {
		for _, item := range nc {
			if m, ok := item.(map[string]any); ok {
				if m["system"] != nil {
					nearbyControlled = append(nearbyControlled, map[string]any{
						"system":      m["system"],
						"state":       m["state"],
						"distance_ly": math.Round(m["distance"].(float64)*10) / 10,
					})
				}
			}
		}
	}

	// Process nearby_acquisition
	var nearbyAcquisition []map[string]any
	if na, ok := row["nearby_acquisition"].([]any); ok {
		for _, item := range na {
			if m, ok := item.(map[string]any); ok {
				if m["system"] != nil {
					entry := map[string]any{
						"system":      m["system"],
						"state":       m["state"],
						"distance_ly": math.Round(m["distance"].(float64)*10) / 10,
					}
					if powers, ok := m["powers"].([]any); ok {
						var powerList []string
						for _, p := range powers {
							if ps, ok := p.(string); ok {
								powerList = append(powerList, ps)
							}
						}
						entry["powers"] = powerList
					}
					nearbyAcquisition = append(nearbyAcquisition, entry)
				}
			}
		}
	}

	return map[string]any{
		"reference_system": row["reference_system"],
		"reference_state":  row["reference_state"],
		"coordinates":      map[string]any{"x": row["x"], "y": row["y"], "z": row["z"]},
		"power":            powerName,
		"max_distance":     maxDistance,
		"nearby_controlled": map[string]any{
			"count":   len(nearbyControlled),
			"systems": nearbyControlled,
		},
		"nearby_acquisition": map[string]any{
			"count":   len(nearbyAcquisition),
			"systems": nearbyAcquisition,
		},
		"source": "memgraph",
	}, nil
}

// galaxyExpansionFrontier finds systems on the edge of a power's control bubble around a specific control system.
func (e *Executor) galaxyExpansionFrontier(ctx context.Context, args map[string]any) (any, error) {
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}
	if e.memgraph == nil {
		return nil, errors.New("memgraph not available")
	}

	controlSystem := strings.TrimSpace(getString(args, "control_system"))
	if controlSystem == "" {
		return nil, errors.New("control_system is required (name of a Fortified or Stronghold system)")
	}
	powerName := strings.TrimSpace(getString(args, "power_name"))
	if powerName == "" {
		powerName = "Nakato Kaine"
	}
	showType := strings.ToLower(strings.TrimSpace(getString(args, "show")))
	if showType == "" {
		showType = "both"
	}

	// Query for systems at the edge of the control bubble
	query := `
		MATCH (ctrl:System {name: $control_system})
		WHERE ctrl.powerplay_state IN ['Fortified', 'Stronghold']
		WITH ctrl,
		     CASE WHEN ctrl.powerplay_state = 'Stronghold' THEN 30 ELSE 20 END AS max_range,
		     ctrl.controlling_power AS power_name

		// Find systems at the edge of this control system's bubble
		MATCH (s:System)
		WHERE s.name <> ctrl.name
		WITH ctrl, max_range, power_name, s,
		     sqrt((s.x - ctrl.x)*(s.x - ctrl.x) + (s.y - ctrl.y)*(s.y - ctrl.y) + (s.z - ctrl.z)*(s.z - ctrl.z)) AS dist
		WHERE dist >= max_range - 5 AND dist <= max_range + 10  // Edge zone: 5Ly inside to 10Ly outside

		// Check if controlled by any power
		OPTIONAL MATCH (p:Power)-[r:CONTROLS]->(s)
		WITH ctrl, max_range, power_name, s, dist, p IS NOT NULL AS is_controlled
		WHERE NOT is_controlled

		RETURN
		    s.name AS system,
		    s.powerplay_state AS state,
		    s.powers AS powers,
		    ctrl.name AS control_system,
		    ctrl.powerplay_state AS control_type,
		    max_range AS range_limit,
		    dist AS distance_ly,
		    dist <= max_range AS in_range
		ORDER BY dist
		LIMIT 30
	`

	params := map[string]any{
		"control_system": controlSystem,
		"power_name":     powerName,
	}

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to query expansion frontier: %w", err)
	}

	if len(results) == 0 {
		return map[string]any{
			"error":          "no frontier systems found or control system not found",
			"control_system": controlSystem,
			"power_name":     powerName,
			"source":         "memgraph",
		}, nil
	}

	// Separate into inside (valid targets) and outside (future targets)
	var insideFrontier, outsideFrontier []map[string]any

	for _, row := range results {
		inRange, _ := row["in_range"].(bool)
		dist, _ := row["distance_ly"].(float64)
		rangeLimit, _ := row["range_limit"].(int64)

		entry := map[string]any{
			"system":      row["system"],
			"state":       row["state"],
			"distance_ly": math.Round(dist*10) / 10,
		}

		// Add powers if present
		if powers, ok := row["powers"].([]any); ok {
			var powerList []string
			for _, p := range powers {
				if ps, ok := p.(string); ok && ps != "" {
					powerList = append(powerList, ps)
				}
			}
			if len(powerList) > 0 {
				entry["powers"] = powerList
			}
		}

		if inRange {
			entry["status"] = fmt.Sprintf("VALID - %.1f Ly inside range", float64(rangeLimit)-dist)
			insideFrontier = append(insideFrontier, entry)
		} else {
			entry["status"] = fmt.Sprintf("OUTSIDE - %.1f Ly beyond range", dist-float64(rangeLimit))
			entry["gap_ly"] = math.Round((dist-float64(rangeLimit))*10) / 10
			outsideFrontier = append(outsideFrontier, entry)
		}
	}

	// Filter based on showType
	result := map[string]any{
		"control_system": controlSystem,
		"control_type":   results[0]["control_type"],
		"range_limit":    results[0]["range_limit"],
		"power":          powerName,
		"source":         "memgraph",
	}

	switch showType {
	case "inside":
		result["frontier_inside"] = map[string]any{
			"count":       len(insideFrontier),
			"description": "Valid expansion targets just inside the control bubble",
			"systems":     insideFrontier,
		}
	case "outside":
		result["frontier_outside"] = map[string]any{
			"count":       len(outsideFrontier),
			"description": "Potential future targets just outside the control bubble",
			"systems":     outsideFrontier,
		}
	default: // "both"
		result["frontier_inside"] = map[string]any{
			"count":       len(insideFrontier),
			"description": "Valid expansion targets just inside the control bubble",
			"systems":     insideFrontier,
		}
		result["frontier_outside"] = map[string]any{
			"count":       len(outsideFrontier),
			"description": "Potential future targets just outside the control bubble",
			"systems":     outsideFrontier,
		}
	}

	return result, nil
}
