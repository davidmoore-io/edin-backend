package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	queryDefaultLimit = 50
	queryMaxLimit     = 500
)

// eventResponse is the wire format for a single journal event returned by the query API.
type eventResponse struct {
	Timestamp time.Time       `json:"timestamp"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
}

// locationResponse is the wire format for the current location endpoint.
type locationResponse struct {
	System    string    `json:"system"`
	Timestamp time.Time `json:"timestamp"`
}

// profileResponse is the wire format for the commander profile endpoint.
type profileResponse struct {
	FID      string    `json:"fid"`
	Name     string    `json:"name"`
	LastSeen time.Time `json:"last_seen"`
}

// handleCommanderEvents handles GET /api/v1/commander/events.
// Query params:
//   - type  (optional): ED event type string; when set, calls EventsByType
//   - limit (optional, default 50, max 500): number of events to return
//   - before (optional, RFC3339): upper-bound timestamp filter (requires type)
//
// Requires withCommanderAuth middleware.
func (s *Server) handleCommanderEvents(w http.ResponseWriter, r *http.Request) {
	fid, err := fidFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	q := r.URL.Query()

	// Parse limit.
	limit := queryDefaultLimit
	if raw := q.Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n <= 0 {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid limit: %q", raw))
			return
		}
		if n > queryMaxLimit {
			s.writeError(w, http.StatusBadRequest,
				fmt.Sprintf("limit %d exceeds maximum of %d", n, queryMaxLimit))
			return
		}
		limit = n
	}

	// Parse before.
	var before time.Time
	if raw := q.Get("before"); raw != "" {
		t, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid before timestamp: %q", raw))
			return
		}
		before = t
	}

	// Dispatch based on whether a type filter was requested.
	eventType := q.Get("type")

	var events []eventResponse

	if eventType != "" {
		// Filtered query: use EventsByType.
		until := before
		if until.IsZero() {
			until = time.Now().UTC()
		}
		rawEvents, queryErr := s.commanderRepo.EventsByType(r.Context(), fid, []string{eventType}, time.Time{}, until)
		if queryErr != nil {
			s.logger.Error(fmt.Sprintf("query events by type fid=%s type=%s", fid, eventType), queryErr)
			s.writeError(w, http.StatusInternalServerError, "failed to query events")
			return
		}
		// Apply limit in-process (EventsByType has no limit param).
		if len(rawEvents) > limit {
			rawEvents = rawEvents[:limit]
		}
		events = make([]eventResponse, 0, len(rawEvents))
		for _, ev := range rawEvents {
			events = append(events, eventResponse{
				Timestamp: ev.Timestamp,
				EventType: ev.EventType,
				Payload:   ev.EventData,
			})
		}
	} else {
		// No type filter: use RecentEvents.
		rawEvents, queryErr := s.commanderRepo.RecentEvents(r.Context(), fid, limit)
		if queryErr != nil {
			s.logger.Error(fmt.Sprintf("query recent events fid=%s", fid), queryErr)
			s.writeError(w, http.StatusInternalServerError, "failed to query events")
			return
		}
		events = make([]eventResponse, 0, len(rawEvents))
		for _, ev := range rawEvents {
			events = append(events, eventResponse{
				Timestamp: ev.Timestamp,
				EventType: ev.EventType,
				Payload:   ev.EventData,
			})
		}
	}

	// Always return an array, never null.
	if events == nil {
		events = []eventResponse{}
	}

	s.writeJSON(w, http.StatusOK, events)
}

// handleCommanderLocation handles GET /api/v1/commander/location.
// Returns the commander's most recent known system location.
// 404 if no location has been recorded yet.
//
// Requires withCommanderAuth middleware.
func (s *Server) handleCommanderLocation(w http.ResponseWriter, r *http.Request) {
	fid, err := fidFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	loc, err := s.commanderRepo.CurrentLocation(r.Context(), fid)
	if err != nil {
		s.logger.Error(fmt.Sprintf("query current location fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to query location")
		return
	}
	if loc == nil {
		s.writeError(w, http.StatusNotFound, "no location recorded")
		return
	}

	s.writeJSON(w, http.StatusOK, locationResponse{
		System:    loc.SystemName,
		Timestamp: loc.UpdatedAt,
	})
}

// handleCommanderProfile handles GET /api/v1/commander/profile.
// Returns the commander's profile (FID, name, last seen).
// 404 if the commander has not yet been registered (no UpsertCommander has run).
//
// Requires withCommanderAuth middleware.
func (s *Server) handleCommanderProfile(w http.ResponseWriter, r *http.Request) {
	fid, err := fidFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	row, err := s.commanderRepo.GetCommander(r.Context(), fid)
	if err != nil {
		s.logger.Error(fmt.Sprintf("query commander profile fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to query profile")
		return
	}
	if row == nil {
		s.writeError(w, http.StatusNotFound, "commander not found")
		return
	}

	s.writeJSON(w, http.StatusOK, profileResponse{
		FID:      row.FID,
		Name:     row.CmdrName,
		LastSeen: row.LastSeenAt,
	})
}
