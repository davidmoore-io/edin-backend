package controlclient

import (
	"encoding/json"
	"time"
)

// Buyer is the union of fields the bot consumes from Plasmium and LTD
// responses. Both backend types (kaine.PlasmiumBuyer and kaine.LTDBuyer) share
// most fields; the commodity columns are mutually exclusive (only one set is
// populated per response). Mirrored from internal/kaine/{plasmium,ltd}.go's
// json tags exactly.
type Buyer struct {
	SystemName      string   `json:"system_name"`
	StationName     string   `json:"station_name"`
	Faction         string   `json:"faction"`
	FactionState    string   `json:"faction_state"`
	Economies       []string `json:"economies,omitempty"`
	DistanceLy      float64  `json:"distance_ly"`
	PowerplayState  string   `json:"powerplay_state"`
	DistanceToKaine float64  `json:"distance_to_kaine,omitempty"`
	KaineProgress   *float64 `json:"kaine_progress,omitempty"`
	LargestPad      string   `json:"largest_pad"`

	// Plasmium-only.
	PlatinumDemand int64 `json:"platinum_demand,omitempty"`
	PlatinumPrice  int64 `json:"platinum_price,omitempty"`
	OsmiumDemand   int64 `json:"osmium_demand,omitempty"`
	OsmiumPrice    int64 `json:"osmium_price,omitempty"`

	// LTD-only.
	LTDDemand int64 `json:"ltd_demand,omitempty"`
	LTDPrice  int64 `json:"ltd_price,omitempty"`

	Score       float64 `json:"score"`
	ScoreReason string  `json:"score_reason"`
	RankScore   float64 `json:"rank_score"`

	BGSUpdatedAt    *time.Time `json:"bgs_updated_at,omitempty"`
	MarketUpdatedAt *time.Time `json:"market_updated_at,omitempty"`
}

// MiningMap is the per-map envelope. Both Plasmium and LTD responses share
// this shape (the LTD-specific live_power_state and search_radius_ly are
// included as omitempty so a Plasmium response decodes cleanly).
type MiningMap struct {
	SystemName     string  `json:"system_name"`
	Body           string  `json:"body,omitempty"`
	RingType       string  `json:"ring_type,omitempty"`
	ReserveLevel   string  `json:"reserve_level,omitempty"`
	PowerState     string  `json:"power_state,omitempty"`
	LivePowerState string  `json:"live_power_state,omitempty"`
	SearchRadiusLy int     `json:"search_radius_ly,omitempty"`
	Buyers         []Buyer `json:"buyers"`

	// Map1 is the operator-curated URL for this bubble's primary mining map
	// page (e.g. ring screenshots, in-game tooling). Surfaced in the bot's
	// Discord embed as the "Map" link. The kaine API response also exposes
	// Map2/Map3 + titles + commodity tags; we only consume Map1 today —
	// "first available link wins" keeps the bot embed clean.
	Map1 string `json:"map_1,omitempty"`
}

// PlasmiumBuyersResponse mirrors kaine.PlasmiumBuyersResponse.
type PlasmiumBuyersResponse struct {
	Maps        []MiningMap `json:"maps"`
	GeneratedAt time.Time   `json:"generated_at"`
	TotalMaps   int         `json:"total_maps"`
	TotalBuyers int         `json:"total_buyers"`
}

// LTDBuyersResponse mirrors kaine.LTDBuyersResponse.
type LTDBuyersResponse struct {
	Maps        []MiningMap `json:"maps"`
	GeneratedAt time.Time   `json:"generated_at"`
	TotalMaps   int         `json:"total_maps"`
	TotalBuyers int         `json:"total_buyers"`
}

// IsStructurallyValid is the spec §6 'response is healthy' sentinel. Returns
// true only if the envelope has both a non-zero GeneratedAt and a non-nil
// Maps slice. Empty Maps slice (zero-result) IS valid.
func (r *PlasmiumBuyersResponse) IsStructurallyValid() bool {
	return r != nil && !r.GeneratedAt.IsZero() && r.Maps != nil
}

func (r *LTDBuyersResponse) IsStructurallyValid() bool {
	return r != nil && !r.GeneratedAt.IsZero() && r.Maps != nil
}

// SystemWatchSnapshot mirrors memgraph.SystemWatchSnapshot — the JSON tags
// match exactly so the bot can decode the API response without a
// translation layer. Kept in sync by virtue of the integration smoke
// test in Phase 6 (an actual /api round-trip would catch any drift).
type SystemWatchSnapshot struct {
	Slug                      string          `json:"slug"`
	Name                      string          `json:"name"`
	Allegiance                string          `json:"allegiance,omitempty"`
	ControllingFaction        string          `json:"controlling_faction,omitempty"`
	ControllingFactionState   string          `json:"controlling_faction_state,omitempty"`
	ControllingPower          string          `json:"controlling_power,omitempty"`
	PowerplayState            string          `json:"powerplay_state,omitempty"`
	Powers                    []string        `json:"powers,omitempty"`
	ControlProgress           *float64        `json:"control_progress,omitempty"`
	PowerplayConflictProgress json.RawMessage `json:"powerplay_conflict_progress,omitempty"`
	Factions                  []WatchFaction  `json:"factions"`
	LastUpdatedAt             time.Time       `json:"last_updated_at"`
}

// WatchFaction mirrors memgraph.WatchFaction — same json tags.
type WatchFaction struct {
	Name      string  `json:"name"`
	State     string  `json:"state,omitempty"`
	Influence float64 `json:"influence,omitempty"`
}

// DiagnoseReport mirrors /admin/diagnose's response shape (spec §5).
type DiagnoseReport struct {
	CheckedAt time.Time              `json:"checked_at"`
	Results   map[string]ProbeResult `json:"results"`
}

type ProbeResult struct {
	OK             bool            `json:"ok"`
	LatencyMs      int             `json:"latency_ms,omitempty"`
	Error          string          `json:"error,omitempty"`
	ContainerState *ContainerState `json:"container_status,omitempty"`
	LastMessageAt  *time.Time      `json:"last_message_at,omitempty"`
}

type ContainerState struct {
	Status string `json:"status"`
	Health string `json:"health,omitempty"`
}
