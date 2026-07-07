// Package httpapi — kaine_watch.go
//
// GET /api/kaine/watcher/systems/{slug}
//
// The bot's /watch slash-command feature polls this endpoint every 120s for
// every system being watched in any channel. The response is intentionally
// narrow (powerplay + factions only, no bodies/stations) so:
//
//   - The state-hash the bot computes off the JSON is small and tight,
//     so an unrelated change (e.g. a new station scanned) doesn't trigger
//     a Discord edit.
//   - Bandwidth on a 50-watch channel × 720 polls/day stays trivial.
//
// Auth: same withKaineAuth middleware as the rest of /api/kaine/* — both
// the bot's bot:edin service identity AND human kaine commanders are
// allowed. The watcher is read-only so no withKaineEditor gate.

package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/edin-space/edin-backend/internal/galaxy"
	"github.com/edin-space/edin-backend/internal/galaxystore"
)

// handleSystemWatchSnapshot returns a SystemWatchSnapshot for the given slug.
// Returns 404 if the slug isn't in galaxy data, 503 if galaxy data is unavailable.
func (s *Server) handleSystemWatchSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}
	if s.galaxyStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "galaxy data unavailable")
		return
	}

	// Extract slug from path. We accept whatever the caller passed —
	// intentionally NOT re-slugifying because that would mask the case
	// where the bot's caller computed a different slug from the user's
	// input (a divergence we want to surface as 404 rather than silently
	// "fix").
	slug := strings.TrimPrefix(r.URL.Path, "/api/kaine/watcher/systems/")
	slug = strings.TrimSuffix(slug, "/")
	if slug == "" {
		s.writeError(w, http.StatusBadRequest, "slug required: /api/kaine/watcher/systems/{slug}")
		return
	}

	// Defensive: a slug must not contain whitespace by construction (see
	// galaxy.Slugify). If the caller sent a name-with-spaces by mistake,
	// reject early so the error is unambiguous rather than "system not
	// found in galaxy data".
	if slug != galaxy.Slugify(slug) {
		s.writeError(w, http.StatusBadRequest, "slug must not contain whitespace; see galaxy.Slugify")
		return
	}

	snap, err := s.galaxyStore.GetSystemWatchSnapshot(r.Context(), slug)
	if err != nil {
		if errors.Is(err, galaxystore.ErrSystemNotFound) {
			s.writeError(w, http.StatusNotFound, "no system with that slug in galaxy data")
			return
		}
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, snap)
}
