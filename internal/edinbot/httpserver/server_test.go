package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/httpserver"
	"github.com/stretchr/testify/require"
)

type fakeHealthOracle struct {
	healthy bool
	per     map[string]bool
}

func (f *fakeHealthOracle) AllBindingsHealthy() bool          { return f.healthy }
func (f *fakeHealthOracle) PerBindingHealth() map[string]bool { return f.per }

func TestHTTPServer_Healthz_AllHealthy_Returns200(t *testing.T) {
	h := httpserver.New(httpserver.Config{
		Health:  &fakeHealthOracle{healthy: true, per: map[string]bool{"a": true}},
		Version: "test-1",
	})

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestHTTPServer_Healthz_AnyUnhealthy_Returns503(t *testing.T) {
	h := httpserver.New(httpserver.Config{
		Health: &fakeHealthOracle{
			healthy: false,
			per:     map[string]bool{"a": true, "b": false},
		},
	})

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Contains(t, body, "bindings")
}

func TestHTTPServer_Version_ReturnsConfiguredString(t *testing.T) {
	h := httpserver.New(httpserver.Config{Version: "v1.2.3-abc"})

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "v1.2.3-abc")
}

func TestHTTPServer_OtherPaths_Return404(t *testing.T) {
	h := httpserver.New(httpserver.Config{Health: &fakeHealthOracle{healthy: true, per: map[string]bool{}}})

	req := httptest.NewRequest("GET", "/anything-else", nil)
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestHTTPServer_StartStop_RespectsContext(t *testing.T) {
	srv := httpserver.New(httpserver.Config{
		Health: &fakeHealthOracle{healthy: true, per: map[string]bool{}},
		Addr:   "127.0.0.1:0",
	})
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		require.NoError(t, err, "context cancel must result in clean shutdown")
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down on context cancel")
	}
}
