package kaine

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ============================================================================
// EXPANSION TARGETS - Monthly Process: Finding strategic expansion opportunities
// See: eddn-listener/docs/kaine-directors-processes/orok-pseudocode.md
// ============================================================================

// ExpansionTarget represents an unoccupied system with mining potential.
type ExpansionTarget struct {
	// System info (from Memgraph)
	SystemName     string  `json:"system_name"`
	X              float64 `json:"x"`
	Y              float64 `json:"y"`
	Z              float64 `json:"z"`
	PowerplayState string  `json:"powerplay_state"` // Should be Unoccupied/null

	// Strategic location
	NearestAnchor      string  `json:"nearest_anchor"`       // Name of nearest Kaine Fort/Stronghold
	DistanceToAnchor   float64 `json:"distance_to_anchor"`   // Distance to nearest anchor
	NearestMap         string  `json:"nearest_map,omitempty"` // Name of nearest existing mining map system
	DistanceToMap      float64 `json:"distance_to_map"`       // Distance to nearest map
	LocationScore      int     `json:"location_score"`        // Strategic location points

	// Ring data (from Memgraph)
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
func (s *Store) FindExpansionTargets(ctx context.Context, memgraph MemgraphClient, progress ProgressFunc) (*ExpansionTargetsResponse, error) {
	if progress == nil {
		progress = func(int, int, string) {}
	}
	const searchRadius = 50.0 // Search up to 50 LY beyond Kaine anchors

	// Step 1: Get Kaine Fortified/Stronghold systems (anchors)
	progress(1, 4, "Fetching Kaine anchor systems from Memgraph")
	anchors, err := getKaineAnchors(ctx, memgraph)
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
	mapSystems, err := s.getMiningMapSystems(ctx)
	if err != nil {
		return nil, fmt.Errorf("get mining map systems: %w", err)
	}

	mapCoords, err := getSystemCoords(ctx, memgraph, mapSystems)
	if err != nil {
		return nil, fmt.Errorf("get map coords: %w", err)
	}

	// Step 3: Find unoccupied systems near anchors with valuable rings
	progress(3, 4, "Scanning for unoccupied systems with valuable rings within 50 Ly")
	targets, err := findUnoccupiedMiningTargets(ctx, memgraph, anchors, searchRadius)
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

// getMiningMapSystems returns the system names of all mining maps.
func (s *Store) getMiningMapSystems(ctx context.Context) ([]string, error) {
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
func findUnoccupiedMiningTargets(ctx context.Context, client MemgraphClient, anchors []anchorCoverage, searchRadius float64) ([]ExpansionTarget, error) {
	session := client.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Build list of anchor coords for the query
	anchorCoords := make([]map[string]any, len(anchors))
	for i, a := range anchors {
		anchorCoords[i] = map[string]any{"x": a.X, "y": a.Y, "z": a.Z, "radius": float64(a.Radius)}
	}

	query := `
		// Start from anchors and use spatial index to find nearby unoccupied systems
		UNWIND $anchors AS anchor
		WITH anchor,
		     point({x: anchor.x - $searchRadius, y: anchor.y - $searchRadius, z: anchor.z - $searchRadius}) AS lowerLeft,
		     point({x: anchor.x + $searchRadius, y: anchor.y + $searchRadius, z: anchor.z + $searchRadius}) AS upperRight

		MATCH (s:System)
		WHERE point.withinbbox(s.location, lowerLeft, upperRight)
		  AND (s.powerplay_state IS NULL OR s.powerplay_state IN ['Unoccupied', ''])
		  AND s.controlling_power IS NULL

		// Deduplicate systems found near multiple anchors
		WITH DISTINCT s

		// Must have at least one valuable ring (Metallic, Metal Rich, or Icy with LTD)
		MATCH (s)-[:HAS_BODY]->(b:Body)-[:HAS_RING]->(r:Ring)
		WHERE r.ring_class IN ['Metallic', 'Metal Rich', 'Icy']

		WITH s, collect(DISTINCT {
			body_name: b.name,
			ring_name: r.name,
			ring_class: r.ring_class,
			reserve_level: r.reserve_level,
			hotspots: r.hotspots,
			has_ltd: r.has_ltd
		}) AS rings

		RETURN
			s.name AS system_name,
			s.location.x AS x, s.location.y AS y, s.location.z AS z,
			s.powerplay_state AS powerplay_state,
			rings

		LIMIT 200
	`

	result, err := session.Run(ctx, query, map[string]any{
		"anchors":      anchorCoords,
		"searchRadius": searchRadius,
	})
	if err != nil {
		return nil, fmt.Errorf("query unoccupied targets: %w", err)
	}

	var targets []ExpansionTarget
	for result.Next(ctx) {
		record := result.Record()

		powerplayState := toString(record, "powerplay_state")
		if powerplayState == "" {
			powerplayState = "Unoccupied"
		}

		target := ExpansionTarget{
			SystemName:     toString(record, "system_name"),
			X:              toFloat64(getRecordValue(record, "x")),
			Y:              toFloat64(getRecordValue(record, "y")),
			Z:              toFloat64(getRecordValue(record, "z")),
			PowerplayState: powerplayState,
		}

		// Parse rings
		if ringsVal, ok := getRecordValue(record, "rings").([]any); ok {
			for _, rv := range ringsVal {
				if rm, ok := rv.(map[string]any); ok {
					ring := ExpansionRing{
						BodyName:     anyToString(rm["body_name"]),
						RingName:     anyToString(rm["ring_name"]),
						RingClass:    anyToString(rm["ring_class"]),
						ReserveLevel: anyToString(rm["reserve_level"]),
					}

					// Parse hotspots
					if hs, ok := rm["hotspots"].([]any); ok {
						for _, h := range hs {
							if hs, ok := h.(string); ok {
								ring.Hotspots = append(ring.Hotspots, hs)
							}
						}
					}

					// Check for LTD
					if hasLTD, ok := rm["has_ltd"].(bool); ok {
						ring.HasLTD = hasLTD
					}

					// Skip Icy rings without LTD (no value for our purposes)
					if ring.RingClass == "Icy" && !ring.HasLTD {
						continue
					}

					target.Rings = append(target.Rings, ring)
				}
			}
		}

		// Only include if we have valuable rings
		if len(target.Rings) > 0 {
			targets = append(targets, target)
		}
	}

	return targets, result.Err()
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

// anyToString converts any value to a string.
func anyToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
