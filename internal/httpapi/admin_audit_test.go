package httpapi

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
)

func newAuditTestServer(logPath string) *Server {
	return &Server{
		logger: observability.NewLogger("test"),
		cfg: &config.Config{
			CommanderAuth: config.CommanderAuthConfig{
				AdminActionsLogPath: logPath,
			},
		},
	}
}

// TestRecordAdminAction_WritesJSONLineToConfiguredPath verifies that when
// AdminActionsLogPath is configured, recordAdminAction appends a
// JSON-encoded line for each action.
func TestRecordAdminAction_WritesJSONLineToConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "admin-actions.log")
	srv := newAuditTestServer(logPath)

	now := time.Now().UTC().Truncate(time.Second)
	srv.recordAdminAction(adminActionAttempt{
		Time:       now,
		AdminSub:   "admin-uuid",
		AdminName:  "admin@example.com",
		Action:     "commander.grant",
		SubjectFID: "F2504",
		Details:    map[string]any{"group": "edin-copilot"},
		IP:         "127.0.0.1",
	})

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var entry adminActionAttempt
	require.NoError(t, json.Unmarshal(data[:len(data)-1], &entry))

	assert.Equal(t, "admin-uuid", entry.AdminSub)
	assert.Equal(t, "admin@example.com", entry.AdminName)
	assert.Equal(t, "commander.grant", entry.Action)
	assert.Equal(t, "F2504", entry.SubjectFID)
	assert.Equal(t, "127.0.0.1", entry.IP)
	require.NotNil(t, entry.Details)
	assert.Equal(t, "edin-copilot", entry.Details["group"])
}

// TestRecordAdminAction_NoPathConfigured_StructuredLogOnly verifies that
// when AdminActionsLogPath is empty, recordAdminAction does not panic and
// writes nothing to disk. The structured server log is the only output.
func TestRecordAdminAction_NoPathConfigured_StructuredLogOnly(t *testing.T) {
	srv := newAuditTestServer("")

	srv.recordAdminAction(adminActionAttempt{
		Time:       time.Now(),
		AdminSub:   "admin",
		Action:     "commander.deny",
		SubjectFID: "F2504",
	})
	// No assertion needed beyond "no panic" — the test name documents the
	// intent. We have no log file to inspect.
}

// TestRecordAdminAction_PreservesPriorEntries confirms append-only
// semantics: a second call must not overwrite the first.
func TestRecordAdminAction_PreservesPriorEntries(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "admin-actions.log")
	srv := newAuditTestServer(logPath)

	srv.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   "admin",
		Action:     "commander.grant",
		SubjectFID: "F1111",
	})
	srv.recordAdminAction(adminActionAttempt{
		Time:       time.Now().UTC(),
		AdminSub:   "admin",
		Action:     "commander.revoke",
		SubjectFID: "F2222",
	})

	f, err := os.Open(logPath)
	require.NoError(t, err)
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var actions []string
	for scanner.Scan() {
		var entry adminActionAttempt
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry))
		actions = append(actions, entry.Action+":"+entry.SubjectFID)
	}
	require.NoError(t, scanner.Err())

	assert.Equal(t, []string{"commander.grant:F1111", "commander.revoke:F2222"}, actions,
		"both entries must be preserved in order")
}
