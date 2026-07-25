package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeDockerClient lets tests inject inspect responses without needing a real
// Docker daemon.
type fakeDockerClient struct {
	inspectFn func(ctx context.Context, name string) (containerState, error)
}

func (f *fakeDockerClient) Inspect(ctx context.Context, name string) (containerState, error) {
	return f.inspectFn(ctx, name)
}

func newTestServer(t *testing.T, client dockerClient) *http.Server {
	t.Helper()
	mux := buildMux(client)
	return &http.Server{Handler: mux} //nolint:gosec // test server
}

func TestSidecar_AllowedName_ReturnsInspectedState(t *testing.T) {
	srv := newTestServer(t, &fakeDockerClient{
		inspectFn: func(ctx context.Context, name string) (containerState, error) {
			return containerState{Status: "running", Health: "healthy"}, nil
		},
	})

	req := httptest.NewRequest("GET", "/inspect/eddn-timescaledb", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var got containerState
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	require.Equal(t, "running", got.Status)
	require.Equal(t, "healthy", got.Health)
}

func TestSidecar_DisallowedName_Returns404(t *testing.T) {
	srv := newTestServer(t, &fakeDockerClient{
		inspectFn: func(ctx context.Context, name string) (containerState, error) {
			t.Fatal("Inspect must not be called for a disallowed name")
			return containerState{}, nil
		},
	})

	// Bad names. URL-encoded forms cover real-world attacker traffic
	// (HTTP clients encode shell metacharacters; the panic-y raw forms
	// would never reach the handler intact).
	badCases := []struct {
		name string
		url  string // path appended to /inspect/
	}{
		{"control-api (not on allowlist)", "control-api"},
		{"random-other-container", "random-other-container"},
		{"shell injection (encoded)", "memgraph%3B%20rm%20-rf%20%2F"},
		{"trailing percent (encoded)", "memgraph%2520"},
		{"path traversal", "../etc/passwd"},
		{"empty", ""},
	}
	for _, c := range badCases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/inspect/"+c.url, nil)
			w := httptest.NewRecorder()
			srv.Handler.ServeHTTP(w, req)
			// Either 404 (handler rejected) or 301 (ServeMux normalized the
			// path to somewhere outside /inspect/ — effectively blocked).
			// What MUST NOT happen is 200 (docker inspect call).
			require.Contains(t, []int{http.StatusNotFound, http.StatusMovedPermanently}, w.Code,
				"url=%q must be blocked (404 or 301), never 200", c.url)
			require.NotEqual(t, http.StatusOK, w.Code, "url=%q must NEVER reach the docker SDK", c.url)
		})
	}
}

func TestSidecar_NonGETMethod_Returns404(t *testing.T) {
	srv := newTestServer(t, &fakeDockerClient{
		inspectFn: func(ctx context.Context, name string) (containerState, error) {
			t.Fatal("Inspect must not be called for non-GET")
			return containerState{}, nil
		},
	})

	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD"} {
		t.Run(m, func(t *testing.T) {
			req := httptest.NewRequest(m, "/inspect/eddn-timescaledb", nil)
			w := httptest.NewRecorder()
			srv.Handler.ServeHTTP(w, req)
			require.Equal(t, http.StatusNotFound, w.Code, "method=%s must return 404", m)
		})
	}
}

func TestSidecar_OtherPaths_Return404(t *testing.T) {
	srv := newTestServer(t, &fakeDockerClient{
		inspectFn: func(ctx context.Context, name string) (containerState, error) {
			t.Fatal("Inspect must not be called for non-/inspect path")
			return containerState{}, nil
		},
	})

	for _, p := range []string{
		"/", "/healthz", "/metrics", "/inspect/", "/inspect/memgraph/extra",
	} {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest("GET", p, nil)
			w := httptest.NewRecorder()
			srv.Handler.ServeHTTP(w, req)
			require.Equal(t, http.StatusNotFound, w.Code)
		})
	}

	// /inspect (no trailing slash) is rewritten to /inspect/ by Go's
	// ServeMux — that's a 301. The redirect target itself 404s (covered
	// above), so both outcomes block the caller.
	t.Run("/inspect (redirect-then-block)", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/inspect", nil)
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)
		require.Contains(t, []int{http.StatusMovedPermanently, http.StatusNotFound}, w.Code,
			"/inspect must be either redirected or 404'd, never 200")
	})
}

func TestSidecar_DockerError_Returns502(t *testing.T) {
	srv := newTestServer(t, &fakeDockerClient{
		inspectFn: func(ctx context.Context, name string) (containerState, error) {
			return containerState{}, errors.New("docker daemon unreachable")
		},
	})

	req := httptest.NewRequest("GET", "/inspect/eddn-timescaledb", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code)
}

func TestSidecar_AllowlistContents(t *testing.T) {
	want := []string{
		"edin-timescaledb",
		"eddn-timescaledb",
		"eddn-listener",
	}
	require.ElementsMatch(t, want, allowedContainers(),
		"allowlist must match exactly; adding a new container needs a plan task")
}

// Sanity check: the source MUST NOT contain os/exec usage. Sidecar uses the
// Docker SDK only; any shell-out is a security regression.
func TestSidecar_NoShellOut(t *testing.T) {
	src := mustReadSource(t, "main.go")
	require.False(t, strings.Contains(src, `"os/exec"`),
		"sidecar must not import os/exec — use the Docker SDK")
	require.False(t, strings.Contains(src, "exec.Command"),
		"sidecar must not call exec.Command")
	require.False(t, strings.Contains(src, "/bin/sh"),
		"sidecar must not reference /bin/sh")
}

func mustReadSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}
