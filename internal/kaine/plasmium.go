package kaine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// ============================================================================
// PLASMIUM BUYERS - Daily Process 1: Finding stations to sell Platinum/Osmium
// See: eddn-listener/docs/kaine-directors-processes/orok-pseudocode.md
// ============================================================================

// PlasmiumBuyer represents a station that can buy Platinum/Osmium near a mining map.
type PlasmiumBuyer struct {
	// Station info (from galaxy.*)
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

	// Powerplay info (from galaxy.*)
	PowerplayState  string   `json:"powerplay_state"`             // Unoccupied, Expansion, Contested (null shown as Unoccupied)
	DistanceToKaine float64  `json:"distance_to_kaine,omitempty"` // Distance to nearest Kaine Fortified/Stronghold
	KaineProgress   *float64 `json:"kaine_progress,omitempty"`    // Nakato Kaine's acquisition progress (0-1) for this system

	// Landing pads (from galaxy.*)
	LargePads  int    `json:"large_pads,omitempty"`
	MediumPads int    `json:"medium_pads,omitempty"`
	SmallPads  int    `json:"small_pads,omitempty"`
	LargestPad string `json:"largest_pad"` // "L", "M", "S", or ""

	// Coordinates (from galaxy.*)
	X float64 `json:"x,omitempty"`
	Y float64 `json:"y,omitempty"`
	Z float64 `json:"z,omitempty"`

	// Market info (from galaxy.*)
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
	SystemName    string   `json:"system_name"`
	Body          string   `json:"body"`
	RingType      string   `json:"ring_type"`
	ReserveLevel  string   `json:"reserve_level"`
	PowerState    string   `json:"power_state"`
	RESNotes      string   `json:"res_notes,omitempty"`
	Hotspots      []string `json:"hotspots,omitempty"`
	Map1          string   `json:"map_1,omitempty"`
	Map1Title     string   `json:"map_1_title,omitempty"`
	Map1Commodity []string `json:"map_1_commodity,omitempty"` // Commodities this map produces
	Map2          string   `json:"map_2,omitempty"`
	Map2Title     string   `json:"map_2_title,omitempty"`
	Map2Commodity []string `json:"map_2_commodity,omitempty"` // Commodities this map produces
	Map3          string   `json:"map_3,omitempty"`
	Map3Title     string   `json:"map_3_title,omitempty"`
	Map3Commodity []string `json:"map_3_commodity,omitempty"` // Commodities this map produces

	// Map coordinates (from galaxy.*)
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`

	// Live power state (from galaxy.*) - the map system IS the source
	LivePowerState string `json:"live_power_state"` // Fortified or Stronghold (live)
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

// GalaxyQuerier is the relational galaxy read interface needed by Kaine mining.
// It is deliberately SQL-shaped because the Kaine package owns the response
// structs and scoring rules while galaxy.* owns the live universe snapshot.
type GalaxyQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// mapSearchParams holds the search parameters for a map.
type mapSearchParams struct {
	Map            *MiningMap
	X, Y, Z        float64
	LivePowerState string // Fortified or Stronghold (from galaxy.*)
	SearchRadius   int    // 20 for Fortified, 30 for Stronghold
}

// anchorCoverage represents an anchor system that covers a map.
type anchorCoverage struct {
	Name       string
	X, Y, Z    float64
	Radius     int    // 20 for Fortified, 30 for Stronghold
	PowerState string // "Fortified" or "Stronghold"
}

// FindPlasmiumBuyers finds stations in Boom state that buy Platinum/Osmium near Kaine mining maps.
// This implements Orok's Daily Process 1 as documented in orok-pseudocode.md.
//
// The map system IS the source (same model as LTD). No anchor intermediary.
// Maps must be Fortified (20 LY radius) or Stronghold (30 LY radius).
// Buyer stations must be in acquisition target systems within that radius.
func (s *Store) FindPlasmiumBuyers(ctx context.Context, galaxy GalaxyQuerier, progress ProgressFunc) (*PlasmiumBuyersResponse, error) {
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

	// Step 2: Get live power state and coordinates for each map system from galaxy.*
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

	// Get Kaine systems for DistanceToKaine display field on buyers
	kaineSystems, err := getKaineFortifiedSystems(ctx, galaxy)
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
	allBoomStations, err := getAllBoomStations(ctx, galaxy)
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
// Note: power_state filtering (Fortified/Stronghold) is done in FindPlasmiumBuyers after galaxy lookup.
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

// getSystemCoords fetches coordinates for systems from galaxy.system_catalog.
func getSystemCoords(ctx context.Context, galaxy GalaxyQuerier, systemNames []string) (map[string]systemCoord, error) {
	if len(systemNames) == 0 {
		return map[string]systemCoord{}, nil
	}
	query := `
		SELECT name, x::float8, y::float8, z::float8
		FROM galaxy.system_catalog
		WHERE name = ANY($1)
		  AND x IS NOT NULL
	`

	rows, err := galaxy.Query(ctx, query, systemNames)
	if err != nil {
		return nil, fmt.Errorf("query system coords: %w", err)
	}
	defer rows.Close()

	coords := make(map[string]systemCoord)
	for rows.Next() {
		var name string
		var coord systemCoord
		if err := rows.Scan(&name, &coord.X, &coord.Y, &coord.Z); err != nil {
			return nil, err
		}
		coords[name] = coord
	}

	return coords, rows.Err()
}

// getPowerStates fetches powerplay states for systems from galaxy.system_power.
func getPowerStates(ctx context.Context, galaxy GalaxyQuerier, systemNames []string) (map[string]string, error) {
	if len(systemNames) == 0 {
		return map[string]string{}, nil
	}

	query := `
		SELECT c.name, COALESCE(sp.powerplay_state, '')
		FROM galaxy.system_catalog c
		LEFT JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
		WHERE c.name = ANY($1)
	`

	rows, err := galaxy.Query(ctx, query, systemNames)
	if err != nil {
		return nil, fmt.Errorf("query power states: %w", err)
	}
	defer rows.Close()

	states := make(map[string]string)
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			return nil, err
		}
		states[name] = state
	}

	return states, rows.Err()
}

// getAllBoomStations fetches ALL stations in ACQUISITION TARGET systems where Boom is present.
//
// Returns two types of matches (indicated by BoomMatch field):
//   - "controlling_faction": The station's controlling faction is in Boom (verified match)
//   - "system_boom": Another faction in the system is in Boom but not the controlling faction
//     (the station may still benefit - worth checking in-game, especially if no market data yet)
//
// IMPORTANT: Only returns stations in systems that are acquisition targets:
// - Acquisition targets: powerplay_state is NULL, "Unoccupied", "Expansion", or "Contested"
// - Excluded: "Exploited", "Fortified", "Stronghold", "HomeSystem" (already controlled by a power)
//
// Market data (Platinum/Osmium demand) is fetched where available but is NOT a filter.
// We may not have market data if nobody has docked at the station recently.
func getAllBoomStations(ctx context.Context, galaxy GalaxyQuerier) ([]PlasmiumBuyer, error) {
	// Find all stations in acquisition target systems where ANY faction has Boom.
	// We track whether the station's own controlling faction is the one in Boom.
	query := `
WITH boom_systems AS (
	SELECT
		sf.system_id64,
		st.market_id,
		bool_or(sf.faction_id = st.controlling_faction_id) AS controlling_boom
	FROM galaxy.system_faction sf
	JOIN galaxy.system_catalog c ON c.id64 = sf.system_id64
	LEFT JOIN galaxy.system_power sp ON sp.system_id64 = sf.system_id64
	JOIN galaxy.station st ON st.system_id64 = sf.system_id64
	WHERE 'Boom' = ANY(sf.active_states)
	  AND c.x IS NOT NULL
	  AND COALESCE(sp.powerplay_state, 'Unoccupied') IN ('', 'Unoccupied', 'Expansion', 'Contested')
	  AND st.controlling_faction_id IS NOT NULL
	GROUP BY sf.system_id64, st.market_id
)
SELECT
	COALESCE(sys.name, c.name) AS system_name,
	c.x::float8, c.y::float8, c.z::float8,
	COALESCE(sp.powerplay_state, '') AS powerplay_state,
	sp.conflict_progress,
	st.name AS station_name,
	COALESCE(f.name, '') AS faction,
	bs.controlling_boom AS is_controlling_faction_boom,
	COALESCE(st.economies, '{}') AS economies,
	COALESCE(st.large_pads, 0) AS large_pads,
	COALESCE(st.medium_pads, 0) AS medium_pads,
	COALESCE(st.small_pads, 0) AS small_pads,
	COALESCE(mc_plat.demand, 0) AS platinum_demand,
	COALESCE(mc_plat.sell_price, 0) AS platinum_price,
	COALESCE(mc_osm.demand, 0) AS osmium_demand,
	COALESCE(mc_osm.sell_price, 0) AS osmium_price,
	m.last_event_time AS market_updated_at
FROM boom_systems bs
JOIN galaxy.station st ON st.market_id = bs.market_id
JOIN galaxy.system_catalog c ON c.id64 = st.system_id64
LEFT JOIN galaxy.system sys ON sys.id64 = st.system_id64
LEFT JOIN galaxy.system_power sp ON sp.system_id64 = st.system_id64
LEFT JOIN galaxy.faction f ON f.faction_id = st.controlling_faction_id
LEFT JOIN galaxy.market m ON m.market_id = st.market_id
LEFT JOIN galaxy.commodity c_plat ON c_plat.name = 'platinum'
LEFT JOIN galaxy.market_commodity mc_plat ON mc_plat.market_id = st.market_id AND mc_plat.commodity_id = c_plat.commodity_id
LEFT JOIN galaxy.commodity c_osm ON c_osm.name = 'osmium'
LEFT JOIN galaxy.market_commodity mc_osm ON mc_osm.market_id = st.market_id AND mc_osm.commodity_id = c_osm.commodity_id
WHERE st.controlling_faction_id IS NOT NULL
	`

	rows, err := galaxy.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query all boom stations: %w", err)
	}
	defer rows.Close()

	var stations []PlasmiumBuyer
	seen := make(map[string]bool) // Dedupe: same station may appear via multiple Boom factions
	for rows.Next() {
		var systemName, powerplayState, stationName, faction string
		var x, y, z float64
		var conflictProgress []byte
		var isControlling bool
		var economies []string
		var largePads, mediumPads, smallPads int
		var platinumDemand, platinumPrice, osmiumDemand, osmiumPrice int64
		var marketUpdatedAt *time.Time
		if err := rows.Scan(
			&systemName, &x, &y, &z,
			&powerplayState, &conflictProgress,
			&stationName, &faction, &isControlling, &economies,
			&largePads, &mediumPads, &smallPads,
			&platinumDemand, &platinumPrice, &osmiumDemand, &osmiumPrice,
			&marketUpdatedAt,
		); err != nil {
			return nil, err
		}
		stationKey := systemName + "|" + stationName

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
			Faction:        faction,
			FactionState:   factionState,
			BoomMatch:      boomMatch,
			PowerplayState: powerplayState,
			LargePads:      largePads,
			MediumPads:     mediumPads,
			SmallPads:      smallPads,
			LargestPad:     largestPad(largePads, mediumPads, smallPads),
			Economies:      economies,
			X:              x,
			Y:              y,
			Z:              z,
			PlatinumDemand: platinumDemand,
			PlatinumPrice:  platinumPrice,
			OsmiumDemand:   osmiumDemand,
			OsmiumPrice:    osmiumPrice,
		}

		buyer.KaineProgress = parseKaineProgress(conflictProgress)

		if marketUpdatedAt != nil && !marketUpdatedAt.IsZero() {
			buyer.MarketUpdatedAt = marketUpdatedAt
		}

		stations = append(stations, buyer)
	}

	return stations, rows.Err()
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

// kaineSystem represents a Kaine-controlled Fortified/Stronghold system for distance calculation.
type kaineSystem struct {
	Name    string
	X, Y, Z float64
}

// getKaineAnchors fetches all Nakato Kaine Fortified and Stronghold systems as anchors.
// Anchors provide the acquisition range: 20 LY for Fortified, 30 LY for Stronghold.
func getKaineAnchors(ctx context.Context, galaxy GalaxyQuerier) ([]anchorCoverage, error) {
	query := `
		SELECT COALESCE(sys.name, c.name) AS name, c.x::float8, c.y::float8, c.z::float8, sp.powerplay_state
		FROM galaxy.system_power sp
		JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
		LEFT JOIN galaxy.system sys ON sys.id64 = sp.system_id64
		WHERE sp.power_name = 'Nakato Kaine'
		  AND sp.powerplay_state IN ('Fortified', 'Stronghold')
		  AND c.x IS NOT NULL
	`

	rows, err := galaxy.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query kaine anchors: %w", err)
	}
	defer rows.Close()

	var anchors []anchorCoverage
	for rows.Next() {
		var anchor anchorCoverage
		if err := rows.Scan(&anchor.Name, &anchor.X, &anchor.Y, &anchor.Z, &anchor.PowerState); err != nil {
			return nil, err
		}
		radius := 20 // Fortified
		if anchor.PowerState == "Stronghold" {
			radius = 30
		}
		anchor.Radius = radius
		anchors = append(anchors, anchor)
	}

	return anchors, rows.Err()
}

// getKaineFortifiedSystems fetches all Nakato Kaine Fortified and Stronghold systems.
// Used for calculating DistanceToKaine display field.
func getKaineFortifiedSystems(ctx context.Context, galaxy GalaxyQuerier) ([]kaineSystem, error) {
	query := `
		SELECT COALESCE(sys.name, c.name) AS name, c.x::float8, c.y::float8, c.z::float8
		FROM galaxy.system_power sp
		JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
		LEFT JOIN galaxy.system sys ON sys.id64 = sp.system_id64
		WHERE sp.power_name = 'Nakato Kaine'
		  AND sp.powerplay_state IN ('Fortified', 'Stronghold')
		  AND c.x IS NOT NULL
	`

	rows, err := galaxy.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query kaine systems: %w", err)
	}
	defer rows.Close()

	var systems []kaineSystem
	for rows.Next() {
		var system kaineSystem
		if err := rows.Scan(&system.Name, &system.X, &system.Y, &system.Z); err != nil {
			return nil, err
		}
		systems = append(systems, system)
	}

	return systems, rows.Err()
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

	switch raw := v.(type) {
	case []byte:
		if len(raw) == 0 {
			return nil
		}
		var parsed []map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
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
	case string:
		if raw == "" {
			return nil
		}
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
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

	// Keep support for already-decoded fixture data.
	entries, ok := v.([]any)
	if !ok {
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
