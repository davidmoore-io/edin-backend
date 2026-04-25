package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/edin-space/edin-backend/internal/authentik"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/store"
	"github.com/google/uuid"
)

// Commander access — single decision point for "should this Frontier-
// authenticated commander get an EDIN JWT, and with what scopes?". Authority
// flows from the commander row + Authentik group membership; there is no
// env-var allowlist any more (retired in plan Task 12). Rejected logins
// append a JSON-lines record to the configured log file for audit.

// loginAttemptAuthFlow labels which of the two Frontier OAuth entry points
// the attempt came through (see commander_auth.go / commander_client_auth.go).
type loginAttemptAuthFlow string

const (
	loginFlowWeb     loginAttemptAuthFlow = "web"     // /api/commander/auth/callback
	loginFlowDesktop loginAttemptAuthFlow = "desktop" // /api/v1/auth/frontier/callback
)

// deniedLoginAttempt is the JSON shape appended to the attempt log. Keeping
// it on a single line per entry makes the file grep-able and amenable to log
// shippers without a multi-line-aware parser.
type deniedLoginAttempt struct {
	Time          time.Time            `json:"time"`
	FID           string               `json:"fid"`
	CommanderName string               `json:"commander_name,omitempty"`
	Flow          loginAttemptAuthFlow `json:"flow"`
	IP            string               `json:"ip,omitempty"`
	UserAgent     string               `json:"user_agent,omitempty"`
	Reason        string               `json:"reason"`
}

// loginAttemptLogMu serialises writes across goroutines. The file handle is
// opened + closed per append — denials are rare enough that keeping a long-
// lived handle isn't worth the lifecycle management, and per-write open lets
// external log-rotation tools rotate the file without the server keeping an
// inode reference to the old file.
var loginAttemptLogMu sync.Mutex

// recordDeniedLogin appends a JSON line to the login-attempt log file (if
// configured) and emits a structured server log entry. A failure to write
// the file is itself logged but never propagates — the 403 to the caller
// must land regardless of whether we could persist the audit line.
func (s *Server) recordDeniedLogin(attempt deniedLoginAttempt) {
	s.logger.Warn(fmt.Sprintf(
		"commander_login_denied fid=%s name=%q flow=%s ip=%s reason=%s",
		attempt.FID, attempt.CommanderName, attempt.Flow, attempt.IP, attempt.Reason,
	))

	path := s.cfg.CommanderAuth.LoginAttemptLogPath
	if path == "" {
		return
	}

	line, err := json.Marshal(attempt)
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_login_denied: marshal attempt fid=%s", attempt.FID), err)
		return
	}
	line = append(line, '\n')

	loginAttemptLogMu.Lock()
	defer loginAttemptLogMu.Unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_login_denied: open %q", path), err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		s.logger.Error(fmt.Sprintf("commander_login_denied: write %q", path), err)
	}
}

// authentikUserGroupResolver is the subset of the Authentik client used
// when deciding what scopes a commander gets. Defining it as an interface
// rather than depending on *authentik.Client directly lets resolveCommanderAccess
// be tested without httptest fixtures, and mirrors the existing
// createShadowUser function-typed seam in spirit (test substitution without
// a wider dependency surface).
type authentikUserGroupResolver interface {
	GetUserByUUID(ctx context.Context, userUUID uuid.UUID) (*authentik.UserWithConnection, error)
}

// commanderAccessDecision is the outcome of resolveCommanderAccess. Allowed
// callers receive Scopes (which flow into JWT issuance) and a Reason label
// (used in metrics). Denied callers receive a populated Denial which the
// caller passes to recordDeniedLogin before writing the 403 response.
//
// The split keeps resolveCommanderAccess HTTP-response-free: it is a pure
// decision function, easy to unit-test in isolation, and the caller owns
// the writer / audit pipeline.
type commanderAccessDecision struct {
	Allowed bool
	Scopes  []authz.Scope
	Reason  string              // labels used in metrics + denial log
	Denial  *deniedLoginAttempt // populated only when Allowed=false
}

// resolveCommanderAccess is the single decision-point for "should this
// Frontier-authenticated commander get a JWT, and with what scopes?"
//
// Decision matrix (post-Task 12 — env-var allowlist retired):
//
//	| commander row     | Authentik state                       | result                                  |
//	|-------------------|---------------------------------------|-----------------------------------------|
//	| linked + approved | groups → ScopesForGroups non-empty    | allowed, scopes=mapped, authentik_groups|
//	| linked + approved | groups → ScopesForGroups EMPTY        | denied, no_scopes_granted               |
//	| linked + approved | Authentik 404 (user deleted)          | denied, authentik_user_missing          |
//	| linked + approved | Authentik transient error             | denied, authentik_unreachable           |
//	| linked + !approved| —                                     | denied, awaiting_approval               |
//	| row absent        | —                                     | denied, not_on_allowlist                |
//
// Notes:
//   - Post-Task 5, every callback invokes ensureCommanderLink before this,
//     so the "row absent" branch should not occur in normal operation.
//     If it does (commanderRepo unwired or unexpected state), deny-closed
//     with reason=not_on_allowlist (retained log label; semantics narrowed
//     to "no Authentik link exists").
//   - s.authentikClient == nil (Authentik disabled in config) on a
//     linked+approved row is treated as authentik_unreachable: deny-closed
//     until the deployment is reconfigured.
//   - s.commanderRepo == nil mirrors the "row absent" branch (the callbacks
//     already guard their Upsert/link block on this same nil-check) and
//     denies-closed.
//   - All return paths increment edin_commander_access_decisions_total{reason}.
func (s *Server) resolveCommanderAccess(
	ctx context.Context,
	r *http.Request,
	flow loginAttemptAuthFlow,
	fid, name string,
) commanderAccessDecision {
	am := initEdinMetrics()

	denyWith := func(reason string) commanderAccessDecision {
		am.commanderAccessDecisionsTotal.WithLabelValues(reason).Inc()
		return commanderAccessDecision{
			Allowed: false,
			Reason:  reason,
			Denial: &deniedLoginAttempt{
				Time:          time.Now().UTC(),
				FID:           fid,
				CommanderName: name,
				Flow:          flow,
				IP:            clientIP(r),
				UserAgent:     r.UserAgent(),
				Reason:        reason,
			},
		}
	}
	allowWith := func(reason string, scopes []authz.Scope) commanderAccessDecision {
		am.commanderAccessDecisionsTotal.WithLabelValues(reason).Inc()
		return commanderAccessDecision{
			Allowed: true,
			Scopes:  scopes,
			Reason:  reason,
		}
	}

	// commanderRepo nil mirrors the "row absent" branch — the callbacks guard
	// the Upsert/link block on this same nil-check, so a deployment without
	// the repo wired never had a row to consult. Post-Task-12 there is no
	// allowlist fallback; deny-closed.
	if s.commanderRepo == nil {
		return denyWith("not_on_allowlist")
	}

	row, err := s.commanderRepo.GetCommanderAsAdmin(ctx, fid)
	switch {
	case errors.Is(err, store.ErrCommanderNotFound):
		// Task 5 guarantees a row at this point; reaching this branch means
		// auto-link failed silently. Deny-closed — there is no Authentik
		// link to consult.
		return denyWith("not_on_allowlist")
	case err != nil:
		// Repo failure that isn't ErrCommanderNotFound — treat as transient
		// and deny-closed. authentik_unreachable is the closest existing
		// label; a future task may add db_unreachable.
		return denyWith("authentik_unreachable")
	}

	// Row exists. Approved + linked → consult Authentik groups.
	if row.Approved && row.AuthentikUserID != nil && *row.AuthentikUserID != uuid.Nil {
		if s.authentikUserGroups == nil {
			// Authentik disabled in config but the row is approved+linked —
			// can't derive scopes, deny-closed.
			return denyWith("authentik_unreachable")
		}
		// 2-second timeout — the callback already paid for one round-trip
		// against Frontier; we want the access-decision lookup to be snappy
		// or fail loudly rather than block the user behind an Authentik hang.
		authentikCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		authentikStart := time.Now()
		user, err := s.authentikUserGroups.GetUserByUUID(authentikCtx, *row.AuthentikUserID)
		authentikLatency := time.Since(authentikStart).Seconds()
		switch {
		case errors.Is(err, authentik.ErrUserNotFound):
			if am.commanderAccessResolutionLatencySeconds != nil {
				am.commanderAccessResolutionLatencySeconds.WithLabelValues("not_found").Observe(authentikLatency)
			}
			return denyWith("authentik_user_missing")
		case err != nil:
			if am.commanderAccessResolutionLatencySeconds != nil {
				am.commanderAccessResolutionLatencySeconds.WithLabelValues("error").Observe(authentikLatency)
			}
			return denyWith("authentik_unreachable")
		}
		if am.commanderAccessResolutionLatencySeconds != nil {
			am.commanderAccessResolutionLatencySeconds.WithLabelValues("ok").Observe(authentikLatency)
		}
		scopes := authz.ScopesForGroups(user.GroupNames)
		if len(scopes) == 0 {
			return denyWith("no_scopes_granted")
		}
		return allowWith("authentik_groups", scopes)
	}

	// Row exists but is NOT approved (or not linked yet).
	// Post-Task-12: no allowlist fallback — admin must Grant via the
	// Kaine AdminPage Commanders tab.
	return denyWith("awaiting_approval")
}
