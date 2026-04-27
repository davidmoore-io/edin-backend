package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/httpserver"
	"github.com/edin-space/edin-backend/internal/edinbot/publisher"
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

type fakeCleaner struct {
	called   string
	response publisher.ClearResult
	err      error
}

func (f *fakeCleaner) ClearHistory(ctx context.Context, b string) (publisher.ClearResult, error) {
	f.called = b
	if f.err != nil {
		return publisher.ClearResult{}, f.err
	}
	r := f.response
	r.BindingID = b
	return r, nil
}

func TestAdminClear_RequiresToken(t *testing.T) {
	c := &fakeCleaner{response: publisher.ClearResult{DiscordDeleted: 5}}
	h := httpserver.New(httpserver.Config{Cleaner: c, AdminToken: "shh"})

	// Wrong token.
	req := httptest.NewRequest("POST", "/admin/clear/kaine-ltd?confirm=true", nil)
	req.Header.Set("X-Admin-Token", "guess")
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, c.called)

	// Missing token.
	req = httptest.NewRequest("POST", "/admin/clear/kaine-ltd?confirm=true", nil)
	w = httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, c.called)
}

func TestAdminClear_RequiresConfirm(t *testing.T) {
	c := &fakeCleaner{}
	h := httpserver.New(httpserver.Config{Cleaner: c, AdminToken: "shh"})

	req := httptest.NewRequest("POST", "/admin/clear/kaine-ltd", nil)
	req.Header.Set("X-Admin-Token", "shh")
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, c.called, "must not invoke cleaner without ?confirm=true")
}

func TestAdminClear_FailsClosedWhenTokenUnset(t *testing.T) {
	c := &fakeCleaner{}
	h := httpserver.New(httpserver.Config{Cleaner: c, AdminToken: ""})

	req := httptest.NewRequest("POST", "/admin/clear/kaine-ltd?confirm=true", nil)
	req.Header.Set("X-Admin-Token", "")
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code,
		"empty server-side token must NOT be a backdoor")
}

func TestAdminClear_HappyPath(t *testing.T) {
	c := &fakeCleaner{response: publisher.ClearResult{DiscordDeleted: 7, RowsPurged: 7, BindingEnabled: true}}
	h := httpserver.New(httpserver.Config{Cleaner: c, AdminToken: "shh"})

	req := httptest.NewRequest("POST", "/admin/clear/kaine-ltd?confirm=true", nil)
	req.Header.Set("X-Admin-Token", "shh")
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "kaine-ltd", c.called)
	var body publisher.ClearResult
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Equal(t, 7, body.DiscordDeleted)
	require.True(t, body.BindingEnabled)
}
