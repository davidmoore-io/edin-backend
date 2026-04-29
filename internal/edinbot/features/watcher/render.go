package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
)

// permalinkBase is the kaine UI's host. Wrapped in a const so a future
// staging environment can swap it in via build tag if we ever need to
// render bot embeds against a non-prod backend.
const permalinkBase = "https://edin.space/kaine/systems/"

// stateHash returns a deterministic hash of the snapshot fields the render
// surfaces. Used by the watch loop to skip Discord edits when a poll
// returns the same data the message already shows. Sensitive to:
//
//   - Powerplay block (controlling power, state, contested powers, control
//     progress, conflict-progress raw payload).
//   - Allegiance, controlling faction + faction state.
//   - Faction list (name, state, influence) in sorted order.
//
// Snapshot.LastUpdatedAt is intentionally NOT in the hash — if Memgraph
// touches a row with no actual content change, we don't want to spam an
// edit just because a timestamp moved. The "Last updated" line on the
// embed reads from LastUpdatedAt directly, so the operator still sees
// the latest server-known refresh time when an edit *does* happen.
func stateHash(s *controlclient.SystemWatchSnapshot) string {
	type hashable struct {
		Allegiance       string
		ControlFaction   string
		ControlState     string
		Power            string
		PowerState       string
		Powers           []string
		ControlProgress  *float64
		ConflictProgress json.RawMessage
		Factions         []controlclient.WatchFaction
	}
	h := hashable{
		Allegiance:       s.Allegiance,
		ControlFaction:   s.ControllingFaction,
		ControlState:     s.ControllingFactionState,
		Power:            s.ControllingPower,
		PowerState:       s.PowerplayState,
		Powers:           append([]string(nil), s.Powers...),
		ControlProgress:  s.ControlProgress,
		ConflictProgress: s.PowerplayConflictProgress,
		Factions:         append([]controlclient.WatchFaction(nil), s.Factions...),
	}
	// Powers is order-stable from the API but we sort defensively to
	// guard against a future ordering-change in the graph that would
	// otherwise force a no-content edit.
	sort.Strings(h.Powers)
	sort.SliceStable(h.Factions, func(i, j int) bool {
		if h.Factions[i].Influence != h.Factions[j].Influence {
			return h.Factions[i].Influence > h.Factions[j].Influence
		}
		return h.Factions[i].Name < h.Factions[j].Name
	})
	buf, _ := json.Marshal(h)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// StateHashForTest exposes stateHash to the package's external test suite.
// Production callers (the Watcher loop) use stateHash through a closure-
// over-fields path inside this package; the rendered hash is part of the
// public contract operator-visible behaviour relies on, so locking it in
// tests is appropriate.
func StateHashForTest(s *controlclient.SystemWatchSnapshot) string {
	return stateHash(s)
}

// powerColour maps a powerplay state to the embed's colour-bar tier.
// Stronghold (high control) → green; Fortified → amber; anything else
// (Exploited / Contested / Unoccupied) → red. Operators wanted a
// glance-cue alongside the data block; one consistent dimension of
// colour beats per-faction colour picking.
func powerColour(powerplayState string) int {
	switch strings.ToLower(powerplayState) {
	case "stronghold":
		return 0x22c55e
	case "fortified":
		return 0xeab308
	default:
		return 0xef4444
	}
}

// Render builds the embed for a watched system. Pure function: no side
// effects, no clock reads — caller passes the timestamps + the user-id it
// wants encoded.
//
// watchedBy is the Discord user-id of whoever ran /watch; rendered as
// a `<@id>` mention so the channel feed shows who owns the watch.
// Empty string ⇒ no "by …" suffix (e.g. boot recovery on a row whose
// creator-id wasn't set in the original DB schema).
//
// Layout:
//
//	### 👁 [HIP 61332](edin.space/kaine/systems/HIP61332)
//	Allegiance: Federation
//
//	**Powerplay**
//	<power> · <state> · <progress%>
//	Contested by: ...
//
//	**Factions**
//	<faction> · <state> · <influence%>
//	...
//
//	Watch started: <t:N:R> by <@user>
//	Last updated: <t:M:R>
func Render(snap *controlclient.SystemWatchSnapshot, watchedAt int64, watchedBy string) *discordgo.MessageEmbed {
	if snap == nil {
		return &discordgo.MessageEmbed{Description: "*system data unavailable*"}
	}

	var d strings.Builder
	fmt.Fprintf(&d, "### 👁 [%s](%s%s)\n", snap.Name, permalinkBase, snap.Slug)

	if snap.Allegiance != "" {
		fmt.Fprintf(&d, "Allegiance: %s\n", snap.Allegiance)
	}

	d.WriteByte('\n')
	d.WriteString("**Powerplay**\n")
	powerLine := snap.ControllingPower
	if powerLine == "" {
		powerLine = "*no controlling power*"
	}
	if snap.PowerplayState != "" {
		powerLine += " · " + snap.PowerplayState
	}
	if snap.ControlProgress != nil {
		powerLine += fmt.Sprintf(" · %.0f%% control", *snap.ControlProgress*100)
	}
	fmt.Fprintln(&d, powerLine)
	if contested := contestedPowers(snap); len(contested) > 0 {
		fmt.Fprintf(&d, "Contested by: %s\n", strings.Join(contested, ", "))
	}

	d.WriteByte('\n')
	d.WriteString("**Factions**\n")
	if snap.ControllingFaction != "" {
		fmt.Fprintf(&d, "_Controlling: %s", snap.ControllingFaction)
		if snap.ControllingFactionState != "" && snap.ControllingFactionState != "None" {
			fmt.Fprintf(&d, " (%s)", snap.ControllingFactionState)
		}
		d.WriteString("_\n")
	}
	for _, f := range snap.Factions {
		// Factions arrive influence-DESC sorted from the snapshotter; we
		// preserve that order so the most-significant faction is on top.
		state := f.State
		if state == "" || state == "None" {
			state = "—"
		}
		fmt.Fprintf(&d, "%s · %s · %.1f%%\n", f.Name, state, f.Influence*100)
	}
	if len(snap.Factions) == 0 {
		d.WriteString("_no faction data_\n")
	}

	d.WriteByte('\n')
	fmt.Fprintf(&d, "Watch started: <t:%d:R>", watchedAt)
	if watchedBy != "" {
		fmt.Fprintf(&d, " by <@%s>", watchedBy)
	}
	d.WriteByte('\n')
	if !snap.LastUpdatedAt.IsZero() {
		fmt.Fprintf(&d, "Last updated: <t:%d:R>", snap.LastUpdatedAt.Unix())
	}

	return &discordgo.MessageEmbed{
		Description: d.String(),
		Color:       powerColour(snap.PowerplayState),
	}
}

// contestedPowers returns the list of non-controlling powers attached to
// the system. The API exposes Powers as the full set of involved powers;
// "contested" is everything except the current controller. When there's
// no controller (Unoccupied), the whole list is contested.
func contestedPowers(snap *controlclient.SystemWatchSnapshot) []string {
	out := make([]string, 0, len(snap.Powers))
	for _, p := range snap.Powers {
		if p == snap.ControllingPower {
			continue
		}
		out = append(out, p)
	}
	return out
}
