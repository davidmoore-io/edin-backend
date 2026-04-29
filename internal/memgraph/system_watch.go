package memgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// SystemWatchSnapshot is the lean payload the bot's /watch feature renders
// into a Discord embed. Distinct from SystemDetailResponse (which carries
// bodies and stations for the orrery view) — the watch render only cares
// about powerplay state and faction state, so a tighter struct keeps the
// state-hash narrow and avoids needless edits.
//
// Field choices:
//   - PowerplayState / ControllingPower / Powers / ControlProgress are the
//     "is this system worth caring about right now" signals operators
//     scan for.
//   - PowerplayConflictProgress is opaque JSON in the graph (the eddn-
//     listener serialises a custom struct as a string); we pass it through
//     as RawMessage so the bot doesn't need to know its shape — it gets
//     hashed verbatim and rendered selectively.
//   - Factions sorted by influence DESC for deterministic rendering.
//   - LastUpdatedAt is the *latest* of the system-level and faction-level
//     update timestamps so the bot's "Last updated" line reflects the
//     most recent change of any tracked field.
type SystemWatchSnapshot struct {
	Slug                      string          `json:"slug"`
	Name                      string          `json:"name"`
	Allegiance                string          `json:"allegiance,omitempty"`
	ControllingFaction        string          `json:"controlling_faction,omitempty"`
	ControllingWatchFaction   string          `json:"controlling_faction_state,omitempty"`
	ControllingPower          string          `json:"controlling_power,omitempty"`
	PowerplayState            string          `json:"powerplay_state,omitempty"`
	Powers                    []string        `json:"powers,omitempty"`
	ControlProgress           *float64        `json:"control_progress,omitempty"`
	PowerplayConflictProgress json.RawMessage `json:"powerplay_conflict_progress,omitempty"`
	Factions                  []WatchFaction  `json:"factions"`
	LastUpdatedAt             time.Time       `json:"last_updated_at"`
}

// WatchFaction pairs a faction with its current state and influence in the
// watched system. Sourced from the (Faction)-[:PRESENT_IN]->(System)
// relationship's properties (state, influence). Named distinctly from the
// existing memgraph.FactionState (BGS-states slice) to avoid collision.
type WatchFaction struct {
	Name      string  `json:"name"`
	State     string  `json:"state,omitempty"`
	Influence float64 `json:"influence,omitempty"`
}

// GetSystemWatchSnapshot fetches the powerplay + faction state for a single
// system addressed by its slug. Returns ErrSystemNotFound if no row matches
// the slug. The query is intentionally a single round-trip — the watch
// poller hits this for every watched system every 120s, so we keep the
// graph traversal minimal.
func (c *Client) GetSystemWatchSnapshot(ctx context.Context, slug string) (*SystemWatchSnapshot, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System {slug: $slug})
		OPTIONAL MATCH (f:Faction)-[r:PRESENT_IN]->(s)
		WITH s,
		     collect({name: f.name, state: r.state, influence: r.influence}) AS faction_rows,
		     CASE WHEN s.last_faction_update IS NOT NULL AND s.last_faction_update > coalesce(s.last_eddn_update, s.last_faction_update)
		          THEN s.last_faction_update
		          ELSE coalesce(s.last_eddn_update, s.last_faction_update)
		     END AS last_updated_at
		RETURN s.slug                       AS slug,
		       s.name                       AS name,
		       s.allegiance                 AS allegiance,
		       s.controlling_faction        AS controlling_faction,
		       s.controlling_faction_state  AS controlling_faction_state,
		       s.controlling_power          AS controlling_power,
		       s.powerplay_state            AS powerplay_state,
		       s.powers                     AS powers,
		       s.control_progress           AS control_progress,
		       s.powerplay_conflict_progress AS powerplay_conflict_progress,
		       faction_rows                 AS factions,
		       last_updated_at              AS last_updated_at
		LIMIT 1
	`

	result, err := session.Run(ctx, query, map[string]any{"slug": slug})
	if err != nil {
		return nil, fmt.Errorf("watch snapshot query: %w", err)
	}
	if !result.Next(ctx) {
		return nil, ErrSystemNotFound
	}
	rec := result.Record()

	out := &SystemWatchSnapshot{}
	if v, ok := rec.Get("slug"); ok && v != nil {
		out.Slug = v.(string)
	}
	if v, ok := rec.Get("name"); ok && v != nil {
		out.Name = v.(string)
	}
	if v, ok := rec.Get("allegiance"); ok && v != nil {
		out.Allegiance = v.(string)
	}
	if v, ok := rec.Get("controlling_faction"); ok && v != nil {
		out.ControllingFaction = v.(string)
	}
	if v, ok := rec.Get("controlling_faction_state"); ok && v != nil {
		out.ControllingWatchFaction = v.(string)
	}
	if v, ok := rec.Get("controlling_power"); ok && v != nil {
		out.ControllingPower = v.(string)
	}
	if v, ok := rec.Get("powerplay_state"); ok && v != nil {
		out.PowerplayState = v.(string)
	}
	if v, ok := rec.Get("powers"); ok && v != nil {
		if arr, ok := v.([]any); ok {
			for _, p := range arr {
				if s, ok := p.(string); ok {
					out.Powers = append(out.Powers, s)
				}
			}
		}
	}
	if v, ok := rec.Get("control_progress"); ok && v != nil {
		f := toFloat64(v)
		out.ControlProgress = &f
	}
	if v, ok := rec.Get("powerplay_conflict_progress"); ok && v != nil {
		if s, ok := v.(string); ok && s != "" {
			out.PowerplayConflictProgress = json.RawMessage(s)
		}
	}
	if v, ok := rec.Get("factions"); ok && v != nil {
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				name, _ := m["name"].(string)
				if name == "" {
					// OPTIONAL MATCH with no faction emits a single nil-name row;
					// skip rather than emit an empty WatchFaction.
					continue
				}
				fs := WatchFaction{Name: name}
				if s, ok := m["state"].(string); ok {
					fs.State = s
				}
				if v, ok := m["influence"]; ok && v != nil {
					fs.Influence = toFloat64(v)
				}
				out.Factions = append(out.Factions, fs)
			}
		}
	}
	if v, ok := rec.Get("last_updated_at"); ok && v != nil {
		out.LastUpdatedAt = toTime(v)
	}

	// Deterministic faction ordering: influence DESC, name ASC tiebreaker.
	// Stable order matters for state-hashing — without it, identical
	// snapshots from two consecutive polls might disagree just because
	// Memgraph's collect() is unordered.
	sort.SliceStable(out.Factions, func(i, j int) bool {
		if out.Factions[i].Influence != out.Factions[j].Influence {
			return out.Factions[i].Influence > out.Factions[j].Influence
		}
		return out.Factions[i].Name < out.Factions[j].Name
	})

	return out, nil
}
