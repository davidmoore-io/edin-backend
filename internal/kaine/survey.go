package kaine

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// SurveyRow represents one row in the survey export: a (map, system, station) tuple.
type SurveyRow struct {
	MapSystem       string     `json:"map_system"`
	MapPowerState   string     `json:"map_power_state"`
	SearchRadiusLY  int        `json:"search_radius_ly"`
	MapBody         string     `json:"map_body"`
	MapRingType     string     `json:"map_ring_type,omitempty"`
	MapReserveLevel string     `json:"map_reserve_level,omitempty"`
	MapRESSites     string     `json:"map_res_sites,omitempty"`
	MapHotspots     string     `json:"map_hotspots,omitempty"`
	SystemName      string     `json:"system_name"`
	DistanceLY      float64    `json:"distance_ly"`
	HasData         bool       `json:"has_data"`
	StationName     string     `json:"station_name,omitempty"`
	LargestPad      string     `json:"largest_pad,omitempty"`
	LastBGSUpdate   *time.Time `json:"last_bgs_update,omitempty"`
	LastMarketUp    *time.Time `json:"last_market_update,omitempty"`
	FactionStates   string     `json:"faction_states,omitempty"`
	PowerplayState  string     `json:"powerplay_state,omitempty"`
	Population      int64      `json:"population,omitempty"`
	RingSummary     string     `json:"ring_summary,omitempty"`
	RingHotspots    string     `json:"ring_hotspots,omitempty"`
	RingReserves    string     `json:"ring_reserves,omitempty"`
}

// SurveyExportResponse wraps the full survey export result.
type SurveyExportResponse struct {
	Rows        []SurveyRow `json:"rows"`
	GeneratedAt time.Time   `json:"generated_at"`
	TotalMaps   int         `json:"total_maps"`
	TotalRows   int         `json:"total_rows"`
}

// surveyMap holds per-map data for the spatial survey query.
type surveyMap struct {
	Name         string
	X, Y, Z      float64
	PowerState   string
	SearchRadius int
	Body         string
	RingType     string
	ReserveLevel string
	RESSites     string
	Hotspots     string
}

// SurveyExport generates a complete survey of ALL systems within range of each mining map,
// regardless of faction state. This reveals "dark" systems with no EDDN data coverage.
func (s *Store) SurveyExport(ctx context.Context, galaxy GalaxyQuerier, progress ProgressFunc) (*SurveyExportResponse, error) {
	if progress == nil {
		progress = func(int, int, string) {}
	}

	// Step 1: Get ALL mining maps from TimescaleDB
	progress(1, 5, "Fetching all mining maps from database")
	maps, err := s.getAllMiningMaps(ctx)
	if err != nil {
		return nil, fmt.Errorf("get mining maps: %w", err)
	}

	if len(maps) == 0 {
		return &SurveyExportResponse{
			Rows:        []SurveyRow{},
			GeneratedAt: time.Now(),
		}, nil
	}

	// Step 2: Get coordinates and live power states from galaxy.*
	progress(2, 5, fmt.Sprintf("Querying galaxy store for %d map system coordinates and power states", len(maps)))
	systemNames := make([]string, len(maps))
	for i, m := range maps {
		systemNames[i] = m.SystemName
	}

	coords, err := getSystemCoords(ctx, galaxy, systemNames)
	if err != nil {
		return nil, fmt.Errorf("get system coords: %w", err)
	}

	powerStates, err := getPowerStates(ctx, galaxy, systemNames)
	if err != nil {
		return nil, fmt.Errorf("get power states: %w", err)
	}

	// Step 3: Filter to Fortified/Stronghold maps
	progress(3, 5, "Filtering to Fortified and Stronghold systems")
	var activeMaps []surveyMap
	for _, m := range maps {
		coord, ok := coords[m.SystemName]
		if !ok {
			continue
		}
		liveState := powerStates[m.SystemName]
		var radius int
		switch liveState {
		case "Fortified":
			radius = 20
		case "Stronghold":
			radius = 30
		default:
			continue
		}
		activeMaps = append(activeMaps, surveyMap{
			Name:         m.SystemName,
			X:            coord.X,
			Y:            coord.Y,
			Z:            coord.Z,
			PowerState:   liveState,
			SearchRadius: radius,
			Body:         m.Body,
			RingType:     m.RingType,
			ReserveLevel: m.ReserveLevel,
			RESSites:     m.RESSites,
			Hotspots:     strings.Join(m.Hotspots, ", "),
		})
	}

	if len(activeMaps) == 0 {
		return &SurveyExportResponse{
			Rows:        []SurveyRow{},
			GeneratedAt: time.Now(),
		}, nil
	}

	// Step 4: For each map, query ALL systems within radius
	progress(4, 5, fmt.Sprintf("Spatial scanning %d maps for nearby metallic ring systems", len(activeMaps)))
	var allRows []SurveyRow
	for _, sm := range activeMaps {
		rows, err := surveySystemsInRadius(ctx, galaxy, sm)
		if err != nil {
			return nil, fmt.Errorf("survey %s: %w", sm.Name, err)
		}
		allRows = append(allRows, rows...)
	}

	// Sort: map name, then has_data (false first = dark systems on top), then distance
	progress(5, 5, fmt.Sprintf("Sorting %d results", len(allRows)))
	sort.Slice(allRows, func(i, j int) bool {
		if allRows[i].MapSystem != allRows[j].MapSystem {
			return allRows[i].MapSystem < allRows[j].MapSystem
		}
		if allRows[i].HasData != allRows[j].HasData {
			return !allRows[i].HasData // dark systems first
		}
		return allRows[i].DistanceLY < allRows[j].DistanceLY
	})

	return &SurveyExportResponse{
		Rows:        allRows,
		GeneratedAt: time.Now(),
		TotalMaps:   len(activeMaps),
		TotalRows:   len(allRows),
	}, nil
}

// surveySystemsInRadius queries galaxy.* for ALL systems within a bounding box,
// then filters to exact Euclidean distance. Returns one row per (system, station) pair.
// Ring data is fetched in a separate batch query for performance.
func surveySystemsInRadius(ctx context.Context, galaxy GalaxyQuerier, sm surveyMap) ([]SurveyRow, error) {
	r := float64(sm.SearchRadius)

	// Main query: systems, stations, factions (no body/ring joins for speed)
	query := `
WITH candidates AS (
	SELECT c.id64, COALESCE(sys.name, c.name) AS name, c.x::float8 AS x, c.y::float8 AS y, c.z::float8 AS z,
		COALESCE(sp.powerplay_state, '') AS powerplay_state,
		COALESCE(sys.population, 0) AS population,
		NULLIF(GREATEST(
			COALESCE(sys.last_event_time, '-infinity'::timestamptz),
			COALESCE(sys.last_faction_update, '-infinity'::timestamptz)
		), '-infinity'::timestamptz) AS last_bgs_update
	FROM galaxy.system_catalog c
	LEFT JOIN galaxy.system sys ON sys.id64 = c.id64
	LEFT JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
	WHERE c.x IS NOT NULL
	  AND cube(ARRAY[c.x::float8, c.y::float8, c.z::float8])
	      <@ cube(
	        ARRAY[$1::float8 - $4::float8, $2::float8 - $4::float8, $3::float8 - $4::float8],
	        ARRAY[$1::float8 + $4::float8, $2::float8 + $4::float8, $3::float8 + $4::float8]
	      )
)
SELECT
	c.name AS system_name,
	c.x, c.y, c.z,
	c.powerplay_state,
	c.population,
	st.name AS station_name,
	COALESCE(st.large_pads, 0) AS large_pads,
	COALESCE(st.medium_pads, 0) AS medium_pads,
	COALESCE(st.small_pads, 0) AS small_pads,
	c.last_bgs_update,
	m.last_event_time AS last_market_update,
	COALESCE(states.faction_states, '') AS faction_states
FROM candidates c
LEFT JOIN galaxy.station st
	ON st.system_id64 = c.id64
	AND COALESCE(st.station_type, '') NOT IN ('FleetCarrier', 'Drake-Class Carrier')
	AND COALESCE(st.kind, '') <> 'fleet_carrier'
LEFT JOIN galaxy.market m ON m.market_id = st.market_id
LEFT JOIN LATERAL (
	SELECT string_agg(DISTINCT active_state.state_name, ', ' ORDER BY active_state.state_name) AS faction_states
	FROM galaxy.system_faction sf
	CROSS JOIN LATERAL unnest(sf.active_states) AS active_state(state_name)
	WHERE sf.system_id64 = c.id64
	  AND active_state.state_name <> ''
) states ON true
	`

	result, err := galaxy.Query(ctx, query, sm.X, sm.Y, sm.Z, r)
	if err != nil {
		return nil, fmt.Errorf("survey query: %w", err)
	}
	defer result.Close()

	var rows []SurveyRow
	seen := make(map[string]bool)        // dedupe system-only rows
	systemNames := make(map[string]bool) // collect unique system names for ring query

	for result.Next() {
		var sysName, ppState, factionStates string
		var sx, sy, sz float64
		var pop int64
		var stationName *string
		var largePads, medPads, smallPads int
		var lastBGS, lastMarket *time.Time
		if err := result.Scan(
			&sysName, &sx, &sy, &sz,
			&ppState, &pop,
			&stationName,
			&largePads, &medPads, &smallPads,
			&lastBGS, &lastMarket,
			&factionStates,
		); err != nil {
			return nil, err
		}

		// Exact Euclidean distance filter (bounding box is a cube, not sphere)
		dist := math.Sqrt((sx-sm.X)*(sx-sm.X) + (sy-sm.Y)*(sy-sm.Y) + (sz-sm.Z)*(sz-sm.Z))
		if dist > r {
			continue
		}

		// Skip the map system itself
		if sysName == sm.Name {
			continue
		}

		if ppState == "" {
			ppState = "Unoccupied"
		}
		station := ""
		if stationName != nil {
			station = *stationName
		}
		hasData := lastBGS != nil

		row := SurveyRow{
			MapSystem:       sm.Name,
			MapPowerState:   sm.PowerState,
			SearchRadiusLY:  sm.SearchRadius,
			MapBody:         sm.Body,
			MapRingType:     sm.RingType,
			MapReserveLevel: sm.ReserveLevel,
			MapRESSites:     sm.RESSites,
			MapHotspots:     sm.Hotspots,
			SystemName:      sysName,
			DistanceLY:      math.Round(dist*10) / 10,
			HasData:         hasData,
			StationName:     station,
			LargestPad:      largestPad(largePads, medPads, smallPads),
			LastBGSUpdate:   lastBGS,
			LastMarketUp:    lastMarket,
			FactionStates:   factionStates,
			PowerplayState:  ppState,
			Population:      pop,
		}

		systemNames[sysName] = true

		if station == "" {
			key := sm.Name + "|" + sysName
			if !seen[key] {
				seen[key] = true
				rows = append(rows, row)
			}
		} else {
			rows = append(rows, row)
		}
	}
	if err := result.Err(); err != nil {
		return nil, err
	}

	// Batch query: find which systems have Metallic planetary rings
	if len(systemNames) > 0 {
		names := make([]string, 0, len(systemNames))
		for n := range systemNames {
			names = append(names, n)
		}
		ringData, err := batchGetRingData(ctx, galaxy, names)
		if err != nil {
			return nil, fmt.Errorf("ring query: %w", err)
		}

		// Filter: only keep systems that have at least one Metallic planetary ring
		var filtered []SurveyRow
		for _, row := range rows {
			if rd, ok := ringData[row.SystemName]; ok && rd.hasMetallic {
				row.RingSummary = rd.summary
				row.RingHotspots = rd.hotspots
				row.RingReserves = rd.reserves
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}

	return rows, nil
}

// systemRingData holds aggregated ring information for a system.
type systemRingData struct {
	summary     string // "Body 1 (Metallic, Rocky); Body 2 (Icy)"
	hotspots    string // "Body 1 A Ring: Platinum x3, Painite x2"
	reserves    string // "Body 1 A Ring: Pristine; Body 2 A Ring: Major"
	hasMetallic bool
}

// batchGetRingData queries planetary ring data for a batch of system names.
// Returns ring summaries, hotspot info (Platinum/LTD only), and reserve levels.
func batchGetRingData(ctx context.Context, galaxy GalaxyQuerier, systemNames []string) (map[string]*systemRingData, error) {
	query := `
SELECT
	COALESCE(sys.name, c.name) AS system_name,
	b.name AS body_name,
	r.name AS ring_name,
	r.ring_class,
	COALESCE(r.reserve_level, '') AS reserve_level,
	COALESCE(array_agg(comm.name || ':' || rh.count ORDER BY comm.name) FILTER (WHERE comm.name IS NOT NULL), '{}') AS hotspots
FROM galaxy.system_catalog c
LEFT JOIN galaxy.system sys ON sys.id64 = c.id64
JOIN galaxy.body b ON b.system_id64 = c.id64
JOIN galaxy.ring r ON r.system_id64 = b.system_id64 AND r.body_id = b.body_id
LEFT JOIN galaxy.ring_hotspot rh ON rh.system_id64 = r.system_id64 AND rh.ring_name = r.name
LEFT JOIN galaxy.commodity comm ON comm.commodity_id = rh.commodity_id
WHERE c.name = ANY($1)
  AND COALESCE(b.type, '') <> 'Star'
  AND r.name NOT ILIKE '%Belt%'
GROUP BY COALESCE(sys.name, c.name), b.name, r.name, r.ring_class, r.reserve_level
	`

	result, err := galaxy.Query(ctx, query, systemNames)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	type ringInfo struct {
		bodyName     string
		ringName     string
		ringClass    string
		hotspots     []string // raw hotspot strings like "Platinum:3"
		reserveLevel string
	}

	systemRings := make(map[string][]ringInfo)

	for result.Next() {
		var sysName, bodyName, ringName, ringClass, reserveLevel string
		var hotspots []string
		if err := result.Scan(&sysName, &bodyName, &ringName, &ringClass, &reserveLevel, &hotspots); err != nil {
			return nil, err
		}

		if sysName == "" || bodyName == "" || ringClass == "" {
			continue
		}

		systemRings[sysName] = append(systemRings[sysName], ringInfo{
			bodyName:     bodyName,
			ringName:     ringName,
			ringClass:    ringClass,
			hotspots:     hotspots,
			reserveLevel: reserveLevel,
		})
	}
	if err := result.Err(); err != nil {
		return nil, err
	}

	// Build structured results
	dataMap := make(map[string]*systemRingData, len(systemRings))
	for sysName, rings := range systemRings {
		rd := &systemRingData{}

		// Ring summary: group ring classes by body
		bodyRings := make(map[string][]string)
		var bodyOrder []string
		for _, ri := range rings {
			if _, seen := bodyRings[ri.bodyName]; !seen {
				bodyOrder = append(bodyOrder, ri.bodyName)
			}
			found := false
			for _, existing := range bodyRings[ri.bodyName] {
				if existing == ri.ringClass {
					found = true
					break
				}
			}
			if !found {
				bodyRings[ri.bodyName] = append(bodyRings[ri.bodyName], ri.ringClass)
			}
			if ri.ringClass == "Metallic" {
				rd.hasMetallic = true
			}
		}
		sort.Strings(bodyOrder)
		var summaryParts []string
		for _, body := range bodyOrder {
			rc := bodyRings[body]
			sort.Strings(rc)
			summaryParts = append(summaryParts, body+" ("+strings.Join(rc, ", ")+")")
		}
		rd.summary = strings.Join(summaryParts, "; ")

		// Hotspots: only Platinum and LTD, grouped by ring name
		var hotspotParts []string
		var reserveParts []string
		seen := make(map[string]bool)
		for _, ri := range rings {
			// Filter hotspots to just Platinum and LTD
			var relevant []string
			for _, h := range ri.hotspots {
				// Format is "Type:Count"
				if strings.HasPrefix(h, "platinum:") || strings.HasPrefix(h, "lowtemperaturediamond:") {
					// Make LTD readable
					display := strings.Replace(h, "lowtemperaturediamond:", "LTD x", 1)
					display = strings.Replace(display, "platinum:", "Platinum x", 1)
					relevant = append(relevant, display)
				}
			}
			if len(relevant) > 0 {
				hotspotParts = append(hotspotParts, ri.ringName+": "+strings.Join(relevant, ", "))
			}

			// Reserve levels per ring (dedupe)
			if ri.reserveLevel != "" && !seen[ri.ringName] {
				seen[ri.ringName] = true
				reserveParts = append(reserveParts, ri.ringName+": "+ri.reserveLevel)
			}
		}
		rd.hotspots = strings.Join(hotspotParts, "; ")
		rd.reserves = strings.Join(reserveParts, "; ")

		dataMap[sysName] = rd
	}

	return dataMap, nil
}

// getAllMiningMaps retrieves ALL mining maps from TimescaleDB (no commodity filter).
func (s *Store) getAllMiningMaps(ctx context.Context) ([]MiningMap, error) {
	query := `
		SELECT
			id, system_name, body,
			COALESCE(ring_type, ''), COALESCE(reserve_level, ''),
			COALESCE(res_sites, ''), COALESCE(hotspots, '{}'),
			COALESCE(map_1, ''), COALESCE(map_1_title, ''), COALESCE(map_1_commodity, '{}'),
			COALESCE(map_2, ''), COALESCE(map_2_title, ''), COALESCE(map_2_commodity, '{}'),
			COALESCE(map_3, ''), COALESCE(map_3_title, ''), COALESCE(map_3_commodity, '{}'),
			COALESCE(search_url, ''),
			COALESCE(expansion_faction, ''), COALESCE(notes, ''),
			created_at, updated_at, COALESCE(created_by, '')
		FROM kaine.mining_maps
		ORDER BY system_name
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query all mining maps: %w", err)
	}
	defer rows.Close()

	var maps []MiningMap
	for rows.Next() {
		m, err := scanMiningMap(rows)
		if err != nil {
			return nil, err
		}
		maps = append(maps, *m)
	}

	return maps, rows.Err()
}
