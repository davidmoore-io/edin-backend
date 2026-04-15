package httpapi

import (
	"net/http"

	"github.com/edin-space/edin-backend/internal/auth"
)

// handleAuthMe returns the authenticated commander's identity.
// Requires withCommanderAuth middleware.
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.ClaimsFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"fid":            claims.FID,
		"commander_name": claims.Name,
	})
}
