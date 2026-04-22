package httpapi

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
)

func TestFIDAllowed_EmptyAllowlist_AllowsEveryone(t *testing.T) {
	// An empty allowlist is the "disabled" state. Any caller proceeds.
	assert.True(t, fidAllowed(nil, "F2504"))
	assert.True(t, fidAllowed([]string{}, "F9999"))
}

func TestFIDAllowed_Membership(t *testing.T) {
	list := []string{"F2504", "F123"}
	assert.True(t, fidAllowed(list, "F2504"))
	assert.True(t, fidAllowed(list, "F123"))
	assert.False(t, fidAllowed(list, "F9999"))
}

func TestFIDAllowed_CaseSensitive(t *testing.T) {
	// FIDs from Frontier are always uppercase "F" + digits. Treat case
	// mismatches as "not on list" so a deployment that accidentally writes
	// "f2504" in config doesn't quietly grant access to both forms.
	list := []string{"F2504"}
	assert.False(t, fidAllowed(list, "f2504"))
}

// TestEnforceCommanderAllowlist_AllowsKnownFID — happy path. No denial
// written, no 403 returned.
func TestEnforceCommanderAllowlist_AllowsKnownFID(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "attempts.log")
	srv := newAllowlistTestServer(t, []string{"F2504"}, logPath)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	ok := srv.enforceCommanderAllowlist(rr, req, loginFlowWeb, "F2504", "Pattern State")

	assert.True(t, ok)
	assert.Equal(t, http.StatusOK, rr.Code) // no response written
	assert.NoFileExists(t, logPath,
		"accepted login must not touch the denial log")
}

// TestEnforceCommanderAllowlist_DisabledWhenEmpty — legacy open posture.
func TestEnforceCommanderAllowlist_DisabledWhenEmpty(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "attempts.log")
	srv := newAllowlistTestServer(t, nil, logPath)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	ok := srv.enforceCommanderAllowlist(rr, req, loginFlowWeb, "F9999", "Random")

	assert.True(t, ok, "empty allowlist must not deny anyone")
	assert.NoFileExists(t, logPath)
}

// TestEnforceCommanderAllowlist_DeniedWritesLogAndReturns403 is the core
// denial path: a 403 is written, and the attempt lands in the log file as
// a single JSON line with identity + request metadata.
func TestEnforceCommanderAllowlist_DeniedWritesLogAndReturns403(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "attempts.log")
	srv := newAllowlistTestServer(t, []string{"F2504"}, logPath)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("User-Agent", "UnitTestUA/1.0")
	req.Header.Set("X-Forwarded-For", "198.51.100.7")

	ok := srv.enforceCommanderAllowlist(rr, req, loginFlowDesktop, "F9999", "CMDR Sneaky")
	assert.False(t, ok)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "not currently permitted")

	// Log file must exist with exactly one line of parseable JSON carrying
	// the full identity + request context.
	contents, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.NotEmpty(t, contents)

	var logged deniedLoginAttempt
	require.NoError(t, json.Unmarshal([]byte(strings.TrimRight(string(contents), "\n")), &logged))
	assert.Equal(t, "F9999", logged.FID)
	assert.Equal(t, "CMDR Sneaky", logged.CommanderName)
	assert.Equal(t, loginFlowDesktop, logged.Flow)
	assert.Equal(t, "198.51.100.7", logged.IP)
	assert.Equal(t, "UnitTestUA/1.0", logged.UserAgent)
	assert.Equal(t, "not_on_allowlist", logged.Reason)
	assert.False(t, logged.Time.IsZero())
}

// TestEnforceCommanderAllowlist_LogIsAppendOnly — two denials must produce
// two JSON lines, not overwrite one another.
func TestEnforceCommanderAllowlist_LogIsAppendOnly(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "attempts.log")
	srv := newAllowlistTestServer(t, []string{"F2504"}, logPath)

	for _, fid := range []string{"F100", "F200", "F300"} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/anything", nil)
		srv.enforceCommanderAllowlist(rr, req, loginFlowWeb, fid, "")
	}

	f, err := os.Open(logPath)
	require.NoError(t, err)
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	require.NoError(t, scanner.Err())
	require.Len(t, lines, 3, "each denial should produce exactly one line")

	for i, line := range lines {
		var entry deniedLoginAttempt
		require.NoErrorf(t, json.Unmarshal([]byte(line), &entry),
			"line %d must be valid JSON: %s", i, line)
	}
}

// TestEnforceCommanderAllowlist_NoLogPathDoesNotError — an operator who
// wants zero log-file state can leave the path empty; denials are still
// enforced and logged to the server logger.
func TestEnforceCommanderAllowlist_NoLogPathDoesNotError(t *testing.T) {
	srv := newAllowlistTestServer(t, []string{"F2504"}, "")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	ok := srv.enforceCommanderAllowlist(rr, req, loginFlowWeb, "F9999", "")
	assert.False(t, ok)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// TestEnforceCommanderAllowlist_UnwritableLogDoesNotBlockResponse — if the
// file can't be opened (permissions, disk full, ...), the 403 must still
// reach the caller. The denial is the primary function; the audit line is
// best-effort.
func TestEnforceCommanderAllowlist_UnwritableLogDoesNotBlockResponse(t *testing.T) {
	// Use a path inside a non-existent directory to force an open failure.
	logPath := filepath.Join(t.TempDir(), "does-not-exist", "attempts.log")
	srv := newAllowlistTestServer(t, []string{"F2504"}, logPath)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	ok := srv.enforceCommanderAllowlist(rr, req, loginFlowWeb, "F9999", "")
	assert.False(t, ok)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// newAllowlistTestServer produces a server stub with only the two fields
// enforceCommanderAllowlist reads, plus a logger. Avoids miniredis setup.
func newAllowlistTestServer(t *testing.T, allowlist []string, logPath string) *Server {
	t.Helper()
	return &Server{
		logger: observability.NewLogger("test"),
		cfg: &config.Config{
			CommanderAuth: config.CommanderAuthConfig{
				AllowedFIDs:         allowlist,
				LoginAttemptLogPath: logPath,
			},
		},
	}
}
