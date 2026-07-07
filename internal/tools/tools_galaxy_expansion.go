package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// galaxyExpansionCheck validates if a system is a valid expansion target for a power.
// It checks distances to nearest Fortified (20 Ly range) and Stronghold (30 Ly range) systems.
func (e *Executor) galaxyExpansionCheck(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	if systemName == "" {
		return nil, errors.New("system_name is required")
	}
	powerName := strings.TrimSpace(getString(args, "power_name"))
	if powerName == "" {
		powerName = "Nakato Kaine"
	}

	var row struct {
		systemName     string
		state          *string
		powers         []string
		x, y, z        float64
		fortSystem     *string
		fortDistance   *float64
		strongSystem   *string
		strongDistance *float64
	}

	err = store.QueryRow(ctx, `
WITH target AS (
	SELECT c.id64, c.name, c.x::float8 AS x, c.y::float8 AS y, c.z::float8 AS z,
	       sp.powerplay_state, COALESCE(sp.powers_present, '{}') AS powers_present
	FROM galaxy.system_catalog c
	LEFT JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
	WHERE lower(c.name) = lower($1)
	LIMIT 1
)
SELECT
	target.name,
	target.powerplay_state,
	target.powers_present,
	target.x,
	target.y,
	target.z,
	fort.name,
	fort.distance_ly,
	strong.name,
	strong.distance_ly
FROM target
LEFT JOIN LATERAL (
	SELECT c.name, sqrt(power(c.x::float8-target.x, 2)+power(c.y::float8-target.y, 2)+power(c.z::float8-target.z, 2)) AS distance_ly
	FROM galaxy.system_power sp
	JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
	WHERE sp.power_name = $2 AND sp.powerplay_state = 'Fortified'
	ORDER BY distance_ly
	LIMIT 1
) fort ON true
LEFT JOIN LATERAL (
	SELECT c.name, sqrt(power(c.x::float8-target.x, 2)+power(c.y::float8-target.y, 2)+power(c.z::float8-target.z, 2)) AS distance_ly
	FROM galaxy.system_power sp
	JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
	WHERE sp.power_name = $2 AND sp.powerplay_state = 'Stronghold'
	ORDER BY distance_ly
	LIMIT 1
) strong ON true`, systemName, powerName).Scan(
		&row.systemName, &row.state, &row.powers, &row.x, &row.y, &row.z,
		&row.fortSystem, &row.fortDistance, &row.strongSystem, &row.strongDistance,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return map[string]any{"error": "system not found", "system_name": systemName, "power_name": powerName, "source": "postgres"}, nil
		}
		return nil, fmt.Errorf("failed to query expansion check: %w", err)
	}

	fortDist := 0.0
	if row.fortDistance != nil {
		fortDist = *row.fortDistance
	}
	strongDist := 0.0
	if row.strongDistance != nil {
		strongDist = *row.strongDistance
	}
	fortInRange := row.fortDistance != nil && fortDist <= 20
	strongInRange := row.strongDistance != nil && strongDist <= 30
	isValid := fortInRange || strongInRange

	reason := "Outside control bubble"
	if isValid {
		if strongInRange {
			reason = fmt.Sprintf("Within Stronghold range: %.1f Ly from %v (max 30 Ly)", strongDist, derefString(row.strongSystem))
		} else {
			reason = fmt.Sprintf("Within Fortified range: %.1f Ly from %v (max 20 Ly)", fortDist, derefString(row.fortSystem))
		}
	} else {
		reason = fmt.Sprintf("Outside control bubble - nearest Stronghold is %.1f Ly (max 30 Ly), nearest Fortified is %.1f Ly (max 20 Ly)", strongDist, fortDist)
	}

	return map[string]any{
		"system":          row.systemName,
		"power":           powerName,
		"is_valid_target": isValid,
		"current_state":   derefString(row.state),
		"powers_active":   row.powers,
		"coordinates":     map[string]any{"x": row.x, "y": row.y, "z": row.z},
		"nearest_fortified": map[string]any{
			"system":       derefString(row.fortSystem),
			"distance_ly":  round1(fortDist),
			"within_range": fortInRange,
			"max_range":    20,
		},
		"nearest_stronghold": map[string]any{
			"system":       derefString(row.strongSystem),
			"distance_ly":  round1(strongDist),
			"within_range": strongInRange,
			"max_range":    30,
		},
		"reason": reason,
		"source": "postgres",
	}, nil
}

// galaxyNearbyPowerplay finds powerplay activity near a system for a specific power.
func (e *Executor) galaxyNearbyPowerplay(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
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

	var ref struct {
		id64    int64
		name    string
		x, y, z float64
		state   *string
	}
	err = store.QueryRow(ctx, `
SELECT c.id64, c.name, c.x::float8, c.y::float8, c.z::float8, sp.powerplay_state
FROM galaxy.system_catalog c
LEFT JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
WHERE lower(c.name) = lower($1)
LIMIT 1`, systemName).Scan(&ref.id64, &ref.name, &ref.x, &ref.y, &ref.z, &ref.state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return map[string]any{"error": "system not found", "system_name": systemName, "power_name": powerName, "source": "postgres"}, nil
		}
		return nil, fmt.Errorf("failed to query nearby powerplay reference: %w", err)
	}

	nearbyControlled, err := queryNearbyPowerSystems(ctx, store, ref.id64, ref.x, ref.y, ref.z, powerName, maxDistance, []string{"Fortified", "Stronghold"}, false, 10)
	if err != nil {
		return nil, err
	}
	nearbyAcquisition, err := queryNearbyPowerSystems(ctx, store, ref.id64, ref.x, ref.y, ref.z, powerName, maxDistance, []string{"Expansion", "Contested"}, true, 10)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"reference_system": ref.name,
		"reference_state":  derefString(ref.state),
		"coordinates":      map[string]any{"x": ref.x, "y": ref.y, "z": ref.z},
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
		"source": "postgres",
	}, nil
}

// galaxyExpansionFrontier finds systems on the edge of a power's control bubble around a specific control system.
func (e *Executor) galaxyExpansionFrontier(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
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

	rows, err := store.Query(ctx, `
WITH ctrl AS (
	SELECT c.id64, c.name, c.x::float8 AS x, c.y::float8 AS y, c.z::float8 AS z,
	       sp.powerplay_state,
	       CASE WHEN sp.powerplay_state = 'Stronghold' THEN 30.0 ELSE 20.0 END AS max_range
	FROM galaxy.system_catalog c
	JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
	WHERE lower(c.name) = lower($1)
	  AND sp.power_name = $2
	  AND sp.powerplay_state IN ('Fortified','Stronghold')
	LIMIT 1
)
SELECT
	c.name AS system,
	sp.powerplay_state AS state,
	COALESCE(sp.powers_present, '{}') AS powers,
	ctrl.name AS control_system,
	ctrl.powerplay_state AS control_type,
	ctrl.max_range::int AS range_limit,
	sqrt(power(c.x::float8-ctrl.x, 2)+power(c.y::float8-ctrl.y, 2)+power(c.z::float8-ctrl.z, 2)) AS distance_ly,
	sqrt(power(c.x::float8-ctrl.x, 2)+power(c.y::float8-ctrl.y, 2)+power(c.z::float8-ctrl.z, 2)) <= ctrl.max_range AS in_range
FROM ctrl
JOIN galaxy.system_catalog c ON c.id64 <> ctrl.id64
LEFT JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
WHERE (sp.powerplay_state IS NULL OR sp.powerplay_state NOT IN ('Fortified','Stronghold','Exploited'))
  AND c.x BETWEEN ctrl.x - (ctrl.max_range + 10) AND ctrl.x + (ctrl.max_range + 10)
  AND c.y BETWEEN ctrl.y - (ctrl.max_range + 10) AND ctrl.y + (ctrl.max_range + 10)
  AND c.z BETWEEN ctrl.z - (ctrl.max_range + 10) AND ctrl.z + (ctrl.max_range + 10)
  AND sqrt(power(c.x::float8-ctrl.x, 2)+power(c.y::float8-ctrl.y, 2)+power(c.z::float8-ctrl.z, 2)) BETWEEN ctrl.max_range - 5 AND ctrl.max_range + 10
ORDER BY distance_ly
LIMIT 30`, controlSystem, powerName)
	if err != nil {
		return nil, fmt.Errorf("failed to query expansion frontier: %w", err)
	}
	results, err := scanGalaxyRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return map[string]any{"error": "no frontier systems found or control system not found", "control_system": controlSystem, "power_name": powerName, "source": "postgres"}, nil
	}

	var insideFrontier, outsideFrontier []map[string]any
	for _, row := range results {
		dist := toFloat(row["distance_ly"])
		rangeLimit := int(toFloat(row["range_limit"]))
		entry := map[string]any{
			"system":      row["system"],
			"state":       row["state"],
			"distance_ly": round1(dist),
		}
		if powers, ok := row["powers"].([]string); ok && len(powers) > 0 {
			entry["powers"] = powers
		}
		if inRange, _ := row["in_range"].(bool); inRange {
			entry["status"] = fmt.Sprintf("VALID - %.1f Ly inside range", float64(rangeLimit)-dist)
			insideFrontier = append(insideFrontier, entry)
		} else {
			entry["status"] = fmt.Sprintf("OUTSIDE - %.1f Ly beyond range", dist-float64(rangeLimit))
			entry["gap_ly"] = round1(dist - float64(rangeLimit))
			outsideFrontier = append(outsideFrontier, entry)
		}
	}

	result := map[string]any{
		"control_system": controlSystem,
		"control_type":   results[0]["control_type"],
		"range_limit":    results[0]["range_limit"],
		"power":          powerName,
		"source":         "postgres",
	}
	switch showType {
	case "inside":
		result["frontier_inside"] = map[string]any{"count": len(insideFrontier), "description": "Valid expansion targets just inside the control bubble", "systems": insideFrontier}
	case "outside":
		result["frontier_outside"] = map[string]any{"count": len(outsideFrontier), "description": "Potential future targets just outside the control bubble", "systems": outsideFrontier}
	default:
		result["frontier_inside"] = map[string]any{"count": len(insideFrontier), "description": "Valid expansion targets just inside the control bubble", "systems": insideFrontier}
		result["frontier_outside"] = map[string]any{"count": len(outsideFrontier), "description": "Potential future targets just outside the control bubble", "systems": outsideFrontier}
	}
	return result, nil
}

func queryNearbyPowerSystems(ctx context.Context, store interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, refID64 int64, x, y, z float64, powerName string, maxDistance float64, states []string, matchPowersArray bool, limit int) ([]map[string]any, error) {
	powerFilter := "sp.power_name = @power_name"
	if matchPowersArray {
		powerFilter = "@power_name = ANY(COALESCE(sp.powers_present, '{}'))"
	}
	rows, err := store.Query(ctx, `
SELECT
	c.name AS system,
	sp.powerplay_state AS state,
	COALESCE(sp.powers_present, '{}') AS powers,
	sqrt(power(c.x::float8-@x, 2)+power(c.y::float8-@y, 2)+power(c.z::float8-@z, 2)) AS distance_ly
FROM galaxy.system_power sp
JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
WHERE c.id64 <> @ref_id64
  AND sp.powerplay_state = ANY(@states)
  AND `+powerFilter+`
  AND c.x BETWEEN @x - @max_distance AND @x + @max_distance
  AND c.y BETWEEN @y - @max_distance AND @y + @max_distance
  AND c.z BETWEEN @z - @max_distance AND @z + @max_distance
  AND sqrt(power(c.x::float8-@x, 2)+power(c.y::float8-@y, 2)+power(c.z::float8-@z, 2)) <= @max_distance
ORDER BY distance_ly
LIMIT @limit`, pgx.NamedArgs{
		"ref_id64":     refID64,
		"x":            x,
		"y":            y,
		"z":            z,
		"power_name":   powerName,
		"states":       states,
		"max_distance": maxDistance,
		"limit":        limit,
	})
	if err != nil {
		return nil, err
	}
	results, err := scanGalaxyRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, row := range results {
		row["distance_ly"] = round1(toFloat(row["distance_ly"]))
	}
	return results, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}
