package dayz

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EconomyStats represents the parsed economy statistics from DayZ server logs
type EconomyStats struct {
	ParsedAt      time.Time `json:"parsed_at"`
	LogFile       string    `json:"log_file,omitempty"`
	ServerStarted time.Time `json:"server_started,omitempty"`

	// Overall stats from LootRespawner
	NominalItems int     `json:"nominal_items"` // Target items in world
	TotalInMap   int     `json:"total_in_map"`  // Actual items in world
	FillPercent  float64 `json:"fill_percent"`  // Percentage of nominal

	// Storage breakdown by area
	StorageAreas []StorageArea `json:"storage_areas"`
	TotalStored  int           `json:"total_stored"`

	// Dynamic events
	DynamicEvents int `json:"dynamic_events"`
	EventSpawns   int `json:"event_spawn_positions"`

	// Type information
	TypesConfigured  int `json:"types_configured"`
	PrototypesLoaded int `json:"prototypes_loaded"`
	GroupsLoaded     int `json:"groups_loaded"`

	// Vehicles (separate from loot)
	VehicleCount int `json:"vehicle_count"`

	// Spawner activity (if log_ce_lootspawn enabled)
	RecentSpawns []SpawnEvent   `json:"recent_spawns,omitempty"`
	SpawnCounts  map[string]int `json:"spawn_counts,omitempty"` // Item type -> count spawned
}

// StorageArea represents items stored in a specific area/file
type StorageArea struct {
	Name      string `json:"name"`
	ItemCount int    `json:"item_count"`
}

// SpawnEvent represents a single item spawn event
type SpawnEvent struct {
	Timestamp time.Time `json:"timestamp"`
	ItemType  string    `json:"item_type"`
	Position  string    `json:"position,omitempty"`
}

// Regular expressions for parsing CE log entries
var (
	// Time pattern at start of log lines: "14:11:47"
	reLogTime = regexp.MustCompile(`^(\d{2}:\d{2}:\d{2})\s+(.*)$`)

	// [CE][LootRespawner] (PRIDummy) :: Initially (re)spawned:0, Nominal:11297, Total in Map: 10260 at 0 (sec)
	reLootRespawner = regexp.MustCompile(`\[CE\]\[LootRespawner\].*Nominal:(\d+),\s*Total in Map:\s*(\d+)`)

	// [CE][Storage] Restoring file "/path/to/file.bin" 130 items.
	reStorageRestore = regexp.MustCompile(`\[CE\]\[Storage\] Restoring file "([^"]+)"\s*(\d+)?\s*items?`)

	// [CE][TypeSetup] :: 573 classes setuped...
	reTypeSetup = regexp.MustCompile(`\[CE\]\[TypeSetup\] :: (\d+) classes`)

	// [CE][LoadPrototype] :: loaded 524 prototypes
	rePrototypes = regexp.MustCompile(`\[CE\]\[LoadPrototype\] :: loaded (\d+) prototypes`)

	// [CE][LoadMap] "Group" :: loaded 8331 groups
	reLoadMap = regexp.MustCompile(`\[CE\]\[LoadMap\] "Group" :: loaded (\d+) groups`)

	// [CE][DynamicEvent] Load  Events:[42] Primary spawners: 42 Secondary spawners: 3
	reDynamicEvents = regexp.MustCompile(`\[CE\]\[DynamicEvent\] Load\s+Events:\[(\d+)\]`)

	// [CE][DE][SPAWNS] :: Total positions: 665
	reEventSpawns = regexp.MustCompile(`\[CE\]\[DE\]\[SPAWNS\] :: Total positions: (\d+)`)

	// [CE][Hive] :: Init sequence finished.
	reInitFinished = regexp.MustCompile(`\[CE\]\[Hive\] :: Init sequence finished`)

	// Item spawn logging (when log_ce_lootspawn=true)
	// Format may vary, common patterns:
	// [CE][Spawn] ItemType at position
	reItemSpawn = regexp.MustCompile(`\[CE\].*[Ss]pawn.*?(\w+)\s+at\s+([\d\.\-,\s]+)`)
)

// ParseEconomyLogs parses DayZ RPT log content and extracts economy statistics
func ParseEconomyLogs(r io.Reader, logFile string) (*EconomyStats, error) {
	stats := &EconomyStats{
		ParsedAt:     time.Now(),
		LogFile:      logFile,
		StorageAreas: make([]StorageArea, 0),
		SpawnCounts:  make(map[string]int),
		RecentSpawns: make([]SpawnEvent, 0),
	}

	scanner := bufio.NewScanner(r)
	var baseDate time.Time // We'll try to extract from filename or use today

	// Try to extract date from log filename (format: DayZServer_2025-12-31_14-11-22.RPT)
	if logFile != "" {
		if parts := regexp.MustCompile(`(\d{4}-\d{2}-\d{2})`).FindStringSubmatch(logFile); len(parts) > 1 {
			if d, err := time.Parse("2006-01-02", parts[1]); err == nil {
				baseDate = d
			}
		}
	}
	if baseDate.IsZero() {
		baseDate = time.Now()
	}

	for scanner.Scan() {
		line := scanner.Text()

		// Extract timestamp if present
		var logTime time.Time
		if matches := reLogTime.FindStringSubmatch(line); len(matches) > 2 {
			if t, err := time.Parse("15:04:05", matches[1]); err == nil {
				logTime = time.Date(baseDate.Year(), baseDate.Month(), baseDate.Day(),
					t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
			}
			line = matches[2] // Rest of line without timestamp
		}

		// Skip non-CE lines for efficiency
		if !strings.Contains(line, "[CE]") {
			continue
		}

		// Parse different CE log types
		if matches := reLootRespawner.FindStringSubmatch(line); len(matches) > 2 {
			stats.NominalItems, _ = strconv.Atoi(matches[1])
			stats.TotalInMap, _ = strconv.Atoi(matches[2])
			if stats.NominalItems > 0 {
				stats.FillPercent = float64(stats.TotalInMap) / float64(stats.NominalItems) * 100
			}
			continue
		}

		if matches := reStorageRestore.FindStringSubmatch(line); len(matches) > 1 {
			area := StorageArea{Name: extractAreaName(matches[1])}
			if len(matches) > 2 && matches[2] != "" {
				area.ItemCount, _ = strconv.Atoi(matches[2])
			}
			// Special handling for vehicles
			if strings.Contains(matches[1], "vehicles") {
				stats.VehicleCount = area.ItemCount
			}
			stats.StorageAreas = append(stats.StorageAreas, area)
			stats.TotalStored += area.ItemCount
			continue
		}

		if matches := reTypeSetup.FindStringSubmatch(line); len(matches) > 1 {
			stats.TypesConfigured, _ = strconv.Atoi(matches[1])
			continue
		}

		if matches := rePrototypes.FindStringSubmatch(line); len(matches) > 1 {
			count, _ := strconv.Atoi(matches[1])
			stats.PrototypesLoaded += count
			continue
		}

		if matches := reLoadMap.FindStringSubmatch(line); len(matches) > 1 {
			stats.GroupsLoaded, _ = strconv.Atoi(matches[1])
			continue
		}

		if matches := reDynamicEvents.FindStringSubmatch(line); len(matches) > 1 {
			stats.DynamicEvents, _ = strconv.Atoi(matches[1])
			continue
		}

		if matches := reEventSpawns.FindStringSubmatch(line); len(matches) > 1 {
			stats.EventSpawns, _ = strconv.Atoi(matches[1])
			continue
		}

		if reInitFinished.MatchString(line) && !logTime.IsZero() {
			stats.ServerStarted = logTime
			continue
		}

		// Track item spawns if logging enabled
		if matches := reItemSpawn.FindStringSubmatch(line); len(matches) > 1 {
			itemType := matches[1]
			stats.SpawnCounts[itemType]++
			if len(stats.RecentSpawns) < 100 { // Keep last 100 spawns
				event := SpawnEvent{
					Timestamp: logTime,
					ItemType:  itemType,
				}
				if len(matches) > 2 {
					event.Position = matches[2]
				}
				stats.RecentSpawns = append(stats.RecentSpawns, event)
			}
		}
	}

	return stats, scanner.Err()
}

// extractAreaName extracts a friendly name from the storage file path
func extractAreaName(path string) string {
	// /dayz/DayZServer/mpmissions/dayzOffline.sakhal/storage_1/data/dynamic_004.bin
	// -> "dynamic_004"
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		filename := parts[len(parts)-1]
		return strings.TrimSuffix(filename, ".bin")
	}
	return path
}

// FormatSummary returns a human-readable summary of the economy stats
func (s *EconomyStats) FormatSummary() string {
	var sb strings.Builder

	sb.WriteString("📊 **DayZ Economy Statistics**\n\n")

	if !s.ServerStarted.IsZero() {
		sb.WriteString(fmt.Sprintf("🕐 Server Started: %s\n", s.ServerStarted.Format("15:04:05")))
	}

	sb.WriteString(fmt.Sprintf("📦 **Loot Overview**\n"))
	sb.WriteString(fmt.Sprintf("   • Items in World: **%d** / %d nominal (%.1f%%)\n",
		s.TotalInMap, s.NominalItems, s.FillPercent))
	sb.WriteString(fmt.Sprintf("   • Stored Items: %d\n", s.TotalStored))
	sb.WriteString(fmt.Sprintf("   • Vehicles: %d\n", s.VehicleCount))

	sb.WriteString(fmt.Sprintf("\n🎯 **Events**\n"))
	sb.WriteString(fmt.Sprintf("   • Dynamic Events: %d\n", s.DynamicEvents))
	sb.WriteString(fmt.Sprintf("   • Spawn Positions: %d\n", s.EventSpawns))

	sb.WriteString(fmt.Sprintf("\n⚙️ **Configuration**\n"))
	sb.WriteString(fmt.Sprintf("   • Item Types: %d\n", s.TypesConfigured))
	sb.WriteString(fmt.Sprintf("   • Prototypes: %d\n", s.PrototypesLoaded))
	sb.WriteString(fmt.Sprintf("   • Map Groups: %d\n", s.GroupsLoaded))

	// Show storage breakdown if interesting
	if len(s.StorageAreas) > 0 {
		sb.WriteString(fmt.Sprintf("\n📁 **Storage Areas** (top 5)\n"))

		// Sort by item count descending
		sorted := make([]StorageArea, len(s.StorageAreas))
		copy(sorted, s.StorageAreas)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].ItemCount > sorted[j].ItemCount
		})

		for i, area := range sorted {
			if i >= 5 {
				break
			}
			if area.ItemCount > 0 {
				sb.WriteString(fmt.Sprintf("   • %s: %d items\n", area.Name, area.ItemCount))
			}
		}
	}

	// Show spawn activity if available
	if len(s.SpawnCounts) > 0 {
		sb.WriteString(fmt.Sprintf("\n🆕 **Recent Spawn Activity** (top 10)\n"))

		// Sort by count descending
		type kv struct {
			Key   string
			Value int
		}
		var sorted []kv
		for k, v := range s.SpawnCounts {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Value > sorted[j].Value
		})

		for i, item := range sorted {
			if i >= 10 {
				break
			}
			sb.WriteString(fmt.Sprintf("   • %s: %d spawned\n", item.Key, item.Value))
		}
	}

	return sb.String()
}

// FormatCompact returns a compact one-line summary for quick status checks
func (s *EconomyStats) FormatCompact() string {
	return fmt.Sprintf("Loot: %d/%d (%.0f%%) | Vehicles: %d | Events: %d",
		s.TotalInMap, s.NominalItems, s.FillPercent, s.VehicleCount, s.DynamicEvents)
}
