//go:build integration || integration_search

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/edin-space/edin-backend/internal/memgraph"
)

// SystemFixture captures every field that memgraph.SearchSystems is expected
// to return. Tests use this to seed a known graph and then assert that the
// returned SystemData round-trips faithfully.
//
// Pointers are used for fields where "unset" is meaningful (e.g. ControlProgress).
// Slices and primitives use Go zero values when unset.
type SystemFixture struct {
	Name                    string
	ID64                    int64
	ControllingPower        string
	Powers                  []string
	PowerplayState          string
	Reinforcement           int64
	Undermining             int64
	ControlProgress         *float64
	Allegiance              string
	Government              string
	Security                string
	Population              int64
	Economy                 string
	SecondEconomy           string
	NeedsPermit             bool
	ControllingFaction      string
	ControllingFactionState string
	X, Y, Z                 float64
	ThargoidState           string
	ThargoidProgress        float64
	LastEDDNUpdate          time.Time
}

// StationFixture captures every field that memgraph.SearchStations returns,
// plus the System node it should be attached to via HAS_STATION.
type StationFixture struct {
	ID64           int64
	Name           string
	Type           string
	SystemID64     int64 // must match an existing SystemFixture.ID64 already seeded
	DistanceLS     float64
	MaxPad         string
	IsPlanetary    bool
	Services       []string
	LastEDDNUpdate time.Time
}

// SeedSystems writes all given systems as :System nodes. Existing nodes with
// the same id64 are replaced (MERGE on id64 + SET).
func SeedSystems(t *testing.T, c *memgraph.Client, systems []SystemFixture) {
	t.Helper()
	ctx := context.Background()
	session := c.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	const cypher = `
		MERGE (s:System {id64: $id64})
		SET s.name                       = $name,
		    s.controlling_power          = $controlling_power,
		    s.powers                     = $powers,
		    s.powerplay_state            = $powerplay_state,
		    s.reinforcement              = $reinforcement,
		    s.undermining                = $undermining,
		    s.control_progress           = $control_progress,
		    s.allegiance                 = $allegiance,
		    s.government                 = $government,
		    s.security                   = $security,
		    s.population                 = $population,
		    s.economy                    = $economy,
		    s.second_economy             = $second_economy,
		    s.needs_permit               = $needs_permit,
		    s.controlling_faction        = $controlling_faction,
		    s.controlling_faction_state  = $controlling_faction_state,
		    s.x                          = $x,
		    s.y                          = $y,
		    s.z                          = $z,
		    s.thargoid_state             = $thargoid_state,
		    s.thargoid_progress          = $thargoid_progress,
		    s.last_eddn_update           = $last_eddn_update
	`

	for _, sys := range systems {
		params := map[string]any{
			"id64":                      sys.ID64,
			"name":                      sys.Name,
			"controlling_power":         sys.ControllingPower,
			"powers":                    sys.Powers,
			"powerplay_state":           sys.PowerplayState,
			"reinforcement":             sys.Reinforcement,
			"undermining":               sys.Undermining,
			"control_progress":          nullableFloat(sys.ControlProgress),
			"allegiance":                sys.Allegiance,
			"government":                sys.Government,
			"security":                  sys.Security,
			"population":                sys.Population,
			"economy":                   sys.Economy,
			"second_economy":            sys.SecondEconomy,
			"needs_permit":              sys.NeedsPermit,
			"controlling_faction":       sys.ControllingFaction,
			"controlling_faction_state": sys.ControllingFactionState,
			"x":                         sys.X,
			"y":                         sys.Y,
			"z":                         sys.Z,
			"thargoid_state":            sys.ThargoidState,
			"thargoid_progress":         sys.ThargoidProgress,
			"last_eddn_update":          sys.LastEDDNUpdate,
		}
		if _, err := session.Run(ctx, cypher, params); err != nil {
			t.Fatalf("seed system %q: %v", sys.Name, err)
		}
	}
}

// SeedStations writes all given stations and connects each to its System
// via the HAS_STATION relationship. The matching System must already exist
// (call SeedSystems first).
func SeedStations(t *testing.T, c *memgraph.Client, stations []StationFixture) {
	t.Helper()
	ctx := context.Background()
	session := c.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	const cypher = `
		MATCH (s:System {id64: $system_id64})
		MERGE (st:Station {id64: $id64})
		SET st.name             = $name,
		    st.type             = $type,
		    st.distance_ls      = $distance_ls,
		    st.max_pad          = $max_pad,
		    st.is_planetary     = $is_planetary,
		    st.services         = $services,
		    st.last_eddn_update = $last_eddn_update
		MERGE (s)-[:HAS_STATION]->(st)
	`

	for _, st := range stations {
		params := map[string]any{
			"system_id64":      st.SystemID64,
			"id64":             st.ID64,
			"name":             st.Name,
			"type":             st.Type,
			"distance_ls":      st.DistanceLS,
			"max_pad":          st.MaxPad,
			"is_planetary":     st.IsPlanetary,
			"services":         st.Services,
			"last_eddn_update": st.LastEDDNUpdate,
		}
		if _, err := session.Run(ctx, cypher, params); err != nil {
			t.Fatalf("seed station %q: %v", st.Name, err)
		}
	}
}

// nullableFloat returns nil for Cypher NULL when p is nil; otherwise *p.
// Memgraph distinguishes missing properties from zero values for float, so
// tests that don't care about ControlProgress should leave it unset.
func nullableFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
