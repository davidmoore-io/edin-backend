//go:build integration_search

// This file uses the `integration_search` build tag rather than the broader
// `integration` tag because internal/httpapi/kaine_integration_test.go (which
// uses `integration`) currently has unrelated compile errors against newer
// versions of the anthropic SDK and memgraph.Client. Until that file is
// repaired separately, our search tests live behind a narrower tag so they
// can run without dragging in the broken neighbour.

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/testutil"
)

// kaineSearchAuthValidator is a copy of the route-test mock validator. We don't
// share the file-level mockValidator because that one lives in a non-tagged
// _test.go and we can't depend across build tags cleanly.
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

// TestKaineSystemSearch_HTTPShape exercises GET /api/kaine/systems/search end-to-end
// against a real testcontainers Memgraph. It is the API-stability contract:
// if any field that the frontend depends on is dropped from the JSON response,
// this test fails.
func TestKaineSystemSearch_HTTPShape(t *testing.T) {
	mg := testutil.StartTestMemgraph(t)
	testutil.SeedSystems(t, mg, []testutil.SystemFixture{
		{
			Name:                    "Sol",
			ID64:                    100_001,
			ControllingPower:        "Felicia Winters",
			Powers:                  []string{"Felicia Winters"},
			PowerplayState:          "Stronghold",
			Allegiance:              "Federation",
			Government:              "Democracy",
			Security:                "High",
			Population:              22_780_870_000,
			Economy:                 "Refinery",
			SecondEconomy:           "Service",
			ControllingFaction:      "Mother Gaia",
			ControllingFactionState: "None",
			X:                       0, Y: 0, Z: 0,
			LastEDDNUpdate: time.Now().UTC(),
		},
	})

	const validToken = "valid-search-test-token"
	server := &Server{
		cfg: &config.Config{
			HTTP:      config.HTTPConfig{InternalKey: "test-key"},
			KaineAuth: config.KaineAuthConfig{CookieName: "kaine_session", CookiePath: "/api/kaine"},
		},
		logger:       observability.NewLogger("test"),
		jwtValidator: &kaineSearchAuthValidator{tokens: map[string]*KaineUser{
			validToken: {Sub: "test-user", Name: "Test User", Groups: []string{"kaine-approved"}},
		}},
		nonceStore: newKaineNonceStore(),
		memgraph:   mg,
	}

	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/systems/search?q=Sol", nil)
	req.Header.Set("Authorization", "Bearer "+validToken)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode top-level: %v body=%s", err, rr.Body.String())
	}
	for _, key := range []string{"systems", "count"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("response missing top-level key %q (got keys: %v)", key, mapKeys(resp))
		}
	}

	var systems []map[string]any
	if err := json.Unmarshal(resp["systems"], &systems); err != nil {
		t.Fatalf("decode systems array: %v", err)
	}
	if len(systems) == 0 {
		t.Fatalf("expected at least one system result, got none")
	}

	// Field-shape contract — the frontend (SystemSearch.jsx, MentionInput.jsx)
	// reads these. If any are dropped or renamed, the UI breaks. Expected names
	// are the JSON tags on memgraph.SystemData.
	expected := []string{
		"name",
		"id64",
		"controlling_power",
		"powers",
		"powerplay_state",
		"allegiance",
		"government",
		"security",
		"population",
		"economy",
		"second_economy",
		"controlling_faction",
		"controlling_faction_state",
		"coordinates",
	}
	first := systems[0]
	for _, key := range expected {
		if _, ok := first[key]; !ok {
			t.Errorf("system result missing field %q (got keys: %v)", key, mapKeys(first))
		}
	}

	// Type guards for the values the frontend actually consumes.
	if name, _ := first["name"].(string); name != "Sol" {
		t.Errorf("expected name=Sol, got %v", first["name"])
	}
	if _, ok := first["id64"].(float64); !ok {
		// JSON numbers decode to float64 in any. id64 must be a number, not a string.
		t.Errorf("id64 must be a JSON number, got %T", first["id64"])
	}
	if coords, ok := first["coordinates"].(map[string]any); !ok {
		t.Errorf("coordinates must be an object, got %T", first["coordinates"])
	} else {
		for _, axis := range []string{"x", "y", "z"} {
			if _, ok := coords[axis]; !ok {
				t.Errorf("coordinates missing axis %q", axis)
			}
		}
	}

	// Compatibility check: id64 must round-trip as an integer-valued float (no
	// loss of precision through JSON). 100001 fits well within float64 mantissa.
	if id, _ := first["id64"].(float64); int64(id) != 100_001 {
		t.Errorf("id64 round-trip: got %v want 100001", id)
	}
}

// TestKaineSearch_HTTPShape exercises the @-mention endpoint
// (GET /api/kaine/search). Different response shape — verifies it independently.
func TestKaineSearch_HTTPShape(t *testing.T) {
	mg := testutil.StartTestMemgraph(t)
	testutil.SeedSystems(t, mg, []testutil.SystemFixture{
		{Name: "Sol", ID64: 100_001},
	})
	testutil.SeedStations(t, mg, []testutil.StationFixture{
		{ID64: 300_001, Name: "Jameson Memorial", SystemID64: 100_001, Type: "Orbis", MaxPad: "L"},
	})

	const validToken = "valid-mention-test-token"
	server := &Server{
		cfg: &config.Config{
			HTTP:      config.HTTPConfig{InternalKey: "test-key"},
			KaineAuth: config.KaineAuthConfig{CookieName: "kaine_session", CookiePath: "/api/kaine"},
		},
		logger:       observability.NewLogger("test"),
		jwtValidator: &kaineSearchAuthValidator{tokens: map[string]*KaineUser{
			validToken: {Sub: "test-user", Name: "Test User", Groups: []string{"kaine-approved"}},
		}},
		nonceStore: newKaineNonceStore(),
		memgraph:   mg,
	}

	mux := http.NewServeMux()
	server.RegisterKaineRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/kaine/search?q=jameson", nil)
	req.Header.Set("Authorization", "Bearer "+validToken)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}

	// At least one station result with the @-mention shape.
	foundStation := false
	for _, r := range resp.Results {
		typ, _ := r["type"].(string)
		if typ != "station" {
			continue
		}
		for _, key := range []string{"type", "name", "systemName", "details"} {
			if _, ok := r[key]; !ok {
				t.Errorf("station result missing field %q (got keys: %v)", key, mapKeys(r))
			}
		}
		if name, _ := r["name"].(string); name != "Jameson Memorial" {
			t.Errorf("expected station name 'Jameson Memorial', got %v", r["name"])
		}
		if sys, _ := r["systemName"].(string); sys != "Sol" {
			t.Errorf("expected systemName 'Sol', got %v", r["systemName"])
		}
		foundStation = true
	}
	if !foundStation {
		t.Fatalf("expected a station result for q=jameson, got %v", resp.Results)
	}
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
