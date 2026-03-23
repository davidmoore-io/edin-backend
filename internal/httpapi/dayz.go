package httpapi

import (
	"net/http"
	"strings"

	"github.com/edin-space/edin-backend/internal/dayz"
)

// DayZAPIResponse wraps all DayZ API responses.
type DayZAPIResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// handleDayZStatus returns the current DayZ server status.
// GET /api/dayz/status
func (s *Server) handleDayZStatus(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.dayz == nil {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   "DayZ service not configured",
		})
		return
	}

	status, err := s.dayz.GetServerStatus(r.Context())
	if err != nil {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: true,
			Data: map[string]any{
				"online":  false,
				"error":   err.Error(),
				"message": "Server offline or unreachable",
			},
		})
		return
	}

	s.writeJSON(w, http.StatusOK, DayZAPIResponse{
		Success: true,
		Data:    status,
	})
}

// handleDayZSpawns returns spawn data for the current map.
// GET /api/dayz/spawns
// Query params:
//   - events: comma-separated list of event types to include (optional)
//   - map: specific map name to query (optional, defaults to current)
func (s *Server) handleDayZSpawns(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.dayz == nil {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   "DayZ service not configured",
		})
		return
	}

	// Check for specific map request
	mapName := r.URL.Query().Get("map")

	var data *dayz.MapSpawnData
	var err error

	if mapName != "" {
		data, err = s.dayz.GetSpawnDataForMap(r.Context(), mapName)
	} else {
		data, err = s.dayz.GetSpawnData(r.Context())
	}

	if err != nil {
		s.logger.Error("dayz spawns error", err)
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	// Filter by event types if requested
	if eventsParam := r.URL.Query().Get("events"); eventsParam != "" {
		eventNames := strings.Split(eventsParam, ",")
		data = dayz.FilterEvents(data, eventNames)
	}

	s.writeJSON(w, http.StatusOK, DayZAPIResponse{
		Success: true,
		Data:    data,
	})
}

// handleDayZMapConfig returns map configuration (calibration, image URL, etc.).
// GET /api/dayz/map-config
// Query params:
//   - map: specific map name (optional, defaults to current)
func (s *Server) handleDayZMapConfig(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	mapName := r.URL.Query().Get("map")

	var cfg dayz.MapConfig
	var ok bool

	if mapName != "" {
		cfg, ok = dayz.GetMapConfig(mapName)
	} else if s.dayz != nil {
		var err error
		cfg, err = s.dayz.GetCurrentMapConfig(r.Context())
		ok = err == nil
	} else {
		// Default to sakhal
		cfg, ok = dayz.GetMapConfig("sakhal")
	}

	if !ok {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   "unknown map",
		})
		return
	}

	s.writeJSON(w, http.StatusOK, DayZAPIResponse{
		Success: true,
		Data:    cfg,
	})
}

// handleDayZCategories returns spawn categories for the UI.
// GET /api/dayz/categories
func (s *Server) handleDayZCategories(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	categories := dayz.DefaultCategories()
	colors := dayz.EventColors

	s.writeJSON(w, http.StatusOK, DayZAPIResponse{
		Success: true,
		Data: map[string]any{
			"categories": categories,
			"colors":     colors,
		},
	})
}

// handleDayZRefresh forces a cache refresh.
// POST /api/dayz/refresh
func (s *Server) handleDayZRefresh(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	if s.dayz == nil {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   "DayZ service not configured",
		})
		return
	}

	s.dayz.InvalidateCache()

	// Fetch fresh data
	data, err := s.dayz.GetSpawnData(r.Context())
	if err != nil {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	s.writeJSON(w, http.StatusOK, DayZAPIResponse{
		Success: true,
		Data: map[string]any{
			"message":      "Cache refreshed",
			"map":          data.MapName,
			"total_points": data.TotalPoints,
			"events":       len(data.Events),
			"cached_until": data.CachedUntil,
		},
	})
}

// handleDayZFull returns all data needed to render the map in one request.
// GET /api/dayz/full
// This combines status, spawns, map config, and categories.
func (s *Server) handleDayZFull(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	result := map[string]any{
		"categories": dayz.DefaultCategories(),
		"colors":     dayz.EventColors,
	}

	// Get server status
	var mapName string
	if s.dayz != nil {
		status, _ := s.dayz.GetServerStatus(r.Context())
		result["status"] = status
		if status != nil && status.Map != "" {
			mapName = status.Map
		}
	}

	// Get map config
	if mapName == "" {
		mapName = "sakhal"
	}
	cfg, _ := dayz.GetMapConfig(mapName)
	result["map_config"] = cfg

	// Get spawn data
	if s.dayz != nil {
		spawns, err := s.dayz.GetSpawnData(r.Context())
		if err != nil {
			result["spawns_error"] = err.Error()
		} else {
			result["spawns"] = spawns
		}
	}

	s.writeJSON(w, http.StatusOK, DayZAPIResponse{
		Success: true,
		Data:    result,
	})
}

// handleDayZEconomy returns economy statistics parsed from RPT logs.
// GET /api/dayz/economy
func (s *Server) handleDayZEconomy(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.ops == nil {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   "Operations manager not configured",
		})
		return
	}

	stats, err := s.ops.GetDayZEconomyStats(r.Context())
	if err != nil {
		s.logger.Error("dayz economy error", err)
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	s.writeJSON(w, http.StatusOK, stats)
}

// handleDayZItemSearch searches for item spawn configurations in types.xml.
// GET /api/dayz/items?search=<item_name>
// Returns matching items with their spawn settings (nominal, min, lifetime, etc.)
func (s *Server) handleDayZItemSearch(w http.ResponseWriter, r *http.Request) {
	s.applyCORSHeaders(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	search := r.URL.Query().Get("search")
	if search == "" {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   "search parameter required",
		})
		return
	}

	if s.ops == nil {
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   "Operations manager not configured",
		})
		return
	}

	result, err := s.ops.GetDayZItemInfo(r.Context(), search)
	if err != nil {
		s.logger.Error("dayz item search error", err)
		s.writeJSON(w, http.StatusOK, DayZAPIResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	// Return result directly (consistent with /dayz/economy endpoint)
	s.writeJSON(w, http.StatusOK, result)
}
