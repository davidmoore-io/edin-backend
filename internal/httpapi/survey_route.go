package httpapi

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// surveySystem represents a system with dockable stations for the survey route.
type surveySystem struct {
	Name       string          `json:"system"`
	X          float64         `json:"x"`
	Y          float64         `json:"y"`
	Z          float64         `json:"z"`
	LastUpdate *time.Time      `json:"last_update,omitempty"`
	Staleness  float64         `json:"staleness_hours"`
	Stations   []surveyStation `json:"stations"`

	// Set during routing
	Order              int     `json:"order"`
	DistFromPreviousLy float64 `json:"distance_from_previous_ly"`
}

type surveyStation struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	DistanceLS float64 `json:"distance_ls"`
}

type surveyRouteResponse struct {
	TotalCandidates    int            `json:"total_candidates"`
	Returned           int            `json:"returned"`
	MiningMapsUsed     int            `json:"mining_maps_used"`
	StartSystem        string         `json:"start_system,omitempty"`
	TotalRouteDistance float64         `json:"total_route_distance_ly"`
	Systems            []surveySystem `json:"systems"`
}

func (s *Server) handleSurveyRoute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	// Optional starting system — route begins here instead of stalest
	startSystem := r.URL.Query().Get("start")

	// Step 1: Get all mining map system names from EDIN TimescaleDB
	mapSystems, err := s.kaineStore.GetMiningMapSystems(ctx)
	if err != nil {
		s.logger.Error("survey_route: get mining maps", err)
		s.writeError(w, http.StatusInternalServerError, "failed to get mining maps")
		return
	}

	if len(mapSystems) == 0 {
		s.writeJSON(w, http.StatusOK, surveyRouteResponse{Systems: []surveySystem{}})
		return
	}

	// Step 2: Get coordinates and power states from Memgraph
	type mapAnchor struct {
		Name   string
		X, Y, Z float64
		Radius  int
	}

	session := s.memgraph.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	var anchors []mapAnchor
	for _, sysName := range mapSystems {
		result, err := session.Run(ctx, `
			MATCH (s:System {name: $name})
			WHERE s.location IS NOT NULL
			RETURN s.location.x AS x, s.location.y AS y, s.location.z AS z, s.powerplay_state AS pp_state
		`, map[string]any{"name": sysName})
		if err != nil {
			continue
		}
		if result.Next(ctx) {
			rec := result.Record()
			ppState, _ := rec.Get("pp_state")
			ppStr, _ := ppState.(string)

			radius := 0
			switch ppStr {
			case "Fortified":
				radius = 20
			case "Stronghold":
				radius = 30
			default:
				continue // Skip maps not in Fortified/Stronghold
			}

			x, _ := rec.Get("x")
			y, _ := rec.Get("y")
			z, _ := rec.Get("z")

			anchors = append(anchors, mapAnchor{
				Name:   sysName,
				X:      toFloat(x),
				Y:      toFloat(y),
				Z:      toFloat(z),
				Radius: radius,
			})
		}
	}

	if len(anchors) == 0 {
		s.writeJSON(w, http.StatusOK, surveyRouteResponse{MiningMapsUsed: len(mapSystems), Systems: []surveySystem{}})
		return
	}

	// Step 3: For each anchor, find systems with large-pad orbitals within radius
	seen := make(map[string]*surveySystem)

	for _, anchor := range anchors {
		result, err := session.Run(ctx, `
			MATCH (s:System)
			WHERE point.distance(s.location, point({x: $x, y: $y, z: $z})) <= $radius
			  AND s.population > 0
			WITH s
			MATCH (s)-[:HAS_STATION]->(st:Station)
			WHERE st.type IN ['Coriolis', 'Orbis', 'Ocellus', 'Dodec', 'Asteroidbase']
			  AND st.large_pads > 0
			RETURN s.name AS name, s.location.x AS x, s.location.y AS y, s.location.z AS z,
			  s.last_eddn_update AS last_update,
			  collect({name: st.name, type: st.type, distance_ls: st.distance_ls}) AS stations
		`, map[string]any{
			"x":      anchor.X,
			"y":      anchor.Y,
			"z":      anchor.Z,
			"radius": float64(anchor.Radius),
		})
		if err != nil {
			s.logger.Warn(fmt.Sprintf("survey_route: query failed for anchor %s: %v", anchor.Name, err))
			continue
		}

		for result.Next(ctx) {
			rec := result.Record()
			name, _ := rec.Get("name")
			nameStr, _ := name.(string)

			if _, exists := seen[nameStr]; exists {
				continue // Deduplicate
			}

			x, _ := rec.Get("x")
			y, _ := rec.Get("y")
			z, _ := rec.Get("z")
			lastUpdate, _ := rec.Get("last_update")
			stationsRaw, _ := rec.Get("stations")

			sys := &surveySystem{
				Name: nameStr,
				X:    toFloat(x),
				Y:    toFloat(y),
				Z:    toFloat(z),
			}

			// Parse last update
			if t := parseTime(lastUpdate); t != nil {
				sys.LastUpdate = t
				sys.Staleness = time.Since(*t).Hours()
			} else {
				sys.Staleness = 999999 // Never updated = maximum staleness
			}

			// Parse stations
			if stList, ok := stationsRaw.([]any); ok {
				for _, raw := range stList {
					if m, ok := raw.(map[string]any); ok {
						st := surveyStation{
							Name: fmt.Sprintf("%v", m["name"]),
							Type: fmt.Sprintf("%v", m["type"]),
						}
						if d, ok := m["distance_ls"].(float64); ok {
							st.DistanceLS = d
						}
						sys.Stations = append(sys.Stations, st)
					}
				}
				// Sort stations by distance from star (shortest supercruise first)
				sort.Slice(sys.Stations, func(i, j int) bool {
					return sys.Stations[i].DistanceLS < sys.Stations[j].DistanceLS
				})
			}

			seen[nameStr] = sys
		}
	}

	// Step 4: Collect, sort by staleness, take top N
	all := make([]surveySystem, 0, len(seen))
	for _, sys := range seen {
		all = append(all, *sys)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Staleness > all[j].Staleness // Most stale first
	})

	totalCandidates := len(all)
	if len(all) > limit {
		all = all[:limit]
	}

	// Step 5: If start system specified, look up its coordinates and prepend as route origin
	var startCoords *surveySystem
	if startSystem != "" {
		startResult, err := session.Run(ctx, `
			MATCH (s:System {name: $name})
			WHERE s.location IS NOT NULL
			RETURN s.location.x AS x, s.location.y AS y, s.location.z AS z
		`, map[string]any{"name": startSystem})
		if err != nil || !startResult.Next(ctx) {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("start system '%s' not found in galaxy database", startSystem))
			return
		}
		rec := startResult.Record()
		sx, _ := rec.Get("x")
		sy, _ := rec.Get("y")
		sz, _ := rec.Get("z")
		startCoords = &surveySystem{
			Name: startSystem,
			X:    toFloat(sx),
			Y:    toFloat(sy),
			Z:    toFloat(sz),
		}
	}

	// Step 6: TSP nearest-neighbour routing
	routed := nearestNeighbourRoute(all, startCoords)

	// Calculate total route distance
	totalDist := 0.0
	for _, sys := range routed {
		totalDist += sys.DistFromPreviousLy
	}

	s.writeJSON(w, http.StatusOK, surveyRouteResponse{
		TotalCandidates:    totalCandidates,
		Returned:           len(routed),
		StartSystem:        startSystem,
		MiningMapsUsed:     len(anchors),
		TotalRouteDistance: math.Round(totalDist*10) / 10,
		Systems:            routed,
	})
}

// nearestNeighbourRoute orders systems by greedy nearest-neighbour TSP.
// If startFrom is provided, the route starts from that position (not included in output).
func nearestNeighbourRoute(systems []surveySystem, startFrom *surveySystem) []surveySystem {
	if len(systems) <= 1 {
		if len(systems) == 1 {
			systems[0].Order = 1
			if startFrom != nil {
				systems[0].DistFromPreviousLy = math.Round(euclidean(*startFrom, systems[0])*10) / 10
			}
		}
		return systems
	}

	n := len(systems)
	visited := make([]bool, n)
	route := make([]surveySystem, 0, n)

	// Find the starting point — nearest to startFrom, or first (most stale)
	current := 0
	if startFrom != nil {
		bestDist := math.MaxFloat64
		for i := 0; i < n; i++ {
			d := euclidean(*startFrom, systems[i])
			if d < bestDist {
				bestDist = d
				current = i
			}
		}
		systems[current].DistFromPreviousLy = math.Round(bestDist*10) / 10
	}

	visited[current] = true
	systems[current].Order = 1
	route = append(route, systems[current])

	for step := 1; step < n; step++ {
		bestDist := math.MaxFloat64
		bestIdx := -1

		for i := 0; i < n; i++ {
			if visited[i] {
				continue
			}
			d := euclidean(systems[current], systems[i])
			if d < bestDist {
				bestDist = d
				bestIdx = i
			}
		}

		if bestIdx < 0 {
			break
		}

		visited[bestIdx] = true
		systems[bestIdx].Order = step + 1
		systems[bestIdx].DistFromPreviousLy = math.Round(bestDist*10) / 10
		route = append(route, systems[bestIdx])
		current = bestIdx
	}

	return route
}

func euclidean(a, b surveySystem) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	dz := a.Z - b.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func toFloat(v any) float64 {
	switch f := v.(type) {
	case float64:
		return f
	case int64:
		return float64(f)
	default:
		return 0
	}
}

func parseTime(v any) *time.Time {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case time.Time:
		return &t
	case string:
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return &parsed
		}
		if parsed, err := time.Parse("2006-01-02T15:04:05", t); err == nil {
			return &parsed
		}
	}
	return nil
}
