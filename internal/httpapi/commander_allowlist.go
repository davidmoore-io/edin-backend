package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Commander allowlist — first-cut access control for the copilot / ingest /
// query endpoints. A deployment that specifies cfg.CommanderAuth.AllowedFIDs
// refuses to mint an EDIN JWT for any Frontier commander whose FID is not on
// the list, and appends a JSON-lines record of the rejection to the
// configured log file.
//
// An empty AllowedFIDs slice means the allowlist is disabled — any
// Frontier-authenticated commander is accepted. This is the same open
// posture the backend had before the allowlist was introduced; deployments
// that want access control populate the list via the access_list Ansible
// role.
//
// This is deliberately a bare-bones implementation: file-backed, no admin
// UI, no per-role scopes. A later iteration will replace it with a DB-backed
// commander table + admin-managed roles; until then this file is the single
// choke point to reason about.

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

// fidAllowed reports whether fid may proceed past the callback. An empty
// allowlist disables the check entirely.
func fidAllowed(allowlist []string, fid string) bool {
	if len(allowlist) == 0 {
		return true
	}
	for _, a := range allowlist {
		if a == fid {
			return true
		}
	}
	return false
}

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

// enforceCommanderAllowlist returns true if the caller should proceed with
// JWT issuance, or false if the request has been rejected and an HTTP
// response has already been written. Callers must return immediately when
// this returns false.
//
// Centralising the check here means web and desktop callbacks deny
// identically and a future change (e.g. switching to a DB-backed list) lives
// in one place.
func (s *Server) enforceCommanderAllowlist(
	w http.ResponseWriter,
	r *http.Request,
	flow loginAttemptAuthFlow,
	fid string,
	commanderName string,
) bool {
	if fidAllowed(s.cfg.CommanderAuth.AllowedFIDs, fid) {
		return true
	}

	s.recordDeniedLogin(deniedLoginAttempt{
		Time:          time.Now().UTC(),
		FID:           fid,
		CommanderName: commanderName,
		Flow:          flow,
		IP:            clientIP(r),
		UserAgent:     r.UserAgent(),
		Reason:        "not_on_allowlist",
	})

	// 403 (not 401) — the OAuth exchange succeeded, we identified the caller,
	// and we're refusing service to that identity. 401 would incorrectly
	// suggest retrying the auth would help.
	s.writeError(w, http.StatusForbidden,
		"this commander is not currently permitted to use EDIN. Contact the administrator to request access.")
	return false
}
