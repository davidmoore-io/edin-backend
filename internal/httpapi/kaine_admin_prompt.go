package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// handleKaineAdminSystemPrompt dispatches GET (list) and POST (save) on
// /api/kaine/admin/system-prompt.
func (s *Server) handleKaineAdminSystemPrompt(w http.ResponseWriter, r *http.Request) {
	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listSystemPromptVersions(w, r)
	case http.MethodPost:
		s.saveSystemPromptVersion(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "only GET and POST allowed")
	}
}

// handleKaineAdminSystemPromptDefault handles GET /api/kaine/admin/system-prompt/default
// and returns the compiled-in embedded default prompt text.
func (s *Server) handleKaineAdminSystemPromptDefault(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{
		"content": s.cfg.LLM.KaineSystemPrompt,
	})
}

// handleKaineAdminSystemPromptByPath is a subtree handler that parses the ID (and
// optional "activate" sub-action) from the path segment after
// /api/kaine/admin/system-prompt/.
//
// Recognised patterns:
//
//	GET  /api/kaine/admin/system-prompt/{id}          → getSystemPromptVersion
//	POST /api/kaine/admin/system-prompt/{id}/activate → activateSystemPromptVersion
func (s *Server) handleKaineAdminSystemPromptByPath(w http.ResponseWriter, r *http.Request) {
	if s.kaineStore == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine store not configured")
		return
	}

	// Strip the prefix to get the remainder, e.g. "42" or "42/activate".
	remainder := strings.TrimPrefix(r.URL.Path, "/api/kaine/admin/system-prompt/")
	remainder = strings.Trim(remainder, "/")
	if remainder == "" {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}

	parts := strings.SplitN(remainder, "/", 2)
	idStr := parts[0]
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		s.writeError(w, http.StatusNotFound, "invalid system prompt version ID")
		return
	}

	// No sub-path → get version.
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
			return
		}
		s.getSystemPromptVersion(w, r, id)
		return
	}

	// Sub-path present — only "activate" is recognised.
	subPath := parts[1]
	if subPath != "activate" {
		s.writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}
	s.activateSystemPromptVersion(w, r, id)
}

// listSystemPromptVersions returns all versions (without content) plus the
// active version's content.
func (s *Server) listSystemPromptVersions(w http.ResponseWriter, r *http.Request) {
	versions, activeContent, err := s.kaineStore.ListSystemPromptVersions(r.Context())
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to list system prompt versions: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to list system prompt versions")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"versions":       versions,
		"active_content": activeContent,
	})
}

// saveSystemPromptVersion creates a new version and hot-reloads the runner.
func (s *Server) saveSystemPromptVersion(w http.ResponseWriter, r *http.Request) {
	if s.kaineRunner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine runner not configured")
		return
	}

	var body struct {
		Content string `json:"content"`
		Label   string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		s.writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	user := KaineUserFromContext(r.Context())
	createdBy := ""
	createdByName := ""
	if user != nil {
		createdBy = user.Sub
		createdByName = user.Name
		if createdByName == "" {
			createdByName = user.Username
		}
	}

	version, err := s.kaineStore.SaveSystemPromptVersion(r.Context(), body.Content, body.Label, createdBy, createdByName)
	if err != nil {
		s.logger.Warn(fmt.Sprintf("failed to save system prompt version: %v", err))
		s.writeError(w, http.StatusInternalServerError, "failed to save system prompt version")
		return
	}

	s.kaineRunner.SetSystemPrompt(version.Content)
	s.writeJSON(w, http.StatusCreated, version)
}

// getSystemPromptVersion retrieves a single version including its content.
func (s *Server) getSystemPromptVersion(w http.ResponseWriter, r *http.Request, id int) {
	version, err := s.kaineStore.GetSystemPromptVersion(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, http.StatusNotFound, "system prompt version not found")
			return
		}
		s.logger.Warn(fmt.Sprintf("failed to get system prompt version %d: %v", id, err))
		s.writeError(w, http.StatusInternalServerError, "failed to get system prompt version")
		return
	}
	s.writeJSON(w, http.StatusOK, version)
}

// activateSystemPromptVersion makes the specified version active and hot-reloads.
func (s *Server) activateSystemPromptVersion(w http.ResponseWriter, r *http.Request, id int) {
	if s.kaineRunner == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kaine runner not configured")
		return
	}

	version, err := s.kaineStore.ActivateSystemPromptVersion(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.writeError(w, http.StatusNotFound, "system prompt version not found")
			return
		}
		s.logger.Warn(fmt.Sprintf("failed to activate system prompt version %d: %v", id, err))
		s.writeError(w, http.StatusInternalServerError, "failed to activate system prompt version")
		return
	}

	s.kaineRunner.SetSystemPrompt(version.Content)
	s.writeJSON(w, http.StatusOK, version)
}
