//go:build integration || integration_search

package memgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/memgraph"
	"github.com/edin-space/edin-backend/internal/testutil"
)

// fixtures used across multiple tests in this file. Names are chosen to
// exercise the behaviours documented in the plan: alphabetical tie-break,
// case insensitivity, multi-token AND, procgen names with reserved chars,
// and a long-tail of common ED naming families.
//
// Note: id64 values are arbitrary 6-digit ints — picked to avoid clashing
// with any plausible production id64 if a test ever runs against a polluted
// instance.
var diagnosticSystems = []testutil.SystemFixture{
	// Recognisable real names that exercise prefix + alphabetical tie-break.
	{Name: "Sol", ID64: 100_001, X: 0, Y: 0, Z: 0, Allegiance: "Federation", Population: 22_780_870_000},
	{Name: "Solati", ID64: 100_002, X: 1, Y: 1, Z: 1},
	{Name: "Solatium", ID64: 100_003, X: 2, Y: 2, Z: 2},
	// Multi-token target: typing "alpha cent" must find this, NOT "Alpha Crucis".
	{Name: "Alpha Centauri", ID64: 100_004, X: -1, Y: -1, Z: -1},
	{Name: "Alpha Crucis", ID64: 100_005, X: -2, Y: -2, Z: -2},
	// Procgen / catalogue names with awkward characters.
	{Name: "BD+45 1882", ID64: 100_006},
	{Name: "BD-12 4523", ID64: 100_007},
	{Name: "HIP 12345", ID64: 100_008},
	{Name: "Col 359 Sector AB-C 2-3", ID64: 100_009},
	{Name: "Synuefai EU-Q c21-10", ID64: 100_010},
	// Field-completeness fixture — every field set non-zero so the regression
	// guard can verify nothing is dropped by the new query path.
	{
		Name:                    "Test System Alpha",
		ID64:                    100_999,
		ControllingPower:        "Nakato Kaine",
		Powers:                  []string{"Nakato Kaine"},
		PowerplayState:          "Stronghold",
		Reinforcement:           1234,
		Undermining:             567,
		ControlProgress:         floatPtr(0.42),
		Allegiance:              "Alliance",
		Government:              "Democracy",
		Security:                "High",
		Population:              500_000,
		Economy:                 "High Tech",
		SecondEconomy:           "Refinery",
		NeedsPermit:             true,
		ControllingFaction:      "Test Faction",
		ControllingFactionState: "Boom",
		X:                       12.5, Y: -7.25, Z: 3.0,
		ThargoidState:    "Controlled",
		ThargoidProgress: 0.75,
		LastEDDNUpdate:   time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
	},
}

// 30 systems sharing the prefix "Prefixy" so we can verify limit handling.
func limitFixtures() []testutil.SystemFixture {
	out := make([]testutil.SystemFixture, 30)
	for i := range out {
		out[i] = testutil.SystemFixture{
			Name: "Prefixy " + numToWord(i),
			ID64: int64(200_000 + i),
		}
	}
	return out
}

func numToWord(i int) string {
	// Crude but deterministic: just pad the number so names sort predictably.
	const digits = "0123456789"
	tens := i / 10
	ones := i % 10
	return string([]byte{digits[tens], digits[ones]})
}

func floatPtr(f float64) *float64 { return &f }

// seedDiagnostic seeds the standard fixture set into a fresh test Memgraph.
func seedDiagnostic(t *testing.T) *memgraph.Client {
	t.Helper()
	c := testutil.StartTestMemgraph(t)
	testutil.SeedSystems(t, c, diagnosticSystems)
	return c
}

func TestSearchSystems_ExactPrefix(t *testing.T) {
	c := seedDiagnostic(t)
	got, err := c.SearchSystems(context.Background(), "Sol", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) < 3 {
		t.Fatalf("expected at least Sol, Solati, Solatium — got %d results", len(got))
	}
	// Score ties on a token-prefix query should break to alphabetical order.
	if got[0].Name != "Sol" {
		t.Errorf("expected Sol first, got %q", got[0].Name)
	}
	// Solati and Solatium follow.
	hasSolati, hasSolatium := false, false
	for _, s := range got {
		if s.Name == "Solati" {
			hasSolati = true
		}
		if s.Name == "Solatium" {
			hasSolatium = true
		}
	}
	if !hasSolati || !hasSolatium {
		t.Errorf("missing Solati(%v) or Solatium(%v) in %v", hasSolati, hasSolatium, names(got))
	}
}

func TestSearchSystems_CaseInsensitive(t *testing.T) {
	c := seedDiagnostic(t)
	a, err := c.SearchSystems(context.Background(), "sol", 10)
	if err != nil {
		t.Fatalf("search lower: %v", err)
	}
	b, err := c.SearchSystems(context.Background(), "SOL", 10)
	if err != nil {
		t.Fatalf("search upper: %v", err)
	}
	d, err := c.SearchSystems(context.Background(), "Sol", 10)
	if err != nil {
		t.Fatalf("search mixed: %v", err)
	}
	if !sameNameSet(a, b) || !sameNameSet(a, d) {
		t.Fatalf("case variants returned different result sets: lower=%v upper=%v mixed=%v",
			names(a), names(b), names(d))
	}
}

func TestSearchSystems_RespectsLimit(t *testing.T) {
	c := testutil.StartTestMemgraph(t)
	testutil.SeedSystems(t, c, limitFixtures())

	got, err := c.SearchSystems(context.Background(), "prefixy", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected limit of 5, got %d (%v)", len(got), names(got))
	}
}

func TestSearchSystems_EmptyQueryRejected(t *testing.T) {
	c := testutil.StartTestMemgraph(t)
	_, err := c.SearchSystems(context.Background(), "", 10)
	if err == nil {
		t.Fatalf("expected error for empty query, got nil")
	}
}

func TestSearchSystems_NoMatch(t *testing.T) {
	c := seedDiagnostic(t)
	got, err := c.SearchSystems(context.Background(), "zzznosuchsystem", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got == nil {
		t.Fatalf("expected empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected zero results, got %d (%v)", len(got), names(got))
	}
}

func TestSearchSystems_SpecialCharsInName(t *testing.T) {
	c := seedDiagnostic(t)
	got, err := c.SearchSystems(context.Background(), "BD+45", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !containsName(got, "BD+45 1882") {
		t.Fatalf("expected BD+45 1882 in results, got %v", names(got))
	}
}

func TestSearchSystems_MultiToken(t *testing.T) {
	c := seedDiagnostic(t)
	got, err := c.SearchSystems(context.Background(), "alpha cent", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !containsName(got, "Alpha Centauri") {
		t.Fatalf("expected Alpha Centauri in results, got %v", names(got))
	}
	if containsName(got, "Alpha Crucis") {
		t.Fatalf("did not expect Alpha Crucis — multi-token AND must exclude it; got %v", names(got))
	}
}

func TestSearchSystems_ReturnedFieldsComplete(t *testing.T) {
	// CRITICAL regression guard: if the new query path drops any field that
	// the old MATCH/WHERE query returned, this test fails. The HTTP API JSON
	// shape must be preserved.
	c := seedDiagnostic(t)
	got, err := c.SearchSystems(context.Background(), "Test System Alpha", 1)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("expected exactly the seeded test system, got nothing")
	}
	s := got[0]

	if s.Name != "Test System Alpha" {
		t.Errorf("Name: got %q", s.Name)
	}
	if s.ID64 != 100_999 {
		t.Errorf("ID64: got %d", s.ID64)
	}
	if s.ControllingPower != "Nakato Kaine" {
		t.Errorf("ControllingPower: got %q", s.ControllingPower)
	}
	if len(s.Powers) != 1 || s.Powers[0] != "Nakato Kaine" {
		t.Errorf("Powers: got %v", s.Powers)
	}
	if s.PowerplayState != "Stronghold" {
		t.Errorf("PowerplayState: got %q", s.PowerplayState)
	}
	if s.Reinforcement != 1234 {
		t.Errorf("Reinforcement: got %d", s.Reinforcement)
	}
	if s.Undermining != 567 {
		t.Errorf("Undermining: got %d", s.Undermining)
	}
	if s.ControlProgress == nil || *s.ControlProgress != 0.42 {
		t.Errorf("ControlProgress: got %v", s.ControlProgress)
	}
	if s.Allegiance != "Alliance" {
		t.Errorf("Allegiance: got %q", s.Allegiance)
	}
	if s.Government != "Democracy" {
		t.Errorf("Government: got %q", s.Government)
	}
	if s.Security != "High" {
		t.Errorf("Security: got %q", s.Security)
	}
	if s.Population != 500_000 {
		t.Errorf("Population: got %d", s.Population)
	}
	if s.Economy != "High Tech" {
		t.Errorf("Economy: got %q", s.Economy)
	}
	if s.SecondEconomy != "Refinery" {
		t.Errorf("SecondEconomy: got %q", s.SecondEconomy)
	}
	if !s.NeedsPermit {
		t.Errorf("NeedsPermit: got false")
	}
	if s.ControllingFaction != "Test Faction" {
		t.Errorf("ControllingFaction: got %q", s.ControllingFaction)
	}
	if s.ControllingFactionState != "Boom" {
		t.Errorf("ControllingFactionState: got %q", s.ControllingFactionState)
	}
	if s.Coordinates == nil || s.Coordinates.X != 12.5 || s.Coordinates.Y != -7.25 || s.Coordinates.Z != 3.0 {
		t.Errorf("Coordinates: got %+v", s.Coordinates)
	}
	if s.ThargoidState != "Controlled" {
		t.Errorf("ThargoidState: got %q", s.ThargoidState)
	}
	if s.ThargoidProgress != 0.75 {
		t.Errorf("ThargoidProgress: got %v", s.ThargoidProgress)
	}
	if s.LastEDDNUpdate.IsZero() {
		t.Errorf("LastEDDNUpdate: got zero time")
	}
}

// ---------- Stations ----------

var diagnosticStations = []testutil.StationFixture{
	{ID64: 300_001, Name: "Jameson Memorial", SystemID64: 100_001, Type: "Orbis", MaxPad: "L"},
	{ID64: 300_002, Name: "Galileo", SystemID64: 100_001, Type: "Coriolis", MaxPad: "L"},
	{ID64: 300_003, Name: "Daedalus", SystemID64: 100_001, Type: "Coriolis", MaxPad: "L"},
	{ID64: 300_004, Name: "Hutton Orbital", SystemID64: 100_004, Type: "Outpost", MaxPad: "M"},
}

func TestSearchStations_ExactPrefix(t *testing.T) {
	c := seedDiagnostic(t)
	testutil.SeedStations(t, c, diagnosticStations)
	got, err := c.SearchStations(context.Background(), "Galileo", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 || got[0].Name != "Galileo" {
		t.Fatalf("expected Galileo first, got %v", stationNames(got))
	}
	if got[0].SystemName != "Sol" {
		t.Errorf("expected SystemName=Sol, got %q", got[0].SystemName)
	}
}

func TestSearchStations_CaseInsensitive(t *testing.T) {
	c := seedDiagnostic(t)
	testutil.SeedStations(t, c, diagnosticStations)

	a, err := c.SearchStations(context.Background(), "jameson", 10)
	if err != nil {
		t.Fatalf("search lower: %v", err)
	}
	b, err := c.SearchStations(context.Background(), "JAMESON", 10)
	if err != nil {
		t.Fatalf("search upper: %v", err)
	}
	if !sameStationNameSet(a, b) {
		t.Fatalf("case variants differ: %v vs %v", stationNames(a), stationNames(b))
	}
}

func TestSearchStations_NoMatch(t *testing.T) {
	c := seedDiagnostic(t)
	testutil.SeedStations(t, c, diagnosticStations)
	got, err := c.SearchStations(context.Background(), "zzznosuchstation", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got == nil {
		t.Fatalf("expected empty slice, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected zero results, got %d", len(got))
	}
}

func TestSearchStations_EmptyQueryRejected(t *testing.T) {
	c := testutil.StartTestMemgraph(t)
	_, err := c.SearchStations(context.Background(), "", 10)
	if err == nil {
		t.Fatalf("expected error for empty query, got nil")
	}
}

// ---------- helpers ----------

func names(systems []memgraph.SystemData) []string {
	out := make([]string, len(systems))
	for i, s := range systems {
		out[i] = s.Name
	}
	return out
}

func stationNames(stations []memgraph.StationData) []string {
	out := make([]string, len(stations))
	for i, s := range stations {
		out[i] = s.Name
	}
	return out
}

func containsName(systems []memgraph.SystemData, name string) bool {
	for _, s := range systems {
		if s.Name == name {
			return true
		}
	}
	return false
}

func sameNameSet(a, b []memgraph.SystemData) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s.Name] = true
	}
	for _, s := range b {
		if !set[s.Name] {
			return false
		}
	}
	return true
}

func sameStationNameSet(a, b []memgraph.StationData) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s.Name] = true
	}
	for _, s := range b {
		if !set[s.Name] {
			return false
		}
	}
	return true
}
