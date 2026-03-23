package kaine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ============================================================================
// PLASMIUM BUYERS - Daily Process 1: Finding stations to sell Platinum/Osmium
// See: eddn-listener/docs/kaine-directors-processes/orok-pseudocode.md
// ============================================================================

// PlasmiumBuyer represents a station that can buy Platinum/Osmium near a mining map.
type PlasmiumBuyer struct {
	// Station info (from Memgraph)
	SystemName   string   `json:"system_name"`
	StationName  string   `json:"station_name"`
	Faction      string   `json:"faction"`
	FactionState string   `json:"faction_state"`
	Economies    []string `json:"economies,omitempty"`
	DistanceLY   float64  `json:"distance_ly"`

	// How this station matched the Boom filter
	// "controlling_faction" = the station's controlling faction is in Boom (verified)
	// "system_boom" = another faction in the system is in Boom (station may also benefit, needs in-game check)
	BoomMatch string `json:"boom_match"`

	// Powerplay info (from Memgraph)
	PowerplayState  string   `json:"powerplay_state"`             // Unoccupied, Expansion, Contested (null shown as Unoccupied)
	DistanceToKaine float64  `json:"distance_to_kaine,omitempty"` // Distance to nearest Kaine Fortified/Stronghold
	KaineProgress   *float64 `json:"kaine_progress,omitempty"`    // Nakato Kaine's acquisition progress (0-1) for this system

	// Landing pads (from Memgraph)
	LargePads  int    `json:"large_pads,omitempty"`
	MediumPads int    `json:"medium_pads,omitempty"`
	SmallPads  int    `json:"small_pads,omitempty"`
	LargestPad string `json:"largest_pad"` // "L", "M", "S", or ""

	// Coordinates (from Memgraph)
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
	Z float64 `json:"z,omitempty"`

	// Market info (from Memgraph)
	PlatinumDemand int64 `json:"platinum_demand,omitempty"`
	PlatinumPrice  int64 `json:"platinum_price,omitempty"`
	OsmiumDemand   int64 `json:"osmium_demand,omitempty"`
	OsmiumPrice    int64 `json:"osmium_price,omitempty"`

	// Calculated
	Score       float64 `json:"score"`
	ScoreReason string  `json:"score_reason"`
	RankScore   float64 `json:"rank_score"` // Composite ranking: freshness + pad + demand + price

	// Freshness indicators
	BGSUpdatedAt    *time.Time `json:"bgs_updated_at,omitempty"`
	MarketUpdatedAt *time.Time `json:"market_updated_at,omitempty"`
}

// PlasmiumMapResult represents a mining map with its nearby buyers.
type PlasmiumMapResult struct {
	// Map info (from TimescaleDB)
	SystemName   string   `json:"system_name"`
	Body         string   `json:"body"`
	RingType     string   `json:"ring_type"`
	ReserveLevel string   `json:"reserve_level"`
	PowerState   string   `json:"power_state"`
	RESNotes     string   `json:"res_notes,omitempty"`
	Hotspots     []string `json:"hotspots,omitempty"`
	Map1          string   `json:"map_1,omitempty"`
	Map1Title     string   `json:"map_1_title,omitempty"`
	Map1Commodity []string `json:"map_1_commodity,omitempty"` // Commodities this map produces
	Map2          string   `json:"map_2,omitempty"`
	Map2Title     string   `json:"map_2_title,omitempty"`
	Map2Commodity []string `json:"map_2_commodity,omitempty"` // Commodities this map produces
	Map3          string   `json:"map_3,omitempty"`
	Map3Title     string   `json:"map_3_title,omitempty"`
	Map3Commodity []string `json:"map_3_commodity,omitempty"` // Commodities this map produces

	// Map coordinates (from Memgraph)
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`

	// Live power state (from Memgraph) - the map system IS the source
	LivePowerState string `json:"live_power_state"` // Fortified or Stronghold (from Memgraph, live)
	SearchRadiusLY int    `json:"search_radius_ly"` // 20 for Fortified, 30 for Stronghold

	// Nearby buyers sorted by score
	Buyers []PlasmiumBuyer `json:"buyers"`
}

// PlasmiumBuyersResponse is the full API response.
type PlasmiumBuyersResponse struct {
	Maps        []PlasmiumMapResult `json:"maps"`
	GeneratedAt time.Time           `json:"generated_at"`
	TotalMaps   int                 `json:"total_maps"`
	TotalBuyers int                 `json:"total_buyers"`
}

// MemgraphClient is the interface needed for Memgraph queries.
// This allows for testing with mocks.
type MemgraphClient interface {
	NewSession(ctx context.Context, config neo4j.SessionConfig) neo4j.SessionWithContext
}

// mapSearchParams holds the search parameters for a map.
type mapSearchParams struct {
	Map            *MiningMap
	X, Y, Z        float64
	LivePowerState string // Fortified or Stronghold (from Memgraph)
	SearchRadius   int    // 20 for Fortified, 30 for Stronghold
}

// anchorCoverage represents an anchor system that covers a map.
type anchorCoverage struct {
	Name        string
	X, Y, Z     float64
	Radius      int    // 20 for Fortified, 30 for Stronghold
	PowerState  string // "Fortified" or "Stronghold"
}

// FindPlasmiumBuyers finds stations in Boom state that buy Platinum/Osmium near Kaine mining maps.
// This implements Orok's Daily Process 1 as documented in orok-pseudocode.md.
//
// The map system IS the source (same model as LTD). No anchor intermediary.
// Maps must be Fortified (20 LY radius) or Stronghold (30 LY radius).
// Buyer stations must be in acquisition target systems within that radius.
func (s *Store) FindPlasmiumBuyers(ctx context.Context, memgraph MemgraphClient, progress ProgressFunc) (*PlasmiumBuyersResponse, error) {
	if progress == nil {
		progress = func(int, int, string) {}
	}

	// Step 1: Get Plasmium maps from TimescaleDB (filtered by commodity: Platinum/Osmium)
	progress(1, 5, "Fetching Plasmium mining maps from database")
	maps, err := s.getPlasmiumMaps(ctx)
	if err != nil {
		return nil, fmt.Errorf("get plasmium maps: %w", err)
	}

	if len(maps) == 0 {
		return &PlasmiumBuyersResponse{
			Maps:        []PlasmiumMapResult{},
			GeneratedAt: time.Now(),
			TotalMaps:   0,
			TotalBuyers: 0,
		}, nil
	}

	// Step 2: Get live power state and coordinates for each map system from Memgraph
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

	// Get Kaine systems for DistanceToKaine display field on buyers
	kaineSystems, err := getKaineFortifiedSystems(ctx, memgraph)
	if err != nil {
		return nil, fmt.Errorf("get kaine systems: %w", err)
	}

	// Step 3: Filter maps - only keep those where the map system itself is Fortified or Stronghold
	progress(3, 5, "Filtering to Fortified and Stronghold systems")
	var searchParams []mapSearchParams
	for i := range maps {
		m := &maps[i]
		coord, ok := coords[m.SystemName]
		if !ok {
			continue
		}

		livePowerState := powerStates[m.SystemName]
		var searchRadius int
		switch livePowerState {
		case "Fortified":
			searchRadius = 20
		case "Stronghold":
			searchRadius = 30
		default:
			continue // Skip maps not in Fortified/Stronghold
		}

		searchParams = append(searchParams, mapSearchParams{
			Map:            m,
			X:              coord.X,
			Y:              coord.Y,
			Z:              coord.Z,
			LivePowerState: livePowerState,
			SearchRadius:   searchRadius,
		})
	}

	if len(searchParams) == 0 {
		return &PlasmiumBuyersResponse{
			Maps:        []PlasmiumMapResult{},
			GeneratedAt: time.Now(),
			TotalMaps:   0,
			TotalBuyers: 0,
		}, nil
	}

	// Step 4: Get ALL Boom stations globally, then filter by distance to each map
	progress(4, 5, fmt.Sprintf("Scanning Boom stations within range of %d qualifying maps", len(searchParams)))
	allBoomStations, err := getAllBoomStations(ctx, memgraph)
	if err != nil {
		return nil, fmt.Errorf("get boom stations: %w", err)
	}

	// Step 5: For each map, find buyers within its search radius
	progress(5, 5, "Scoring and ranking buyers by demand, economy and distance")
	var results []PlasmiumMapResult
	var totalBuyers int

	for _, params := range searchParams {
		m := params.Map

		// Find stations within this map's search radius
		var buyers []PlasmiumBuyer
		for _, station := range allBoomStations {
			dist := distance3D(params.X, params.Y, params.Z, station.X, station.Y, station.Z)
			if dist <= float64(params.SearchRadius) {
				buyer := station
				buyer.DistanceLY = dist
				buyers = append(buyers, buyer)
			}
		}

		// Score buyers and calculate distance to nearest Kaine system
		for i := range buyers {
			buyers[i].Score, buyers[i].ScoreReason = calculatePlasmiumScore(
				buyers[i].PlatinumDemand,
				buyers[i].OsmiumDemand,
				buyers[i].Economies,
			)
			buyers[i].DistanceToKaine = calculateDistanceToKaine(
				buyers[i].X, buyers[i].Y, buyers[i].Z,
				kaineSystems,
			)
			buyers[i].RankScore = calculateRankScore(&buyers[i])
		}

		// Filter out zero-score buyers and those with prices below 100k
		var validBuyers []PlasmiumBuyer
		for _, b := range buyers {
			if b.Score <= 0 {
				continue
			}
			// Exclude stations where we have price data and it's below 100k
			bestPrice := max(b.PlatinumPrice, b.OsmiumPrice)
			if bestPrice > 0 && bestPrice < 100_000 {
				continue
			}
			validBuyers = append(validBuyers, b)
		}

		// Sort by RankScore, then distance as tiebreaker
		sort.Slice(validBuyers, func(i, j int) bool {
			if validBuyers[i].RankScore != validBuyers[j].RankScore {
				return validBuyers[i].RankScore > validBuyers[j].RankScore
			}
			return validBuyers[i].DistanceLY < validBuyers[j].DistanceLY
		})

		// Limit to top 10 per map
		if len(validBuyers) > 10 {
			validBuyers = validBuyers[:10]
		}

		totalBuyers += len(validBuyers)

		results = append(results, PlasmiumMapResult{
			SystemName:     m.SystemName,
			Body:           m.Body,
			RingType:       m.RingType,
			ReserveLevel:   m.ReserveLevel,
			PowerState:     m.PowerState,
			RESNotes:       m.RESSites,
			Hotspots:       m.Hotspots,
			Map1:           m.Map1,
			Map1Title:      m.Map1Title,
			Map1Commodity:  m.Map1Commodity,
			Map2:           m.Map2,
			Map2Title:      m.Map2Title,
			Map2Commodity:  m.Map2Commodity,
			Map3:           m.Map3,
			Map3Title:      m.Map3Title,
			Map3Commodity:  m.Map3Commodity,
			X:              params.X,
			Y:              params.Y,
			Z:              params.Z,
			LivePowerState: params.LivePowerState,
			SearchRadiusLY: params.SearchRadius,
			Buyers:         validBuyers,
		})
	}

	// Sort results by top buyer's rank score (best opportunities first)
	sort.Slice(results, func(i, j int) bool {
		var rankI, rankJ float64
		if len(results[i].Buyers) > 0 {
			rankI = results[i].Buyers[0].RankScore
		}
		if len(results[j].Buyers) > 0 {
			rankJ = results[j].Buyers[0].RankScore
		}

		if rankI != rankJ {
			return rankI > rankJ
		}

		if len(results[i].Buyers) != len(results[j].Buyers) {
			return len(results[i].Buyers) > len(results[j].Buyers)
		}

		// Tiebreaker: Stronghold before Fortified
		if results[i].LivePowerState != results[j].LivePowerState {
			return results[i].LivePowerState == "Stronghold"
		}

		return results[i].SystemName < results[j].SystemName
	})

	return &PlasmiumBuyersResponse{
		Maps:        results,
		GeneratedAt: time.Now(),
		TotalMaps:   len(results),
		TotalBuyers: totalBuyers,
	}, nil
}

// getPlasmiumMaps retrieves mining maps that produce Platinum or Osmium.
// Uses commodity data extracted from map documents (map_1_commodity/map_2_commodity arrays).
// Note: power_state filtering (Fortified/Stronghold) is done in FindPlasmiumBuyers after Memgraph lookup.
func (s *Store) getPlasmiumMaps(ctx context.Context) ([]MiningMap, error) {
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
		WHERE
		  -- Filter by commodity: map must produce Platinum or Osmium
		  -- Note: stored as lowercase (e.g., 'platinum', 'osmium')
		  (
		    'platinum' = ANY(map_1_commodity) OR 'osmium' = ANY(map_1_commodity)
		    OR 'platinum' = ANY(map_2_commodity) OR 'osmium' = ANY(map_2_commodity)
		    OR 'platinum' = ANY(map_3_commodity) OR 'osmium' = ANY(map_3_commodity)
		  )
		ORDER BY system_name
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query plasmium maps: %w", err)
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

// systemCoord holds 3D coordinates for a system.
type systemCoord struct {
	X, Y, Z float64
}

// getSystemCoords fetches coordinates for systems from Memgraph.
func getSystemCoords(ctx context.Context, client MemgraphClient, systemNames []string) (map[string]systemCoord, error) {
	session := client.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		UNWIND $names AS name
		MATCH (s:System {name: name})
		WHERE s.location IS NOT NULL
		RETURN s.name AS name, s.location.x AS x, s.location.y AS y, s.location.z AS z
	`

	result, err := session.Run(ctx, query, map[string]any{"names": systemNames})
	if err != nil {
		return nil, fmt.Errorf("query system coords: %w", err)
	}

	coords := make(map[string]systemCoord)
	for result.Next(ctx) {
		record := result.Record()
		name, _ := record.Get("name")
		x, _ := record.Get("x")
		y, _ := record.Get("y")
		z, _ := record.Get("z")

		if nameStr, ok := name.(string); ok {
			coords[nameStr] = systemCoord{
				X: toFloat64(x),
				Y: toFloat64(y),
				Z: toFloat64(z),
			}
		}
	}

	return coords, result.Err()
}

// getPowerStates fetches powerplay states for systems from Memgraph.
func getPowerStates(ctx context.Context, client MemgraphClient, systemNames []string) (map[string]string, error) {
	if len(systemNames) == 0 {
		return map[string]string{}, nil
	}

	session := client.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		UNWIND $names AS name
		MATCH (s:System {name: name})
		RETURN s.name AS name, s.powerplay_state AS powerplay_state
	`

	result, err := session.Run(ctx, query, map[string]any{"names": systemNames})
	if err != nil {
		return nil, fmt.Errorf("query power states: %w", err)
	}

	states := make(map[string]string)
	for result.Next(ctx) {
		record := result.Record()
		name, _ := record.Get("name")
		state, _ := record.Get("powerplay_state")

		if nameStr, ok := name.(string); ok {
			stateStr := ""
			if state != nil {
				stateStr, _ = state.(string)
			}
			states[nameStr] = stateStr
		}
	}

	return states, result.Err()
}

// getAllBoomStations fetches ALL stations in ACQUISITION TARGET systems where Boom is present.
//
// Returns two types of matches (indicated by BoomMatch field):
// - "controlling_faction": The station's controlling faction is in Boom (verified match)
// - "system_boom": Another faction in the system is in Boom but not the controlling faction
//   (the station may still benefit - worth checking in-game, especially if no market data yet)
//
// IMPORTANT: Only returns stations in systems that are acquisition targets:
// - Acquisition targets: powerplay_state is NULL, "Unoccupied", "Expansion", or "Contested"
// - Excluded: "Exploited", "Fortified", "Stronghold", "HomeSystem" (already controlled by a power)
//
// Market data (Platinum/Osmium demand) is fetched where available but is NOT a filter.
// We may not have market data if nobody has docked at the station recently.
func getAllBoomStations(ctx context.Context, client MemgraphClient) ([]PlasmiumBuyer, error) {
	session := client.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Find all stations in acquisition target systems where ANY faction has Boom.
	// We track whether the station's own controlling faction is the one in Boom.
	query := `
		// Find systems that have at least one faction in Boom and are acquisition targets
		MATCH (f:Faction)-[p:PRESENT_IN]->(s:System)
		WHERE 'Boom' IN p.active_states
		  AND s.location IS NOT NULL
		  AND (s.powerplay_state IS NULL
		       OR s.powerplay_state = ''
		       OR s.powerplay_state IN ['Unoccupied', 'Expansion', 'Contested'])

		// Find ALL stations in those systems (not just ones controlled by the Boom faction)
		MATCH (s)-[:HAS_STATION]->(st:Station)
		WHERE st.controlling_faction IS NOT NULL

		// Check if the station's controlling faction is the Boom faction
		WITH s, st, f, p,
		     CASE WHEN st.controlling_faction = f.name THEN true ELSE false END AS is_controlling

		// Get market data for Platinum and Osmium (optional - not a filter)
		OPTIONAL MATCH (st)-[:HAS_MARKET]->(m:Market)
		OPTIONAL MATCH (m)-[t_plat:TRADES]->(c_plat:Commodity {name: 'platinum'})
		OPTIONAL MATCH (m)-[t_osm:TRADES]->(c_osm:Commodity {name: 'osmium'})

		RETURN DISTINCT
			s.name AS system_name,
			s.location.x AS x, s.location.y AS y, s.location.z AS z,
			s.powerplay_state AS powerplay_state,
			s.powerplay_conflict_progress AS conflict_progress,
			st.name AS station_name,
			st.controlling_faction AS faction,
			is_controlling AS is_controlling_faction_boom,
			st.economies AS economies,
			COALESCE(st.landing_pads_large, st.large_pads, 0) AS large_pads,
			COALESCE(st.landing_pads_medium, st.medium_pads, 0) AS medium_pads,
			COALESCE(st.landing_pads_small, st.small_pads, 0) AS small_pads,
			t_plat.demand AS platinum_demand,
			t_plat.sell_price AS platinum_price,
			t_osm.demand AS osmium_demand,
			t_osm.sell_price AS osmium_price,
			m.last_event_time AS market_updated_at
	`

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("query all boom stations: %w", err)
	}

	var stations []PlasmiumBuyer
	seen := make(map[string]bool) // Dedupe: same station may appear via multiple Boom factions
	for result.Next(ctx) {
		record := result.Record()

		systemName := toString(record, "system_name")
		stationName := toString(record, "station_name")
		stationKey := systemName + "|" + stationName

		// Determine boom match type
		isControlling := false
		if val, ok := getRecordValue(record, "is_controlling_faction_boom").(bool); ok {
			isControlling = val
		}

		// If we've already seen this station, only upgrade from system_boom to controlling_faction
		if seen[stationKey] {
			// We already have this station - skip unless this is a controlling faction match
			// (we prefer controlling_faction over system_boom)
			if !isControlling {
				continue
			}
			// Upgrade: find and replace the existing entry
			for i := range stations {
				if stations[i].SystemName == systemName && stations[i].StationName == stationName {
					stations[i].BoomMatch = "controlling_faction"
					stations[i].FactionState = "Station Boom"
					break
				}
			}
			continue
		}
		seen[stationKey] = true

		largePads := int(toInt64(getRecordValue(record, "large_pads")))
		mediumPads := int(toInt64(getRecordValue(record, "medium_pads")))
		smallPads := int(toInt64(getRecordValue(record, "small_pads")))

		powerplayState := toString(record, "powerplay_state")
		if powerplayState == "" {
			powerplayState = "Unoccupied"
		}

		boomMatch := "system_boom"
		factionState := "System Boom"
		if isControlling {
			boomMatch = "controlling_faction"
			factionState = "Station Boom"
		}

		buyer := PlasmiumBuyer{
			SystemName:     systemName,
			StationName:    stationName,
			Faction:        toString(record, "faction"),
			FactionState:   factionState,
			BoomMatch:      boomMatch,
			PowerplayState: powerplayState,
			LargePads:      largePads,
			MediumPads:     mediumPads,
			SmallPads:      smallPads,
			LargestPad:     largestPad(largePads, mediumPads, smallPads),
			X:              toFloat64(getRecordValue(record, "x")),
			Y:              toFloat64(getRecordValue(record, "y")),
			Z:              toFloat64(getRecordValue(record, "z")),
			PlatinumDemand: toInt64(getRecordValue(record, "platinum_demand")),
			PlatinumPrice:  toInt64(getRecordValue(record, "platinum_price")),
			OsmiumDemand:   toInt64(getRecordValue(record, "osmium_demand")),
			OsmiumPrice:    toInt64(getRecordValue(record, "osmium_price")),
		}

		// Parse economies array
		if econ, ok := getRecordValue(record, "economies").([]any); ok {
			for _, e := range econ {
				if es, ok := e.(string); ok {
					buyer.Economies = append(buyer.Economies, es)
				}
			}
		}

		// Parse Kaine's acquisition progress from conflict_progress
		// Stored as JSON string: '[{"Power":"Nakato Kaine","ConflictProgress":0.5}, ...]'
		// or possibly as []any if driver parses it
		buyer.KaineProgress = parseKaineProgress(getRecordValue(record, "conflict_progress"))

		// Parse timestamps
		if t := toTime(getRecordValue(record, "market_updated_at")); !t.IsZero() {
			buyer.MarketUpdatedAt = &t
		}

		stations = append(stations, buyer)
	}

	return stations, result.Err()
}

// calculatePlasmiumScore calculates the score for a station based on Orok's formula.
// Scoring (mutually exclusive, check in order):
// - Platinum demand >= 1288: 100 pts (optimal)
// - Osmium demand >= 1288: 80 pts (good)
// - Platinum demand 1-1287: linear scale (demand/1288)*100
// - Osmium demand 1-1287: linear scale (demand/1288)*80
// - Military/Colony economy: 40 pts (hidden demand)
// - None: 0 (skip)
func calculatePlasmiumScore(platinumDemand, osmiumDemand int64, economies []string) (float64, string) {
	const optimalDemand = 1288 // 4 × Type-9 loads (322 tons each)

	// Optimal: High Platinum demand
	if platinumDemand >= optimalDemand {
		return 100, fmt.Sprintf("Platinum demand %dt (optimal)", platinumDemand)
	}

	// Good: High Osmium demand
	if osmiumDemand >= optimalDemand {
		return 80, fmt.Sprintf("Osmium demand %dt (optimal)", osmiumDemand)
	}

	// Linear scaling for sub-threshold Platinum (Orok's feedback)
	if platinumDemand > 0 {
		score := (float64(platinumDemand) / float64(optimalDemand)) * 100
		return score, fmt.Sprintf("Platinum demand %dt (%.0f%%)", platinumDemand, score)
	}

	// Linear scaling for sub-threshold Osmium
	if osmiumDemand > 0 {
		score := (float64(osmiumDemand) / float64(optimalDemand)) * 80
		return score, fmt.Sprintf("Osmium demand %dt (%.0f%%)", osmiumDemand, score)
	}

	// Hidden demand stations (Military, Colony, possibly others)
	for _, econ := range economies {
		if econ == "Military" || econ == "Colony" {
			return 40, fmt.Sprintf("%s economy (hidden Osmium demand ~120k/t)", econ)
		}
	}

	return 0, "No viable demand"
}

// calculateRankScore computes a composite ranking score that prioritizes:
// - Fresh market data (0-100 pts)
// - Large landing pads (0-50 pts)
// - High demand via plasmium score (0-100 pts)
// - Price bonus (0-50 pts)
// - Kaine progress bonus (0-80 pts) — low progress = higher priority
// Total max: 380 pts
func calculateRankScore(b *PlasmiumBuyer) float64 {
	var rankScore float64

	// Freshness score (0-100): prioritize recent data
	if b.MarketUpdatedAt != nil {
		hoursAgo := time.Since(*b.MarketUpdatedAt).Hours()
		switch {
		case hoursAgo < 6:
			rankScore += 100 // Very fresh
		case hoursAgo < 24:
			rankScore += 75 // Fresh
		case hoursAgo < 48:
			rankScore += 40 // Aging
		case hoursAgo < 168: // 7 days
			rankScore += 15 // Stale
		default:
			rankScore += 5 // Very stale but has data
		}
	}
	// No data = 0 freshness points

	// Pad score (0-50): large pads are important for mining ships
	switch b.LargestPad {
	case "L":
		rankScore += 50
	case "M":
		rankScore += 25
	case "S":
		rankScore += 10
	}

	// Demand score (0-100): use existing plasmium score
	rankScore += b.Score

	// Price bonus (0-50): higher prices are better
	// Platinum typically sells for 200-300k, Osmium for 100-200k
	// Normalize: 300k = 50 pts, 0 = 0 pts
	maxPrice := max(b.PlatinumPrice, b.OsmiumPrice)
	if maxPrice > 0 {
		priceBonus := float64(maxPrice) / 300000.0 * 50.0
		if priceBonus > 50 {
			priceBonus = 50
		}
		rankScore += priceBonus
	}

	// Kaine progress bonus (0-80): prioritize systems we don't yet control
	rankScore += progressScoreBonus(b.KaineProgress)

	return rankScore
}

// distance3D calculates Euclidean distance in 3D space.
func distance3D(x1, y1, z1, x2, y2, z2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	dz := z2 - z1
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// largestPad returns "L", "M", "S", or "" based on which pads are available.
func largestPad(large, medium, small int) string {
	if large > 0 {
		return "L"
	}
	if medium > 0 {
		return "M"
	}
	if small > 0 {
		return "S"
	}
	return ""
}

// Helper functions for Neo4j type conversion

func getRecordValue(record *neo4j.Record, key string) any {
	val, _ := record.Get(key)
	return val
}

func toString(record *neo4j.Record, key string) string {
	val, _ := record.Get(key)
	if s, ok := val.(string); ok {
		return s
	}
	return ""
}

func toFloat64(val any) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}

func toInt64(val any) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	default:
		return 0
	}
}

func toTime(val any) time.Time {
	switch v := val.(type) {
	case time.Time:
		return v
	case neo4j.LocalDateTime:
		return v.Time()
	case neo4j.Date:
		return v.Time()
	case neo4j.Time: // ZonedTime - has timezone info
		return v.Time()
	case neo4j.LocalTime:
		return v.Time()
	case string:
		// Handle ISO-8601 string format (fallback if Memgraph returns string)
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if t, err := time.Parse("2006-01-02T15:04:05", v); err == nil {
			return t
		}
		// Handle Memgraph ZonedDateTime format: "2026-02-01T19:42:33.000000+00:00[Etc/UTC]"
		if idx := strings.Index(v, "["); idx > 0 {
			if t, err := time.Parse(time.RFC3339, v[:idx]); err == nil {
				return t
			}
		}
		return time.Time{}
	default:
		return time.Time{}
	}
}

// kaineSystem represents a Kaine-controlled Fortified/Stronghold system for distance calculation.
type kaineSystem struct {
	Name    string
	X, Y, Z float64
}

// getKaineAnchors fetches all Nakato Kaine Fortified and Stronghold systems as anchors.
// Anchors provide the acquisition range: 20 LY for Fortified, 30 LY for Stronghold.
func getKaineAnchors(ctx context.Context, client MemgraphClient) ([]anchorCoverage, error) {
	session := client.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.controlling_power = 'Nakato Kaine'
		  AND s.powerplay_state IN ['Fortified', 'Stronghold']
		  AND s.location IS NOT NULL
		RETURN s.name AS name, s.location.x AS x, s.location.y AS y, s.location.z AS z, s.powerplay_state AS powerplay_state
	`

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("query kaine anchors: %w", err)
	}

	var anchors []anchorCoverage
	for result.Next(ctx) {
		record := result.Record()
		powerState := toString(record, "powerplay_state")
		radius := 20 // Fortified
		if powerState == "Stronghold" {
			radius = 30
		}
		anchors = append(anchors, anchorCoverage{
			Name:       toString(record, "name"),
			X:          toFloat64(getRecordValue(record, "x")),
			Y:          toFloat64(getRecordValue(record, "y")),
			Z:          toFloat64(getRecordValue(record, "z")),
			Radius:     radius,
			PowerState: powerState,
		})
	}

	return anchors, result.Err()
}

// getKaineFortifiedSystems fetches all Nakato Kaine Fortified and Stronghold systems.
// Used for calculating DistanceToKaine display field.
func getKaineFortifiedSystems(ctx context.Context, client MemgraphClient) ([]kaineSystem, error) {
	session := client.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.controlling_power = 'Nakato Kaine'
		  AND s.powerplay_state IN ['Fortified', 'Stronghold']
		  AND s.location IS NOT NULL
		RETURN s.name AS name, s.location.x AS x, s.location.y AS y, s.location.z AS z
	`

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("query kaine systems: %w", err)
	}

	var systems []kaineSystem
	for result.Next(ctx) {
		record := result.Record()
		systems = append(systems, kaineSystem{
			Name: toString(record, "name"),
			X:    toFloat64(getRecordValue(record, "x")),
			Y:    toFloat64(getRecordValue(record, "y")),
			Z:    toFloat64(getRecordValue(record, "z")),
		})
	}

	return systems, result.Err()
}

// progressScoreBonus returns a ranking bonus that prioritizes systems with LOW Kaine acquisition progress.
// Systems we don't yet control need more attention from miners, so they rank higher.
// Returns 0-80 pts (inverse of progress — lower progress = higher bonus).
func progressScoreBonus(progress *float64) float64 {
	if progress == nil {
		// No progress data — moderate bonus (worth investigating)
		return 40
	}
	p := *progress
	switch {
	case p <= 0:
		return 80 // Not started — highest priority
	case p < 0.5:
		return 60 // Under 50% — high priority
	case p < 1.0:
		return 40 // Under 100% — moderate priority
	case p < 2.0:
		return 20 // 100-200% — lower priority (we're ahead)
	case p < 5.0:
		return 10 // 200-500% — we're well ahead
	default:
		return 0 // 500%+ — already dominant, no bonus
	}
}

// calculateDistanceToKaine calculates the distance from a point to the nearest Kaine Fortified/Stronghold system.
func calculateDistanceToKaine(x, y, z float64, kaineSystems []kaineSystem) float64 {
	if len(kaineSystems) == 0 {
		return 0
	}

	minDist := math.MaxFloat64
	for _, ks := range kaineSystems {
		dist := distance3D(x, y, z, ks.X, ks.Y, ks.Z)
		if dist < minDist {
			minDist = dist
		}
	}

	return minDist
}

// parseKaineProgress extracts Nakato Kaine's acquisition progress from conflict_progress data.
// The data may be stored as a JSON string or an already-parsed []any.
// Returns nil if Kaine has no progress entry for this system.
func parseKaineProgress(v any) *float64 {
	if v == nil {
		return nil
	}

	// Try as []any first (if neo4j driver parsed it)
	entries, ok := v.([]any)
	if !ok {
		// Try as JSON string
		str, ok := v.(string)
		if !ok || str == "" {
			return nil
		}
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(str), &parsed); err != nil {
			return nil
		}
		for _, m := range parsed {
			if power, _ := m["Power"].(string); power == "Nakato Kaine" {
				if prog, ok := m["ConflictProgress"].(float64); ok {
					return &prog
				}
			}
		}
		return nil
	}

	for _, item := range entries {
		if m, ok := item.(map[string]any); ok {
			if power, _ := m["Power"].(string); power == "Nakato Kaine" {
				if prog, ok := m["ConflictProgress"].(float64); ok {
					return &prog
				}
			}
		}
	}
	return nil
}
