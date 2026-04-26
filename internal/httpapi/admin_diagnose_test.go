package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubProbers wires individual probe outcomes via closures.
type stubProbers struct {
	memgraph       func(ctx context.Context) error
	edinTSPing     func(ctx context.Context) error
	eddnTSPing     func(ctx context.Context) error
	listenerLag    func(ctx context.Context) (time.Duration, error)
	sidecarInspect func(ctx context.Context, name string) (containerState, error)
}

func (s *stubProbers) ProbeMemgraph(ctx context.Context) error { return s.memgraph(ctx) }

type stubPgProberDiagnose struct{ fn func(ctx context.Context) error }

func (s *stubPgProberDiagnose) Ping(ctx context.Context) error { return s.fn(ctx) }

type stubListener struct {
	fn func(ctx context.Context) (time.Duration, error)
}

func (s *stubListener) Lag(ctx context.Context) (time.Duration, error) { return s.fn(ctx) }

type stubSidecar struct {
	fn func(ctx context.Context, name string) (containerState, error)
}

func (s *stubSidecar) Inspect(ctx context.Context, name string) (containerState, error) {
	return s.fn(ctx, name)
}

func newDiagnoseHandler(t *testing.T, p *stubProbers) http.Handler {
	t.Helper()
	return diagnoseHandler(diagnoseDeps{
		memgraph: p,
		edinTS:   &stubPgProberDiagnose{fn: p.edinTSPing},
		eddnTS:   &stubPgProberDiagnose{fn: p.eddnTSPing},
		listener: &stubListener{fn: p.listenerLag},
		sidecar:  &stubSidecar{fn: p.sidecarInspect},
	})
}

func TestDiagnoseHandler_AllChecksHealthy(t *testing.T) {
	h := newDiagnoseHandler(t, &stubProbers{
		memgraph:       func(ctx context.Context) error { return nil },
		edinTSPing:     func(ctx context.Context) error { return nil },
		eddnTSPing:     func(ctx context.Context) error { return nil },
		listenerLag:    func(ctx context.Context) (time.Duration, error) { return 30 * time.Second, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) { return containerState{Status: "running", Health: "healthy"}, nil },
	})

	body := bytes.NewBufferString(`{"checks":["memgraph","edin-timescaledb","eddn-timescaledb","eddn-listener"]}`)
	req := httptest.NewRequest("POST", "/admin/diagnose", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		CheckedAt time.Time              `json:"checked_at"`
		Results   map[string]probeResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Len(t, resp.Results, 4)
	for name, r := range resp.Results {
		require.True(t, r.OK, "%s should be ok", name)
	}
}

// SECURITY-CRITICAL: any check name not in the allowlist must be rejected
// with 400 BEFORE any probe runs.
func TestDiagnoseHandler_RejectsNonAllowlistedChecks(t *testing.T) {
	probeRan := false
	h := newDiagnoseHandler(t, &stubProbers{
		memgraph: func(ctx context.Context) error {
			probeRan = true
			return nil
		},
		edinTSPing:     func(ctx context.Context) error { return nil },
		eddnTSPing:     func(ctx context.Context) error { return nil },
		listenerLag:    func(ctx context.Context) (time.Duration, error) { return 0, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) { return containerState{}, nil },
	})

	for _, bad := range []string{
		"memgraph; rm -rf /",
		"control-api",
		"random",
		"../etc/passwd",
		"",
	} {
		t.Run(bad, func(t *testing.T) {
			body := bytes.NewBufferString(`{"checks":["` + bad + `"]}`)
			req := httptest.NewRequest("POST", "/admin/diagnose", body)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			require.Equal(t, http.StatusBadRequest, w.Code, "must reject bad check %q before probing", bad)
		})
	}
	require.False(t, probeRan, "no probe must have run for any rejected request")
}

func TestDiagnoseHandler_PartialFailure_IndividualResultsReflectIt(t *testing.T) {
	h := newDiagnoseHandler(t, &stubProbers{
		memgraph:    func(ctx context.Context) error { return errors.New("connection refused") },
		edinTSPing:  func(ctx context.Context) error { return nil },
		eddnTSPing:  func(ctx context.Context) error { return nil },
		listenerLag: func(ctx context.Context) (time.Duration, error) { return 30 * time.Second, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) {
			return containerState{Status: "running", Health: "healthy"}, nil
		},
	})

	body := bytes.NewBufferString(`{"checks":["memgraph","edin-timescaledb"]}`)
	req := httptest.NewRequest("POST", "/admin/diagnose", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "endpoint returns 200 even on per-probe failures")
	var resp struct {
		Results map[string]probeResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.False(t, resp.Results["memgraph"].OK)
	require.True(t, resp.Results["edin-timescaledb"].OK)
}

func TestDiagnoseHandler_SidecarUnreachable_FailsOpen(t *testing.T) {
	h := newDiagnoseHandler(t, &stubProbers{
		memgraph:    func(ctx context.Context) error { return nil },
		edinTSPing:  func(ctx context.Context) error { return nil },
		eddnTSPing:  func(ctx context.Context) error { return nil },
		listenerLag: func(ctx context.Context) (time.Duration, error) { return 30 * time.Second, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) {
			return containerState{}, errSidecarUnreachable
		},
	})

	body := bytes.NewBufferString(`{"checks":["memgraph","edin-timescaledb","eddn-timescaledb","eddn-listener"]}`)
	req := httptest.NewRequest("POST", "/admin/diagnose", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "must fail-open on sidecar unreachable; per spec §5 (4)")
	var resp struct {
		Results map[string]probeResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	for name, r := range resp.Results {
		require.True(t, r.OK, "the underlying probe still ran for %s", name)
		require.Nil(t, r.ContainerState, "container_status must be nil when sidecar unreachable")
	}
}

func TestDiagnoseHandler_AllowlistContents(t *testing.T) {
	want := []string{"memgraph", "edin-timescaledb", "eddn-timescaledb", "eddn-listener"}
	got := make([]string, 0, len(allowedDiagnoseChecks))
	for k := range allowedDiagnoseChecks {
		got = append(got, k)
	}
	require.ElementsMatch(t, want, got, "allowlist must match sidecar's allowedContainers() — see ALLOWLIST.md")
}

// Source guard: this file MUST NOT contain os/exec usage.
func TestDiagnoseHandler_NoShellOut(t *testing.T) {
	src := mustReadAdminDiagnoseSource(t)
	require.False(t, strings.Contains(src, `"os/exec"`), "/admin/diagnose must not import os/exec")
	require.False(t, strings.Contains(src, "exec.Command"), "/admin/diagnose must not call exec.Command")
	require.False(t, strings.Contains(src, "/bin/sh"), "/admin/diagnose must not reference /bin/sh")
}

func mustReadAdminDiagnoseSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("admin_diagnose.go")
	require.NoError(t, err)
	return string(b)
}
