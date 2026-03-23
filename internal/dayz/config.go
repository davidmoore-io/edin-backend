package dayz

// MapConfigs holds calibration and display info for supported maps.
var MapConfigs = map[string]MapConfig{
	"sakhal": {
		Name:        "sakhal",
		DisplayName: "Sakhal",
		ImageURL:    "/dayz-maps/sakhal/map.png",
		WorldSize:   12800,
		ImageWidth:  4096,
		ImageHeight: 3700,
		Calibration: Calibration{
			XScale:  0.2423,
			ZScale:  0.2416,
			XOffset: 170,
			ZOffset: 3705,
		},
	},
	"chernarusplus": {
		Name:        "chernarusplus",
		DisplayName: "Chernarus",
		ImageURL:    "/dayz-maps/chernarus/map.png",
		WorldSize:   15360,
		ImageWidth:  8192,
		ImageHeight: 8192,
		Calibration: Calibration{
			// Chernarus uses a simpler 1:1 mapping typically
			XScale:  0.533,
			ZScale:  0.533,
			XOffset: 0,
			ZOffset: 8192,
		},
	},
	"enoch": {
		Name:        "enoch",
		DisplayName: "Livonia",
		ImageURL:    "/dayz-maps/livonia/map.png",
		WorldSize:   12800,
		ImageWidth:  8192,
		ImageHeight: 8192,
		Calibration: Calibration{
			XScale:  0.64,
			ZScale:  0.64,
			XOffset: 0,
			ZOffset: 8192,
		},
	},
}

// GetMapConfig returns the configuration for a map name.
// It handles mission folder name format (e.g., "dayzOffline.sakhal" → "sakhal").
func GetMapConfig(mapName string) (MapConfig, bool) {
	// Try direct lookup first
	if cfg, ok := MapConfigs[mapName]; ok {
		return cfg, true
	}

	// Try extracting map name from mission folder format
	// e.g., "dayzOffline.sakhal" → "sakhal"
	if len(mapName) > 12 && mapName[:12] == "dayzOffline." {
		shortName := mapName[12:]
		if cfg, ok := MapConfigs[shortName]; ok {
			return cfg, true
		}
	}

	// Default to sakhal if unknown
	if cfg, ok := MapConfigs["sakhal"]; ok {
		return cfg, true
	}

	return MapConfig{}, false
}

// EventColors maps event types to display colors.
var EventColors = map[string]string{
	// Seasonal
	"StaticChristmasTree": "#22c55e",
	"StaticSantaCrash":    "#ef4444",
	"InfectedSanta":       "#dc2626",

	// Crashes & Events
	"StaticHeliCrash": "#f59e0b",
	"StaticBonfire":   "#ff6b35",
	"StaticAmbulance": "#ff69b4",

	// Boats
	"StaticBoatFishing":  "#06b6d4",
	"StaticBoatMilitary": "#3b82f6",

	// Vehicles
	"VehicleCivilianSedan":    "#8b5cf6",
	"VehicleHatchback02":      "#a855f7",
	"VehicleOffroadHatchback": "#6366f1",
	"VehicleSedan02":          "#7c3aed",
	"VehicleBoat":             "#0ea5e9",
	"VehicleMilitaryBoat":     "#1d4ed8",
	"VehicleTruck01":          "#dc2626",

	// Static
	"StaticContainerLocked": "#fbbf24",
	"StaticPoliceCar":       "#2563eb",
	"StaticScientist":       "#10b981",
	"ItemPlanks":            "#92400e",
}

// GetEventColor returns the display color for an event type.
func GetEventColor(eventName string) string {
	if color, ok := EventColors[eventName]; ok {
		return color
	}
	return "#888888" // Default gray
}
