package watcher_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features/watcher"
)

// loadSnapshot reads a real-shaped fixture from testdata/. The fixtures
// are committed JSON dumped from the control-API's response shape, so the
// test is exercising the same byte sequence the bot will see at runtime.
// No mocking, no monkey-patching, no synthetic structs.
func loadSnapshot(t *testing.T, name string) *controlclient.SystemWatchSnapshot {
	t.Helper()
	buf, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	var snap controlclient.SystemWatchSnapshot
	require.NoError(t, json.Unmarshal(buf, &snap))
	return &snap
}

func TestRender_HIP61332Fixture(t *testing.T) {
	snap := loadSnapshot(t, "snapshot_HIP61332.json")
	embed := watcher.Render(snap, 1714400000) // arbitrary fixed unix ts

	require.NotNil(t, embed)
	desc := embed.Description

	// Title row uses the slug for the kaine permalink, not the name.
	// This is exactly the round-trip the bot promises commanders: the
	// link they click matches what they'd type.
	require.Contains(t, desc, "[HIP 61332](https://edin.space/kaine/systems/HIP61332)",
		"title must use slug-form permalink, not URL-encoded name")

	// Allegiance line.
	require.Contains(t, desc, "Allegiance: Federation")

	// Powerplay block — controller, state, and a control% rendered to 0
	// decimals (operators don't need precision past percent).
	require.Contains(t, desc, "**Powerplay**")
	require.Contains(t, desc, "Felicia Winters · Stronghold · 67% control")

	// Contested powers = Powers minus the controller. Ordering is
	// preserved from the snapshot — operators who care about contesting
	// often want to know the *order of precedence* in which the
	// challengers appear.
	require.Contains(t, desc, "Contested by: Aisling Duval, Jerome Archer")

	// Factions block — controlling faction is highlighted; subsequent
	// rows are influence-desc as ordered by the snapshot.
	require.Contains(t, desc, "**Factions**")
	require.Contains(t, desc, "_Controlling: Alliance of HIP 61332 (Boom)_")
	require.Contains(t, desc,
		"Alliance of HIP 61332 · Boom · 42.1%")
	require.Contains(t, desc,
		"HIP 61332 Empire League · Election · 28.5%")
	// State "None" is rendered as a placeholder em-dash (— specifically)
	// rather than literal "None" — gentler on the eye in the embed.
	require.Contains(t, desc, "HIP 61332 Free · — · 19.4%")

	// Watch metadata. Watch-started is fixed by the caller; data-updated
	// comes from the snapshot. Both render as live-relative tokens.
	require.Contains(t, desc, "Watch started: <t:1714400000:R>")
	require.Contains(t, desc, "Data updated: <t:")

	// Stronghold → green colour bar.
	require.Equal(t, 0x22c55e, embed.Color)
}

func TestRender_NoFactionsDataPath(t *testing.T) {
	// Real-shaped snapshot but with an empty factions list — happens
	// for newly-discovered systems before BGS data lands. The render
	// must still emit a coherent embed, not an awkward gap.
	snap := &controlclient.SystemWatchSnapshot{
		Slug: "BleepBlorp", Name: "Bleep Blorp",
		ControllingPower: "Pranav Antal",
		PowerplayState:   "Fortified",
	}
	embed := watcher.Render(snap, 1714400000)

	desc := embed.Description
	require.Contains(t, desc, "**Factions**")
	require.Contains(t, desc, "_no faction data_",
		"empty faction list must render an explicit placeholder, not a void")

	// Fortified → amber.
	require.Equal(t, 0xeab308, embed.Color)
}

func TestRender_UnoccupiedColourTier(t *testing.T) {
	// No controlling power → red tier. Mirrors the typical "frontier"
	// system where the watch is set up to track first-power capture.
	snap := &controlclient.SystemWatchSnapshot{
		Slug: "Wregoe", Name: "Wregoe",
		PowerplayState: "Unoccupied",
	}
	embed := watcher.Render(snap, 1714400000)
	require.Equal(t, 0xef4444, embed.Color)
	require.Contains(t, embed.Description, "*no controlling power*")
}

// stateHash determinism: two calls on identical data must agree, and a
// trivial shuffle of fields the watcher considers must NOT change the
// hash. This is the contract the watch loop relies on to skip needless
// Discord edits.
func TestStateHash_Deterministic(t *testing.T) {
	snap := loadSnapshot(t, "snapshot_HIP61332.json")
	h1 := watcher.StateHashForTest(snap)
	h2 := watcher.StateHashForTest(snap)
	require.Equal(t, h1, h2, "stateHash on identical input must agree")

	// Powers permuted — the hash must NOT move (sorted internally).
	snap2 := loadSnapshot(t, "snapshot_HIP61332.json")
	snap2.Powers = []string{"Jerome Archer", "Felicia Winters", "Aisling Duval"}
	h3 := watcher.StateHashForTest(snap2)
	require.Equal(t, h1, h3, "Powers ordering must not affect stateHash")

	// Faction influence shifted — hash MUST move (real change).
	snap3 := loadSnapshot(t, "snapshot_HIP61332.json")
	snap3.Factions[0].Influence = 0.55
	h4 := watcher.StateHashForTest(snap3)
	require.NotEqual(t, h1, h4, "Faction influence change must move the hash")

	// Last-updated-at moved but no content change — hash must NOT move.
	// This is the property that prevents idle "this row was touched in
	// memgraph" updates from triggering Discord edits.
	snap4 := loadSnapshot(t, "snapshot_HIP61332.json")
	snap4.LastUpdatedAt = snap4.LastUpdatedAt.AddDate(0, 0, 7)
	h5 := watcher.StateHashForTest(snap4)
	require.Equal(t, h1, h5, "LastUpdatedAt alone must not affect stateHash")
}

// belt-and-braces: the embed never carries an empty description even
// when called with nil — important for the watcher loop's recovery path
// after a Memgraph 5xx where we fall back to the "data unavailable" stub.
func TestRender_NilSnapshotIsSafe(t *testing.T) {
	embed := watcher.Render(nil, 0)
	require.NotNil(t, embed)
	require.NotEmpty(t, strings.TrimSpace(embed.Description))
}
