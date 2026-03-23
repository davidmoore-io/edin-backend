// Package dayz provides DayZ server data querying and caching.
package dayz

import "time"

// SpawnPoint represents a single spawn location in the game world.
type SpawnPoint struct {
	X float64 `json:"x"`
	Z float64 `json:"z"`
	A float64 `json:"a,omitempty"` // Rotation angle (optional)
}

// EventSpawns holds spawn points for a specific event type.
type EventSpawns struct {
	Name   string       `json:"name"`
	Points []SpawnPoint `json:"points"`
	Count  int          `json:"count"`
}

// MapSpawnData contains all spawn data for a map.
type MapSpawnData struct {
	MapName     string                  `json:"map_name"`
	Events      map[string]*EventSpawns `json:"events"`
	TotalPoints int                     `json:"total_points"`
	FetchedAt   time.Time               `json:"fetched_at"`
	CachedUntil time.Time               `json:"cached_until"`
}

// MapConfig holds configuration for a specific map.
type MapConfig struct {
	Name        string      `json:"name"`
	DisplayName string      `json:"display_name"`
	ImageURL    string      `json:"image_url"`
	WorldSize   int         `json:"world_size"`
	ImageWidth  int         `json:"image_width"`
	ImageHeight int         `json:"image_height"`
	Calibration Calibration `json:"calibration"`
}

// Calibration holds coordinate transformation values for a map.
type Calibration struct {
	XScale  float64 `json:"x_scale"`  // Pixels per game meter (X axis)
	ZScale  float64 `json:"z_scale"`  // Pixels per game meter (Z axis)
	XOffset float64 `json:"x_offset"` // Pixel X when game X = 0
	ZOffset float64 `json:"z_offset"` // Pixel Y when game Z = 0
}

// ServerStatus holds current DayZ server information.
type ServerStatus struct {
	Online     bool   `json:"online"`
	Name       string `json:"name"`
	Map        string `json:"map"`
	Players    int    `json:"players"`
	MaxPlayers int    `json:"max_players"`
	Version    string `json:"version"`
}

// SpawnCategory groups related spawn types for the frontend.
type SpawnCategory struct {
	Name   string   `json:"name"`
	Icon   string   `json:"icon"`
	Color  string   `json:"color"`
	Events []string `json:"events"`
}

// DefaultCategories returns the spawn categories for the map UI.
func DefaultCategories() []SpawnCategory {
	return []SpawnCategory{
		{
			Name:   "Seasonal",
			Icon:   "🎄",
			Color:  "#22c55e",
			Events: []string{"StaticChristmasTree", "StaticSantaCrash", "InfectedSanta"},
		},
		{
			Name:   "Crashes",
			Icon:   "💥",
			Color:  "#f59e0b",
			Events: []string{"StaticHeliCrash", "StaticBonfire", "StaticAmbulance"},
		},
		{
			Name:   "Boats",
			Icon:   "⛵",
			Color:  "#06b6d4",
			Events: []string{"StaticBoatFishing", "StaticBoatMilitary"},
		},
		{
			Name:  "Vehicles",
			Icon:  "🚗",
			Color: "#8b5cf6",
			Events: []string{
				"VehicleCivilianSedan", "VehicleHatchback02", "VehicleOffroadHatchback",
				"VehicleSedan02", "VehicleBoat", "VehicleMilitaryBoat", "VehicleTruck01",
			},
		},
		{
			Name:   "Static",
			Icon:   "📦",
			Color:  "#fbbf24",
			Events: []string{"StaticContainerLocked", "StaticPoliceCar", "StaticScientist", "ItemPlanks"},
		},
	}
}
