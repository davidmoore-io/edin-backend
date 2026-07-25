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
	galaxyReader   func(ctx context.Context) error
	edinTSPing     func(ctx context.Context) error
	eddnTSPing     func(ctx context.Context) error
	listenerLag    func(ctx context.Context) (time.Duration, error)
	sidecarInspect func(ctx context.Context, name string) (containerState, error)
}

func (s *stubProbers) ProbeReader(ctx context.Context) error { return s.galaxyReader(ctx) }

type stubPgProberDiagnose struct {
	fn func(ctx context.Context) error
}

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
		galaxyReader: p,
		edinTS:       &stubPgProberDiagnose{fn: p.edinTSPing},
		eddnTS:       &stubPgProberDiagnose{fn: p.eddnTSPing},
		listener:     &stubListener{fn: p.listenerLag},
		sidecar:      &stubSidecar{fn: p.sidecarInspect},
	})
}

func TestDiagnoseHandler_AllChecksHealthy(t *testing.T) {
	h := newDiagnoseHandler(t, &stubProbers{
		galaxyReader: func(ctx context.Context) error { return nil },
		edinTSPing:   func(ctx context.Context) error { return nil },
		eddnTSPing:   func(ctx context.Context) error { return nil },
		listenerLag:  func(ctx context.Context) (time.Duration, error) { return 30 * time.Second, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) {
			return containerState{Status: "running", Health: "healthy"}, nil
		},
	})

	body := bytes.NewBufferString(`{"checks":["galaxy-reader","edin-timescaledb","eddn-timescaledb","eddn-listener"]}`)
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
		galaxyReader: func(ctx context.Context) error {
			probeRan = true
			return nil
		},
		edinTSPing:     func(ctx context.Context) error { return nil },
		eddnTSPing:     func(ctx context.Context) error { return nil },
		listenerLag:    func(ctx context.Context) (time.Duration, error) { return 0, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) { return containerState{}, nil },
	})

	for _, bad := range []string{
		"galaxy-reader; rm -rf /",
		"memgraph",
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
		galaxyReader: func(ctx context.Context) error { return errors.New("connection refused") },
		edinTSPing:   func(ctx context.Context) error { return nil },
		eddnTSPing:   func(ctx context.Context) error { return nil },
		listenerLag:  func(ctx context.Context) (time.Duration, error) { return 30 * time.Second, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) {
			return containerState{Status: "running", Health: "healthy"}, nil
		},
	})

	body := bytes.NewBufferString(`{"checks":["galaxy-reader","edin-timescaledb"]}`)
	req := httptest.NewRequest("POST", "/admin/diagnose", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "endpoint returns 200 even on per-probe failures")
	var resp struct {
		Results map[string]probeResult `json:"results"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.False(t, resp.Results["galaxy-reader"].OK)
	require.True(t, resp.Results["edin-timescaledb"].OK)
}

func TestDiagnoseHandler_SidecarUnreachable_FailsOpen(t *testing.T) {
	h := newDiagnoseHandler(t, &stubProbers{
		galaxyReader: func(ctx context.Context) error { return nil },
		edinTSPing:   func(ctx context.Context) error { return nil },
		eddnTSPing:   func(ctx context.Context) error { return nil },
		listenerLag:  func(ctx context.Context) (time.Duration, error) { return 30 * time.Second, nil },
		sidecarInspect: func(ctx context.Context, name string) (containerState, error) {
			return containerState{}, errSidecarUnreachable
		},
	})

	body := bytes.NewBufferString(`{"checks":["galaxy-reader","edin-timescaledb","eddn-timescaledb","eddn-listener"]}`)
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
	want := []string{"galaxy-reader", "edin-timescaledb", "eddn-timescaledb", "eddn-listener"}
	got := make([]string, 0, len(allowedDiagnoseChecks))
	for k := range allowedDiagnoseChecks {
		got = append(got, k)
	}
	require.ElementsMatch(t, want, got)
	for _, spec := range allowedDiagnoseChecks {
		require.Contains(t,
			[]string{"edin-timescaledb", "eddn-timescaledb", "eddn-listener"},
			spec.container,
			"diagnose container values must be a subset of the sidecar allowlist",
		)
	}
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

// Phase 6.4: route wire-up test. Asserts that withBotEdinOnly composes
// withKaineAuth + the kaineBotIdentityKey check correctly.

func TestWithBotEdinOnly_RejectsNoAuth(t *testing.T) {
	ts := newTestableServer()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached without auth")
	})
	wrapped := ts.withKaineAuthMock(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate withBotEdinOnly's inner check.
		if _, ok := r.Context().Value(kaineBotIdentityKey{}).(string); !ok {
			ts.writeError(w, http.StatusForbidden, "bot:edin only")
			return
		}
		h.ServeHTTP(w, r)
	}).ServeHTTP)

	req := httptest.NewRequest("POST", "/admin/diagnose", nil)
	rr := httptest.NewRecorder()
	wrapped(rr, req)
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestWithBotEdinOnly_RejectsUserJWT(t *testing.T) {
	ts := newTestableServer()
	ts.mockValidator.addUser("director-token", &KaineUser{
		Sub:    "user-director",
		Groups: []string{"kaine-directors"},
	})
	wrapped := ts.withKaineAuthMock(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(kaineBotIdentityKey{}).(string); !ok {
			ts.writeError(w, http.StatusForbidden, "bot:edin only")
			return
		}
		w.WriteHeader(http.StatusOK)
	}).ServeHTTP)

	req := httptest.NewRequest("POST", "/admin/diagnose", nil)
	req.Header.Set("Authorization", "Bearer director-token")
	rr := httptest.NewRecorder()
	wrapped(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code, "user JWTs (even directors) MUST NOT reach /admin/diagnose")
}

func TestWithBotEdinOnly_AllowsBotEdin(t *testing.T) {
	ts := newTestableServer()
	ts.mockValidator.addUser("bot-token", &KaineUser{
		Sub:    "svc-edin-bot",
		Groups: []string{botEdinGroup},
	})
	reached := false
	wrapped := ts.withKaineAuthMock(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Value(kaineBotIdentityKey{}).(string); !ok {
			ts.writeError(w, http.StatusForbidden, "bot:edin only")
			return
		}
		reached = true
		w.WriteHeader(http.StatusOK)
	}).ServeHTTP)

	req := httptest.NewRequest("POST", "/admin/diagnose", nil)
	req.Header.Set("Authorization", "Bearer bot-token")
	rr := httptest.NewRecorder()
	wrapped(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.True(t, reached)
}
