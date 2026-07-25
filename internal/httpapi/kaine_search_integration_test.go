//go:build integration_search

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type kaineSearchAuthValidator struct {
	tokens map[string]*KaineUser
}

func (m *kaineSearchAuthValidator) ValidateToken(token string) (*KaineUser, error) {
	if u, ok := m.tokens[token]; ok {
		return u, nil
	}
	return nil, errors.New("invalid token")
}

func (m *kaineSearchAuthValidator) Close() {}

func relationalSearchServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("GALAXY_TEST_DSN")
	require.NotEmpty(t, dsn, "GALAXY_TEST_DSN is required for integration_search")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	when := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	_, err = pool.Exec(ctx, `
INSERT INTO galaxy.system_catalog (id64,x,y,z,first_seen,name)
VALUES (922337203685420001,0,0,0,$1,'Sol')
ON CONFLICT (id64) DO UPDATE SET x=0,y=0,z=0,name='Sol';
INSERT INTO galaxy.system
	(id64,population,last_event_time,name,allegiance,government,economy,
	 second_economy,security)
VALUES
	(922337203685420001,22780870000,$1,'Sol','Federation','Democracy',
	 'Refinery','Service','High')
ON CONFLICT (id64) DO UPDATE SET population=EXCLUDED.population,
	last_event_time=EXCLUDED.last_event_time,name=EXCLUDED.name;
INSERT INTO galaxy.station
	(market_id,system_id64,last_event_time,dist_from_star_ls,large_pads,
	 name,station_type,kind)
VALUES
	(922337203685420101,922337203685420001,$1,100,1,
	 'Jameson Memorial','Orbis','station')
ON CONFLICT (market_id) DO UPDATE SET name=EXCLUDED.name,
	last_event_time=EXCLUDED.last_event_time;
`, when)
	require.NoError(t, err)

	const validToken = "valid-search-test-token"
	return &Server{
		cfg: &config.Config{
			HTTP:      config.HTTPConfig{InternalKey: "test-key"},
			KaineAuth: config.KaineAuthConfig{CookieName: "kaine_session", CookiePath: "/api/kaine"},
		},
		logger: observability.NewLogger("test"),
		jwtValidator: &kaineSearchAuthValidator{tokens: map[string]*KaineUser{
			validToken: {Sub: "test-user", Name: "Test User", Groups: []string{"kaine-approved"}},
		}},
		nonceStore:  newKaineNonceStore(),
		galaxyStore: galaxystore.New(pool),
	}
}

func TestKaineSystemSearch_HTTPShape(t *testing.T) {
	server := relationalSearchServer(t)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/systems/search?q=Sol", nil)
	req.Header.Set("Authorization", "Bearer valid-search-test-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Systems []map[string]any `json:"systems"`
		Count   int              `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.Equal(t, 1, resp.Count)
	require.Len(t, resp.Systems, 1)
	for _, key := range []string{
		"name", "id64", "allegiance", "government", "security", "population",
		"economy", "second_economy", "coordinates",
	} {
		require.Contains(t, resp.Systems[0], key)
	}
	require.Equal(t, "Sol", resp.Systems[0]["name"])
}

func TestKaineSearch_HTTPShape(t *testing.T) {
	server := relationalSearchServer(t)
	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/search?q=jameson", nil)
	req.Header.Set("Authorization", "Bearer valid-search-test-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Results []map[string]any `json:"results"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Results)
	require.Equal(t, "station", resp.Results[0]["type"])
	require.Equal(t, "Jameson Memorial", resp.Results[0]["name"])
	require.Equal(t, "Sol", resp.Results[0]["systemName"])
	require.Contains(t, resp.Results[0], "details")
}
