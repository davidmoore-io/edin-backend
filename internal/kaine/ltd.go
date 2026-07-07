package kaine

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ============================================================================
// LTD BUYERS - Daily Process 2: Finding stations to sell Low Temperature Diamonds
// The map system IS the source — it must be Fortified or Stronghold.
// Buyers must be within the source's acquisition radius (20/30 LY).
// See: eddn-listener/docs/kaine-directors-processes/orok-pseudocode.md
// ============================================================================

// LTDBuyer represents a station in Expansion state that can buy LTDs.
type LTDBuyer struct {
	// Station info (from galaxy.*)
	SystemName   string   `json:"system_name"`
	StationName  string   `json:"station_name"`
	Faction      string   `json:"faction"`
	FactionState string   `json:"faction_state"` // "Expansion" or similar
	Economies    []string `json:"economies,omitempty"`
	DistanceLY   float64  `json:"distance_ly"`

	// Powerplay info (from galaxy.*)
	PowerplayState  string   `json:"powerplay_state"`             // Unoccupied, Expansion, Contested
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
	LTDDemand int64 `json:"ltd_demand,omitempty"`
	LTDPrice  int64 `json:"ltd_price,omitempty"` // Often stale - verify in-game

	// Calculated
	Score       float64 `json:"score"`
	ScoreReason string  `json:"score_reason"`
	RankScore   float64 `json:"rank_score"` // Composite ranking

	// Freshness indicators
	BGSUpdatedAt    *time.Time `json:"bgs_updated_at,omitempty"`
	MarketUpdatedAt *time.Time `json:"market_updated_at,omitempty"`
}

// LTDMapResult represents an LTD mining map with its nearby Expansion buyers.
type LTDMapResult struct {
	// Map info (from TimescaleDB)
	SystemName   string   `json:"system_name"`
	Body         string   `json:"body"`
	RingType     string   `json:"ring_type"` // Should be "Icy"
	ReserveLevel string   `json:"reserve_level"`
	PowerState   string   `json:"power_state"` // From CSV (may be stale)
	RESNotes     string   `json:"res_notes,omitempty"`
	Hotspots     []string `json:"hotspots,omitempty"`
	Map1         string   `json:"map_1,omitempty"`
	Map1Title    string   `json:"map_1_title,omitempty"`
	Map2         string   `json:"map_2,omitempty"`
	Map2Title    string   `json:"map_2_title,omitempty"`
	Map3         string   `json:"map_3,omitempty"`
	Map3Title    string   `json:"map_3_title,omitempty"`

	// Map coordinates (from galaxy.*)
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`

	// Source info (the map system IS the source — must be Fortified/Stronghold)
	AnchorName       string  `json:"anchor_name"`        // Same as SystemName (map is the source)
	AnchorState      string  `json:"anchor_state"`       // Live power state: "Fortified" or "Stronghold"
	DistanceToAnchor float64 `json:"distance_to_anchor"` // Always 0 (map is the source)
	SearchRadiusLY   int     `json:"search_radius_ly"`   // Source's acquisition radius (20 or 30)

	// Nearby buyers sorted by score
	Buyers []LTDBuyer `json:"buyers"`
}

// LTDBuyersResponse is the full API response.
type LTDBuyersResponse struct {
	Maps        []LTDMapResult `json:"maps"`
	GeneratedAt time.Time      `json:"generated_at"`
	TotalMaps   int            `json:"total_maps"`
	TotalBuyers int            `json:"total_buyers"`
}

// FindLTDBuyers finds stations with Expansion factions that buy LTDs near Kaine LTD mining maps.
// The map system IS the source — it must be Fortified or Stronghold. Buyers must be within
// the source system's acquisition radius (20 LY for Fortified, 30 LY for Stronghold).
// See: eddn-listener/docs/kaine-directors-processes/orok-pseudocode.md
func (s *Store) FindLTDBuyers(ctx context.Context, galaxy GalaxyQuerier, progress ProgressFunc) (*LTDBuyersResponse, error) {
	if progress == nil {
		progress = func(int, int, string) {}
	}

	// Step 1: Get LTD maps from TimescaleDB (Icy rings with LTD hotspots)
	progress(1, 5, "Fetching LTD mining maps from database")
	maps, err := s.getLTDMaps(ctx)
	if err != nil {
		return nil, fmt.Errorf("get ltd maps: %w", err)
	}

	if len(maps) == 0 {
		return &LTDBuyersResponse{
			Maps:        []LTDMapResult{},
			GeneratedAt: time.Now(),
			TotalMaps:   0,
			TotalBuyers: 0,
		}, nil
	}

	// Step 2: Get Kaine Fortified/Stronghold systems (for DistanceToKaine display field)
	progress(2, 5, fmt.Sprintf("Querying galaxy store for %d map system coordinates and power states", len(maps)))
	kaineSystems, err := getKaineFortifiedSystems(ctx, galaxy)
	if err != nil {
		return nil, fmt.Errorf("get kaine systems: %w", err)
	}

	// Step 3: Get map system coordinates and live power states from galaxy.*
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

	// Step 4: Filter maps — only those in Fortified/Stronghold (the map IS the source)
	progress(3, 5, "Filtering to Fortified and Stronghold systems")
	type ltdSourceMap struct {
		Map          *MiningMap
		X, Y, Z      float64
		LiveState    string
		SearchRadius int // 20 for Fortified, 30 for Stronghold
	}

	var sourceMaps []ltdSourceMap
	for i := range maps {
		m := &maps[i]
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
			continue // Skip maps not in Fortified/Stronghold
		}

		sourceMaps = append(sourceMaps, ltdSourceMap{
			Map:          m,
			X:            coord.X,
			Y:            coord.Y,
			Z:            coord.Z,
			LiveState:    liveState,
			SearchRadius: radius,
		})
	}

	if len(sourceMaps) == 0 {
		return &LTDBuyersResponse{
			Maps:        []LTDMapResult{},
			GeneratedAt: time.Now(),
			TotalMaps:   0,
			TotalBuyers: 0,
		}, nil
	}

	// Step 5: Get ALL Expansion stations globally
	progress(4, 5, fmt.Sprintf("Scanning Expansion stations within range of %d qualifying maps", len(sourceMaps)))
	allExpansionStations, err := getAllExpansionStations(ctx, galaxy)
	if err != nil {
		return nil, fmt.Errorf("get expansion stations: %w", err)
	}

	// Step 6: For each source map, find stations within its radius
	progress(5, 5, "Scoring and ranking buyers by demand and distance")
	var results []LTDMapResult
	var totalBuyers int

	for _, src := range sourceMaps {
		var buyers []LTDBuyer
		seen := make(map[string]bool)

		for _, station := range allExpansionStations {
			dist := distance3D(src.X, src.Y, src.Z, station.X, station.Y, station.Z)
			if dist <= float64(src.SearchRadius) {
				stationKey := station.SystemName + "|" + station.StationName
				if !seen[stationKey] {
					seen[stationKey] = true
					buyer := station
					buyer.DistanceLY = dist
					buyers = append(buyers, buyer)
				}
			}
		}

		// Score buyers and calculate distance to nearest Kaine system
		for i := range buyers {
			buyers[i].Score, buyers[i].ScoreReason = calculateLTDScore(buyers[i].LTDDemand)
			buyers[i].DistanceToKaine = calculateDistanceToKaine(
				buyers[i].X, buyers[i].Y, buyers[i].Z,
				kaineSystems,
			)
			buyers[i].RankScore = calculateLTDRankScore(&buyers[i])
		}

		// Filter out zero-score buyers and those with LTD price below 100k
		var validBuyers []LTDBuyer
		for _, b := range buyers {
			if b.Score <= 0 {
				continue
			}
			// Exclude stations where we have price data and it's below 100k
			if b.LTDPrice > 0 && b.LTDPrice < 100_000 {
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

		m := src.Map
		results = append(results, LTDMapResult{
			SystemName:       m.SystemName,
			Body:             m.Body,
			RingType:         m.RingType,
			ReserveLevel:     m.ReserveLevel,
			PowerState:       m.PowerState,
			RESNotes:         m.RESSites,
			Hotspots:         m.Hotspots,
			Map1:             m.Map1,
			Map1Title:        m.Map1Title,
			Map2:             m.Map2,
			Map2Title:        m.Map2Title,
			Map3:             m.Map3,
			Map3Title:        m.Map3Title,
			X:                src.X,
			Y:                src.Y,
			Z:                src.Z,
			AnchorName:       m.SystemName,     // Source is the map itself
			AnchorState:      src.LiveState,    // Live power state from galaxy.*
			DistanceToAnchor: 0,                // Map IS the source — distance is 0
			SearchRadiusLY:   src.SearchRadius, // 20 for Fortified, 30 for Stronghold
			Buyers:           validBuyers,
		})
	}

	// Sort results by top buyer's rank score
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
		return results[i].SystemName < results[j].SystemName
	})

	return &LTDBuyersResponse{
		Maps:        results,
		GeneratedAt: time.Now(),
		TotalMaps:   len(results),
		TotalBuyers: totalBuyers,
	}, nil
}

// getLTDMaps retrieves mining maps that produce Low Temperature Diamonds.
// Filters on map commodity fields (consistent with plasmium query pattern).
func (s *Store) getLTDMaps(ctx context.Context) ([]MiningMap, error) {
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
		  (
		    'lowtemperaturediamond' = ANY(map_1_commodity)
		    OR 'lowtemperaturediamond' = ANY(map_2_commodity)
		    OR 'lowtemperaturediamond' = ANY(map_3_commodity)
		  )
		ORDER BY system_name
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query ltd maps: %w", err)
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

// getAllExpansionStations fetches ALL stations with Expansion faction state in ACQUISITION TARGET systems.
func getAllExpansionStations(ctx context.Context, galaxy GalaxyQuerier) ([]LTDBuyer, error) {
	query := `
SELECT DISTINCT
	COALESCE(sys.name, c.name) AS system_name,
	c.x::float8, c.y::float8, c.z::float8,
	COALESCE(sp.powerplay_state, '') AS powerplay_state,
	sp.conflict_progress,
	st.name AS station_name,
	COALESCE(f.name, '') AS faction,
	sf.active_states,
	COALESCE(st.economies, '{}') AS economies,
	COALESCE(st.large_pads, 0) AS large_pads,
	COALESCE(st.medium_pads, 0) AS medium_pads,
	COALESCE(st.small_pads, 0) AS small_pads,
	COALESCE(mc_ltd.demand, 0) AS ltd_demand,
	COALESCE(mc_ltd.sell_price, 0) AS ltd_price,
	sf.last_event_time AS bgs_updated_at,
	m.last_event_time AS market_updated_at
FROM galaxy.system_faction sf
JOIN galaxy.faction f ON f.faction_id = sf.faction_id
JOIN galaxy.system_catalog c ON c.id64 = sf.system_id64
LEFT JOIN galaxy.system sys ON sys.id64 = sf.system_id64
LEFT JOIN galaxy.system_power sp ON sp.system_id64 = sf.system_id64
JOIN galaxy.station st ON st.system_id64 = sf.system_id64 AND st.controlling_faction_id = sf.faction_id
LEFT JOIN galaxy.market m ON m.market_id = st.market_id
LEFT JOIN galaxy.commodity c_ltd ON c_ltd.name = 'lowtemperaturediamond'
LEFT JOIN galaxy.market_commodity mc_ltd ON mc_ltd.market_id = st.market_id AND mc_ltd.commodity_id = c_ltd.commodity_id
WHERE 'Expansion' = ANY(sf.active_states)
  AND c.x IS NOT NULL
  AND COALESCE(sp.powerplay_state, 'Unoccupied') IN ('', 'Unoccupied', 'Expansion', 'Contested')
	`

	rows, err := galaxy.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query all expansion stations: %w", err)
	}
	defer rows.Close()

	var stations []LTDBuyer
	for rows.Next() {
		var systemName, powerplayState, stationName, faction string
		var x, y, z float64
		var conflictProgress []byte
		var activeStates []string
		var economies []string
		var largePads, mediumPads, smallPads int
		var ltdDemand, ltdPrice int64
		var bgsUpdatedAt, marketUpdatedAt *time.Time
		if err := rows.Scan(
			&systemName, &x, &y, &z,
			&powerplayState, &conflictProgress,
			&stationName, &faction, &activeStates, &economies,
			&largePads, &mediumPads, &smallPads,
			&ltdDemand, &ltdPrice,
			&bgsUpdatedAt, &marketUpdatedAt,
		); err != nil {
			return nil, err
		}

		if powerplayState == "" {
			powerplayState = "Unoccupied"
		}

		factionState := "Expansion"
		if len(activeStates) > 0 && activeStates[0] != "" {
			factionState = activeStates[0]
		}

		buyer := LTDBuyer{
			SystemName:     systemName,
			StationName:    stationName,
			Faction:        faction,
			FactionState:   factionState,
			Economies:      economies,
			PowerplayState: powerplayState,
			LargePads:      largePads,
			MediumPads:     mediumPads,
			SmallPads:      smallPads,
			LargestPad:     largestPad(largePads, mediumPads, smallPads),
			X:              x,
			Y:              y,
			Z:              z,
			LTDDemand:      ltdDemand,
			LTDPrice:       ltdPrice,
		}

		buyer.KaineProgress = parseKaineProgress(conflictProgress)

		if bgsUpdatedAt != nil && !bgsUpdatedAt.IsZero() {
			buyer.BGSUpdatedAt = bgsUpdatedAt
		}
		if marketUpdatedAt != nil && !marketUpdatedAt.IsZero() {
			buyer.MarketUpdatedAt = marketUpdatedAt
		}

		stations = append(stations, buyer)
	}

	return stations, rows.Err()
}

// calculateLTDScore calculates the score for an LTD station.
// LTD demand >= 1288: 100 pts (optimal)
// LTD demand > 0: linear scale
// No demand but Industrial economy: 60 pts (can sustain acquisition)
// Note: Price filtering is NOT done here - EDDN market data can be stale
func calculateLTDScore(ltdDemand int64) (float64, string) {
	const optimalDemand = 1288

	if ltdDemand >= optimalDemand {
		return 100, fmt.Sprintf("LTD demand %dt (optimal)", ltdDemand)
	}

	if ltdDemand > 0 {
		score := (float64(ltdDemand) / float64(optimalDemand)) * 100
		return score, fmt.Sprintf("LTD demand %dt (%.0f%%)", ltdDemand, score)
	}

	// Even without visible LTD demand, Expansion stations are useful
	// because price must be verified in-game anyway
	return 40, "Expansion state (verify LTD price in-game)"
}

// calculateLTDRankScore computes a composite ranking score for LTD buyers.
// Components: freshness(0-100) + pad(0-50) + demand(0-100) + price(0-50) + industrial(0-20) + progress(0-80)
// Total max: 400 pts. Low Kaine progress = higher priority (needs more mining attention).
func calculateLTDRankScore(b *LTDBuyer) float64 {
	var rankScore float64

	// Freshness score (0-100)
	if b.MarketUpdatedAt != nil {
		hoursAgo := time.Since(*b.MarketUpdatedAt).Hours()
		switch {
		case hoursAgo < 6:
			rankScore += 100
		case hoursAgo < 24:
			rankScore += 75
		case hoursAgo < 48:
			rankScore += 40
		case hoursAgo < 168:
			rankScore += 15
		default:
			rankScore += 5
		}
	}

	// Pad score (0-50)
	switch b.LargestPad {
	case "L":
		rankScore += 50
	case "M":
		rankScore += 25
	case "S":
		rankScore += 10
	}

	// Demand score (0-100)
	rankScore += b.Score

	// Price bonus (0-50) - LTD typically sells for 150-250k
	if b.LTDPrice > 0 {
		priceBonus := float64(b.LTDPrice) / 250000.0 * 50.0
		if priceBonus > 50 {
			priceBonus = 50
		}
		rankScore += priceBonus
	}

	// Industrial economy bonus (sustains acquisition)
	for _, econ := range b.Economies {
		if econ == "Industrial" {
			rankScore += 20
			break
		}
	}

	// Kaine progress bonus (0-80): prioritize systems we don't yet control
	rankScore += progressScoreBonus(b.KaineProgress)

	return rankScore
}
