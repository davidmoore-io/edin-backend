package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/edin-space/edin-backend/internal/security"
	"github.com/edin-space/edin-backend/internal/store"
)

// ingestFIDRateLimiter provides per-FID rate limiting for ingest endpoints.
// Uses the same TokenBucket from internal/security as the IP limiter.
type ingestFIDRateLimiter struct {
	buckets sync.Map
}

func newIngestFIDRateLimiter() *ingestFIDRateLimiter {
	return &ingestFIDRateLimiter{}
}

// Allow returns true if the FID is within its rate limit (100 events/min).
func (r *ingestFIDRateLimiter) Allow(fid string) bool {
	bucket, _ := r.buckets.LoadOrStore(fid, security.NewTokenBucket(100, time.Minute))
	return bucket.(*security.TokenBucket).Allow()
}

// ingestEventPayload is the wire format for a single journal event from the client.
type ingestEventPayload struct {
	Timestamp     string          `json:"timestamp"`
	Event         string          `json:"event"`
	FID           string          `json:"fid"`
	CommanderName string          `json:"commander_name"`
	EventData     json.RawMessage `json:"event_data"`
}

// ingestSingleRequest is the body for POST /api/v1/ingest/event.
type ingestSingleRequest struct {
	Event ingestEventPayload `json:"event"`
}

// ingestBatchRequest is the body for POST /api/v1/ingest/events.
type ingestBatchRequest struct {
	Events []ingestEventPayload `json:"events"`
}

// ingestResponse is returned by both ingest endpoints.
type ingestResponse struct {
	EventsWritten     int `json:"events_written"`
	EventsDuplicated  int `json:"events_duplicated"`
}

const (
	ingestMaxBodyBytes = 2 * 1024 * 1024 // 2 MB
	ingestMaxBatch     = 500
	ingestMaxAgeBack   = 365 * 24 * time.Hour
	ingestMaxAgeFwd    = 5 * time.Minute
)

// validateIngestEvent checks the timestamp window and event type allowlist.
// Returns a human-readable error string or "" if valid.
func validateIngestEvent(ev *ingestEventPayload, now time.Time) string {
	if !AllowedEDEventTypes[ev.Event] {
		return fmt.Sprintf("unknown event type: %q", ev.Event)
	}
	ts, err := time.Parse(time.RFC3339, ev.Timestamp)
	if err != nil {
		// Also try the ED journal format: "2023-11-15T21:00:00Z"
		ts, err = time.Parse("2006-01-02T15:04:05Z", ev.Timestamp)
		if err != nil {
			return fmt.Sprintf("invalid timestamp: %q", ev.Timestamp)
		}
	}
	if ts.Before(now.Add(-ingestMaxAgeBack)) {
		return fmt.Sprintf("timestamp too old: %s", ev.Timestamp)
	}
	if ts.After(now.Add(ingestMaxAgeFwd)) {
		return fmt.Sprintf("timestamp too far in the future: %s", ev.Timestamp)
	}
	return ""
}

// parseTimestamp parses an ED journal timestamp (RFC3339 or bare UTC format).
func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05Z", s)
}

// handleIngestSingle handles POST /api/v1/ingest/event.
// Requires withCommanderAuth middleware.
func (s *Server) handleIngestSingle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	m := initEdinMetrics()
	start := time.Now()
	const endpoint = "single"

	r.Body = http.MaxBytesReader(w, r.Body, ingestMaxBodyBytes)

	var req ingestSingleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Extract authoritative FID from JWT context.
	fid, err := fidFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Warn if body FID disagrees with JWT FID, but always use JWT FID.
	if req.Event.FID != "" && req.Event.FID != fid {
		s.logger.Warn(fmt.Sprintf("ingest: body FID %q does not match JWT FID %q — using JWT FID", req.Event.FID, fid))
	}

	fh := fidHash(fid)

	now := time.Now()
	if errMsg := validateIngestEvent(&req.Event, now); errMsg != "" {
		m.ingestEventsTotal.WithLabelValues("rejected", fh).Inc()
		m.ingestLatencySeconds.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
		s.writeError(w, http.StatusBadRequest, errMsg)
		return
	}

	if !s.ingestRateLimiter.Allow(fid) {
		s.writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	ts, _ := parseTimestamp(req.Event.Timestamp)
	ev := store.JournalEvent{
		FID:       fid,
		Timestamp: ts,
		EventType: req.Event.Event,
		EventData: req.Event.EventData,
	}

	inserted, duplicated, err := s.commanderRepo.InsertEvents(r.Context(), fid, []store.JournalEvent{ev})
	if err != nil {
		s.logger.Error(fmt.Sprintf("ingest: failed to insert event fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to store event")
		return
	}

	// Record per-event outcomes.
	for i := 0; i < inserted; i++ {
		m.ingestEventsTotal.WithLabelValues("accepted", fh).Inc()
	}
	for i := 0; i < duplicated; i++ {
		m.ingestEventsTotal.WithLabelValues("duplicate", fh).Inc()
	}
	m.ingestLatencySeconds.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())

	s.writeJSON(w, http.StatusOK, ingestResponse{
		EventsWritten:    inserted,
		EventsDuplicated: duplicated,
	})
}

// handleIngestBatch handles POST /api/v1/ingest/events.
// Requires withCommanderAuth middleware.
func (s *Server) handleIngestBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	m := initEdinMetrics()
	start := time.Now()
	const endpoint = "batch"

	r.Body = http.MaxBytesReader(w, r.Body, ingestMaxBodyBytes)

	var req ingestBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if err.Error() == "http: request body too large" {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if len(req.Events) > ingestMaxBatch {
		s.writeError(w, http.StatusBadRequest,
			fmt.Sprintf("batch too large: %d events (max %d)", len(req.Events), ingestMaxBatch))
		return
	}

	// Extract authoritative FID from JWT context.
	fid, err := fidFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	fh := fidHash(fid)

	// Validate ALL events first (fail-fast — reject entire batch on any error).
	now := time.Now()
	for i, ev := range req.Events {
		if errMsg := validateIngestEvent(&ev, now); errMsg != "" {
			m.ingestEventsTotal.WithLabelValues("rejected", fh).Add(float64(len(req.Events)))
			m.ingestLatencySeconds.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
			s.writeError(w, http.StatusBadRequest,
				fmt.Sprintf("event[%d]: %s", i, errMsg))
			return
		}
	}

	if !s.ingestRateLimiter.Allow(fid) {
		s.writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	events := make([]store.JournalEvent, 0, len(req.Events))
	for _, ev := range req.Events {
		ts, _ := parseTimestamp(ev.Timestamp)
		events = append(events, store.JournalEvent{
			FID:       fid,
			Timestamp: ts,
			EventType: ev.Event,
			EventData: ev.EventData,
		})
	}

	inserted, duplicated, err := s.commanderRepo.InsertEvents(r.Context(), fid, events)
	if err != nil {
		s.logger.Error(fmt.Sprintf("ingest: failed to insert batch fid=%s count=%d", fid, len(events)), err)
		s.writeError(w, http.StatusInternalServerError, "failed to store events")
		return
	}

	// Record per-event outcomes.
	if inserted > 0 {
		m.ingestEventsTotal.WithLabelValues("accepted", fh).Add(float64(inserted))
	}
	if duplicated > 0 {
		m.ingestEventsTotal.WithLabelValues("duplicate", fh).Add(float64(duplicated))
	}
	m.ingestLatencySeconds.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())

	s.writeJSON(w, http.StatusOK, ingestResponse{
		EventsWritten:    inserted,
		EventsDuplicated: duplicated,
	})
}
