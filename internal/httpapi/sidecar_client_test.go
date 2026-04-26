package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSidecarClient_Inspect_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/inspect/memgraph", r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "running", "health": "healthy"})
	}))
	defer srv.Close()

	client := newSidecarClient(srv.URL)
	state, err := client.Inspect(context.Background(), "memgraph")
	require.NoError(t, err)
	require.Equal(t, "running", state.Status)
	require.Equal(t, "healthy", state.Health)
}

func TestSidecarClient_Inspect_404_ReturnsErrUnknownContainer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := newSidecarClient(srv.URL)
	_, err := client.Inspect(context.Background(), "memgraph")
	require.ErrorIs(t, err, errUnknownContainer)
}

func TestSidecarClient_Inspect_Unreachable_ReturnsErrSidecarUnreachable(t *testing.T) {
	client := newSidecarClient("http://127.0.0.1:1") // unroutable
	_, err := client.Inspect(context.Background(), "memgraph")
	require.ErrorIs(t, err, errSidecarUnreachable)
}

func TestSidecarClient_Inspect_5xx_ReturnsErrSidecarFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newSidecarClient(srv.URL)
	_, err := client.Inspect(context.Background(), "memgraph")
	require.ErrorIs(t, err, errSidecarFailed)
}
