package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/edin-space/edin-backend/internal/auth"
	"github.com/edin-space/edin-backend/internal/llm"
)

// Copilot chat session management endpoints.
//
// Deliberately separate file / separate handlers from kaine_chat.go even though
// the underlying llm.MultiSessionBackend is shared: the two products use
// different auth models (Authentik JWT for Kaine, Frontier commander JWT for
// Copilot), so their HTTP surfaces must not cross-contaminate. Anything the
// copilot exposes reads identity only from auth.ClaimsFromContext (set by the
// withCommanderAuth middleware) and never touches KaineUser.
//
// llmStore is keyed by a stable user identifier; for copilot that is the
// commander FID (same value used by the WS handler via user.FID). Sessions
// are therefore naturally partitioned per-commander and a request from one
// FID can never enumerate or activate another FID's sessions.

// handleCopilotChatSessions returns the list of chat sessions for the
// authenticated commander.
//
// GET /api/commander/chat/sessions
//
// Response: {"sessions": [...], "count": N}
// Must be mounted behind withCommanderAuth, and within the commander session
// cookie's Path scope (/api/commander) so browser callers receive the cookie.
func (s *Server) handleCopilotChatSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	claims, err := auth.ClaimsFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	ms, ok := s.llmStore.(llm.MultiSessionBackend)
	if !ok {
		// Single-session backend — return empty list so the picker UI can
		// render a valid but empty state instead of erroring.
		s.writeJSON(w, http.StatusOK, map[string]any{"sessions": []any{}, "count": 0})
		return
	}

	sessions, err := ms.ListUserSessions(claims.FID)
	if err != nil {
		s.logger.Error(fmt.Sprintf("copilot_list_sessions_error fid=%s", claims.FID), err)
		s.writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"sessions": sessions,
		"count":    len(sessions),
	})
}

// handleCopilotChatActivateSession sets a stored session as the active one for
// the authenticated commander. The next WebSocket connect will load this
// session's history.
//
// POST /api/commander/chat/sessions/{id}/activate
//
// Ownership is enforced by MultiSessionBackend.SetActiveSession — it will
// return an error if the target session_id does not belong to the caller's
// user ID, so a commander cannot pivot to another commander's conversation.
func (s *Server) handleCopilotChatActivateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	claims, err := auth.ClaimsFromContext(r.Context())
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Extract session ID from /api/commander/chat/sessions/{id}/activate.
	path := strings.TrimPrefix(r.URL.Path, "/api/commander/chat/sessions/")
	sessionID := strings.TrimSuffix(path, "/activate")
	if sessionID == "" || strings.Contains(sessionID, "/") {
		s.writeError(w, http.StatusBadRequest, "session ID required")
		return
	}

	ms, ok := s.llmStore.(llm.MultiSessionBackend)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "multi-session not available")
		return
	}

	if err := ms.SetActiveSession(claims.FID, sessionID); err != nil {
		// SetActiveSession rejects cross-user activations, so a caller trying
		// to hijack another commander's session lands here. Log at Warn rather
		// than Error because it is expected for adversarial input.
		s.logger.Warn(fmt.Sprintf("copilot_activate_session_denied fid=%s session=%s: %v", claims.FID, sessionID, err))
		s.writeError(w, http.StatusInternalServerError, "failed to activate session")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"status":     "ok",
		"session_id": sessionID,
	})
}
