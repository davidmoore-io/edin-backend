package httpapi

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// adminActionAttempt is the JSON shape appended to the admin-actions log.
// One line per attempt — grep-able and amenable to log shippers without a
// multi-line-aware parser. Mirrors deniedLoginAttempt's shape so the two
// audit streams are uniform.
type adminActionAttempt struct {
	Time       time.Time      `json:"time"`
	AdminSub   string         `json:"admin_sub"`            // Authentik subject (sub claim)
	AdminName  string         `json:"admin_name,omitempty"` // human-readable identifier (email or display name)
	Action     string         `json:"action"`               // e.g. "commander.grant"
	SubjectFID string         `json:"subject_fid"`
	Details    map[string]any `json:"details,omitempty"`
	IP         string         `json:"ip,omitempty"`
}

// adminActionLogMu serialises writes across goroutines. The file handle is
// opened + closed per append — admin actions are rare enough that keeping a
// long-lived handle isn't worth the lifecycle management, and per-write
// open lets external log-rotation tools rotate the file without the server
// keeping an inode reference to the old file. Mirrors loginAttemptLogMu.
var adminActionLogMu sync.Mutex

// recordAdminAction appends a JSON line to the admin-actions log file (if
// configured) and emits a structured server log entry. A failure to write
// the file is itself logged but never propagates — the action has already
// happened and the audit line is best-effort.
func (s *Server) recordAdminAction(attempt adminActionAttempt) {
	s.logger.Info(fmt.Sprintf(
		"commander_admin_action admin=%s action=%s subject_fid=%s",
		attempt.AdminSub, attempt.Action, attempt.SubjectFID,
	))

	path := ""
	if s.cfg != nil {
		path = s.cfg.CommanderAuth.AdminActionsLogPath
	}
	if path == "" {
		return
	}

	line, err := json.Marshal(attempt)
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_action: marshal action=%s subject_fid=%s",
			attempt.Action, attempt.SubjectFID), err)
		return
	}
	line = append(line, '\n')

	adminActionLogMu.Lock()
	defer adminActionLogMu.Unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_action: open %q", path), err)
		return
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		s.logger.Error(fmt.Sprintf("commander_admin_action: write %q", path), err)
	}
}
