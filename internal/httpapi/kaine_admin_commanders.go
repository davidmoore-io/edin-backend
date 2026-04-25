package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/edin-space/edin-backend/internal/authentik"
	"github.com/edin-space/edin-backend/internal/store"
)

// commanderAdminAuthentik is the subset of *authentik.Client used by the
// commander-admin handlers. Defining it as an interface keeps the handlers
// testable without httptest fixtures and mirrors the
// authentikUserGroupResolver seam used by resolveCommanderAccess. The
// production server passes its concrete *authentik.Client.
type commanderAdminAuthentik interface {
	GetUserByUUID(ctx context.Context, userUUID uuid.UUID) (*authentik.UserWithConnection, error)
	AddUserToGroup(ctx context.Context, userPK int, groupName string) error
	RemoveUserFromGroup(ctx context.Context, userPK int, groupName string) error
}

// managedCopilotGroups is the narrow allow-list of Authentik groups Grant
// can assign. Deliberately tighter than the existing /admin/users/{id}
// endpoint (which accepts any kaine-* or edin-* group): a slipped Grant on
// `kaine-admin` would be a privilege escalation.
var managedCopilotGroups = []string{"edin-copilot", "edin-copilot-trusted"}

// commanderAdminView is the JSON shape returned by list/get/mutation
// endpoints for a single commander row, including Authentik link state.
type commanderAdminView struct {
	FID                  string   `json:"fid"`
	CmdrName             string   `json:"cmdr_name"`
	Approved             bool     `json:"approved"`
	AuthentikUserID      *string  `json:"authentik_user_id"`
	AuthentikUserPresent *bool    `json:"authentik_user_present"`
	AuthentikUsername    string   `json:"authentik_username,omitempty"`
	Groups               []string `json:"groups,omitempty"`
	FirstSeenAt          string   `json:"first_seen_at"`
	LastSeenAt           string   `json:"last_seen_at"`
}

// handleKaineAdminCommandersSubtree dispatches the
// /api/kaine/admin/commanders/... subtree based on path + method. Mirrors
// the subtree pattern used by handleKaineAdminSystemPromptByPath (line
// ~330 of kaine.go).
func (s *Server) handleKaineAdminCommandersSubtree(w http.ResponseWriter, r *http.Request) {
	if s.commanderRepo == nil {
		s.writeError(w, http.StatusServiceUnavailable, "commander repository not configured")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/kaine/admin/commanders")
	path = strings.TrimSuffix(path, "/")

	// "" or "/" → list (GET only)
	if path == "" {
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
			return
		}
		s.handleKaineAdminCommandersList(w, r)
		return
	}

	// Strip leading "/" → "{fid}" or "{fid}/{action}"
	rest := strings.TrimPrefix(path, "/")
	parts := strings.Split(rest, "/")

	// Reject empty FID or path traversal (slashes inside FID would have
	// produced more parts; an empty first part is rejected here).
	fid := parts[0]
	if fid == "" {
		s.writeError(w, http.StatusBadRequest, "fid is required")
		return
	}
	// FIDs from Frontier are always uppercase F followed by digits — but
	// this handler only enforces "non-empty, no path-separator" because
	// admins might also need to manage legacy/imported rows.
	if strings.ContainsAny(fid, "/?#") {
		s.writeError(w, http.StatusBadRequest, "invalid fid")
		return
	}

	switch len(parts) {
	case 1:
		// /api/kaine/admin/commanders/{fid} — GET only
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
			return
		}
		s.handleKaineAdminCommanderGet(w, r, fid)
	case 2:
		// /api/kaine/admin/commanders/{fid}/{action} — POST only
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
			return
		}
		action := parts[1]
		switch action {
		case "grant":
			s.handleKaineAdminCommanderGrant(w, r, fid)
		case "revoke":
			s.handleKaineAdminCommanderRevoke(w, r, fid)
		case "link":
			s.handleKaineAdminCommanderLink(w, r, fid)
		case "unlink":
			s.handleKaineAdminCommanderUnlink(w, r, fid)
		case "approve":
			s.handleKaineAdminCommanderApprove(w, r, fid)
		case "deny":
			s.handleKaineAdminCommanderDeny(w, r, fid)
		default:
			s.writeError(w, http.StatusNotFound, "unknown action")
		}
	default:
		s.writeError(w, http.StatusNotFound, "unknown path")
	}
}

// adminAuthentik returns the Authentik admin seam. Production wires
// s.authentikClient. Tests can override s.adminAuthentikOverride.
func (s *Server) adminAuthentik() commanderAdminAuthentik {
	if s.adminAuthentikOverride != nil {
		return s.adminAuthentikOverride
	}
	if s.authentikClient == nil {
		return nil
	}
	return s.authentikClient
}

// handleKaineAdminCommandersList — GET /api/kaine/admin/commanders.
//
// Returns every commander row (admin-scope) with Authentik link state
// enriched per row. N+1 against Authentik is acceptable here: the list
// runs at human cadence and the population is small. Broken-link rows
// (Authentik 404) surface as authentik_user_present=false; transient
// errors leave authentik_user_present nil so the UI can flag them.
func (s *Server) handleKaineAdminCommandersList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.commanderRepo.ListAllCommanders(r.Context())
	if err != nil {
		s.logger.Error("commander_admin_list_failed", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list commanders")
		return
	}

	az := s.adminAuthentik()
	views := make([]commanderAdminView, 0, len(rows))
	for _, row := range rows {
		view := buildCommanderAdminViewBase(row)
		if row.AuthentikUserID != nil && *row.AuthentikUserID != uuid.Nil && az != nil {
			user, lookupErr := az.GetUserByUUID(r.Context(), *row.AuthentikUserID)
			switch {
			case lookupErr == nil:
				present := true
				view.AuthentikUserPresent = &present
				view.AuthentikUsername = user.Username
				// Groups intentionally omitted from the list view to keep
				// the payload bounded; Get-by-FID returns groups for a
				// single commander when the admin drills down.
			case errors.Is(lookupErr, authentik.ErrUserNotFound):
				present := false
				view.AuthentikUserPresent = &present
			default:
				s.logger.Warn(fmt.Sprintf(
					"commander_admin_list_authentik_lookup_failed fid=%s err=%v",
					row.FID, lookupErr))
				// authentik_user_present stays nil → UI knows it's unknown.
			}
		}
		views = append(views, view)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"commanders": views,
		"count":      len(views),
	})
}

// handleKaineAdminCommanderGet — GET /api/kaine/admin/commanders/{fid}.
//
// Includes the commander's Authentik group memberships. Transient
// Authentik errors return 503 here (this endpoint is for inspection so
// surfacing the failure is preferable to partial data). Authentik 404
// surfaces as authentik_user_present=false with an empty groups list.
func (s *Server) handleKaineAdminCommanderGet(w http.ResponseWriter, r *http.Request, fid string) {
	row, err := s.commanderRepo.GetCommanderAsAdmin(r.Context(), fid)
	if errors.Is(err, store.ErrCommanderNotFound) {
		s.writeError(w, http.StatusNotFound, "commander not found")
		return
	}
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_get_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to read commander")
		return
	}

	view := buildCommanderAdminViewBase(*row)

	if row.AuthentikUserID != nil && *row.AuthentikUserID != uuid.Nil {
		az := s.adminAuthentik()
		if az == nil {
			s.writeError(w, http.StatusServiceUnavailable, "authentik client not configured")
			return
		}
		user, lookupErr := az.GetUserByUUID(r.Context(), *row.AuthentikUserID)
		switch {
		case lookupErr == nil:
			present := true
			view.AuthentikUserPresent = &present
			view.AuthentikUsername = user.Username
			view.Groups = append([]string{}, user.GroupNames...)
		case errors.Is(lookupErr, authentik.ErrUserNotFound):
			present := false
			view.AuthentikUserPresent = &present
			view.Groups = []string{}
		default:
			s.logger.Warn(fmt.Sprintf(
				"commander_admin_get_authentik_lookup_failed fid=%s err=%v",
				fid, lookupErr))
			s.writeError(w, http.StatusServiceUnavailable, "authentik unreachable")
			return
		}
	}

	s.writeJSON(w, http.StatusOK, view)
}

// handleKaineAdminCommanderGrant — POST /api/kaine/admin/commanders/{fid}/grant.
// Body: {"group":"edin-copilot"|"edin-copilot-trusted"}.
//
// Bundled approve + group-add. Steps documented inline; partial-failure
// behaviour: if AddUserToGroup succeeds but SetApproved fails, the user
// is in the group but not yet approved (login still denied by
// awaiting_approval until the admin retries). The audit line records
// what happened.
func (s *Server) handleKaineAdminCommanderGrant(w http.ResponseWriter, r *http.Request, fid string) {
	if !s.requireFetchHeader(w, r) {
		return
	}

	var input struct {
		Group string `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !isManagedCopilotGroup(input.Group) {
		s.writeError(w, http.StatusBadRequest, "invalid_group")
		return
	}

	row, err := s.commanderRepo.GetCommanderAsAdmin(r.Context(), fid)
	if errors.Is(err, store.ErrCommanderNotFound) {
		s.writeError(w, http.StatusNotFound, "commander not found")
		return
	}
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_grant_get_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to read commander")
		return
	}
	if row.AuthentikUserID == nil || *row.AuthentikUserID == uuid.Nil {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "commander_not_linked"})
		return
	}

	az := s.adminAuthentik()
	if az == nil {
		s.writeError(w, http.StatusServiceUnavailable, "authentik client not configured")
		return
	}
	user, err := az.GetUserByUUID(r.Context(), *row.AuthentikUserID)
	if errors.Is(err, authentik.ErrUserNotFound) {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "authentik_user_missing"})
		return
	}
	if err != nil {
		s.logger.Warn(fmt.Sprintf("commander_admin_grant_authentik_lookup_failed fid=%s err=%v", fid, err))
		s.writeError(w, http.StatusServiceUnavailable, "authentik unreachable")
		return
	}

	if err := az.AddUserToGroup(r.Context(), user.PK, input.Group); err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_grant_add_group_failed fid=%s group=%s", fid, input.Group), err)
		s.writeError(w, http.StatusInternalServerError, "failed to add commander to group")
		return
	}

	if err := s.commanderRepo.SetApproved(r.Context(), fid, true); err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_grant_set_approved_failed fid=%s", fid), err)
		// Audit before returning — partial state happened.
		s.recordAdminAction(adminActionAttempt{
			Time:       time.Now().UTC(),
			AdminSub:   adminSub(r),
			AdminName:  adminName(r),
			Action:     "commander.grant.partial",
			SubjectFID: fid,
			Details:    map[string]any{"group": input.Group, "set_approved_failed": err.Error()},
			IP:         clientIP(r),
		})
		s.writeError(w, http.StatusInternalServerError, "failed to mark approved")
		return
	}

	s.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   adminSub(r),
		AdminName:  adminName(r),
		Action:     "commander.grant",
		SubjectFID: fid,
		Details:    map[string]any{"group": input.Group},
		IP:         clientIP(r),
	})

	s.writeUpdatedCommanderState(w, r, fid)
}

// handleKaineAdminCommanderRevoke — POST /api/kaine/admin/commanders/{fid}/revoke.
//
// Bundled approved=false + remove-managed-groups + revokeAllSessions.
// Idempotent: removing a group the user isn't in is a no-op. Authentik
// errors during group removal are logged but don't abort the flow — the
// approved column is the authoritative gate.
func (s *Server) handleKaineAdminCommanderRevoke(w http.ResponseWriter, r *http.Request, fid string) {
	if !s.requireFetchHeader(w, r) {
		return
	}

	row, err := s.commanderRepo.GetCommanderAsAdmin(r.Context(), fid)
	if errors.Is(err, store.ErrCommanderNotFound) {
		s.writeError(w, http.StatusNotFound, "commander not found")
		return
	}
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_revoke_get_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to read commander")
		return
	}

	if err := s.commanderRepo.SetApproved(r.Context(), fid, false); err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_revoke_set_approved_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to set approved=false")
		return
	}

	groupsRemoved := []string{}
	if row.AuthentikUserID != nil && *row.AuthentikUserID != uuid.Nil {
		az := s.adminAuthentik()
		if az != nil {
			user, lookupErr := az.GetUserByUUID(r.Context(), *row.AuthentikUserID)
			if lookupErr == nil {
				for _, g := range managedCopilotGroups {
					if removeErr := az.RemoveUserFromGroup(r.Context(), user.PK, g); removeErr != nil {
						s.logger.Warn(fmt.Sprintf(
							"commander_admin_revoke_remove_group_failed fid=%s group=%s err=%v",
							fid, g, removeErr))
						continue
					}
					groupsRemoved = append(groupsRemoved, g)
				}
			} else if !errors.Is(lookupErr, authentik.ErrUserNotFound) {
				s.logger.Warn(fmt.Sprintf(
					"commander_admin_revoke_authentik_lookup_failed fid=%s err=%v",
					fid, lookupErr))
			}
		}
	}

	if err := s.revokeAllSessions(r.Context(), fid); err != nil {
		s.logger.Warn(fmt.Sprintf("commander_admin_revoke_sessions_partial fid=%s err=%v", fid, err))
	}

	details := map[string]any{}
	if len(groupsRemoved) > 0 {
		details["groups_removed"] = groupsRemoved
	}
	s.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   adminSub(r),
		AdminName:  adminName(r),
		Action:     "commander.revoke",
		SubjectFID: fid,
		Details:    details,
		IP:         clientIP(r),
	})

	s.writeUpdatedCommanderState(w, r, fid)
}

// handleKaineAdminCommanderLink — POST /api/kaine/admin/commanders/{fid}/link.
// Body: {"authentik_user_id":"<uuid>"}.
//
// Break-glass: re-link a commander row to a different Authentik user.
// Calls revokeAllSessions because the scope-derivation source has
// changed and any live JWT was issued under the prior link.
func (s *Server) handleKaineAdminCommanderLink(w http.ResponseWriter, r *http.Request, fid string) {
	if !s.requireFetchHeader(w, r) {
		return
	}

	var input struct {
		AuthentikUserID string `json:"authentik_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	parsedUUID, err := uuid.Parse(input.AuthentikUserID)
	if err != nil || parsedUUID == uuid.Nil {
		s.writeError(w, http.StatusBadRequest, "authentik_user_id must be a valid UUID")
		return
	}

	az := s.adminAuthentik()
	if az == nil {
		s.writeError(w, http.StatusServiceUnavailable, "authentik client not configured")
		return
	}
	if _, err := az.GetUserByUUID(r.Context(), parsedUUID); err != nil {
		if errors.Is(err, authentik.ErrUserNotFound) {
			s.writeError(w, http.StatusBadRequest, "authentik_user_does_not_exist")
			return
		}
		s.logger.Warn(fmt.Sprintf("commander_admin_link_authentik_lookup_failed fid=%s err=%v", fid, err))
		s.writeError(w, http.StatusServiceUnavailable, "authentik unreachable")
		return
	}

	if err := s.commanderRepo.SetAuthentikLink(r.Context(), fid, &parsedUUID); err != nil {
		switch {
		case errors.Is(err, store.ErrCommanderNotFound):
			s.writeError(w, http.StatusNotFound, "commander not found")
			return
		case errors.Is(err, store.ErrAuthentikUserAlreadyLinked):
			conflictingFID := s.findConflictingFID(r.Context(), parsedUUID)
			s.writeJSON(w, http.StatusConflict, map[string]any{
				"error":           "authentik_user_already_linked",
				"conflicting_fid": conflictingFID,
			})
			return
		default:
			s.logger.Error(fmt.Sprintf("commander_admin_link_set_failed fid=%s", fid), err)
			s.writeError(w, http.StatusInternalServerError, "failed to set authentik link")
			return
		}
	}

	if err := s.revokeAllSessions(r.Context(), fid); err != nil {
		s.logger.Warn(fmt.Sprintf("commander_admin_link_sessions_partial fid=%s err=%v", fid, err))
	}

	s.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   adminSub(r),
		AdminName:  adminName(r),
		Action:     "commander.link",
		SubjectFID: fid,
		Details:    map[string]any{"authentik_user_id": parsedUUID.String()},
		IP:         clientIP(r),
	})

	s.writeUpdatedCommanderState(w, r, fid)
}

// handleKaineAdminCommanderUnlink — POST /api/kaine/admin/commanders/{fid}/unlink.
//
// Break-glass: clear the Authentik link, force approved=false, and
// revoke all live sessions so the commander cannot continue using a
// JWT minted under the old link. After unlink, the commander must
// re-authenticate (which will auto-link a new shadow user, per Task 5).
func (s *Server) handleKaineAdminCommanderUnlink(w http.ResponseWriter, r *http.Request, fid string) {
	if !s.requireFetchHeader(w, r) {
		return
	}

	if err := s.commanderRepo.SetAuthentikLink(r.Context(), fid, nil); err != nil {
		if errors.Is(err, store.ErrCommanderNotFound) {
			s.writeError(w, http.StatusNotFound, "commander not found")
			return
		}
		s.logger.Error(fmt.Sprintf("commander_admin_unlink_set_link_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to clear authentik link")
		return
	}
	if err := s.commanderRepo.SetApproved(r.Context(), fid, false); err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_unlink_set_approved_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to set approved=false")
		return
	}
	if err := s.revokeAllSessions(r.Context(), fid); err != nil {
		s.logger.Warn(fmt.Sprintf("commander_admin_unlink_sessions_partial fid=%s err=%v", fid, err))
	}

	s.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   adminSub(r),
		AdminName:  adminName(r),
		Action:     "commander.unlink",
		SubjectFID: fid,
		IP:         clientIP(r),
	})

	s.writeUpdatedCommanderState(w, r, fid)
}

// handleKaineAdminCommanderApprove — POST /api/kaine/admin/commanders/{fid}/approve.
//
// Low-level: flips approved=true only. Does NOT touch Authentik group
// state and does NOT revoke sessions (approval is a scope-addition;
// existing JWTs continue to work and pick up the change at next login
// when scopes are re-derived from groups).
func (s *Server) handleKaineAdminCommanderApprove(w http.ResponseWriter, r *http.Request, fid string) {
	if !s.requireFetchHeader(w, r) {
		return
	}

	if err := s.commanderRepo.SetApproved(r.Context(), fid, true); err != nil {
		if errors.Is(err, store.ErrCommanderNotFound) {
			s.writeError(w, http.StatusNotFound, "commander not found")
			return
		}
		s.logger.Error(fmt.Sprintf("commander_admin_approve_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to mark approved")
		return
	}

	s.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   adminSub(r),
		AdminName:  adminName(r),
		Action:     "commander.approve",
		SubjectFID: fid,
		IP:         clientIP(r),
	})

	s.writeUpdatedCommanderState(w, r, fid)
}

// handleKaineAdminCommanderDeny — POST /api/kaine/admin/commanders/{fid}/deny.
//
// Low-level: flips approved=false AND revokes all live sessions. Useful
// for "temporary lockout without losing group config" (Authentik group
// state is preserved, so a later Approve restores access without the
// admin having to remember which group they had).
func (s *Server) handleKaineAdminCommanderDeny(w http.ResponseWriter, r *http.Request, fid string) {
	if !s.requireFetchHeader(w, r) {
		return
	}

	if err := s.commanderRepo.SetApproved(r.Context(), fid, false); err != nil {
		if errors.Is(err, store.ErrCommanderNotFound) {
			s.writeError(w, http.StatusNotFound, "commander not found")
			return
		}
		s.logger.Error(fmt.Sprintf("commander_admin_deny_failed fid=%s", fid), err)
		s.writeError(w, http.StatusInternalServerError, "failed to set approved=false")
		return
	}

	if err := s.revokeAllSessions(r.Context(), fid); err != nil {
		s.logger.Warn(fmt.Sprintf("commander_admin_deny_sessions_partial fid=%s err=%v", fid, err))
	}

	s.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   adminSub(r),
		AdminName:  adminName(r),
		Action:     "commander.deny",
		SubjectFID: fid,
		IP:         clientIP(r),
	})

	s.writeUpdatedCommanderState(w, r, fid)
}

// writeUpdatedCommanderState re-reads the row + Authentik state and
// writes it as the 200 response. Mutating handlers call this at the end
// so the caller (admin UI) can refresh without an extra round trip.
func (s *Server) writeUpdatedCommanderState(w http.ResponseWriter, r *http.Request, fid string) {
	row, err := s.commanderRepo.GetCommanderAsAdmin(r.Context(), fid)
	if err != nil {
		// The mutation succeeded; surface the read failure as 200 with
		// minimal info rather than rolling anything back.
		s.writeJSON(w, http.StatusOK, map[string]any{"fid": fid})
		return
	}
	view := buildCommanderAdminViewBase(*row)
	if row.AuthentikUserID != nil && *row.AuthentikUserID != uuid.Nil {
		az := s.adminAuthentik()
		if az != nil {
			user, lookupErr := az.GetUserByUUID(r.Context(), *row.AuthentikUserID)
			switch {
			case lookupErr == nil:
				present := true
				view.AuthentikUserPresent = &present
				view.AuthentikUsername = user.Username
				view.Groups = append([]string{}, user.GroupNames...)
			case errors.Is(lookupErr, authentik.ErrUserNotFound):
				present := false
				view.AuthentikUserPresent = &present
				view.Groups = []string{}
			}
		}
	}
	s.writeJSON(w, http.StatusOK, view)
}

// findConflictingFID returns the FID currently linked to the given
// Authentik UUID, or "" if not found / the lookup fails. Used by the
// Link conflict response to help the admin reconcile. Implemented by
// scanning ListAllCommanders — adequate for admin-cadence calls.
func (s *Server) findConflictingFID(ctx context.Context, userUUID uuid.UUID) string {
	rows, err := s.commanderRepo.ListAllCommanders(ctx)
	if err != nil {
		return ""
	}
	for _, row := range rows {
		if row.AuthentikUserID != nil && *row.AuthentikUserID == userUUID {
			return row.FID
		}
	}
	return ""
}

// buildCommanderAdminViewBase builds the JSON shape from a row WITHOUT
// consulting Authentik. Callers fill in AuthentikUserPresent / Username
// / Groups as appropriate.
func buildCommanderAdminViewBase(row store.CommanderRow) commanderAdminView {
	view := commanderAdminView{
		FID:         row.FID,
		CmdrName:    row.CmdrName,
		Approved:    row.Approved,
		FirstSeenAt: row.FirstSeenAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		LastSeenAt:  row.LastSeenAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if row.AuthentikUserID != nil && *row.AuthentikUserID != uuid.Nil {
		s := row.AuthentikUserID.String()
		view.AuthentikUserID = &s
	}
	return view
}

// isManagedCopilotGroup is true for the narrow allow-list of groups the
// Grant endpoint accepts.
func isManagedCopilotGroup(g string) bool {
	for _, mg := range managedCopilotGroups {
		if mg == g {
			return true
		}
	}
	return false
}

// adminSub returns the Authentik subject (sub claim) of the requesting
// admin from the KaineUser context value, or "" if absent.
func adminSub(r *http.Request) string {
	if u := KaineUserFromContext(r.Context()); u != nil {
		return u.Sub
	}
	return ""
}

// adminName returns a human-friendly admin identifier for audit logs.
// Prefers the email claim (administrators are usually identified by
// email in the runbook); falls back to display name then preferred
// username. Returns "" when no claim is populated.
func adminName(r *http.Request) string {
	u := KaineUserFromContext(r.Context())
	if u == nil {
		return ""
	}
	if u.Email != "" {
		return u.Email
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}
