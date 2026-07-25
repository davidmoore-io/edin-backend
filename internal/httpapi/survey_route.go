package httpapi

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/edin-space/edin-backend/internal/galaxystore"
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
	TotalRouteDistance float64        `json:"total_route_distance_ly"`
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

	if s.galaxyStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Galaxy database not configured")
		return
	}

	projection, err := s.galaxyStore.GetSurveyProjection(ctx, mapSystems, startSystem)
	if err != nil {
		if errors.Is(err, galaxystore.ErrSystemNotFound) ||
			errors.Is(err, galaxystore.ErrSurveyStartLookup) {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("start system '%s' not found in galaxy database", startSystem))
			return
		}
		s.logger.Error("survey_route: relational projection", err)
		s.writeError(w, http.StatusInternalServerError, "failed to query galaxy database")
		return
	}

	if projection.AnchorsUsed == 0 {
		s.writeJSON(w, http.StatusOK, surveyRouteResponse{MiningMapsUsed: len(mapSystems), Systems: []surveySystem{}})
		return
	}

	all := surveySystemsFromProjection(projection, time.Now())
	sort.Slice(all, func(i, j int) bool {
		return all[i].Staleness > all[j].Staleness // Most stale first
	})

	totalCandidates := len(all)
	if len(all) > limit {
		all = all[:limit]
	}

	var startCoords *surveySystem
	if projection.Start != nil {
		startCoords = &surveySystem{
			Name: startSystem,
			X:    projection.Start.X,
			Y:    projection.Start.Y,
			Z:    projection.Start.Z,
		}
	}

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
		MiningMapsUsed:     projection.AnchorsUsed,
		TotalRouteDistance: math.Round(totalDist*10) / 10,
		Systems:            routed,
	})
}

func surveySystemsFromProjection(projection *galaxystore.SurveyProjection, now time.Time) []surveySystem {
	all := make([]surveySystem, 0, len(projection.Candidates))
	for _, candidate := range projection.Candidates {
		sys := surveySystem{
			Name:       candidate.Name,
			X:          candidate.X,
			Y:          candidate.Y,
			Z:          candidate.Z,
			LastUpdate: candidate.LastUpdate,
			Stations:   make([]surveyStation, 0, len(candidate.Stations)),
		}
		if candidate.LastUpdate == nil {
			sys.Staleness = 999999
		} else {
			sys.Staleness = now.Sub(*candidate.LastUpdate).Hours()
		}
		for _, station := range candidate.Stations {
			sys.Stations = append(sys.Stations, surveyStation{
				Name:       station.Name,
				Type:       station.Type,
				DistanceLS: station.DistanceLS,
			})
		}
		all = append(all, sys)
	}
	return all
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
