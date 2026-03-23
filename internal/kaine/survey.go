package kaine

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// SurveyRow represents one row in the survey export: a (map, system, station) tuple.
type SurveyRow struct {
	MapSystem      string     `json:"map_system"`
	MapPowerState  string     `json:"map_power_state"`
	SearchRadiusLY int        `json:"search_radius_ly"`
	MapBody        string     `json:"map_body"`
	MapRingType    string     `json:"map_ring_type,omitempty"`
	MapReserveLevel string   `json:"map_reserve_level,omitempty"`
	MapRESSites    string     `json:"map_res_sites,omitempty"`
	MapHotspots    string     `json:"map_hotspots,omitempty"`
	SystemName     string     `json:"system_name"`
	DistanceLY     float64    `json:"distance_ly"`
	HasData        bool       `json:"has_data"`
	StationName    string     `json:"station_name,omitempty"`
	LargestPad     string     `json:"largest_pad,omitempty"`
	LastBGSUpdate  *time.Time `json:"last_bgs_update,omitempty"`
	LastMarketUp   *time.Time `json:"last_market_update,omitempty"`
	FactionStates  string     `json:"faction_states,omitempty"`
	PowerplayState string     `json:"powerplay_state,omitempty"`
	Population     int64      `json:"population,omitempty"`
	RingSummary    string     `json:"ring_summary,omitempty"`
	RingHotspots   string     `json:"ring_hotspots,omitempty"`
	RingReserves   string     `json:"ring_reserves,omitempty"`
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
func (s *Store) SurveyExport(ctx context.Context, memgraph MemgraphClient, progress ProgressFunc) (*SurveyExportResponse, error) {
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

	// Step 2: Get coordinates and live power states from Memgraph
	progress(2, 5, fmt.Sprintf("Querying Memgraph for %d map system coordinates and power states", len(maps)))
	systemNames := make([]string, len(maps))
	for i, m := range maps {
		systemNames[i] = m.SystemName
	}

	coords, err := getSystemCoords(ctx, memgraph, systemNames)
	if err != nil {
		return nil, fmt.Errorf("get system coords: %w", err)
	}

	powerStates, err := getPowerStates(ctx, memgraph, systemNames)
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
		rows, err := surveySystemsInRadius(ctx, memgraph, sm)
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

// surveySystemsInRadius queries Memgraph for ALL systems within a bounding box,
// then filters to exact Euclidean distance. Returns one row per (system, station) pair.
// Ring data is fetched in a separate batch query for performance.
func surveySystemsInRadius(ctx context.Context, client MemgraphClient, sm surveyMap) ([]SurveyRow, error) {
	session := client.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	r := float64(sm.SearchRadius)

	// Main query: systems, stations, factions (no body/ring joins for speed)
	query := `
		WITH $cx AS cx, $cy AS cy, $cz AS cz, $radius AS radius,
		     point({x: $cx - $radius, y: $cy - $radius, z: $cz - $radius}) AS lowerLeft,
		     point({x: $cx + $radius, y: $cy + $radius, z: $cz + $radius}) AS upperRight

		MATCH (s:System)
		WHERE point.withinbbox(s.location, lowerLeft, upperRight)

		// Get all stations (optional)
		OPTIONAL MATCH (s)-[:HAS_STATION]->(st:Station)
		WHERE st.type <> 'FleetCarrier' AND st.type <> 'Drake-Class Carrier'

		// Get market freshness
		OPTIONAL MATCH (st)-[:HAS_MARKET]->(m:Market)

		// Get faction presence for active states
		OPTIONAL MATCH (f:Faction)-[p:PRESENT_IN]->(s)

		WITH s, st, m,
		     collect(DISTINCT p.active_states) AS all_state_arrays

		RETURN
			s.name AS system_name,
			s.location.x AS x, s.location.y AS y, s.location.z AS z,
			s.powerplay_state AS powerplay_state,
			s.population AS population,
			st.name AS station_name,
			COALESCE(st.landing_pads_large, st.large_pads, 0) AS large_pads,
			COALESCE(st.landing_pads_medium, st.medium_pads, 0) AS medium_pads,
			COALESCE(st.landing_pads_small, st.small_pads, 0) AS small_pads,
			s.last_eddn_update AS last_bgs_update,
			m.last_event_time AS last_market_update,
			all_state_arrays
	`

	result, err := session.Run(ctx, query, map[string]any{
		"cx":     sm.X,
		"cy":     sm.Y,
		"cz":     sm.Z,
		"radius": r,
	})
	if err != nil {
		return nil, fmt.Errorf("survey query: %w", err)
	}

	var rows []SurveyRow
	seen := make(map[string]bool)        // dedupe system-only rows
	systemNames := make(map[string]bool)  // collect unique system names for ring query

	for result.Next(ctx) {
		rec := result.Record()

		sysName := toString(rec, "system_name")
		sx := toFloat64(getRecordValue(rec, "x"))
		sy := toFloat64(getRecordValue(rec, "y"))
		sz := toFloat64(getRecordValue(rec, "z"))

		// Exact Euclidean distance filter (bounding box is a cube, not sphere)
		dist := math.Sqrt((sx-sm.X)*(sx-sm.X) + (sy-sm.Y)*(sy-sm.Y) + (sz-sm.Z)*(sz-sm.Z))
		if dist > r {
			continue
		}

		// Skip the map system itself
		if sysName == sm.Name {
			continue
		}

		stationName := toString(rec, "station_name")
		largePads := int(toInt64(getRecordValue(rec, "large_pads")))
		medPads := int(toInt64(getRecordValue(rec, "medium_pads")))
		smallPads := int(toInt64(getRecordValue(rec, "small_pads")))

		ppState := toString(rec, "powerplay_state")
		if ppState == "" {
			ppState = "Unoccupied"
		}
		pop := toInt64(getRecordValue(rec, "population"))

		var lastBGS *time.Time
		if t := toTime(getRecordValue(rec, "last_bgs_update")); !t.IsZero() {
			lastBGS = &t
		}
		var lastMarket *time.Time
		if t := toTime(getRecordValue(rec, "last_market_update")); !t.IsZero() {
			lastMarket = &t
		}

		factionStates := flattenFactionStates(getRecordValue(rec, "all_state_arrays"))
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
			StationName:     stationName,
			LargestPad:      largestPad(largePads, medPads, smallPads),
			LastBGSUpdate:   lastBGS,
			LastMarketUp:    lastMarket,
			FactionStates:   factionStates,
			PowerplayState:  ppState,
			Population:      pop,
		}

		systemNames[sysName] = true

		if stationName == "" {
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
		ringData, err := batchGetRingData(ctx, session, names)
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
func batchGetRingData(ctx context.Context, session neo4j.SessionWithContext, systemNames []string) (map[string]*systemRingData, error) {
	query := `
		UNWIND $names AS sysName
		MATCH (s:System {name: sysName})-[:HAS_BODY]->(b:Body)
		WHERE b.type <> 'Star'
		MATCH (b)-[:HAS_RING]->(ring:Ring)
		WHERE NOT ring.name CONTAINS 'Belt'
		RETURN s.name AS system_name, b.name AS body_name,
		       ring.name AS ring_name, ring.ring_class AS ring_class,
		       ring.hotspots AS hotspots, ring.reserve_level AS reserve_level
	`

	result, err := session.Run(ctx, query, map[string]any{"names": systemNames})
	if err != nil {
		return nil, err
	}

	type ringInfo struct {
		bodyName     string
		ringName     string
		ringClass    string
		hotspots     []string // raw hotspot strings like "Platinum:3"
		reserveLevel string
	}

	systemRings := make(map[string][]ringInfo)

	for result.Next(ctx) {
		rec := result.Record()
		sysName := toString(rec, "system_name")
		bodyName := toString(rec, "body_name")
		ringName := toString(rec, "ring_name")
		ringClass := toString(rec, "ring_class")
		reserveLevel := toString(rec, "reserve_level")

		if sysName == "" || bodyName == "" || ringClass == "" {
			continue
		}

		// Parse hotspots array
		var hotspots []string
		if v := getRecordValue(rec, "hotspots"); v != nil {
			if arr, ok := v.([]any); ok {
				for _, item := range arr {
					if s, ok := item.(string); ok {
						hotspots = append(hotspots, s)
					}
				}
			}
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
				if strings.HasPrefix(h, "Platinum:") || strings.HasPrefix(h, "LowTemperatureDiamond:") {
					// Make LTD readable
					display := strings.Replace(h, "LowTemperatureDiamond:", "LTD x", 1)
					display = strings.Replace(display, "Platinum:", "Platinum x", 1)
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

// flattenFactionStates extracts unique faction states from nested arrays.
func flattenFactionStates(v any) string {
	if v == nil {
		return ""
	}

	outerArr, ok := v.([]any)
	if !ok {
		return ""
	}

	unique := make(map[string]bool)
	for _, inner := range outerArr {
		if innerArr, ok := inner.([]any); ok {
			for _, s := range innerArr {
				if str, ok := s.(string); ok && str != "" {
					unique[str] = true
				}
			}
		}
	}

	if len(unique) == 0 {
		return ""
	}

	states := make([]string, 0, len(unique))
	for s := range unique {
		states = append(states, s)
	}
	sort.Strings(states)

	result := states[0]
	for _, s := range states[1:] {
		result += ", " + s
	}
	return result
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
