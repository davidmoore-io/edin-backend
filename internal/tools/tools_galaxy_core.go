package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/jackc/pgx/v5"
)

// galaxySystem queries a system with all relationships from the relational galaxy store.
// The optional "include" parameter filters which sections are returned.
func (e *Executor) galaxySystem(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
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

	full, err := store.GetSystemFull(ctx, systemName)
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
		"source": "postgres",
	}

	if includeSet["system"] && full.System != nil {
		response["system"] = full.System
	}
	if includeSet["stations"] && len(full.Stations) > 0 {
		response["stations"] = full.Stations
		response["station_count"] = len(full.Stations)
	}
	if includeSet["bodies"] {
		bodies, err := e.queryBodiesInSystem(ctx, full.System.ID64, full.System.Name)
		if err != nil {
			return nil, err
		}
		if len(bodies) > 0 {
			response["bodies"] = bodies
			response["body_count"] = len(bodies)
		}
	}
	if includeSet["factions"] && len(full.Factions) > 0 {
		response["factions"] = full.Factions
		response["faction_count"] = len(full.Factions)
	}
	if includeSet["signals"] {
		signals, err := e.querySystemSignals(ctx, full.System.ID64, full.System.Name, "")
		if err != nil {
			return nil, err
		}
		if len(signals) > 0 {
			response["signals"] = signals
			response["signal_count"] = len(signals)
		}
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

// galaxyStation queries station data from the relational galaxy store.
func (e *Executor) galaxyStation(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	// Support lookup by market_id, name search, or system
	marketID := int64(getInt(args, "market_id", 0))
	stationName := strings.TrimSpace(getString(args, "station_name"))
	systemName := strings.TrimSpace(getString(args, "system_name"))

	if marketID > 0 {
		station, err := queryStationByMarketID(ctx, store, marketID)
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
			"source":  "postgres",
		}, nil
	}

	if stationName != "" {
		limit := getInt(args, "limit", 10)
		stations, err := store.SearchStations(ctx, stationName, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":    len(stations) > 0,
			"stations": stations,
			"count":    len(stations),
			"query":    stationName,
			"source":   "postgres",
		}, nil
	}

	if systemName != "" {
		full, err := store.GetSystemFull(ctx, systemName)
		if err != nil {
			return nil, err
		}
		var stations any = []any{}
		count := 0
		if full != nil {
			stations = full.Stations
			count = len(full.Stations)
			systemName = full.System.Name
		}
		return map[string]any{
			"found":    count > 0,
			"stations": stations,
			"count":    count,
			"system":   systemName,
			"source":   "postgres",
		}, nil
	}

	return nil, errors.New("market_id, station_name, or system_name parameter is required")
}

// galaxyFleetCarrier queries fleet carrier data from the relational galaxy store.
func (e *Executor) galaxyFleetCarrier(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	carrierID := strings.TrimSpace(getString(args, "carrier_id"))
	systemName := strings.TrimSpace(getString(args, "system_name"))

	if carrierID != "" {
		carrier, err := queryFleetCarrier(ctx, store, carrierID)
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
			"source":  "postgres",
		}, nil
	}

	if systemName != "" {
		carriers, resolved, err := queryFleetCarriersInSystem(ctx, store, systemName)
		if err != nil {
			return nil, err
		}
		if resolved != "" {
			systemName = resolved
		}
		return map[string]any{
			"found":    len(carriers) > 0,
			"carriers": carriers,
			"count":    len(carriers),
			"system":   systemName,
			"source":   "postgres",
		}, nil
	}

	return nil, errors.New("carrier_id or system_name parameter is required")
}

// galaxyBodies queries body data from the relational galaxy store.
func (e *Executor) galaxyBodies(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	signalType := strings.TrimSpace(getString(args, "signal_type"))
	minSignals := getInt(args, "min_signals", 1)
	limit := getInt(args, "limit", 50)

	if systemName != "" {
		var systemID64 int64
		var resolved string
		err := store.QueryRow(ctx, `SELECT id64, name FROM galaxy.system_catalog WHERE lower(name) = lower($1) LIMIT 1`, systemName).Scan(&systemID64, &resolved)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return map[string]any{"found": false, "bodies": []any{}, "count": 0, "system": systemName, "source": "postgres"}, nil
			}
			return nil, err
		}
		bodies, err := e.queryBodiesInSystem(ctx, systemID64, resolved)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":  len(bodies) > 0,
			"bodies": bodies,
			"count":  len(bodies),
			"system": resolved,
			"source": "postgres",
		}, nil
	}

	if signalType != "" {
		bodies, err := e.queryBodiesWithSignals(ctx, signalType, minSignals, limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":       len(bodies) > 0,
			"bodies":      bodies,
			"count":       len(bodies),
			"signal_type": signalType,
			"min_signals": minSignals,
			"source":      "postgres",
		}, nil
	}

	return nil, errors.New("system_name or signal_type parameter is required")
}

// galaxySignals queries system-level signals from the relational galaxy store.
func (e *Executor) galaxySignals(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	signalType := strings.TrimSpace(getString(args, "signal_type"))

	if systemName == "" {
		return nil, errors.New("system_name parameter is required")
	}

	var systemID64 int64
	var resolved string
	err = store.QueryRow(ctx, `SELECT id64, name FROM galaxy.system_catalog WHERE lower(name) = lower($1) LIMIT 1`, systemName).Scan(&systemID64, &resolved)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return map[string]any{
				"found":       false,
				"signals":     []any{},
				"count":       0,
				"system":      systemName,
				"signal_type": signalType,
				"source":      "postgres",
			}, nil
		}
		return nil, err
	}

	signals, err := e.querySystemSignals(ctx, systemID64, resolved, signalType)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"found":       len(signals) > 0,
		"signals":     signals,
		"count":       len(signals),
		"system":      resolved,
		"signal_type": signalType,
		"source":      "postgres",
	}, nil
}

func queryStationByMarketID(ctx context.Context, store *galaxystore.Store, marketID int64) (map[string]any, error) {
	rows, err := store.Query(ctx, `
SELECT
	st.market_id AS id64,
	st.name,
	st.station_type,
	st.system_id64,
	COALESCE(sys.name, c.name, m.system_name) AS system_name,
	st.dist_from_star_ls::float8,
	CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' WHEN st.small_pads > 0 THEN 'S' ELSE '' END AS max_pad,
	st.kind = 'planetary_depot' AS is_planetary,
	st.services,
	cf.name AS controlling_faction,
	st.last_event_time,
	m.market_id IS NOT NULL AS has_market,
	sy.market_id IS NOT NULL AS has_shipyard,
	o.market_id IS NOT NULL AS has_outfitting
FROM galaxy.station st
LEFT JOIN galaxy.system sys ON sys.id64 = st.system_id64
LEFT JOIN galaxy.system_catalog c ON c.id64 = st.system_id64
LEFT JOIN galaxy.market m ON m.market_id = st.market_id
LEFT JOIN galaxy.faction cf ON cf.faction_id = st.controlling_faction_id
LEFT JOIN galaxy.shipyard sy ON sy.market_id = st.market_id
LEFT JOIN galaxy.outfitting o ON o.market_id = st.market_id
WHERE st.market_id = $1
LIMIT 1`, marketID)
	if err != nil {
		return nil, err
	}
	stations, err := scanGalaxyRows(ctx, rows)
	if err != nil || len(stations) == 0 {
		return nil, err
	}
	return stations[0], nil
}

func queryFleetCarrier(ctx context.Context, store *galaxystore.Store, carrierID string) (map[string]any, error) {
	rows, err := store.Query(ctx, `
SELECT
	fc.carrier_id,
	fc.name,
	fc.current_system_id64,
	COALESCE(sys.name, c.name, '') AS current_system_name,
	fc.first_seen,
	fc.last_event_time AS last_seen,
	fc.jump_count
FROM galaxy.fleet_carrier fc
LEFT JOIN galaxy.system sys ON sys.id64 = fc.current_system_id64
LEFT JOIN galaxy.system_catalog c ON c.id64 = fc.current_system_id64
WHERE fc.carrier_id = $1
LIMIT 1`, strings.ToUpper(carrierID))
	if err != nil {
		return nil, err
	}
	out, err := scanGalaxyRows(ctx, rows)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out[0], nil
}

func queryFleetCarriersInSystem(ctx context.Context, store *galaxystore.Store, systemName string) ([]map[string]any, string, error) {
	rows, err := store.Query(ctx, `
WITH target AS (
	SELECT id64, name
	FROM galaxy.system_catalog
	WHERE lower(name) = lower($1)
	LIMIT 1
)
SELECT
	fc.carrier_id,
	fc.name,
	fc.current_system_id64,
	target.name AS current_system_name,
	fc.first_seen,
	fc.last_event_time AS last_seen,
	fc.jump_count
FROM target
JOIN galaxy.fleet_carrier fc ON fc.current_system_id64 = target.id64
ORDER BY fc.last_event_time DESC, fc.carrier_id`, systemName)
	if err != nil {
		return nil, "", err
	}
	carriers, err := scanGalaxyRows(ctx, rows)
	if err != nil {
		return nil, "", err
	}
	resolved, _, _ := queryOneString(ctx, store, `SELECT name FROM galaxy.system_catalog WHERE lower(name) = lower($1) LIMIT 1`, systemName)
	return carriers, resolved, nil
}

func (e *Executor) queryBodiesInSystem(ctx context.Context, systemID64 int64, systemName string) ([]map[string]any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(ctx, `
SELECT
	b.system_id64 AS id64,
	b.body_id,
	b.name,
	b.type,
	b.sub_type,
	b.distance_from_arrival::float8,
	b.is_landable,
	b.terraform_state,
	b.physical,
	b.last_event_time AS last_eddn_update,
	COALESCE(r.rings, '[]'::jsonb) AS rings
FROM galaxy.body b
LEFT JOIN LATERAL (
	SELECT jsonb_agg(
		jsonb_build_object(
			'name', r.name,
			'ring_class', r.ring_class,
			'reserve_level', r.reserve_level,
			'hotspot_types', COALESCE(h.hotspot_types, '[]'::jsonb),
			'hotspots', COALESCE(h.hotspots, '[]'::jsonb)
		)
		ORDER BY r.name
	) AS rings
	FROM galaxy.ring r
	LEFT JOIN LATERAL (
		SELECT
			jsonb_agg(c.name ORDER BY c.name) AS hotspot_types,
			jsonb_agg(c.name || ':' || rh.count::text ORDER BY c.name) AS hotspots
		FROM galaxy.ring_hotspot rh
		JOIN galaxy.commodity c ON c.commodity_id = rh.commodity_id
		WHERE rh.system_id64 = r.system_id64
		  AND rh.ring_name = r.name
	) h ON true
	WHERE r.system_id64 = b.system_id64
	  AND r.body_id = b.body_id
) r ON true
WHERE b.system_id64 = $1
ORDER BY b.distance_from_arrival NULLS LAST, b.body_id`, systemID64)
	if err != nil {
		return nil, err
	}
	bodies, err := scanGalaxyRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, body := range bodies {
		body["system_name"] = systemName
	}
	return bodies, nil
}

func (e *Executor) queryBodiesWithSignals(ctx context.Context, signalType string, minSignals, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(ctx, `
SELECT
	COALESCE(sys.name, c.name) AS system_name,
	b.system_id64 AS id64,
	b.body_id,
	b.name,
	b.type,
	b.sub_type,
	b.distance_from_arrival::float8,
	b.is_landable,
	b.terraform_state,
	bs.type AS signal_type,
	bs.type_localised AS signal_name,
	bs.count,
	b.last_event_time AS last_eddn_update
FROM galaxy.body_signal bs
JOIN galaxy.body b ON b.system_id64 = bs.system_id64 AND b.body_id = bs.body_id
JOIN galaxy.system_catalog c ON c.id64 = b.system_id64
LEFT JOIN galaxy.system sys ON sys.id64 = b.system_id64
WHERE (bs.type ILIKE '%' || $1 || '%' OR bs.type_localised ILIKE '%' || $1 || '%')
  AND bs.count >= $2
ORDER BY bs.count DESC, c.name, b.body_id
LIMIT $3`, signalType, minSignals, limit)
	if err != nil {
		return nil, err
	}
	return scanGalaxyRows(ctx, rows)
}

func (e *Executor) querySystemSignals(ctx context.Context, systemID64 int64, systemName, signalType string) ([]map[string]any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(ctx, `
SELECT
	$2::text AS system_name,
	bs.type AS signal_type,
	bs.type_localised AS signal_name,
	''::text AS uss_type,
	false AS is_station,
	''::text AS spawning_faction,
	''::text AS spawning_state,
	SUM(bs.count)::int AS count,
	MIN(bs.last_event_time) AS first_seen,
	MAX(bs.last_event_time) AS last_eddn_update
FROM galaxy.body_signal bs
WHERE bs.system_id64 = $1
  AND ($3 = '' OR bs.type ILIKE '%' || $3 || '%' OR bs.type_localised ILIKE '%' || $3 || '%')
GROUP BY bs.type, bs.type_localised
ORDER BY bs.type, count DESC`, systemID64, systemName, signalType)
	if err != nil {
		return nil, err
	}
	return scanGalaxyRows(ctx, rows)
}
