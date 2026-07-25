package controlclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/stretchr/testify/require"
)

type fakeTokenSource struct{ tok string }

func (f *fakeTokenSource) Token(ctx context.Context) (string, error) { return f.tok, nil }

func TestClient_PlasmiumBuyers_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/kaine/mining/plasmium-buyers", r.URL.Path)
		require.Equal(t, "Bearer fake-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"maps": []any{
				map[string]any{
					"system_name": "Sol",
					"buyers": []any{
						map[string]any{
							"system_name":     "Sol",
							"station_name":    "Galileo",
							"faction":         "FedX",
							"faction_state":   "Boom",
							"platinum_demand": 1500,
							"platinum_price":  280000,
							"score":           175.0,
						},
					},
				},
			},
			"generated_at": "2026-04-26T14:23:01Z",
			"total_maps":   1,
			"total_buyers": 1,
		})
	}))
	defer srv.Close()

	c := controlclient.New(srv.URL, &fakeTokenSource{tok: "fake-token"})
	resp, err := c.PlasmiumBuyers(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, resp.TotalMaps)
	require.Len(t, resp.Maps, 1)
	require.Equal(t, "Sol", resp.Maps[0].SystemName)
	require.True(t, resp.IsStructurallyValid())
	require.Len(t, resp.Maps[0].Buyers, 1)
	require.EqualValues(t, 1500, resp.Maps[0].Buyers[0].PlatinumDemand)
}

func TestClient_PlasmiumBuyers_5xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := controlclient.New(srv.URL, &fakeTokenSource{tok: "fake-token"})
	_, err := c.PlasmiumBuyers(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestClient_LTDBuyers_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/kaine/mining/ltd-buyers", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"maps":         []any{},
			"generated_at": "2026-04-26T14:23:01Z",
			"total_maps":   0,
			"total_buyers": 0,
		})
	}))
	defer srv.Close()

	c := controlclient.New(srv.URL, &fakeTokenSource{tok: "fake-token"})
	resp, err := c.LTDBuyers(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, resp.TotalMaps)
	require.True(t, resp.IsStructurallyValid(), "empty result IS valid (quiet day)")
}

func TestClient_Diagnose_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/admin/diagnose", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.ElementsMatch(t,
			[]any{"galaxy-reader", "edin-timescaledb", "eddn-timescaledb", "eddn-listener"},
			body["checks"])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"checked_at": "2026-04-26T14:23:01Z",
			"results": map[string]any{
				"galaxy-reader": map[string]any{"ok": true, "latency_ms": 12},
			},
		})
	}))
	defer srv.Close()

	c := controlclient.New(srv.URL, &fakeTokenSource{tok: "fake-token"})
	report, err := c.Diagnose(context.Background())
	require.NoError(t, err)
	require.Contains(t, report.Results, "galaxy-reader")
}

func TestClient_StructurallyValidEnvelope(t *testing.T) {
	r := &controlclient.PlasmiumBuyersResponse{
		Maps:        []controlclient.MiningMap{{SystemName: "Sol"}},
		GeneratedAt: time.Now(),
	}
	require.True(t, r.IsStructurallyValid())

	require.False(t, (&controlclient.PlasmiumBuyersResponse{}).IsStructurallyValid())
	require.False(t, (&controlclient.PlasmiumBuyersResponse{GeneratedAt: time.Now()}).IsStructurallyValid(), "Maps nil → invalid")
}
