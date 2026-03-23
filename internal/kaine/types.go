// Package kaine provides types and storage for Nakato Kaine powerplay objectives.
package kaine

import (
	"time"
)

// Priority levels for objectives
const (
	PriorityHigh   = "high"
	PriorityMedium = "medium"
	PriorityLow    = "low"
)

// Objective types
const (
	TypeAcquisition   = "acquisition"
	TypeReinforcement = "reinforcement"
	TypeUndermining   = "undermining"
)

// Objective categories (tags for objectives, not to be confused with boards)
const (
	CategoryStandard   = "standard"
	CategoryMiningBoom = "mining_boom"
	CategorySafety     = "safety"
)

// Objective boards (the three main objective lists)
const (
	BoardMain        = "main"
	BoardOps         = "ops"
	BoardThunderdome = "thunderdome"
)

// ValidBoards is the list of valid board values.
var ValidBoards = []string{BoardMain, BoardOps, BoardThunderdome}

// Objective states
const (
	StateDraft     = "draft"
	StateApproved  = "approved"
	StateActive    = "active"
	StateCompleted = "completed"
	StateCancelled = "cancelled"
	StateArchived  = "archived"
)

// ValidStateTransitions defines which state transitions are allowed.
// Key is the current state, value is the list of valid next states.
var ValidStateTransitions = map[string][]string{
	StateDraft:     {StateApproved, StateCancelled},
	StateApproved:  {StateActive, StateDraft, StateCancelled},
	StateActive:    {StateCompleted, StateCancelled},
	StateCompleted: {StateArchived},
	StateCancelled: {StateArchived},
	StateArchived:  {}, // terminal state - no transitions allowed
}

// IsValidStateTransition checks if transitioning from one state to another is allowed.
func IsValidStateTransition(from, to string) bool {
	validTargets, ok := ValidStateTransitions[from]
	if !ok {
		return false
	}
	for _, valid := range validTargets {
		if valid == to {
			return true
		}
	}
	return false
}

// IsValidBoard checks if the given board value is valid.
func IsValidBoard(board string) bool {
	for _, b := range ValidBoards {
		if b == board {
			return true
		}
	}
	return false
}

// Access levels
const (
	AccessPublic = "public"
	AccessPledge = "pledge"
	AccessOps    = "ops"
)

// ExternalLink represents a link to an external resource (maps, docs, etc.)
type ExternalLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Objective represents a Kaine powerplay objective.
type Objective struct {
	ID                string         `json:"id"`
	SystemName        string         `json:"system_name"`
	Priority          string         `json:"priority"`
	ObjectiveType     string         `json:"objective_type"`
	Category          string         `json:"category"`
	Board             string         `json:"board"`
	Title             string         `json:"title,omitempty"`
	Description       string         `json:"description,omitempty"`
	BGSNotes          string         `json:"bgs_notes,omitempty"`
	MeritMethods      []string       `json:"merit_methods,omitempty"`
	ExternalLinks     []ExternalLink `json:"external_links,omitempty"`
	State             string         `json:"state"`
	AccessLevel       string         `json:"access_level"`
	CreatedAt         time.Time      `json:"created_at"`
	CreatedBy         string         `json:"created_by,omitempty"`
	CreatedByName     string         `json:"created_by_name,omitempty"`
	UpdatedAt         time.Time      `json:"updated_at"`
	UpdatedBy         string         `json:"updated_by,omitempty"`
	ApprovedAt        *time.Time     `json:"approved_at,omitempty"`
	ApprovedBy        string         `json:"approved_by,omitempty"`
	PublishAt         *time.Time     `json:"publish_at,omitempty"`
	CompletedAt       *time.Time     `json:"completed_at,omitempty"`
	ArchivedAt        *time.Time     `json:"archived_at,omitempty"`
	SuggestedComplete int            `json:"suggested_complete"`
	MeritTarget       *int64         `json:"merit_target,omitempty"`
	CycleNumber       *int           `json:"cycle_number,omitempty"`
}

// CreateObjectiveInput represents the input for creating a new objective.
type CreateObjectiveInput struct {
	SystemName    string         `json:"system_name"`
	Priority      string         `json:"priority"`
	ObjectiveType string         `json:"objective_type"`
	Category      string         `json:"category,omitempty"`
	Board         string         `json:"board,omitempty"` // Defaults to "main" if not specified
	Title         string         `json:"title,omitempty"`
	Description   string         `json:"description,omitempty"`
	BGSNotes      string         `json:"bgs_notes,omitempty"`
	MeritMethods  []string       `json:"merit_methods,omitempty"`
	ExternalLinks []ExternalLink `json:"external_links,omitempty"`
	AccessLevel   string         `json:"access_level,omitempty"`
	MeritTarget   *int64         `json:"merit_target,omitempty"`
	CycleNumber   *int           `json:"cycle_number,omitempty"`
	PublishAt     *time.Time     `json:"publish_at,omitempty"`
}

// UpdateObjectiveInput represents the input for updating an objective.
type UpdateObjectiveInput struct {
	SystemName    *string        `json:"system_name,omitempty"`
	Priority      *string        `json:"priority,omitempty"`
	ObjectiveType *string        `json:"objective_type,omitempty"`
	Category      *string        `json:"category,omitempty"`
	Board         *string        `json:"board,omitempty"`
	Title         *string        `json:"title,omitempty"`
	Description   *string        `json:"description,omitempty"`
	BGSNotes      *string        `json:"bgs_notes,omitempty"`
	MeritMethods  []string       `json:"merit_methods,omitempty"`
	ExternalLinks []ExternalLink `json:"external_links,omitempty"`
	State         *string        `json:"state,omitempty"`
	AccessLevel   *string        `json:"access_level,omitempty"`
	MeritTarget   *int64         `json:"merit_target,omitempty"`
	CycleNumber   *int           `json:"cycle_number,omitempty"`
	PublishAt     *time.Time     `json:"publish_at,omitempty"`
}

// ObjectiveSuggestion represents a user's suggestion that an objective is complete.
type ObjectiveSuggestion struct {
	ObjectiveID     string    `json:"objective_id"`
	DiscordUserID   string    `json:"discord_user_id"`
	DiscordUsername string    `json:"discord_username,omitempty"`
	SuggestedAt     time.Time `json:"suggested_at"`
}

// ListObjectivesFilter defines filters for listing objectives.
type ListObjectivesFilter struct {
	Board          string   `json:"board,omitempty"`           // Filter by board (main, ops, thunderdome)
	State          string   `json:"state,omitempty"`           // Filter by specific state
	States         []string `json:"states,omitempty"`          // Filter by multiple states
	IncludeArchived bool    `json:"include_archived,omitempty"` // Include archived objectives (default: false)
	AccessLevels   []string `json:"access_levels,omitempty"`   // Filter to these access levels
	ObjectiveType  string   `json:"objective_type,omitempty"`
	Category       string   `json:"category,omitempty"`
	CycleNumber    *int     `json:"cycle_number,omitempty"`
}

// ObjectiveCounts holds counts of objectives by board and state.
type ObjectiveCounts struct {
	ByBoard map[string]int            `json:"by_board"` // Count per board (active objectives only)
	ByState map[string]map[string]int `json:"by_state"` // Count per board per state
}

// ProgressFunc is called by long-running store methods to report progress.
// step is the current step (1-based), total is the total number of steps,
// and message describes what is currently happening.
// If nil, progress reporting is skipped.
type ProgressFunc func(step int, total int, message string)

// Ring type constants
const (
	RingMetallic  = "Metallic"
	RingMetalRich = "Metal Rich"
	RingIcy       = "Icy"
)

// Reserve level constants
const (
	ReservePristine = "Pristine"
	ReserveMajor    = "Major"
	ReserveCommon   = "Common"
	ReserveDepleted = "Depleted"
	ReserveLow      = "Low"
)

// MiningMap represents a mining hotspot map for a planetary ring.
type MiningMap struct {
	ID               int       `json:"id"`
	SystemName       string    `json:"system_name"`
	Body             string    `json:"body"`
	RingType         string    `json:"ring_type,omitempty"`
	ReserveLevel     string    `json:"reserve_level,omitempty"`
	PowerState       string    `json:"power_state,omitempty"`
	RESSites         string    `json:"res_sites,omitempty"`
	Hotspots         []string  `json:"hotspots,omitempty"`
	Map1             string    `json:"map_1,omitempty"`
	Map1Title        string    `json:"map_1_title,omitempty"`
	Map1Commodity    []string  `json:"map_1_commodity,omitempty"`
	Map2             string    `json:"map_2,omitempty"`
	Map2Title        string    `json:"map_2_title,omitempty"`
	Map2Commodity    []string  `json:"map_2_commodity,omitempty"`
	Map3             string    `json:"map_3,omitempty"`
	Map3Title        string    `json:"map_3_title,omitempty"`
	Map3Commodity    []string  `json:"map_3_commodity,omitempty"`
	SearchURL        string    `json:"search_url,omitempty"`
	ExpansionFaction string    `json:"expansion_faction,omitempty"`
	Notes            string    `json:"notes,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	CreatedBy        string    `json:"created_by,omitempty"`
}

// CreateMiningMapInput represents the input for creating a new mining map.
// Note: power_state is not stored here - it comes from Memgraph live data.
type CreateMiningMapInput struct {
	SystemName       string   `json:"system_name"`
	Body             string   `json:"body"`
	RingType         string   `json:"ring_type,omitempty"`
	ReserveLevel     string   `json:"reserve_level,omitempty"`
	RESSites         string   `json:"res_sites,omitempty"`
	Hotspots         []string `json:"hotspots,omitempty"`
	Map1             string   `json:"map_1,omitempty"`
	Map1Title        string   `json:"map_1_title,omitempty"`
	Map1Commodity    []string `json:"map_1_commodity,omitempty"`
	Map2             string   `json:"map_2,omitempty"`
	Map2Title        string   `json:"map_2_title,omitempty"`
	Map2Commodity    []string `json:"map_2_commodity,omitempty"`
	Map3             string   `json:"map_3,omitempty"`
	Map3Title        string   `json:"map_3_title,omitempty"`
	Map3Commodity    []string `json:"map_3_commodity,omitempty"`
	SearchURL        string   `json:"search_url,omitempty"`
	ExpansionFaction string   `json:"expansion_faction,omitempty"`
	Notes            string   `json:"notes,omitempty"`
}

// UpdateMiningMapInput represents the input for updating a mining map.
// Note: power_state is not stored here - it comes from Memgraph live data.
type UpdateMiningMapInput struct {
	SystemName       *string  `json:"system_name,omitempty"`
	Body             *string  `json:"body,omitempty"`
	RingType         *string  `json:"ring_type,omitempty"`
	ReserveLevel     *string  `json:"reserve_level,omitempty"`
	RESSites         *string  `json:"res_sites,omitempty"`
	Hotspots         []string `json:"hotspots,omitempty"`
	Map1             *string  `json:"map_1,omitempty"`
	Map1Title        *string  `json:"map_1_title,omitempty"`
	Map1Commodity    []string `json:"map_1_commodity,omitempty"`
	Map2             *string  `json:"map_2,omitempty"`
	Map2Title        *string  `json:"map_2_title,omitempty"`
	Map2Commodity    []string `json:"map_2_commodity,omitempty"`
	Map3             *string  `json:"map_3,omitempty"`
	Map3Title        *string  `json:"map_3_title,omitempty"`
	Map3Commodity    []string `json:"map_3_commodity,omitempty"`
	SearchURL        *string  `json:"search_url,omitempty"`
	ExpansionFaction *string  `json:"expansion_faction,omitempty"`
	Notes            *string  `json:"notes,omitempty"`
}

// ListMiningMapsFilter defines filters for listing mining maps.
type ListMiningMapsFilter struct {
	SystemName   string `json:"system_name,omitempty"`
	PowerState   string `json:"power_state,omitempty"`
	RingType     string `json:"ring_type,omitempty"`
	ReserveLevel string `json:"reserve_level,omitempty"`
	Hotspot      string `json:"hotspot,omitempty"` // Filter by commodity in hotspots array
}

// CommodityLabelToKey maps human-readable commodity labels to internal keys.
// Used when importing XLSX files where columns contain display labels.
var CommodityLabelToKey = map[string]string{
	"platinum":             "platinum",
	"osmium":               "osmium",
	"ltd":                  "lowtemperaturediamond",
	"low temperature diamond":  "lowtemperaturediamond",
	"low temperature diamonds": "lowtemperaturediamond",
	"painite":              "painite",
	"rhodplumsite":         "rhodplumsite",
	"serendibite":          "serendibite",
	"gold":                 "gold",
	"silver":               "silver",
	"tritium":              "tritium",
}

// ValidCommodityKeys is the set of valid commodity key values.
var ValidCommodityKeys = map[string]bool{
	"platinum":             true,
	"osmium":               true,
	"lowtemperaturediamond": true,
	"painite":              true,
	"rhodplumsite":         true,
	"serendibite":          true,
	"gold":                 true,
	"silver":               true,
	"tritium":              true,
}

// ImportMiningMapRowResult describes the outcome of importing a single XLSX row.
type ImportMiningMapRowResult struct {
	Row        int      `json:"row"`
	SystemName string   `json:"system_name"`
	Body       string   `json:"body"`
	Action     string   `json:"action"` // "created", "updated", "skipped", "error"
	Error      string   `json:"error,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// ImportMiningMapsResponse is the response from the import endpoint.
type ImportMiningMapsResponse struct {
	TotalRows      int                        `json:"total_rows"`
	Created        int                        `json:"created"`
	Updated        int                        `json:"updated"`
	Skipped        int                        `json:"skipped"`
	Errors         int                        `json:"errors"`
	Results        []ImportMiningMapRowResult  `json:"results"`
	InvalidSystems []string                   `json:"invalid_systems,omitempty"`
}
