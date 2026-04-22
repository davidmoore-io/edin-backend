package httpapi

import (
	"net/http"

	"github.com/edin-space/edin-backend/internal/auth"
)

func (s *Server) handleIngestStats(w http.ResponseWriter, r *http.Request) {
	if s.commanderRepo == nil {
		s.writeError(w, http.StatusServiceUnavailable, "ingest not available")
		return
	}

	claims, err := auth.ClaimsFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	stats, err := s.commanderRepo.GetEventStats(r.Context(), claims.FID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to query stats")
		return
	}

	s.writeJSON(w, http.StatusOK, stats)
}
