package dayz

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// TypesXML represents the root of a DayZ types.xml file.
type TypesXML struct {
	XMLName xml.Name   `xml:"types"`
	Types   []ItemType `xml:"type"`
}

// ItemType represents a single item type definition from types.xml.
// See: https://community.bistudio.com/wiki/DayZ:Central_Economy_mission_files#types.xml
type ItemType struct {
	Name     string    `xml:"name,attr" json:"name"`
	Nominal  int       `xml:"nominal" json:"nominal"`             // Target count in world
	Lifetime int       `xml:"lifetime" json:"lifetime"`           // Seconds before despawn
	Restock  int       `xml:"restock" json:"restock"`             // Seconds until respawn eligible
	Min      int       `xml:"min" json:"min"`                     // Minimum count in world
	QuantMin int       `xml:"quantmin" json:"quantmin,omitempty"` // Min quantity (ammo, etc)
	QuantMax int       `xml:"quantmax" json:"quantmax,omitempty"` // Max quantity
	Cost     int       `xml:"cost" json:"cost,omitempty"`         // Spawn priority (higher = rarer)
	Flags    ItemFlags `xml:"flags" json:"flags"`                 // Spawn behavior flags
	Category *ItemRef  `xml:"category" json:"category,omitempty"` // Category reference
	Usages   []ItemRef `xml:"usage" json:"usages,omitempty"`      // Usage tier references
	Values   []ItemRef `xml:"value" json:"values,omitempty"`      // Value tier references
	Tags     []ItemRef `xml:"tag" json:"tags,omitempty"`          // Tag references
}

// ItemFlags controls spawn behavior for an item.
type ItemFlags struct {
	CountInCargo   bool `xml:"count_in_cargo,attr" json:"count_in_cargo"`
	CountInHoarder bool `xml:"count_in_hoarder,attr" json:"count_in_hoarder"`
	CountInMap     bool `xml:"count_in_map,attr" json:"count_in_map"`
	CountInPlayer  bool `xml:"count_in_player,attr" json:"count_in_player"`
	Crafted        bool `xml:"crafted,attr" json:"crafted"`
	Deloot         bool `xml:"deloot,attr" json:"deloot"` // Dynamic event loot
}

// ItemRef represents a named reference (category, usage, value, tag).
type ItemRef struct {
	Name string `xml:"name,attr" json:"name"`
}

// ParseTypesXML parses a types.xml file and returns the structured data.
func ParseTypesXML(r io.Reader) (*TypesXML, error) {
	var types TypesXML
	decoder := xml.NewDecoder(r)
	if err := decoder.Decode(&types); err != nil {
		return nil, fmt.Errorf("failed to parse types.xml: %w", err)
	}
	return &types, nil
}

// FindItem searches for an item by name (case-insensitive).
// Returns nil if not found.
func (t *TypesXML) FindItem(name string) *ItemType {
	lowerName := strings.ToLower(name)
	for i := range t.Types {
		if strings.ToLower(t.Types[i].Name) == lowerName {
			return &t.Types[i]
		}
	}
	return nil
}

// SearchItems returns all items matching a search term (case-insensitive, partial match).
// Limits results to maxResults.
func (t *TypesXML) SearchItems(search string, maxResults int) []ItemType {
	if maxResults <= 0 {
		maxResults = 25
	}

	lowerSearch := strings.ToLower(search)
	var results []ItemType

	for _, item := range t.Types {
		if strings.Contains(strings.ToLower(item.Name), lowerSearch) {
			results = append(results, item)
			if len(results) >= maxResults {
				break
			}
		}
	}

	return results
}

// FormatDiscord returns a Discord-friendly string representation of the item.
func (it *ItemType) FormatDiscord() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("🔍 **%s**\n\n", it.Name))

	// Spawn settings
	sb.WriteString("**Spawn Settings**\n")
	sb.WriteString(fmt.Sprintf("> Nominal: **%d** (target in world)\n", it.Nominal))
	sb.WriteString(fmt.Sprintf("> Min: **%d** (triggers respawn)\n", it.Min))

	// Lifetime formatting
	lifetime := formatDuration(it.Lifetime)
	sb.WriteString(fmt.Sprintf("> Lifetime: **%s** (%d sec)\n", lifetime, it.Lifetime))

	restock := formatDuration(it.Restock)
	sb.WriteString(fmt.Sprintf("> Restock: **%s** (%d sec)\n", restock, it.Restock))

	// Quantity (if applicable)
	if it.QuantMin > 0 || it.QuantMax > 0 {
		sb.WriteString(fmt.Sprintf("> Quantity: **%d-%d**\n", it.QuantMin, it.QuantMax))
	}

	// Cost/rarity
	if it.Cost > 0 {
		sb.WriteString(fmt.Sprintf("> Cost: **%d** (higher = rarer)\n", it.Cost))
	}

	// Category
	if it.Category != nil {
		sb.WriteString(fmt.Sprintf("\n**Category:** %s\n", it.Category.Name))
	}

	// Usages (spawn locations)
	if len(it.Usages) > 0 {
		usages := make([]string, len(it.Usages))
		for i, u := range it.Usages {
			usages[i] = u.Name
		}
		sb.WriteString(fmt.Sprintf("**Spawn Locations:** %s\n", strings.Join(usages, ", ")))
	}

	// Values (rarity tiers)
	if len(it.Values) > 0 {
		values := make([]string, len(it.Values))
		for i, v := range it.Values {
			values[i] = v.Name
		}
		sb.WriteString(fmt.Sprintf("**Value Tiers:** %s\n", strings.Join(values, ", ")))
	}

	// Flags
	var flags []string
	if it.Flags.CountInCargo {
		flags = append(flags, "cargo")
	}
	if it.Flags.CountInHoarder {
		flags = append(flags, "hoarder")
	}
	if it.Flags.CountInMap {
		flags = append(flags, "map")
	}
	if it.Flags.CountInPlayer {
		flags = append(flags, "player")
	}
	if it.Flags.Crafted {
		flags = append(flags, "crafted")
	}
	if it.Flags.Deloot {
		flags = append(flags, "deloot")
	}
	if len(flags) > 0 {
		sb.WriteString(fmt.Sprintf("**Count Flags:** %s\n", strings.Join(flags, ", ")))
	}

	return sb.String()
}

// formatDuration converts seconds to a human-readable duration.
func formatDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	if seconds < 86400 {
		hours := seconds / 3600
		mins := (seconds % 3600) / 60
		if mins > 0 {
			return fmt.Sprintf("%dh%dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	if hours > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}
