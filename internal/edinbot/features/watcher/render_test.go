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
	embed := watcher.Render(snap, 1714400000, "") // arbitrary fixed unix ts

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
	require.Contains(t, desc, "Last updated: <t:")

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
	embed := watcher.Render(snap, 1714400000, "")

	desc := embed.Description
	require.Contains(t, desc, "**Factions**")
	require.Contains(t, desc, "_no faction data_",
		"empty faction list must render an explicit placeholder, not a void")

	// Sidebar is now uniformly green — see powerColour comment for why.
	require.Equal(t, 0x22c55e, embed.Color)
}

func TestRender_ReinforcementUnderminingNumbers(t *testing.T) {
	// Operators want the absolute counts, not just the % control.
	// Verify both render with comma thousands-separators on a single
	// line, and that the line appears even when only one of the two
	// fields is populated (a freshly-tracked system after cycle reset
	// might have undermining=0 but reinforcement set, or vice versa).
	r := int64(112086)
	u := int64(93297)
	snap := &controlclient.SystemWatchSnapshot{
		Slug: "LTT4042", Name: "LTT 4042",
		ControllingPower: "Nakato Kaine",
		PowerplayState:   "Exploited",
		Reinforcement:    &r,
		Undermining:      &u,
	}
	embed := watcher.Render(snap, 1714400000, "")
	require.Contains(t, embed.Description, "Reinforcement: 112,086 · Undermining: 93,297")

	// State-hash sensitivity: changing one of the numbers must change
	// the hash, otherwise the watcher loop will skip the edit.
	h1 := watcher.StateHashForTest(snap)
	r2 := int64(112087)
	snap.Reinforcement = &r2
	h2 := watcher.StateHashForTest(snap)
	require.NotEqual(t, h1, h2,
		"reinforcement delta must change state-hash so watcher edits the embed")
}

func TestRender_UnoccupiedState(t *testing.T) {
	// Unoccupied systems suppress the merit/conflict block entirely
	// in favour of a one-liner placeholder, mirroring how the Kaine
	// system modal at edin.space/powerplay handles the state.
	snap := &controlclient.SystemWatchSnapshot{
		Slug: "Wregoe", Name: "Wregoe",
		PowerplayState: "Unoccupied",
	}
	embed := watcher.Render(snap, 1714400000, "")
	require.Equal(t, 0x22c55e, embed.Color)
	require.Contains(t, embed.Description, "Unoccupied — no powerplay activity")
	require.NotContains(t, embed.Description, "Reinforcement",
		"Unoccupied systems must not render merit lines")
	require.NotContains(t, embed.Description, "% control",
		"Unoccupied systems have no control progress")
}

func TestRender_ExpansionState_PerPowerProgress(t *testing.T) {
	// Expansion-state systems carry no controller and zero merits;
	// the meaningful data is the per-power ConflictProgress in the
	// PowerplayConflictProgress JSON blob. Real shape from Nadur's
	// EDDN feed (Apr 30 21:00 UTC).
	r := int64(0)
	u := int64(0)
	snap := &controlclient.SystemWatchSnapshot{
		Slug: "Nadur", Name: "Nadur",
		PowerplayState: "Expansion",
		Powers:         []string{"Aisling Duval", "Nakato Kaine"},
		Reinforcement:  &r,
		Undermining:    &u,
		PowerplayConflictProgress: json.RawMessage(
			`[{"Power":"Aisling Duval","ConflictProgress":0.1056},` +
				`{"Power":"Nakato Kaine","ConflictProgress":0.355875}]`),
	}
	embed := watcher.Render(snap, 1714400000, "")
	desc := embed.Description

	// State header line.
	require.Contains(t, desc, "**Powerplay**\nExpansion\n")

	// Per-power progress, leading power first (Kaine 35.6% > Aisling 10.6%).
	kainePos := strings.Index(desc, "Nakato Kaine · 35.6%")
	aislingPos := strings.Index(desc, "Aisling Duval · 10.6%")
	require.GreaterOrEqual(t, kainePos, 0, "Kaine entry must render with one-decimal percent")
	require.GreaterOrEqual(t, aislingPos, 0, "Aisling entry must render with one-decimal percent")
	require.Less(t, kainePos, aislingPos,
		"Conflict entries must sort by ConflictProgress DESC — leader first")

	// The Expansion path must NOT emit the controlled-system fields.
	require.NotContains(t, desc, "Reinforcement",
		"Expansion systems have zero merits; render must omit the line")
	require.NotContains(t, desc, "% control",
		"Expansion systems have no control progress")
	require.NotContains(t, desc, "*no controlling power*",
		"Expansion handler renders state line, not the controlled-system placeholder")
}

func TestRender_ContestedState_PerPowerProgress(t *testing.T) {
	// Contested systems use the same per-power progress rendering as
	// Expansion. The state classifier matches the substring "contested".
	snap := &controlclient.SystemWatchSnapshot{
		Slug: "HIP1", Name: "HIP 1",
		PowerplayState: "Contested",
		PowerplayConflictProgress: json.RawMessage(
			`[{"Power":"Felicia Winters","ConflictProgress":0.42},` +
				`{"Power":"Aisling Duval","ConflictProgress":0.31}]`),
	}
	embed := watcher.Render(snap, 1714400000, "")
	require.Contains(t, embed.Description, "**Powerplay**\nContested\n")
	require.Contains(t, embed.Description, "Felicia Winters · 42.0%")
	require.Contains(t, embed.Description, "Aisling Duval · 31.0%")
}

func TestRender_ConflictState_FallbackToPowerList(t *testing.T) {
	// Edge case: PowerplayConflictProgress missing (eddn-listener never
	// wrote it, or stored an empty payload). Render must fall back to
	// the bare Powers array rather than emit "no data" — operators
	// still want to know who's competing.
	snap := &controlclient.SystemWatchSnapshot{
		Slug: "X1", Name: "X 1",
		PowerplayState: "Expansion",
		Powers:         []string{"Power A", "Power B"},
		// PowerplayConflictProgress intentionally nil
	}
	embed := watcher.Render(snap, 1714400000, "")
	require.Contains(t, embed.Description, "Competing: Power A, Power B")
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
	embed := watcher.Render(nil, 0, "")
	require.NotNil(t, embed)
	require.NotEmpty(t, strings.TrimSpace(embed.Description))
}

// Watch-by attribution: "Watch started: <t:N:R> by <@user>" must appear
// when watchedBy is supplied; the user-id renders as a Discord mention
// so the channel feed shows who started the watch.
func TestRender_WatchByAttribution(t *testing.T) {
	snap := loadSnapshot(t, "snapshot_HIP61332.json")
	embed := watcher.Render(snap, 1714400000, "user-12345")
	require.Contains(t, embed.Description, "Watch started: <t:1714400000:R> by <@user-12345>")
	require.Contains(t, embed.Description, "Last updated: <t:")
}

func TestRender_WatchByOmittedWhenEmpty(t *testing.T) {
	snap := loadSnapshot(t, "snapshot_HIP61332.json")
	embed := watcher.Render(snap, 1714400000, "")
	require.Contains(t, embed.Description, "Watch started: <t:1714400000:R>")
	require.NotContains(t, embed.Description, " by <@",
		"empty user-id must NOT render a malformed mention")
}
