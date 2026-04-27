//go:build integration || integration_search

package testutil

import (
	"context"
	"strings"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TestStartTestMemgraph_HarnessSelfTest proves the harness brings up a clean
// Memgraph, applies the production schema (Power nodes seeded, indexes created),
// and that the SeedSystems/SeedStations helpers round-trip data through Bolt.
//
// If this test fails, every other integration test in this package will fail
// for the same reason — fix this one first.
func TestStartTestMemgraph_HarnessSelfTest(t *testing.T) {
	c := StartTestMemgraph(t)
	ctx := context.Background()

	// Production init seeds 12 Power nodes. If this count is off, the schema
	// template drifted from what the harness expects.
	session := c.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	res, err := session.Run(ctx, "MATCH (p:Power) RETURN count(p) AS n", nil)
	if err != nil {
		t.Fatalf("count powers: %v", err)
	}
	if !res.Next(ctx) {
		t.Fatalf("no rows from count powers")
	}
	got, _ := res.Record().Get("n")
	if got != int64(12) {
		t.Fatalf("expected 12 Power nodes after init, got %v", got)
	}

	// Text indexes for the new search path must be created by the init template.
	// Memgraph 3.8 reports text indexes via SHOW INDEX INFO with `index type` of
	// the form `label_text (name: systems_name_text)`. Probe by substring match
	// on that column rather than relying on a column name we don't control.
	res, err = session.Run(ctx, "SHOW INDEX INFO", nil)
	if err != nil {
		t.Fatalf("show index info: %v", err)
	}
	wantText := map[string]bool{"systems_name_text": false, "stations_name_text": false}
	for res.Next(ctx) {
		rec := res.Record()
		idxType, _ := rec.Get("index type")
		s, ok := idxType.(string)
		if !ok {
			continue
		}
		for name := range wantText {
			if strings.Contains(s, "name: "+name) {
				wantText[name] = true
			}
		}
	}
	for name, found := range wantText {
		if !found {
			t.Errorf("expected text index %q to exist after init, not found", name)
		}
	}

	// SeedSystems should round-trip: write one, read it back by name.
	SeedSystems(t, c, []SystemFixture{
		{Name: "TestSol", ID64: 999_001, X: 0, Y: 0, Z: 0},
	})
	res, err = session.Run(ctx,
		"MATCH (s:System {name: $name}) RETURN s.id64 AS id, s.name AS name",
		map[string]any{"name": "TestSol"},
	)
	if err != nil {
		t.Fatalf("read seeded system: %v", err)
	}
	if !res.Next(ctx) {
		t.Fatalf("seeded system not found")
	}
	id, _ := res.Record().Get("id")
	if id != int64(999_001) {
		t.Fatalf("expected id 999001, got %v", id)
	}

	// SeedStations should attach via HAS_STATION.
	SeedStations(t, c, []StationFixture{
		{ID64: 999_101, Name: "Test Station", SystemID64: 999_001, Type: "Coriolis"},
	})
	res, err = session.Run(ctx,
		"MATCH (s:System {id64: $sys})-[:HAS_STATION]->(st:Station) RETURN st.name AS name",
		map[string]any{"sys": int64(999_001)},
	)
	if err != nil {
		t.Fatalf("read attached station: %v", err)
	}
	if !res.Next(ctx) {
		t.Fatalf("attached station not found")
	}
	name, _ := res.Record().Get("name")
	if name != "Test Station" {
		t.Fatalf("expected station name 'Test Station', got %v", name)
	}
}
