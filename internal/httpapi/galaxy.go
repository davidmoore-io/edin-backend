// Package httpapi provides HTTP handlers for the control API.
// This file implements the galaxy visualization API endpoints.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/edin-space/edin-backend/internal/memgraph"
)

// RegisterGalaxyRoutes registers the galaxy visualization API routes.
func (s *Server) RegisterGalaxyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/galaxy/view", s.handleGalaxyView)
	mux.HandleFunc("GET /api/galaxy/system/{systemId64}", s.handleSystemDetail)
	mux.HandleFunc("GET /api/galaxy/system/name/{systemName}", s.handleSystemDetailByName)
	mux.HandleFunc("GET /api/galaxy/search", s.handleGalaxySearch)
	mux.HandleFunc("GET /api/galaxy/stats", s.handleGalaxyStats)
}

// handleGalaxyView returns systems within a 3D bounding box.
func (s *Server) handleGalaxyView(w http.ResponseWriter, r *http.Request) {
	req, err := parseGalaxyViewRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validateBounds(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	start := time.Now()
	systems, totalCount, err := s.memgraph.GetSystemsInBounds(ctx, req)
	if err != nil {
		s.logger.Error(fmt.Sprintf("galaxy view query failed: %v", err), err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	resp := memgraph.GalaxyViewResponse{
		Systems:     systems,
		TotalCount:  totalCount,
		Truncated:   len(systems) < totalCount,
		QueryTimeMs: time.Since(start).Milliseconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Query-Time-Ms", strconv.FormatInt(resp.QueryTimeMs, 10))
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error(fmt.Sprintf("failed to encode galaxy view response: %v", err), err)
	}
}

// handleSystemDetail returns full system information by ID.
func (s *Server) handleSystemDetail(w http.ResponseWriter, r *http.Request) {
	systemIDStr := r.PathValue("systemId64")
	systemID, err := strconv.ParseInt(systemIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid system ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	detail, err := s.memgraph.GetSystemDetail(ctx, systemID)
	if err != nil {
		if errors.Is(err, memgraph.ErrSystemNotFound) {
			http.Error(w, "system not found", http.StatusNotFound)
			return
		}
		s.logger.Error(fmt.Sprintf("system detail query failed for id %d: %v", systemID, err), err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300") // 5 min cache
	if err := json.NewEncoder(w).Encode(detail); err != nil {
		s.logger.Error(fmt.Sprintf("failed to encode system detail response: %v", err), err)
	}
}

// handleSystemDetailByName returns full system information by name.
func (s *Server) handleSystemDetailByName(w http.ResponseWriter, r *http.Request) {
	systemName := r.PathValue("systemName")
	if systemName == "" {
		http.Error(w, "system name required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	detail, err := s.memgraph.GetSystemDetailByName(ctx, systemName)
	if err != nil {
		if errors.Is(err, memgraph.ErrSystemNotFound) {
			http.Error(w, "system not found", http.StatusNotFound)
			return
		}
		s.logger.Error(fmt.Sprintf("system detail query failed for %s: %v", systemName, err), err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if err := json.NewEncoder(w).Encode(detail); err != nil {
		s.logger.Error(fmt.Sprintf("failed to encode system detail response: %v", err), err)
	}
}

// handleGalaxySearch returns systems matching a name prefix.
func (s *Server) handleGalaxySearch(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("q")
	if prefix == "" {
		http.Error(w, "search query (q) required", http.StatusBadRequest)
		return
	}

	if len(prefix) < 2 {
		http.Error(w, "search query must be at least 2 characters", http.StatusBadRequest)
		return
	}

	limit := 10
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 50 {
			limit = l
		}
	}

	ctx := r.Context()

	systems, err := s.memgraph.SearchSystemsByPrefix(ctx, prefix, limit)
	if err != nil {
		s.logger.Error(fmt.Sprintf("galaxy search failed for prefix %s: %v", prefix, err), err)
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"systems": systems,
		"count":   len(systems),
	}); err != nil {
		s.logger.Error(fmt.Sprintf("failed to encode search response: %v", err), err)
	}
}

// handleGalaxyStats returns aggregate galaxy statistics.
func (s *Server) handleGalaxyStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	stats, err := s.memgraph.GetGalaxyViewStats(ctx)
	if err != nil {
		s.logger.Error(fmt.Sprintf("galaxy stats query failed: %v", err), err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60") // 1 min cache
	if err := json.NewEncoder(w).Encode(stats); err != nil {
		s.logger.Error(fmt.Sprintf("failed to encode stats response: %v", err), err)
	}
}

// parseGalaxyViewRequest extracts bounding box from query parameters.
func parseGalaxyViewRequest(r *http.Request) (memgraph.GalaxyViewRequest, error) {
	q := r.URL.Query()

	parseFloat := func(key string) (float64, error) {
		s := q.Get(key)
		if s == "" {
			return 0, fmt.Errorf("missing required parameter: %s", key)
		}
		return strconv.ParseFloat(s, 64)
	}

	minX, err := parseFloat("min_x")
	if err != nil {
		return memgraph.GalaxyViewRequest{}, err
	}
	maxX, err := parseFloat("max_x")
	if err != nil {
		return memgraph.GalaxyViewRequest{}, err
	}
	minY, err := parseFloat("min_y")
	if err != nil {
		return memgraph.GalaxyViewRequest{}, err
	}
	maxY, err := parseFloat("max_y")
	if err != nil {
		return memgraph.GalaxyViewRequest{}, err
	}
	minZ, err := parseFloat("min_z")
	if err != nil {
		return memgraph.GalaxyViewRequest{}, err
	}
	maxZ, err := parseFloat("max_z")
	if err != nil {
		return memgraph.GalaxyViewRequest{}, err
	}

	limit := 50000 // Default limit
	if limitStr := q.Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	// Cap limit to prevent memory issues
	if limit > 100000 {
		limit = 100000
	}

	return memgraph.GalaxyViewRequest{
		MinX:       minX,
		MaxX:       maxX,
		MinY:       minY,
		MaxY:       maxY,
		MinZ:       minZ,
		MaxZ:       maxZ,
		Limit:      limit,
		Power:      q.Get("power"),
		Allegiance: q.Get("allegiance"),
		State:      q.Get("state"),
	}, nil
}

// validateBounds ensures the bounding box is within acceptable limits.
func validateBounds(req memgraph.GalaxyViewRequest) error {
	// Prevent queries larger than 10,000 ly cube (performance guard)
	maxSize := 10000.0
	if req.MaxX-req.MinX > maxSize ||
		req.MaxY-req.MinY > maxSize ||
		req.MaxZ-req.MinZ > maxSize {
		return fmt.Errorf("bounding box exceeds maximum size of %.0f ly per dimension", maxSize)
	}

	// Ensure valid bounds
	if req.MinX > req.MaxX || req.MinY > req.MaxY || req.MinZ > req.MaxZ {
		return fmt.Errorf("invalid bounds: min must be less than max")
	}

	return nil
}
