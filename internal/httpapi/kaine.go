package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/edin-space/edin-backend/internal/kaine"
	"github.com/xuri/excelize/v2"
)

// KaineUser represents an authenticated Kaine portal user from JWT claims.
type KaineUser struct {
	Sub      string   `json:"sub"`
	Name     string   `json:"name,omitempty"`
	Email    string   `json:"email,omitempty"`
	Username string   `json:"preferred_username,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

// contextKey is a type for context keys.
type kaineUserKey struct{}

// KaineUserFromContext retrieves the authenticated user from context.
func KaineUserFromContext(ctx context.Context) *KaineUser {
	user, _ := ctx.Value(kaineUserKey{}).(*KaineUser)
	return user
}

// HasRole checks if the user has a specific role (handles -test suffix).
func (u *KaineUser) HasRole(role string) bool {
	testRole := "kaine-" + role + "-test"
	prodRole := "kaine-" + role
	for _, g := range u.Groups {
		if g == testRole || g == prodRole {
			return true
		}
	}
	return false
}

// IsGodMode checks if user is in kaine-god group (super admin).
func (u *KaineUser) IsGodMode() bool {
	return u.HasRole("god")
}

// CanViewObjectives checks if user can view objectives.
func (u *KaineUser) CanViewObjectives() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("pledge") || u.HasRole("ops") ||
		u.HasRole("lead-ops") || u.HasRole("directors") || u.HasRole("objectives-editor")
}

// CanEditObjectives checks if user can create/edit objectives.
func (u *KaineUser) CanEditObjectives() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("directors") || u.HasRole("lead-ops") || u.HasRole("objectives-editor")
}

// CanViewOps checks if user can view ops-level objectives.
func (u *KaineUser) CanViewOps() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("directors") || u.HasRole("lead-ops") || u.HasRole("ops")
}

// CanViewPledge checks if user can view pledge-level objectives.
func (u *KaineUser) CanViewPledge() bool {
	return u.CanViewOps() || u.HasRole("pledge")
}

// CanAccessChat checks if user can use the galaxy chat feature.
func (u *KaineUser) CanAccessChat() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("chat") || u.HasRole("chat-debug")
}

// CanAccessChatDebug checks if user can see full debug info (thinking, tool params/results).
func (u *KaineUser) CanAccessChatDebug() bool {
	return u.IsGodMode() || u.HasRole("chat-debug")
}

// CanViewIntel checks if user can view system intel.
func (u *KaineUser) CanViewIntel() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("intel-viewer") || u.HasRole("intel-full") ||
		u.HasRole("ops") || u.HasRole("lead-ops") || u.HasRole("directors")
}

// CanViewFullIntel checks if user can view full system intel (events, traffic, software).
func (u *KaineUser) CanViewFullIntel() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("intel-full") || u.HasRole("lead-ops") || u.HasRole("directors")
}

// CanViewMiningMaps checks if user can view mining maps.
func (u *KaineUser) CanViewMiningMaps() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("mining-viewer") || u.HasRole("mining-editor") ||
		u.HasRole("pledge") || u.HasRole("ops") || u.HasRole("lead-ops") || u.HasRole("directors")
}

// CanEditMiningMaps checks if user can create/edit mining maps.
func (u *KaineUser) CanEditMiningMaps() bool {
	return u.IsGodMode() || u.HasRole("approved") || u.HasRole("directors") || u.HasRole("lead-ops") || u.HasRole("mining-editor")
}

// CanManageUsers checks if user can manage other users via admin interface.
func (u *KaineUser) CanManageUsers() bool {
	return u.IsGodMode()
}

// GetAccessLevels returns the access levels this user can view.
func (u *KaineUser) GetAccessLevels() []string {
	levels := []string{kaine.AccessPublic}
	if u.CanViewPledge() {
		levels = append(levels, kaine.AccessPledge)
	}
	if u.CanViewOps() {
		levels = append(levels, kaine.AccessOps)
	}
	return levels
}

// withKaineAuth validates the Authentik JWT token against the JWKS endpoint and extracts user info.
// This middleware cryptographically verifies the token signature, expiration, issuer, and audience.
func (s *Server) withKaineAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.jwtValidator == nil {
			s.logger.Error("kaine_auth: JWT validator not configured", nil)
			s.writeError(w, http.StatusServiceUnavailable, "authentication service unavailable")
			return
		}

		var token string

		// Check Authorization header first
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		} else {
			// Fall back to query parameter (for WebSocket connections)
			token = r.URL.Query().Get("token")
		}

		if token == "" {
			s.writeError(w, http.StatusUnauthorized, "missing or invalid authorization")
			return
		}

		// Validate JWT and extract user info
		user, err := s.jwtValidator.ValidateToken(token)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("kaine_auth: token validation failed: %v", err))
			s.writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}

		ctx := context.WithValue(r.Context(), kaineUserKey{}, user)
		next(w, r.WithContext(ctx))
	}
}

// withKaineEditor requires the user to have edit permissions.
func (s *Server) withKaineEditor(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := KaineUserFromContext(r.Context())
		if user == nil || !user.CanEditObjectives() {
			s.writeError(w, http.StatusForbidden, "requires director or lead-ops role")
			return
		}
		next(w, r)
	}
}

// withChatAccess requires the user to have chat access permissions.
func (s *Server) withChatAccess(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := KaineUserFromContext(r.Context())
		if user == nil || !user.CanAccessChat() {
			s.writeError(w, http.StatusForbidden, "requires chat access (kaine-chat or kaine-chat-debug group)")
			return
		}
		next(w, r)
	}
}

// withKaineAdmin requires the user to have admin permissions.
func (s *Server) withKaineAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := KaineUserFromContext(r.Context())
		if user == nil || !user.CanManageUsers() {
			s.writeError(w, http.StatusForbidden, "requires admin access (kaine-admin or kaine-directors group)")
			return
		}
		next(w, r)
	}
}

// RegisterKaineRoutes adds Kaine API routes to the mux.
func (s *Server) RegisterKaineRoutes(mux *http.ServeMux) {
	// Public (but auth required) endpoints
	mux.HandleFunc("/api/kaine/objectives", s.withKaineAuth(s.handleKaineObjectives))
	mux.HandleFunc("/api/kaine/objectives/counts", s.withKaineAuth(s.handleKaineObjectiveCounts))
	mux.HandleFunc("/api/kaine/objectives/", s.withKaineAuth(s.handleKaineObjectiveByID))

	// Editor-only endpoints
	mux.HandleFunc("/api/kaine/objectives/create", s.withKaineAuth(s.withKaineEditor(s.handleKaineCreateObjective)))

	// Mining maps (auth required, edit requires editor role)
	mux.HandleFunc("/api/kaine/mining-maps", s.withKaineAuth(s.handleKaineMiningMaps))
	mux.HandleFunc("/api/kaine/mining-maps/stats", s.withKaineAuth(s.handleKaineMiningMapStats))
	mux.HandleFunc("/api/kaine/mining-maps/create", s.withKaineAuth(s.withKaineEditor(s.handleKaineCreateMiningMap)))
	mux.HandleFunc("/api/kaine/mining-maps/import", s.withKaineAuth(s.handleKaineImportMiningMaps))
	mux.HandleFunc("/api/kaine/mining-maps/", s.withKaineAuth(s.handleKaineMiningMapByID))

	// Mining intelligence (directors only)
	mux.HandleFunc("/api/kaine/mining/plasmium-buyers", s.withKaineAuth(s.withKaineEditor(s.handlePlasmiumBuyers)))
	mux.HandleFunc("/api/kaine/mining/ltd-buyers", s.withKaineAuth(s.withKaineEditor(s.handleLTDBuyers)))
	mux.HandleFunc("/api/kaine/mining/expansion-targets", s.withKaineAuth(s.withKaineEditor(s.handleExpansionTargets)))
	mux.HandleFunc("/api/kaine/mining/survey-export", s.withKaineAuth(s.withKaineEditor(s.handleSurveyExport)))

	// System lookup (for autocomplete) - requires auth
	mux.HandleFunc("/api/kaine/systems/search", s.withKaineAuth(s.handleKaineSystemSearch))
	mux.HandleFunc("/api/kaine/systems/intel/", s.withKaineAuth(s.handleSystemIntel))
	mux.HandleFunc("/api/kaine/systems/", s.withKaineAuth(s.handleKaineSystemDetails))

	// EDDN event detail (for modal display) - requires auth
	mux.HandleFunc("/api/kaine/events/", s.withKaineAuth(s.handleEventDetail))

	// Market data endpoints
	mux.HandleFunc("/api/kaine/market/station", s.withKaineAuth(s.handleKaineStationMarket))
	mux.HandleFunc("/api/kaine/market/carrier", s.withKaineAuth(s.handleKaineCarrierMarket))

	// Unified search for @ mentions (systems + stations)
	mux.HandleFunc("/api/kaine/search", s.withKaineAuth(s.handleKaineSearch))

	// Chat WebSocket - requires chat access
	mux.HandleFunc("/api/kaine/chat/ws", s.withKaineAuth(s.withChatAccess(s.handleKaineChatWebSocket)))
	// Chat session management
	mux.HandleFunc("/api/kaine/chat/sessions", s.withKaineAuth(s.withChatAccess(s.handleKaineChatSessions)))
	mux.HandleFunc("/api/kaine/chat/sessions/", s.withKaineAuth(s.withChatAccess(s.handleKaineChatActivateSession)))

	// Admin endpoints - requires admin access
	mux.HandleFunc("/api/kaine/admin/users", s.withKaineAuth(s.withKaineAdmin(s.handleKaineAdminUsers)))
	mux.HandleFunc("/api/kaine/admin/users/", s.withKaineAuth(s.withKaineAdmin(s.handleKaineAdminUserByID)))
	mux.HandleFunc("/api/kaine/admin/groups", s.withKaineAuth(s.withKaineAdmin(s.handleKaineAdminGroups)))
}

// handleKaineObjectives handles GET /api/kaine/objectives - list objectives.
func (s *Server) handleKaineObjectives(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.kaineStore == nil {
		// Return demo data if store not configured
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"objectives":   []interface{}{},
			"access_level": "demo",
			"user":         user,
		})
		return
	}

	filter := kaine.ListObjectivesFilter{
		AccessLevels: user.GetAccessLevels(),
	}

	// Optional filters from query params
	if board := r.URL.Query().Get("board"); board != "" {
		filter.Board = board
	}
	if state := r.URL.Query().Get("state"); state != "" {
		filter.State = state
	}
	if objType := r.URL.Query().Get("type"); objType != "" {
		filter.ObjectiveType = objType
	}
	if category := r.URL.Query().Get("category"); category != "" {
		filter.Category = category
	}
	// Include archived objectives if requested (toggle)
	if r.URL.Query().Get("include_archived") == "true" {
		filter.IncludeArchived = true
	}

	objectives, err := s.kaineStore.ListObjectives(r.Context(), filter)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to list kaine objectives: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to list objectives")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"objectives":    objectives,
		"access_levels": user.GetAccessLevels(),
		"can_edit":      user.CanEditObjectives(),
	})
}

// handleKaineObjectiveCounts handles GET /api/kaine/objectives/counts - get objective counts by board/state.
func (s *Server) handleKaineObjectiveCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.kaineStore == nil {
		s.writeJSON(w, http.StatusOK, &kaine.ObjectiveCounts{
			ByBoard: make(map[string]int),
			ByState: make(map[string]map[string]int),
		})
		return
	}

	counts, err := s.kaineStore.GetObjectiveCounts(r.Context())
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to get objective counts: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to get counts")
		return
	}

	s.writeJSON(w, http.StatusOK, counts)
}

// handleKaineObjectiveByID handles GET/PUT/DELETE /api/kaine/objectives/{id}
// Also handles POST /api/kaine/objectives/{id}/state for state transitions
func (s *Server) handleKaineObjectiveByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path (may include /state suffix)
	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/objectives/")
	path = strings.TrimSuffix(path, "/")

	// Skip special endpoints handled elsewhere
	if path == "create" || path == "counts" {
		return
	}

	// Check for state transition endpoint: {id}/state
	var id string
	var isStateTransition bool
	if strings.HasSuffix(path, "/state") {
		id = strings.TrimSuffix(path, "/state")
		isStateTransition = true
	} else {
		id = path
	}

	if id == "" {
		s.writeError(w, http.StatusBadRequest, "objective ID required")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	// Handle state transition endpoint
	if isStateTransition {
		s.handleKaineObjectiveStateTransition(w, r, id, user)
		return
	}

	switch r.Method {
	case http.MethodGet:
		objective, err := s.kaineStore.GetObjective(r.Context(), id)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("failed to get kaine objective: %v", err))
			s.writeError(w, http.StatusInternalServerError, "failed to get objective")
			return
		}
		if objective == nil {
			s.writeError(w, http.StatusNotFound, "objective not found")
			return
		}

		// Check access level
		canView := false
		for _, level := range user.GetAccessLevels() {
			if level == objective.AccessLevel {
				canView = true
				break
			}
		}
		if !canView {
			s.writeError(w, http.StatusForbidden, "insufficient access level")
			return
		}

		s.writeJSON(w, http.StatusOK, objective)

	case http.MethodPut:
		if !user.CanEditObjectives() {
			s.writeError(w, http.StatusForbidden, "requires editor role")
			return
		}

		var input kaine.UpdateObjectiveInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		objective, err := s.kaineStore.UpdateObjective(r.Context(), id, input, user.Sub)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("failed to update kaine objective: %v", err))
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update objective: %v", err))
			return
		}

		s.writeJSON(w, http.StatusOK, objective)

	case http.MethodDelete:
		if !user.CanEditObjectives() {
			s.writeError(w, http.StatusForbidden, "requires editor role")
			return
		}

		if err := s.kaineStore.DeleteObjective(r.Context(), id, user.Sub); err != nil {
			s.logger.Warn(fmt.Sprintf("failed to delete kaine objective: %v", err))
			s.writeError(w, http.StatusInternalServerError, "failed to delete objective")
			return
		}

		s.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleKaineObjectiveStateTransition handles POST /api/kaine/objectives/{id}/state
// Validates and performs state transitions with proper error messages.
func (s *Server) handleKaineObjectiveStateTransition(w http.ResponseWriter, r *http.Request, id string, user *KaineUser) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed for state transitions")
		return
	}

	if !user.CanEditObjectives() {
		s.writeError(w, http.StatusForbidden, "requires editor role")
		return
	}

	// Parse request body
	var input struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if input.State == "" {
		s.writeError(w, http.StatusBadRequest, "state is required")
		return
	}

	// Perform the state transition (validates internally)
	objective, err := s.kaineStore.TransitionState(r.Context(), id, input.State, user.Sub)
	if err != nil {
		// Check if it's a validation error vs internal error
		if strings.Contains(err.Error(), "invalid state transition") || strings.Contains(err.Error(), "not found") {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.logger.Warn(fmt.Sprintf("failed to transition objective state: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to transition state")
		return
	}

	s.writeJSON(w, http.StatusOK, objective)
}

// handleKaineCreateObjective handles POST /api/kaine/objectives/create
func (s *Server) handleKaineCreateObjective(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	var input kaine.CreateObjectiveInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	userName := user.Name
	if userName == "" {
		userName = user.Username
	}

	objective, err := s.kaineStore.CreateObjective(r.Context(), input, user.Sub, userName)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to create kaine objective: %v", err))
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create objective: %v", err))
		return
	}

	s.writeJSON(w, http.StatusCreated, objective)
}

// handleKaineSystemSearch handles GET /api/kaine/systems/search?q=<query>
// Returns up to 10 matching systems for autocomplete.
func (s *Server) handleKaineSystemSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" || len(query) < 2 {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"systems": []interface{}{},
			"message": "query must be at least 2 characters",
		})
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	systems, err := s.memgraph.SearchSystems(r.Context(), query, 10)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("system search failed: %v", err))
		s.writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"systems": systems,
		"count":   len(systems),
	})
}

// handleKaineSearch handles GET /api/kaine/search?q=<query>&type=<system|station|all>
// Returns matching systems and/or stations for @ mention autocomplete.
func (s *Server) handleKaineSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" || len(query) < 2 {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"results": []interface{}{},
			"message": "query must be at least 2 characters",
		})
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	searchType := r.URL.Query().Get("type")
	if searchType == "" {
		searchType = "all"
	}

	type searchResult struct {
		Type       string `json:"type"`       // "system" or "station"
		Name       string `json:"name"`       // display name
		SystemName string `json:"systemName"` // for stations, the parent system
		Details    string `json:"details"`    // additional info (type, allegiance, etc.)
	}

	var results []searchResult

	// Search systems
	if searchType == "all" || searchType == "system" {
		systems, err := s.memgraph.SearchSystems(r.Context(), query, 5)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("system search failed: %v", err))
		} else {
			for _, sys := range systems {
				details := ""
				if sys.Allegiance != "" {
					details = sys.Allegiance
				}
				if sys.PowerplayState != "" {
					if details != "" {
						details += " · "
					}
					details += sys.PowerplayState
				}
				results = append(results, searchResult{
					Type:       "system",
					Name:       sys.Name,
					SystemName: sys.Name,
					Details:    details,
				})
			}
		}
	}

	// Search stations
	if searchType == "all" || searchType == "station" {
		stations, err := s.memgraph.SearchStations(r.Context(), query, 5)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("station search failed: %v", err))
		} else {
			for _, st := range stations {
				details := st.Type
				if st.MaxPad != "" {
					details += " · " + st.MaxPad + " pads"
				}
				results = append(results, searchResult{
					Type:       "station",
					Name:       st.Name,
					SystemName: st.SystemName,
					Details:    details,
				})
			}
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"count":   len(results),
	})
}

// handleKaineSystemDetails handles GET /api/kaine/systems/{name}
// Returns full details for a specific system.
func (s *Server) handleKaineSystemDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	// Extract system name from path
	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/systems/")
	systemName := strings.TrimSuffix(path, "/")

	if systemName == "" || systemName == "search" {
		s.writeError(w, http.StatusBadRequest, "system name required")
		return
	}

	// URL decode the system name
	decodedName, err := url.QueryUnescape(systemName)
	if err != nil {
		decodedName = systemName
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	// Check if full data is requested
	fullData := r.URL.Query().Get("full") == "true"

	if fullData {
		systemFull, err := s.memgraph.GetSystemFull(r.Context(), decodedName)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("system full lookup failed: %v", err))
			s.writeError(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		if systemFull == nil {
			s.writeError(w, http.StatusNotFound, "system not found")
			return
		}
		s.writeJSON(w, http.StatusOK, systemFull)
		return
	}

	system, err := s.memgraph.GetSystem(r.Context(), decodedName)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("system lookup failed: %v", err))
		s.writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	if system == nil {
		s.writeError(w, http.StatusNotFound, "system not found")
		return
	}

	s.writeJSON(w, http.StatusOK, system)
}

// handleKaineStationMarket handles GET /api/kaine/market/station?system=<name>&station=<name>
// Returns full market data for a station.
func (s *Server) handleKaineStationMarket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	systemName := r.URL.Query().Get("system")
	stationName := r.URL.Query().Get("station")

	if systemName == "" || stationName == "" {
		s.writeError(w, http.StatusBadRequest, "system and station parameters required")
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	market, err := s.memgraph.GetStationMarket(r.Context(), systemName, stationName)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("station market lookup failed: %v", err))
		s.writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	if market == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"station_name": stationName,
			"system_name":  systemName,
			"commodities":  []any{},
			"message":      "No market data available",
		})
		return
	}

	s.writeJSON(w, http.StatusOK, market)
}

// handleKaineCarrierMarket handles GET /api/kaine/market/carrier?id=<carrier_id>
// Returns full market data for a fleet carrier.
func (s *Server) handleKaineCarrierMarket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	carrierID := r.URL.Query().Get("id")
	if carrierID == "" {
		s.writeError(w, http.StatusBadRequest, "id parameter required")
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	market, err := s.memgraph.GetFleetCarrierMarket(r.Context(), carrierID)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("carrier market lookup failed: %v", err))
		s.writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	if market == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"carrier_id":  carrierID,
			"commodities": []any{},
			"message":     "No market data available",
		})
		return
	}

	s.writeJSON(w, http.StatusOK, market)
}

// ============================================================================
// Mining Maps Handlers
// ============================================================================

// handleKaineMiningMaps handles GET /api/kaine/mining-maps - list mining maps.
// Power state is fetched live from Memgraph (not stored in TimescaleDB).
func (s *Server) handleKaineMiningMaps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.kaineStore == nil {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"mining_maps": []interface{}{},
			"total":       0,
		})
		return
	}

	// Get filter params (power_state filter applied after Memgraph enrichment)
	powerStateFilter := r.URL.Query().Get("power_state")
	filter := kaine.ListMiningMapsFilter{
		SystemName:   r.URL.Query().Get("system"),
		RingType:     r.URL.Query().Get("ring_type"),
		ReserveLevel: r.URL.Query().Get("reserve_level"),
		Hotspot:      r.URL.Query().Get("hotspot"),
	}

	maps, err := s.kaineStore.ListMiningMaps(r.Context(), filter)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to list mining maps: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to list mining maps")
		return
	}

	// Enrich with live power state from Memgraph
	if s.memgraph != nil && len(maps) > 0 {
		systemNames := make([]string, len(maps))
		for i, m := range maps {
			systemNames[i] = m.SystemName
		}

		powerStates, err := s.memgraph.GetSystemPowerStates(r.Context(), systemNames)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("failed to get power states from memgraph: %v", err))
			// Continue without power state data rather than failing
		} else {
			for i := range maps {
				if state, ok := powerStates[maps[i].SystemName]; ok {
					maps[i].PowerState = state
				}
			}
		}
	}

	// Apply power state filter (after Memgraph enrichment)
	if powerStateFilter != "" {
		filtered := make([]kaine.MiningMap, 0)
		for _, m := range maps {
			if m.PowerState == powerStateFilter {
				filtered = append(filtered, m)
			}
		}
		maps = filtered
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"mining_maps": maps,
		"total":       len(maps),
		"can_edit":    user.CanEditObjectives(),
	})
}

// handleKaineMiningMapStats handles GET /api/kaine/mining-maps/stats
// Power state counts come from Memgraph (live data), ring type counts from TimescaleDB.
func (s *Server) handleKaineMiningMapStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.kaineStore == nil {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"total":          0,
			"by_power_state": map[string]int{},
			"by_ring_type":   map[string]int{},
		})
		return
	}

	// Get basic stats from TimescaleDB (total, by_ring_type)
	stats, err := s.kaineStore.GetMiningMapStats(r.Context())
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to get mining map stats: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to get stats")
		return
	}

	// Get power state counts from Memgraph (live data)
	if s.memgraph != nil {
		// Get all mining maps to enumerate system names
		maps, err := s.kaineStore.ListMiningMaps(r.Context(), kaine.ListMiningMapsFilter{})
		if err == nil && len(maps) > 0 {
			systemNames := make([]string, len(maps))
			for i, m := range maps {
				systemNames[i] = m.SystemName
			}

			powerStates, err := s.memgraph.GetSystemPowerStates(r.Context(), systemNames)
			if err == nil {
				byPowerState := make(map[string]int)
				for _, m := range maps {
					state := powerStates[m.SystemName]
					if state == "" {
						state = "Unknown"
					}
					byPowerState[state]++
				}
				stats["by_power_state"] = byPowerState
			}
		}
	}

	s.writeJSON(w, http.StatusOK, stats)
}

// handleKaineCreateMiningMap handles POST /api/kaine/mining-maps/create
func (s *Server) handleKaineCreateMiningMap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	var input kaine.CreateMiningMapInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	userName := user.Name
	if userName == "" {
		userName = user.Username
	}

	miningMap, err := s.kaineStore.CreateMiningMap(r.Context(), input, userName)
	if err != nil {
		// Check for duplicate error
		if strings.Contains(err.Error(), "already exists") {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.logger.Warn(fmt.Sprintf("failed to create mining map: %v", err))
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create mining map: %v", err))
		return
	}

	s.writeJSON(w, http.StatusCreated, miningMap)
}

// handleKaineImportMiningMaps handles POST /api/kaine/mining-maps/import
// Accepts a multipart/form-data upload with an .xlsx file, parses it,
// validates systems against Memgraph, and upserts each row.
func (s *Server) handleKaineImportMiningMaps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if !user.CanEditMiningMaps() {
		s.writeError(w, http.StatusForbidden, "requires mining editor role")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	// Parse multipart form (10MB max)
	const maxUploadSize = 10 << 20
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		s.writeError(w, http.StatusBadRequest, "file too large or invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".xlsx" {
		s.writeError(w, http.StatusBadRequest, "only .xlsx files are supported")
		return
	}

	// Parse XLSX
	f, err := excelize.OpenReader(file)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to parse XLSX: %v", err))
		return
	}
	defer f.Close()

	// Read first sheet
	sheetName := f.GetSheetName(0)
	if sheetName == "" {
		s.writeError(w, http.StatusBadRequest, "XLSX file has no sheets")
		return
	}

	rows, err := f.GetRows(sheetName)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read sheet: %v", err))
		return
	}

	if len(rows) < 2 {
		s.writeError(w, http.StatusBadRequest, "XLSX file must have a header row and at least one data row")
		return
	}

	// Map headers to column indices (case-insensitive)
	headerMap := make(map[string]int)
	for i, cell := range rows[0] {
		headerMap[strings.ToLower(strings.TrimSpace(cell))] = i
	}

	// Verify required headers exist
	systemCol, hasSystem := headerMap["system"]
	bodyCol, hasBody := headerMap["body"]
	if !hasSystem || !hasBody {
		s.writeError(w, http.StatusBadRequest, "XLSX must have 'System' and 'Body' columns")
		return
	}

	// Optional column indices
	colIdx := func(name string) int {
		if idx, ok := headerMap[name]; ok {
			return idx
		}
		return -1
	}
	ringTypeCol := colIdx("ring type")
	reserveCol := colIdx("reserve level")
	resSitesCol := colIdx("res sites")
	hotspotsCol := colIdx("hotspots")
	map1Col := colIdx("map 1 url")
	map1TitleCol := colIdx("map 1 title")
	map1CommodityCol := colIdx("map 1 commodities")
	map2Col := colIdx("map 2 url")
	map2TitleCol := colIdx("map 2 title")
	map2CommodityCol := colIdx("map 2 commodities")
	expansionCol := colIdx("expansion faction")
	notesCol := colIdx("notes")
	searchURLCol := colIdx("inara search")

	cellValue := func(row []string, col int) string {
		if col < 0 || col >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[col])
	}

	// parseCommodities splits a comma-separated list and maps labels to keys.
	// Returns resolved keys and warnings for unknown labels.
	parseCommodities := func(raw string) ([]string, []string) {
		if raw == "" {
			return nil, nil
		}
		var keys []string
		var warnings []string
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			lower := strings.ToLower(part)

			// Try as display label first
			if key, ok := kaine.CommodityLabelToKey[lower]; ok {
				keys = append(keys, key)
				continue
			}
			// Try as raw key
			if kaine.ValidCommodityKeys[lower] {
				keys = append(keys, lower)
				continue
			}
			warnings = append(warnings, fmt.Sprintf("unknown commodity: %s", part))
		}
		return keys, warnings
	}

	// Collect unique system names for batch Memgraph validation
	systemNames := make(map[string]bool)
	dataRows := rows[1:]
	for _, row := range dataRows {
		sysName := cellValue(row, systemCol)
		if sysName != "" {
			systemNames[sysName] = true
		}
	}

	// Batch validate against Memgraph
	validSystems := make(map[string]bool)
	var invalidSystems []string
	if s.memgraph != nil && len(systemNames) > 0 {
		nameList := make([]string, 0, len(systemNames))
		for name := range systemNames {
			nameList = append(nameList, name)
		}
		powerStates, err := s.memgraph.GetSystemPowerStates(r.Context(), nameList)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("import: memgraph validation failed: %v", err))
			// If Memgraph is down, allow all systems through with a warning
			for name := range systemNames {
				validSystems[name] = true
			}
		} else {
			for name := range systemNames {
				if _, ok := powerStates[name]; ok {
					validSystems[name] = true
				} else {
					invalidSystems = append(invalidSystems, name)
				}
			}
		}
	} else {
		// No Memgraph, allow all systems
		for name := range systemNames {
			validSystems[name] = true
		}
	}

	userName := user.Name
	if userName == "" {
		userName = user.Username
	}

	// Process each row
	resp := kaine.ImportMiningMapsResponse{
		TotalRows:      len(dataRows),
		Results:        make([]kaine.ImportMiningMapRowResult, 0, len(dataRows)),
		InvalidSystems: invalidSystems,
	}

	for i, row := range dataRows {
		rowNum := i + 2 // 1-indexed, skip header
		result := kaine.ImportMiningMapRowResult{Row: rowNum}

		sysName := cellValue(row, systemCol)
		body := cellValue(row, bodyCol)
		result.SystemName = sysName
		result.Body = body

		// Validate required fields
		if sysName == "" || body == "" {
			result.Action = "skipped"
			result.Error = "missing required System or Body"
			resp.Skipped++
			resp.Results = append(resp.Results, result)
			continue
		}

		// Validate system exists in Memgraph
		if !validSystems[sysName] {
			result.Action = "error"
			result.Error = "system not found in galaxy data"
			resp.Errors++
			resp.Results = append(resp.Results, result)
			continue
		}

		// Parse commodities
		hotspots, hWarnings := parseCommodities(cellValue(row, hotspotsCol))
		result.Warnings = append(result.Warnings, hWarnings...)

		map1Commodities, m1Warnings := parseCommodities(cellValue(row, map1CommodityCol))
		result.Warnings = append(result.Warnings, m1Warnings...)

		map2Commodities, m2Warnings := parseCommodities(cellValue(row, map2CommodityCol))
		result.Warnings = append(result.Warnings, m2Warnings...)

		input := kaine.CreateMiningMapInput{
			SystemName:       sysName,
			Body:             body,
			RingType:         cellValue(row, ringTypeCol),
			ReserveLevel:     cellValue(row, reserveCol),
			RESSites:         cellValue(row, resSitesCol),
			Hotspots:         hotspots,
			Map1:             cellValue(row, map1Col),
			Map1Title:        cellValue(row, map1TitleCol),
			Map1Commodity:    map1Commodities,
			Map2:             cellValue(row, map2Col),
			Map2Title:        cellValue(row, map2TitleCol),
			Map2Commodity:    map2Commodities,
			SearchURL:        cellValue(row, searchURLCol),
			ExpansionFaction: cellValue(row, expansionCol),
			Notes:            cellValue(row, notesCol),
		}

		_, wasCreated, err := s.kaineStore.UpsertMiningMap(r.Context(), input, userName)
		if err != nil {
			result.Action = "error"
			result.Error = err.Error()
			resp.Errors++
			resp.Results = append(resp.Results, result)
			continue
		}

		if wasCreated {
			result.Action = "created"
			resp.Created++
		} else {
			result.Action = "updated"
			resp.Updated++
		}
		resp.Results = append(resp.Results, result)
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// handleKaineMiningMapByID handles GET/PUT/DELETE /api/kaine/mining-maps/{id}
func (s *Server) handleKaineMiningMapByID(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/mining-maps/")
	idStr := strings.TrimSuffix(path, "/")

	// Skip endpoints handled separately
	if idStr == "stats" || idStr == "create" || idStr == "import" {
		return
	}

	if idStr == "" {
		s.writeError(w, http.StatusBadRequest, "mining map ID required")
		return
	}

	// Parse ID
	var id int
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid mining map ID")
		return
	}

	user := KaineUserFromContext(r.Context())
	if user == nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		miningMap, err := s.kaineStore.GetMiningMap(r.Context(), id)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("failed to get mining map: %v", err))
			s.writeError(w, http.StatusInternalServerError, "failed to get mining map")
			return
		}
		if miningMap == nil {
			s.writeError(w, http.StatusNotFound, "mining map not found")
			return
		}
		s.writeJSON(w, http.StatusOK, miningMap)

	case http.MethodPut:
		if !user.CanEditObjectives() {
			s.writeError(w, http.StatusForbidden, "requires editor role")
			return
		}

		var input kaine.UpdateMiningMapInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		miningMap, err := s.kaineStore.UpdateMiningMap(r.Context(), id, input)
		if err != nil {
			if strings.Contains(err.Error(), "already exists") {
				s.writeError(w, http.StatusConflict, err.Error())
				return
			}
			s.logger.Warn(fmt.Sprintf("failed to update mining map: %v", err))
			s.writeError(w, http.StatusInternalServerError, "failed to update mining map")
			return
		}

		s.writeJSON(w, http.StatusOK, miningMap)

	case http.MethodDelete:
		if !user.CanEditObjectives() {
			s.writeError(w, http.StatusForbidden, "requires editor role")
			return
		}

		if err := s.kaineStore.DeleteMiningMap(r.Context(), id); err != nil {
			if strings.Contains(err.Error(), "not found") {
				s.writeError(w, http.StatusNotFound, "mining map not found")
				return
			}
			s.logger.Warn(fmt.Sprintf("failed to delete mining map: %v", err))
			s.writeError(w, http.StatusInternalServerError, "failed to delete mining map")
			return
		}

		s.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ============================================================================
// Mining Intelligence Handlers
// ============================================================================

// handlePlasmiumBuyers handles GET /api/kaine/mining/plasmium-buyers
// Returns Boom stations that buy Platinum/Osmium near Kaine mining maps.
// Requires director or lead-ops role.
func (s *Server) handlePlasmiumBuyers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	progress := s.sseProgressFunc(w, r)
	result, err := s.kaineStore.FindPlasmiumBuyers(r.Context(), s.memgraph, progress)
	if err != nil {
		s.logger.Error(fmt.Sprintf("failed to find plasmium buyers: %v", err), nil)
		if progress != nil {
			s.sseError(w, "failed to find plasmium buyers")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "failed to find plasmium buyers")
		return
	}

	if progress != nil {
		s.sseResult(w, result)
	} else {
		s.writeJSON(w, http.StatusOK, result)
	}
}

// handleLTDBuyers handles GET /api/kaine/mining/ltd-buyers
// Returns Expansion stations that buy LTDs near Kaine LTD mining maps.
// Requires director or lead-ops role.
func (s *Server) handleLTDBuyers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	progress := s.sseProgressFunc(w, r)
	result, err := s.kaineStore.FindLTDBuyers(r.Context(), s.memgraph, progress)
	if err != nil {
		s.logger.Error(fmt.Sprintf("failed to find LTD buyers: %v", err), nil)
		if progress != nil {
			s.sseError(w, "failed to find LTD buyers")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "failed to find LTD buyers")
		return
	}

	if progress != nil {
		s.sseResult(w, result)
	} else {
		s.writeJSON(w, http.StatusOK, result)
	}
}

// handleExpansionTargets handles GET /api/kaine/mining/expansion-targets
// Returns unoccupied systems with mining potential near Kaine space.
// Requires director or lead-ops role.
func (s *Server) handleExpansionTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	progress := s.sseProgressFunc(w, r)
	result, err := s.kaineStore.FindExpansionTargets(r.Context(), s.memgraph, progress)
	if err != nil {
		s.logger.Error(fmt.Sprintf("failed to find expansion targets: %v", err), nil)
		if progress != nil {
			s.sseError(w, "failed to find expansion targets")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "failed to find expansion targets")
		return
	}

	if progress != nil {
		s.sseResult(w, result)
	} else {
		s.writeJSON(w, http.StatusOK, result)
	}
}

// handleSurveyExport handles GET /api/kaine/mining/survey-export
// Returns ALL systems within range of each mining map for data coverage analysis.
// Requires editor role.
func (s *Server) handleSurveyExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	if s.memgraph == nil {
		s.writeError(w, http.StatusServiceUnavailable, "memgraph not configured")
		return
	}

	progress := s.sseProgressFunc(w, r)
	result, err := s.kaineStore.SurveyExport(r.Context(), s.memgraph, progress)
	if err != nil {
		s.logger.Error(fmt.Sprintf("failed to generate survey export: %v", err), nil)
		if progress != nil {
			s.sseError(w, "failed to generate survey export")
			return
		}
		s.writeError(w, http.StatusInternalServerError, "failed to generate survey export")
		return
	}

	if progress != nil {
		s.sseResult(w, result)
	} else {
		s.writeJSON(w, http.StatusOK, result)
	}
}

// ============================================================================
// Admin Handlers
// ============================================================================

// handleKaineAdminUsers handles GET /api/kaine/admin/users - list all users.
func (s *Server) handleKaineAdminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.authentikClient == nil {
		s.writeError(w, http.StatusServiceUnavailable, "authentik client not configured")
		return
	}

	users, err := s.authentikClient.ListUsers(r.Context())
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to list users: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"users": users,
		"count": len(users),
	})
}

// handleKaineAdminUserByID handles GET/POST/DELETE /api/kaine/admin/users/{id}
// POST with {"group": "name"} adds user to group
// DELETE with {"group": "name"} removes user from group
func (s *Server) handleKaineAdminUserByID(w http.ResponseWriter, r *http.Request) {
	// Extract user ID from path
	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/admin/users/")
	path = strings.TrimSuffix(path, "/")

	if path == "" {
		s.writeError(w, http.StatusBadRequest, "user ID required")
		return
	}

	// Parse user PK
	var userPK int
	if _, err := fmt.Sscanf(path, "%d", &userPK); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid user ID")
		return
	}

	if s.authentikClient == nil {
		s.writeError(w, http.StatusServiceUnavailable, "authentik client not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		user, err := s.authentikClient.GetUser(r.Context(), userPK)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("failed to get user: %v", err))
			s.writeError(w, http.StatusInternalServerError, "failed to get user")
			return
		}
		if user == nil {
			s.writeError(w, http.StatusNotFound, "user not found")
			return
		}
		s.writeJSON(w, http.StatusOK, user)

	case http.MethodPost:
		// Add user to group
		var input struct {
			Group string `json:"group"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if input.Group == "" {
			s.writeError(w, http.StatusBadRequest, "group name required")
			return
		}
		// Validate group is a kaine group
		if !strings.HasPrefix(input.Group, "kaine-") {
			s.writeError(w, http.StatusBadRequest, "can only manage kaine-* groups")
			return
		}

		if err := s.authentikClient.AddUserToGroup(r.Context(), userPK, input.Group); err != nil {
			s.logger.Warn(fmt.Sprintf("failed to add user to group: %v", err))
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to add user to group: %v", err))
			return
		}

		// Return updated user
		user, _ := s.authentikClient.GetUser(r.Context(), userPK)
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "added",
			"user":   user,
		})

	case http.MethodDelete:
		// Remove user from group
		var input struct {
			Group string `json:"group"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if input.Group == "" {
			s.writeError(w, http.StatusBadRequest, "group name required")
			return
		}
		// Validate group is a kaine group
		if !strings.HasPrefix(input.Group, "kaine-") {
			s.writeError(w, http.StatusBadRequest, "can only manage kaine-* groups")
			return
		}

		if err := s.authentikClient.RemoveUserFromGroup(r.Context(), userPK, input.Group); err != nil {
			s.logger.Warn(fmt.Sprintf("failed to remove user from group: %v", err))
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to remove user from group: %v", err))
			return
		}

		// Return updated user
		user, _ := s.authentikClient.GetUser(r.Context(), userPK)
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "removed",
			"user":   user,
		})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleKaineAdminGroups handles GET /api/kaine/admin/groups - list all kaine groups.
func (s *Server) handleKaineAdminGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	if s.authentikClient == nil {
		s.writeError(w, http.StatusServiceUnavailable, "authentik client not configured")
		return
	}

	groups, err := s.authentikClient.ListGroups(r.Context())
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to list groups: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to list groups")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"groups": groups,
		"count":  len(groups),
	})
}
