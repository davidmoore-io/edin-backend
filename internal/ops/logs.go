package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogEntry represents a single structured log record.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level,omitempty"`
	Message   string    `json:"message"`
}

// DayZ RPT log directory - where the actual game server logs are stored.
// The server-manager container logs only show status messages, not player activity.
//
// TODO: Investigate why dayz-server-manager's IngameReport shows "0 players" even
// when players are connected. The manager likely uses Steam Query (port 2305) which
// may require challenge-response that's failing intermittently. For now, we read
// the RPT logs directly which have accurate player connection information.
const dayzRPTLogDir = "/srv/dayz/data/DayZServer/profiles"

// Player-related keywords for filtering DayZ RPT logs.
// These capture login attempts, connections, disconnections, kicks, and auth issues.
var dayzPlayerKeywords = []string{
	"Player",
	"player",
	"Login",
	"login",
	"connect",
	"Connect",
	"disconnect",
	"Disconnect",
	"kicked",
	"Kicked",
	"Auth",
	"queue",
	"Queue",
}

// TailLogs returns a slice of the most recent logs for the provided service.
func (m *Manager) TailLogs(ctx context.Context, service string, lines int) ([]LogEntry, error) {
	if lines <= 0 {
		lines = m.cfg.LogTailDefault
	}

	// Special case for DayZ: read from RPT log files instead of container logs
	// The container logs only show server-manager status (e.g., "0 players")
	// but RPT logs show actual game events (player connects, disconnects, etc.)
	if service == "dayz" {
		return m.tailDayZRPTLogs(ctx, lines, false)
	}

	// dayz-player: filtered view showing only player activity (logins, kicks, etc.)
	// Useful for diagnosing connection issues like AuthPlayerLoginState timeouts
	// See: https://feedback.bistudio.com/T175043
	if service == "dayz-player" {
		return m.tailDayZRPTLogs(ctx, lines, true)
	}

	def, err := m.containerForService(service)
	if err != nil {
		return nil, err
	}

	args := []string{"logs", "--tail", strconv.Itoa(lines), "--timestamps", def.Container}
	output, err := m.runCommand(ctx, m.cfg.DockerBinary, args...)
	if err != nil {
		return nil, err
	}

	rawLines := strings.Split(strings.TrimSpace(output), "\n")
	entries := make([]LogEntry, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry := parseLogLine(line)
		entries = append(entries, entry)
	}
	return entries, nil
}

// tailDayZRPTLogs reads the latest DayZ RPT log file and returns the last N lines.
// RPT logs contain actual game events: player logins, disconnects, errors, etc.
// If playerOnly is true, filters to only show player-related lines.
func (m *Manager) tailDayZRPTLogs(ctx context.Context, lines int, playerOnly bool) ([]LogEntry, error) {
	// Find the latest .RPT file
	rptFile, err := findLatestRPTFile(dayzRPTLogDir)
	if err != nil {
		m.logger.Warn("Failed to find DayZ RPT log, falling back to container logs: " + err.Error())
		return m.tailContainerLogs(ctx, "dayz", lines)
	}

	// For player-only logs, we need to read more lines and filter
	// because player events are sparse in the RPT log
	readLines := lines
	if playerOnly {
		readLines = lines * 50 // Read 50x more lines to find enough player events
	}

	// Use tail command to get last N lines
	args := []string{"-n", strconv.Itoa(readLines), rptFile}
	output, err := m.runCommand(ctx, "tail", args...)
	if err != nil {
		m.logger.Warn("Failed to tail DayZ RPT log, falling back to container logs: " + err.Error())
		return m.tailContainerLogs(ctx, "dayz", lines)
	}

	rawLines := strings.Split(strings.TrimSpace(output), "\n")
	entries := make([]LogEntry, 0, len(rawLines))

	// Get the date from the RPT filename (format: DayZServer_2025-12-29_04-00-25.RPT)
	baseDate := extractDateFromRPTFilename(filepath.Base(rptFile))

	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// If filtering for player-only, check if line contains player keywords
		if playerOnly && !containsPlayerKeyword(line) {
			continue
		}

		entry := parseRPTLogLine(line, baseDate)
		entries = append(entries, entry)

		// Stop once we have enough filtered entries
		if playerOnly && len(entries) >= lines {
			break
		}
	}
	return entries, nil
}

// containsPlayerKeyword checks if a log line contains player-related content.
func containsPlayerKeyword(line string) bool {
	for _, keyword := range dayzPlayerKeywords {
		if strings.Contains(line, keyword) {
			return true
		}
	}
	return false
}

// tailContainerLogs is the fallback for when RPT logs can't be read.
func (m *Manager) tailContainerLogs(ctx context.Context, service string, lines int) ([]LogEntry, error) {
	def, err := m.containerForService(service)
	if err != nil {
		return nil, err
	}

	args := []string{"logs", "--tail", strconv.Itoa(lines), "--timestamps", def.Container}
	output, err := m.runCommand(ctx, m.cfg.DockerBinary, args...)
	if err != nil {
		return nil, err
	}

	rawLines := strings.Split(strings.TrimSpace(output), "\n")
	entries := make([]LogEntry, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry := parseLogLine(line)
		entries = append(entries, entry)
	}
	return entries, nil
}

// findLatestRPTFile finds the most recently modified .RPT file in the given directory.
func findLatestRPTFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	var rptFiles []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToUpper(entry.Name()), ".RPT") {
			rptFiles = append(rptFiles, entry)
		}
	}

	if len(rptFiles) == 0 {
		return "", os.ErrNotExist
	}

	// Sort by modification time (newest first)
	sort.Slice(rptFiles, func(i, j int) bool {
		infoI, _ := rptFiles[i].Info()
		infoJ, _ := rptFiles[j].Info()
		if infoI == nil || infoJ == nil {
			return false
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})

	return filepath.Join(dir, rptFiles[0].Name()), nil
}

// extractDateFromRPTFilename extracts the date from an RPT filename.
// Format: DayZServer_2025-12-29_04-00-25.RPT -> 2025-12-29
func extractDateFromRPTFilename(filename string) time.Time {
	// Try to parse: DayZServer_YYYY-MM-DD_HH-MM-SS.RPT
	parts := strings.Split(filename, "_")
	if len(parts) >= 2 {
		dateStr := parts[1] // "2025-12-29"
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

// parseRPTLogLine parses a DayZ RPT log line.
// Format: " HH:MM:SS Message content here"
// The leading space and time are consistent in RPT logs.
func parseRPTLogLine(line string, baseDate time.Time) LogEntry {
	line = strings.TrimSpace(line)

	// RPT lines typically start with a timestamp like "10:23:45"
	if len(line) >= 8 && line[2] == ':' && line[5] == ':' {
		timeStr := line[:8]
		message := strings.TrimSpace(line[8:])

		// Parse the time and combine with base date
		if t, err := time.Parse("15:04:05", timeStr); err == nil {
			ts := time.Date(
				baseDate.Year(), baseDate.Month(), baseDate.Day(),
				t.Hour(), t.Minute(), t.Second(), 0, time.UTC,
			)
			return LogEntry{
				Timestamp: ts,
				Level:     extractRPTLevel(message),
				Message:   message,
			}
		}
	}

	// Fallback: no timestamp found
	return LogEntry{
		Timestamp: time.Now().UTC(),
		Message:   line,
	}
}

// extractRPTLevel tries to identify the log level from RPT message content.
func extractRPTLevel(message string) string {
	msgLower := strings.ToLower(message)
	switch {
	case strings.Contains(msgLower, "error"):
		return "ERROR"
	case strings.Contains(msgLower, "warning"):
		return "WARN"
	case strings.Contains(msgLower, "has connected"):
		return "INFO"
	case strings.Contains(msgLower, "disconnected"):
		return "INFO"
	case strings.Contains(msgLower, "kicked"):
		return "WARN"
	case strings.Contains(msgLower, "login"):
		return "INFO"
	default:
		return ""
	}
}

func parseLogLine(line string) LogEntry {
	fields := strings.SplitN(line, " ", 2)
	if len(fields) < 2 {
		return LogEntry{
			Timestamp: time.Now().UTC(),
			Message:   line,
		}
	}

	tsString := strings.TrimSpace(fields[0])
	messagePortion := strings.TrimSpace(fields[1])

	ts, err := time.Parse(time.RFC3339Nano, tsString)
	if err != nil {
		ts, _ = time.Parse(time.RFC3339, tsString)
		if ts.IsZero() {
			ts = time.Now().UTC()
			messagePortion = strings.TrimSpace(line)
		}
	}

	level, message := extractLevel(messagePortion)
	return LogEntry{
		Timestamp: ts,
		Level:     level,
		Message:   message,
	}
}

func extractLevel(message string) (string, string) {
	parts := strings.Split(message, "|")
	if len(parts) >= 3 {
		possibleLevel := strings.TrimSpace(parts[1])
		if isLikelyLevel(possibleLevel) {
			return possibleLevel, strings.TrimSpace(strings.Join(parts[2:], "|"))
		}
	}
	return "", strings.TrimSpace(message)
}

func isLikelyLevel(level string) bool {
	switch strings.ToUpper(level) {
	case "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "CRITICAL", "IMPORTANT":
		return true
	default:
		return false
	}
}

// GetDayZEconomyStats parses the latest DayZ RPT log and returns economy statistics.
// This can be used by Discord bot commands to show loot statistics.
func (m *Manager) GetDayZEconomyStats(ctx context.Context) (*DayZEconomyStats, error) {
	// Find the latest RPT file
	rptFile, err := findLatestRPTFile(dayzRPTLogDir)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(rptFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stats, err := parseDayZEconomyLogs(f, filepath.Base(rptFile))
	if err != nil {
		return nil, err
	}

	return stats, nil
}

// DayZEconomyStats represents economy statistics from DayZ server logs.
// Simplified version of dayz.EconomyStats for use in Discord commands.
type DayZEconomyStats struct {
	ParsedAt        time.Time `json:"parsed_at"`
	LogFile         string    `json:"log_file"`
	NominalItems    int       `json:"nominal_items"`
	TotalInMap      int       `json:"total_in_map"`
	FillPercent     float64   `json:"fill_percent"`
	VehicleCount    int       `json:"vehicle_count"`
	DynamicEvents   int       `json:"dynamic_events"`
	EventSpawns     int       `json:"event_spawn_positions"`
	TypesConfigured int       `json:"types_configured"`
}

// FormatSummary returns a Discord-friendly summary of the economy stats.
func (s *DayZEconomyStats) FormatSummary() string {
	var sb strings.Builder
	sb.WriteString("📊 **DayZ Economy Statistics**\n\n")
	sb.WriteString("📦 **Loot Overview**\n")
	sb.WriteString("   • Items in World: **")
	sb.WriteString(strconv.Itoa(s.TotalInMap))
	sb.WriteString("** / ")
	sb.WriteString(strconv.Itoa(s.NominalItems))
	sb.WriteString(" nominal (")
	sb.WriteString(strconv.FormatFloat(s.FillPercent, 'f', 1, 64))
	sb.WriteString("%)\n")
	sb.WriteString("   • Vehicles: ")
	sb.WriteString(strconv.Itoa(s.VehicleCount))
	sb.WriteString("\n\n")
	sb.WriteString("🎯 **Events**\n")
	sb.WriteString("   • Dynamic Events: ")
	sb.WriteString(strconv.Itoa(s.DynamicEvents))
	sb.WriteString("\n")
	sb.WriteString("   • Spawn Positions: ")
	sb.WriteString(strconv.Itoa(s.EventSpawns))
	sb.WriteString("\n\n")
	sb.WriteString("⚙️ **Configuration**\n")
	sb.WriteString("   • Item Types: ")
	sb.WriteString(strconv.Itoa(s.TypesConfigured))
	sb.WriteString("\n")
	return sb.String()
}

// FormatCompact returns a one-line summary.
func (s *DayZEconomyStats) FormatCompact() string {
	return "Loot: " + strconv.Itoa(s.TotalInMap) + "/" + strconv.Itoa(s.NominalItems) +
		" (" + strconv.FormatFloat(s.FillPercent, 'f', 0, 64) + "%) | " +
		"Vehicles: " + strconv.Itoa(s.VehicleCount) + " | " +
		"Events: " + strconv.Itoa(s.DynamicEvents)
}

// parseDayZEconomyLogs parses RPT log content for economy statistics.
// This is a simplified inline version - for full parsing use internal/dayz.ParseEconomyLogs
func parseDayZEconomyLogs(f *os.File, logFile string) (*DayZEconomyStats, error) {
	stats := &DayZEconomyStats{
		ParsedAt: time.Now(),
		LogFile:  logFile,
	}

	content, err := os.ReadFile(f.Name())
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if !strings.Contains(line, "[CE]") {
			continue
		}

		// [CE][LootRespawner] (PRIDummy) :: Initially (re)spawned:0, Nominal:11297, Total in Map: 10260
		if strings.Contains(line, "[CE][LootRespawner]") && strings.Contains(line, "Nominal:") {
			if idx := strings.Index(line, "Nominal:"); idx >= 0 {
				rest := line[idx+8:]
				if commaIdx := strings.Index(rest, ","); commaIdx >= 0 {
					stats.NominalItems, _ = strconv.Atoi(strings.TrimSpace(rest[:commaIdx]))
				}
			}
			if idx := strings.Index(line, "Total in Map:"); idx >= 0 {
				rest := strings.TrimSpace(line[idx+13:]) // "10329 at 0 (sec)"
				// Find where the number ends (first non-digit after trimming)
				numEnd := 0
				for i, r := range rest {
					if r < '0' || r > '9' {
						numEnd = i
						break
					}
				}
				if numEnd > 0 {
					stats.TotalInMap, _ = strconv.Atoi(rest[:numEnd])
				} else {
					stats.TotalInMap, _ = strconv.Atoi(rest)
				}
			}
			if stats.NominalItems > 0 {
				stats.FillPercent = float64(stats.TotalInMap) / float64(stats.NominalItems) * 100
			}
		}

		// [CE][Storage] Restoring file "...vehicles.bin" 157 items.
		if strings.Contains(line, "vehicles.bin") && strings.Contains(line, "items") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "items." || p == "items" {
					if i > 0 {
						stats.VehicleCount, _ = strconv.Atoi(parts[i-1])
					}
					break
				}
			}
		}

		// [CE][TypeSetup] :: 573 classes setuped...
		if strings.Contains(line, "[CE][TypeSetup]") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "classes" && i > 0 {
					stats.TypesConfigured, _ = strconv.Atoi(parts[i-1])
					break
				}
			}
		}

		// [CE][DynamicEvent] Load  Events:[42]
		if strings.Contains(line, "[CE][DynamicEvent]") && strings.Contains(line, "Events:[") {
			if idx := strings.Index(line, "Events:["); idx >= 0 {
				rest := line[idx+8:]
				if endIdx := strings.Index(rest, "]"); endIdx >= 0 {
					stats.DynamicEvents, _ = strconv.Atoi(rest[:endIdx])
				}
			}
		}

		// [CE][DE][SPAWNS] :: Total positions: 665
		if strings.Contains(line, "[CE][DE][SPAWNS]") {
			if idx := strings.Index(line, "Total positions:"); idx >= 0 {
				rest := strings.TrimSpace(line[idx+16:])
				stats.EventSpawns, _ = strconv.Atoi(rest)
			}
		}
	}

	return stats, nil
}

// DayZ types.xml directory - where item spawn configurations are stored.
const dayzTypesXMLPath = "/srv/dayz/data/DayZServer/mpmissions/dayzOffline.sakhal/db/types.xml"

// DayZItemInfo represents item spawn configuration from types.xml.
type DayZItemInfo struct {
	Name     string   `json:"name"`
	Nominal  int      `json:"nominal"`            // Target count in world
	Min      int      `json:"min"`                // Minimum count
	Lifetime int      `json:"lifetime"`           // Seconds before despawn
	Restock  int      `json:"restock"`            // Seconds until respawn
	QuantMin int      `json:"quantmin,omitempty"` // Min quantity (ammo, etc)
	QuantMax int      `json:"quantmax,omitempty"` // Max quantity
	Cost     int      `json:"cost,omitempty"`     // Spawn priority
	Category string   `json:"category,omitempty"` // Item category
	Usages   []string `json:"usages,omitempty"`   // Spawn locations
	Values   []string `json:"values,omitempty"`   // Value tiers
}

// DayZItemSearchResult holds item search results.
type DayZItemSearchResult struct {
	Query      string         `json:"query"`
	Items      []DayZItemInfo `json:"items"`
	TotalTypes int            `json:"total_types"`
}

// GetDayZItemInfo looks up a specific item or searches for matching items in types.xml.
func (m *Manager) GetDayZItemInfo(ctx context.Context, search string) (*DayZItemSearchResult, error) {
	f, err := os.Open(dayzTypesXMLPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open types.xml: %w", err)
	}
	defer f.Close()

	types, err := parseDayZTypesXML(f)
	if err != nil {
		return nil, err
	}

	result := &DayZItemSearchResult{
		Query:      search,
		TotalTypes: len(types),
	}

	// Search for matching items (case-insensitive, partial match)
	lowerSearch := strings.ToLower(search)
	for _, item := range types {
		if strings.Contains(strings.ToLower(item.Name), lowerSearch) {
			result.Items = append(result.Items, item)
			if len(result.Items) >= 25 { // Limit results for Discord
				break
			}
		}
	}

	return result, nil
}

// parseDayZTypesXML parses types.xml and returns item info.
// Simplified inline parser - for full parsing use internal/dayz.ParseTypesXML
func parseDayZTypesXML(f *os.File) ([]DayZItemInfo, error) {
	content, err := os.ReadFile(f.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to read types.xml: %w", err)
	}

	var items []DayZItemInfo
	lines := strings.Split(string(content), "\n")

	var current *DayZItemInfo
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Start of item type
		if strings.HasPrefix(line, "<type name=\"") {
			if idx := strings.Index(line, "name=\""); idx >= 0 {
				rest := line[idx+6:]
				if endIdx := strings.Index(rest, "\""); endIdx >= 0 {
					current = &DayZItemInfo{Name: rest[:endIdx]}
				}
			}
			continue
		}

		// End of item type
		if line == "</type>" && current != nil {
			items = append(items, *current)
			current = nil
			continue
		}

		if current == nil {
			continue
		}

		// Parse item properties
		if strings.HasPrefix(line, "<nominal>") {
			current.Nominal = parseXMLInt(line, "nominal")
		} else if strings.HasPrefix(line, "<min>") {
			current.Min = parseXMLInt(line, "min")
		} else if strings.HasPrefix(line, "<lifetime>") {
			current.Lifetime = parseXMLInt(line, "lifetime")
		} else if strings.HasPrefix(line, "<restock>") {
			current.Restock = parseXMLInt(line, "restock")
		} else if strings.HasPrefix(line, "<quantmin>") {
			current.QuantMin = parseXMLInt(line, "quantmin")
		} else if strings.HasPrefix(line, "<quantmax>") {
			current.QuantMax = parseXMLInt(line, "quantmax")
		} else if strings.HasPrefix(line, "<cost>") {
			current.Cost = parseXMLInt(line, "cost")
		} else if strings.HasPrefix(line, "<category name=\"") {
			current.Category = parseXMLAttr(line, "name")
		} else if strings.HasPrefix(line, "<usage name=\"") {
			if usage := parseXMLAttr(line, "name"); usage != "" {
				current.Usages = append(current.Usages, usage)
			}
		} else if strings.HasPrefix(line, "<value name=\"") {
			if value := parseXMLAttr(line, "name"); value != "" {
				current.Values = append(current.Values, value)
			}
		}
	}

	return items, nil
}

// parseXMLInt extracts an integer from a simple XML element like <tag>123</tag>
func parseXMLInt(line, tag string) int {
	start := fmt.Sprintf("<%s>", tag)
	end := fmt.Sprintf("</%s>", tag)
	if idx := strings.Index(line, start); idx >= 0 {
		rest := line[idx+len(start):]
		if endIdx := strings.Index(rest, end); endIdx >= 0 {
			val, _ := strconv.Atoi(strings.TrimSpace(rest[:endIdx]))
			return val
		}
	}
	return 0
}

// parseXMLAttr extracts an attribute value from an XML element like <tag name="value"/>
func parseXMLAttr(line, attr string) string {
	pattern := fmt.Sprintf(`%s="`, attr)
	if idx := strings.Index(line, pattern); idx >= 0 {
		rest := line[idx+len(pattern):]
		if endIdx := strings.Index(rest, "\""); endIdx >= 0 {
			return rest[:endIdx]
		}
	}
	return ""
}
