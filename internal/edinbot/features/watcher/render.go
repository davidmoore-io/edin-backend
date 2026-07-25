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
// Snapshot.LastUpdatedAt is intentionally NOT in the hash — if relational state
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
		Reinforcement    *int64
		Undermining      *int64
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
		Reinforcement:    s.Reinforcement,
		Undermining:      s.Undermining,
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

// powerColour returns the embed's left sidebar colour. Originally tier-
// based (Stronghold green, Fortified amber, else red), but red on
// Exploited systems read as alarmist when most Kaine systems sit there
// by default — operators preferred a uniform green accent. Kept as a
// function (not a const) so we can reintroduce tiering later without
// touching the call site.
func powerColour(powerplayState string) int {
	return 0x22c55e
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
	renderPowerplayBlock(&d, snap)

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

// conflictEntry mirrors the per-power objects inside the
// PowerplayConflictProgress array the eddn-listener stores as JSON. Field
// names match the EDDN journal schema (capitalised) so the JSON unmarshal
// is direct.
type conflictEntry struct {
	Power            string  `json:"Power"`
	ConflictProgress float64 `json:"ConflictProgress"`
}

// isConflictState returns true for powerplay states where the system has
// no single controller and per-power ConflictProgress is the meaningful
// signal — Expansion (no power yet, multiple powers competing) and
// Contested (a former controller is being challenged). Mirrors the
// frontend's isExpansionState / isContestedState in
// edin-frontend/src/pages/powerplay/types/powerplay.js so the bot embed
// agrees with the kaine system modal at edin.space/powerplay.
func isConflictState(powerplayState string) bool {
	s := strings.ToLower(powerplayState)
	return strings.Contains(s, "expansion") || strings.Contains(s, "contested")
}

// isUnoccupiedState returns true for the explicit Unoccupied state where
// no powerplay activity is happening at all. Rendering should suppress
// the merit/conflict block entirely and show a placeholder.
func isUnoccupiedState(powerplayState string) bool {
	return strings.EqualFold(strings.TrimSpace(powerplayState), "unoccupied")
}

// renderPowerplayBlock writes the body of the **Powerplay** section,
// dispatching on PowerplayState. Three buckets:
//
//   - Conflict (Expansion / Contested) — render per-power ConflictProgress
//     percentages. No controlling power, no reinforcement/undermining
//     merits (those fields are zero during a conflict cycle).
//   - Unoccupied — render a single placeholder line. Suppress everything
//     else; the system has no active powerplay state.
//   - Controlled (Stronghold / Fortified / Exploited / fallback) — render
//     controller · state · control% line, then Reinforcement /
//     Undermining merit counts. This is the path that existed before
//     conflict-state handling was added.
//
// Each branch is responsible for emitting its own "Contested by:" tail
// where applicable; the conflict branch encodes that information in the
// ConflictProgress list directly so a separate line is redundant.
func renderPowerplayBlock(d *strings.Builder, snap *controlclient.SystemWatchSnapshot) {
	switch {
	case isConflictState(snap.PowerplayState):
		renderConflictBlock(d, snap)
	case isUnoccupiedState(snap.PowerplayState):
		fmt.Fprintln(d, "Unoccupied — no powerplay activity")
	default:
		renderControlledBlock(d, snap)
	}
}

// renderControlledBlock is the original Stronghold/Fortified/Exploited
// path — controller, state, control%, then merit counts.
func renderControlledBlock(d *strings.Builder, snap *controlclient.SystemWatchSnapshot) {
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
	fmt.Fprintln(d, powerLine)
	if snap.Reinforcement != nil || snap.Undermining != nil {
		var reinf, undr int64
		if snap.Reinforcement != nil {
			reinf = *snap.Reinforcement
		}
		if snap.Undermining != nil {
			undr = *snap.Undermining
		}
		fmt.Fprintf(d, "Reinforcement: %s · Undermining: %s\n", thousands(reinf), thousands(undr))
	}
	if contested := contestedPowers(snap); len(contested) > 0 {
		fmt.Fprintf(d, "Contested by: %s\n", strings.Join(contested, ", "))
	}
}

// renderConflictBlock handles Expansion and Contested. Sorts entries by
// ConflictProgress DESC so the leading power is first; sub-entries
// (other competing powers) follow underneath. Falls back to the bare
// `Powers` list when ConflictProgress is missing or unparseable — rare,
// but possible if the eddn-listener stored a malformed payload.
func renderConflictBlock(d *strings.Builder, snap *controlclient.SystemWatchSnapshot) {
	// State header line — shows just the state so operators can see
	// they're looking at a conflict cycle, not a controlled system.
	fmt.Fprintln(d, snap.PowerplayState)

	entries := parseConflictProgress(snap.PowerplayConflictProgress)
	if len(entries) == 0 {
		// Fallback: show the bare power list. Better than nothing,
		// avoids a "no data" line that would mask the powers
		// themselves.
		if len(snap.Powers) > 0 {
			fmt.Fprintf(d, "Competing: %s\n", strings.Join(snap.Powers, ", "))
		}
		return
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ConflictProgress > entries[j].ConflictProgress
	})
	for _, e := range entries {
		fmt.Fprintf(d, "%s · %.1f%%\n", e.Power, e.ConflictProgress*100)
	}
}

// parseConflictProgress decodes the snapshot's opaque JSON-blob field
// into a typed slice. Returns nil for empty / malformed input — the
// caller treats nil and empty identically (both fall back to the bare
// power list).
func parseConflictProgress(raw json.RawMessage) []conflictEntry {
	if len(raw) == 0 {
		return nil
	}
	var out []conflictEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// thousands formats an int with comma thousands-separators ("112,086").
// Standalone helper rather than pulling in golang.org/x/text/message —
// the bot's render path runs in a tight poll loop and we don't need
// locale-aware formatting; the embed is English-only.
func thousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
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
