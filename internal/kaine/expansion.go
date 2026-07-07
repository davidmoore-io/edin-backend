package kaine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// ============================================================================
// EXPANSION TARGETS - Monthly Process: Finding strategic expansion opportunities
// See: eddn-listener/docs/kaine-directors-processes/orok-pseudocode.md
// ============================================================================

// ExpansionTarget represents an unoccupied system with mining potential.
type ExpansionTarget struct {
	// System info (from galaxy.*)
	SystemName     string  `json:"system_name"`
	X              float64 `json:"x"`
	Y              float64 `json:"y"`
	Z              float64 `json:"z"`
	PowerplayState string  `json:"powerplay_state"` // Should be Unoccupied/null

	// Strategic location
	NearestAnchor    string  `json:"nearest_anchor"`        // Name of nearest Kaine Fort/Stronghold
	DistanceToAnchor float64 `json:"distance_to_anchor"`    // Distance to nearest anchor
	NearestMap       string  `json:"nearest_map,omitempty"` // Name of nearest existing mining map system
	DistanceToMap    float64 `json:"distance_to_map"`       // Distance to nearest map
	LocationScore    int     `json:"location_score"`        // Strategic location points

	// Ring data (from galaxy.*)
	Rings []ExpansionRing `json:"rings,omitempty"`

	// Calculated
	TotalScore  int    `json:"total_score"`
	ScoreReason string `json:"score_reason"`
}

// ExpansionRing represents a ring in a potential expansion target.
type ExpansionRing struct {
	BodyName     string   `json:"body_name"`
	RingName     string   `json:"ring_name"`
	RingClass    string   `json:"ring_class"`    // Metallic, Metal Rich, Icy, Rocky
	ReserveLevel string   `json:"reserve_level"` // Pristine, Major, Common, Depleted
	Hotspots     []string `json:"hotspots,omitempty"`
	HasLTD       bool     `json:"has_ltd,omitempty"`
	RingScore    int      `json:"ring_score"`
}

// ExpansionTargetsResponse is the full API response.
type ExpansionTargetsResponse struct {
	Targets     []ExpansionTarget `json:"targets"`
	GeneratedAt time.Time         `json:"generated_at"`
	TotalFound  int               `json:"total_found"`
	SearchedLY  float64           `json:"searched_ly"` // How far beyond anchors we searched
}

// FindExpansionTargets finds unoccupied systems near Kaine space with mining potential.
// This implements Orok's Monthly Process for strategic expansion planning.
func (s *Store) FindExpansionTargets(ctx context.Context, galaxy GalaxyQuerier, progress ProgressFunc) (*ExpansionTargetsResponse, error) {
	if progress == nil {
		progress = func(int, int, string) {}
	}
	const searchRadius = 50.0 // Search up to 50 LY beyond Kaine anchors

	// Step 1: Get Kaine Fortified/Stronghold systems (anchors)
	progress(1, 4, "Fetching Kaine anchor systems from galaxy store")
	anchors, err := getKaineAnchors(ctx, galaxy)
	if err != nil {
		return nil, fmt.Errorf("get kaine anchors: %w", err)
	}

	if len(anchors) == 0 {
		return &ExpansionTargetsResponse{
			Targets:     []ExpansionTarget{},
			GeneratedAt: time.Now(),
			TotalFound:  0,
			SearchedLY:  searchRadius,
		}, nil
	}

	// Step 2: Get existing mining map systems (for proximity scoring)
	progress(2, 4, fmt.Sprintf("Loading mining map coordinates for %d anchor systems", len(anchors)))
	mapSystems, err := s.GetMiningMapSystems(ctx)
	if err != nil {
		return nil, fmt.Errorf("get mining map systems: %w", err)
	}

	mapCoords, err := getSystemCoords(ctx, galaxy, mapSystems)
	if err != nil {
		return nil, fmt.Errorf("get map coords: %w", err)
	}

	// Step 3: Find unoccupied systems near anchors with valuable rings
	progress(3, 4, "Scanning for unoccupied systems with valuable rings within 50 Ly")
	targets, err := findUnoccupiedMiningTargets(ctx, galaxy, anchors, searchRadius)
	if err != nil {
		return nil, fmt.Errorf("find unoccupied targets: %w", err)
	}

	// Step 4: Score each target
	progress(4, 4, fmt.Sprintf("Scoring %d expansion targets by location and ring quality", len(targets)))
	for i := range targets {
		// Find nearest anchor
		nearestAnchorDist := math.MaxFloat64
		nearestAnchorName := ""
		for _, anchor := range anchors {
			dist := distance3D(targets[i].X, targets[i].Y, targets[i].Z, anchor.X, anchor.Y, anchor.Z)
			if dist < nearestAnchorDist {
				nearestAnchorDist = dist
				nearestAnchorName = anchor.Name
			}
		}
		targets[i].NearestAnchor = nearestAnchorName
		targets[i].DistanceToAnchor = nearestAnchorDist

		// Find nearest existing map
		nearestMapDist := math.MaxFloat64
		nearestMapName := ""
		for name, coord := range mapCoords {
			dist := distance3D(targets[i].X, targets[i].Y, targets[i].Z, coord.X, coord.Y, coord.Z)
			if dist < nearestMapDist {
				nearestMapDist = dist
				nearestMapName = name
			}
		}
		targets[i].NearestMap = nearestMapName
		targets[i].DistanceToMap = nearestMapDist

		// Calculate strategic location score
		targets[i].LocationScore = calculateLocationScore(nearestMapDist)

		// Calculate ring scores
		ringScore := 0
		for j := range targets[i].Rings {
			targets[i].Rings[j].RingScore = calculateRingScore(&targets[i].Rings[j])
			ringScore += targets[i].Rings[j].RingScore
		}

		// Total score = location + rings
		targets[i].TotalScore = targets[i].LocationScore + ringScore
		targets[i].ScoreReason = fmt.Sprintf("Location: +%d (%.1f LY to nearest map), Rings: +%d",
			targets[i].LocationScore, nearestMapDist, ringScore)
	}

	// Sort by total score (highest first)
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].TotalScore != targets[j].TotalScore {
			return targets[i].TotalScore > targets[j].TotalScore
		}
		return targets[i].DistanceToAnchor < targets[j].DistanceToAnchor
	})

	// Limit to top 50 results
	if len(targets) > 50 {
		targets = targets[:50]
	}

	return &ExpansionTargetsResponse{
		Targets:     targets,
		GeneratedAt: time.Now(),
		TotalFound:  len(targets),
		SearchedLY:  searchRadius,
	}, nil
}

// GetMiningMapSystems returns the system names of all mining maps.
func (s *Store) GetMiningMapSystems(ctx context.Context) ([]string, error) {
	query := `SELECT DISTINCT system_name FROM kaine.mining_maps`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query mining map systems: %w", err)
	}
	defer rows.Close()

	var systems []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		systems = append(systems, name)
	}

	return systems, rows.Err()
}

// findUnoccupiedMiningTargets finds unoccupied systems with valuable rings near anchors.
func findUnoccupiedMiningTargets(ctx context.Context, galaxy GalaxyQuerier, anchors []anchorCoverage, searchRadius float64) ([]ExpansionTarget, error) {
	if len(anchors) == 0 {
		return []ExpansionTarget{}, nil
	}
	ax := make([]float64, len(anchors))
	ay := make([]float64, len(anchors))
	az := make([]float64, len(anchors))
	for i, a := range anchors {
		ax[i] = a.X
		ay[i] = a.Y
		az[i] = a.Z
	}
	query := `
WITH anchors AS (
	SELECT * FROM unnest($1::float8[], $2::float8[], $3::float8[]) AS a(x, y, z)
),
candidates AS (
	SELECT DISTINCT c.id64, COALESCE(sys.name, c.name) AS name, c.x::float8 AS x, c.y::float8 AS y, c.z::float8 AS z,
		COALESCE(sp.powerplay_state, '') AS powerplay_state
	FROM anchors a
	JOIN galaxy.system_catalog c
	  ON c.x IS NOT NULL
	 AND cube(ARRAY[c.x::float8, c.y::float8, c.z::float8])
	     <@ cube(
	        ARRAY[a.x - $4, a.y - $4, a.z - $4],
	        ARRAY[a.x + $4, a.y + $4, a.z + $4]
	     )
	LEFT JOIN galaxy.system sys ON sys.id64 = c.id64
	LEFT JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
	WHERE (sp.system_id64 IS NULL OR (sp.power_name IS NULL AND COALESCE(sp.powerplay_state, '') IN ('', 'Unoccupied')))
)
SELECT
	c.name,
	c.x, c.y, c.z,
	c.powerplay_state,
	jsonb_agg(DISTINCT jsonb_build_object(
		'body_name', b.name,
		'ring_name', r.name,
		'ring_class', r.ring_class,
		'reserve_level', COALESCE(r.reserve_level, ''),
		'hotspots', COALESCE(h.hotspots, '[]'::jsonb),
		'has_ltd', COALESCE(h.has_ltd, false)
	)) AS rings
FROM candidates c
JOIN galaxy.body b ON b.system_id64 = c.id64
JOIN galaxy.ring r ON r.system_id64 = b.system_id64 AND r.body_id = b.body_id
LEFT JOIN LATERAL (
	SELECT
		jsonb_agg(lower(comm.name) || ':' || rh.count ORDER BY comm.name) AS hotspots,
		bool_or(comm.name = 'lowtemperaturediamond') AS has_ltd
	FROM galaxy.ring_hotspot rh
	JOIN galaxy.commodity comm ON comm.commodity_id = rh.commodity_id
	WHERE rh.system_id64 = r.system_id64
	  AND rh.ring_name = r.name
) h ON true
WHERE r.ring_class IN ('Metallic', 'Metal Rich', 'Icy')
GROUP BY c.id64, c.name, c.x, c.y, c.z, c.powerplay_state
LIMIT 200
	`

	rows, err := galaxy.Query(ctx, query, ax, ay, az, searchRadius)
	if err != nil {
		return nil, fmt.Errorf("query unoccupied targets: %w", err)
	}
	defer rows.Close()

	var targets []ExpansionTarget
	for rows.Next() {
		var ringsJSON []byte
		target := ExpansionTarget{}
		if err := rows.Scan(
			&target.SystemName,
			&target.X, &target.Y, &target.Z,
			&target.PowerplayState,
			&ringsJSON,
		); err != nil {
			return nil, err
		}
		powerplayState := target.PowerplayState
		if powerplayState == "" {
			powerplayState = "Unoccupied"
		}
		target.PowerplayState = powerplayState

		// Parse rings
		var rings []struct {
			BodyName     string   `json:"body_name"`
			RingName     string   `json:"ring_name"`
			RingClass    string   `json:"ring_class"`
			ReserveLevel string   `json:"reserve_level"`
			Hotspots     []string `json:"hotspots"`
			HasLTD       bool     `json:"has_ltd"`
		}
		if err := json.Unmarshal(ringsJSON, &rings); err != nil {
			return nil, fmt.Errorf("parse target rings: %w", err)
		}
		for _, rm := range rings {
			ring := ExpansionRing{
				BodyName:     rm.BodyName,
				RingName:     rm.RingName,
				RingClass:    rm.RingClass,
				ReserveLevel: rm.ReserveLevel,
				Hotspots:     rm.Hotspots,
				HasLTD:       rm.HasLTD,
			}

			// Skip Icy rings without LTD (no value for our purposes)
			if ring.RingClass == "Icy" && !ring.HasLTD {
				continue
			}

			target.Rings = append(target.Rings, ring)
		}

		// Only include if we have valuable rings
		if len(target.Rings) > 0 {
			targets = append(targets, target)
		}
	}

	return targets, rows.Err()
}

// calculateLocationScore calculates strategic location points based on distance to nearest map.
// Proximity to existing maps is the PRIMARY factor.
func calculateLocationScore(distanceToMap float64) int {
	switch {
	case distanceToMap <= 20:
		return 100 // Excellent - near existing vector
	case distanceToMap <= 30:
		return 60 // Good - within Stronghold acquisition range
	case distanceToMap <= 50:
		return 20 // Marginal - could extend a vector
	default:
		return 0 // Isolated
	}
}

// calculateRingScore calculates points for a ring based on Orok's scoring.
// Ring quality is SECONDARY to strategic location.
func calculateRingScore(ring *ExpansionRing) int {
	score := 0

	// Base score by ring type
	switch ring.RingClass {
	case "Metallic":
		score += 30
	case "Metal Rich":
		score += 20
	case "Icy":
		if ring.HasLTD {
			score += 25
		} else {
			return 0 // Icy without LTD has no value
		}
	default:
		return 0
	}

	// Reserve level bonus (small - maps compensate for low yield)
	switch ring.ReserveLevel {
	case "Pristine":
		score += 15
	case "Major":
		score += 10
	case "Common":
		score += 5
		// Depleted: +0 but don't skip entirely
	}

	// Hotspot bonus
	if len(ring.Hotspots) > 0 {
		switch ring.RingClass {
		case "Metallic":
			score += 20
		case "Metal Rich":
			score += 15
		}
		// Icy LTD hotspot is already accounted for in the base score
	}

	return score
}
