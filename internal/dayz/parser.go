package dayz

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// xmlEventPosDef represents the root element of cfgeventspawns.xml.
type xmlEventPosDef struct {
	XMLName xml.Name   `xml:"eventposdef"`
	Events  []xmlEvent `xml:"event"`
}

// xmlEvent represents a single event in the spawn config.
type xmlEvent struct {
	Name      string   `xml:"name,attr"`
	Positions []xmlPos `xml:"pos"`
}

// xmlPos represents a spawn position.
type xmlPos struct {
	X string `xml:"x,attr"`
	Z string `xml:"z,attr"`
	A string `xml:"a,attr"` // Optional rotation angle
}

// ParseEventSpawns parses the cfgeventspawns.xml content and returns spawn data.
func ParseEventSpawns(xmlContent []byte) (*MapSpawnData, error) {
	var eventDef xmlEventPosDef
	if err := xml.Unmarshal(xmlContent, &eventDef); err != nil {
		return nil, fmt.Errorf("parse xml: %w", err)
	}

	data := &MapSpawnData{
		Events: make(map[string]*EventSpawns),
	}

	for _, event := range eventDef.Events {
		if len(event.Positions) == 0 {
			continue // Skip events with no spawn points
		}

		spawns := &EventSpawns{
			Name:   event.Name,
			Points: make([]SpawnPoint, 0, len(event.Positions)),
		}

		for _, pos := range event.Positions {
			x, err := parseFloat(pos.X)
			if err != nil {
				continue
			}
			z, err := parseFloat(pos.Z)
			if err != nil {
				continue
			}

			point := SpawnPoint{X: x, Z: z}
			if pos.A != "" {
				if a, err := parseFloat(pos.A); err == nil {
					point.A = a
				}
			}

			spawns.Points = append(spawns.Points, point)
		}

		spawns.Count = len(spawns.Points)
		data.Events[event.Name] = spawns
		data.TotalPoints += spawns.Count
	}

	return data, nil
}

// parseFloat handles float parsing with tolerance for whitespace.
func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// FilterEvents returns only the specified event types from spawn data.
func FilterEvents(data *MapSpawnData, eventNames []string) *MapSpawnData {
	filtered := &MapSpawnData{
		MapName:     data.MapName,
		Events:      make(map[string]*EventSpawns),
		FetchedAt:   data.FetchedAt,
		CachedUntil: data.CachedUntil,
	}

	eventSet := make(map[string]bool)
	for _, name := range eventNames {
		eventSet[name] = true
	}

	for name, spawns := range data.Events {
		if eventSet[name] {
			filtered.Events[name] = spawns
			filtered.TotalPoints += spawns.Count
		}
	}

	return filtered
}

// GetEventsByCategory groups events by their category.
func GetEventsByCategory(data *MapSpawnData) map[string][]*EventSpawns {
	categories := DefaultCategories()
	result := make(map[string][]*EventSpawns)

	// Build reverse lookup: event name → category
	eventCategory := make(map[string]string)
	for _, cat := range categories {
		for _, eventName := range cat.Events {
			eventCategory[eventName] = cat.Name
		}
	}

	// Group spawns by category
	for eventName, spawns := range data.Events {
		catName := eventCategory[eventName]
		if catName == "" {
			catName = "Other"
		}
		result[catName] = append(result[catName], spawns)
	}

	return result
}
