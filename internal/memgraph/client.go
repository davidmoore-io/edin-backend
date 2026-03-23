// Package memgraph provides a client for querying the Memgraph graph database.
// This client connects to Memgraph at db.ssg.sh over the WireGuard VPN.
package memgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Client provides methods to query Memgraph.
type Client struct {
	driver neo4j.DriverWithContext
	uri    string
}

// Config holds Memgraph connection configuration.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
}

// NewClient creates a new Memgraph client.
func NewClient(cfg Config) (*Client, error) {
	uri := fmt.Sprintf("bolt://%s:%d", cfg.Host, cfg.Port)

	var auth neo4j.AuthToken
	if cfg.Username != "" {
		auth = neo4j.BasicAuth(cfg.Username, cfg.Password, "")
	} else {
		auth = neo4j.NoAuth()
	}

	driver, err := neo4j.NewDriverWithContext(uri, auth)
	if err != nil {
		return nil, fmt.Errorf("failed to create Memgraph driver: %w", err)
	}

	return &Client{
		driver: driver,
		uri:    uri,
	}, nil
}

// Connect verifies the connection to Memgraph.
func (c *Client) Connect(ctx context.Context) error {
	err := c.driver.VerifyConnectivity(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to Memgraph at %s: %w", c.uri, err)
	}
	log.Printf("✅ Connected to Memgraph at %s", c.uri)
	return nil
}

// Close closes the Memgraph driver.
func (c *Client) Close(ctx context.Context) error {
	return c.driver.Close(ctx)
}

// NewSession creates a new Neo4j session with the given configuration.
// This method is exposed to allow packages like kaine to use the driver directly
// for custom queries while keeping the driver encapsulated.
func (c *Client) NewSession(ctx context.Context, config neo4j.SessionConfig) neo4j.SessionWithContext {
	return c.driver.NewSession(ctx, config)
}

// CGSystemData represents a CG system with full powerplay data from Memgraph.
// Aligned with eddn-listener/MEMGRAPH-SCHEMA.md v3 (2026-01-06)
type CGSystemData struct {
	SystemName                string           `json:"system_name"`
	ControllingPower          string           `json:"controlling_power,omitempty"`
	Powers                    []string         `json:"powers,omitempty"`
	PowerplayState            string           `json:"powerplay_state"`
	Reinforcement             int64            `json:"reinforcement"`
	Undermining               int64            `json:"undermining"`
	ControlProgress           *float64         `json:"control_progress,omitempty"`
	PowerplayConflictProgress []map[string]any `json:"powerplay_conflict_progress,omitempty"`
	Allegiance                string           `json:"allegiance,omitempty"`
	Government                string           `json:"government,omitempty"`
	Population                int64            `json:"population,omitempty"`
	ControllingFaction        string           `json:"controlling_faction,omitempty"`
	ControllingFactionState   string           `json:"controlling_faction_state,omitempty"`
	LastEDDNUpdate            time.Time        `json:"last_eddn_update,omitempty"`

	// Additional fields for CSV export
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Z        float64 `json:"z"`
	Security string  `json:"security,omitempty"`
	Economy  string  `json:"economy,omitempty"`

	// Station info (nearest large pad station)
	HasLargePad      bool    `json:"has_large_pad"`
	NearestStation   string  `json:"nearest_station,omitempty"`
	NearestStationLs float64 `json:"nearest_station_ls,omitempty"`
	StationCount     int     `json:"station_count"`
}

// GetCGSystems fetches all Col 359 sector systems from Memgraph.
func (c *Client) GetCGSystems(ctx context.Context, systemNames []string) ([]CGSystemData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Query Memgraph for system data with station info
	// Note: We don't use OPTIONAL MATCH for Power edges as it causes duplicates
	// when systems have multiple controlling powers. The s.controlling_power
	// property already contains this data.
	// Station subquery finds the nearest permanent station with large pads.
	query := `
		MATCH (s:System)
		WHERE s.name IN $names
		OPTIONAL MATCH (s)-[:HAS_STATION]->(st:Station)
		WHERE st.type <> "Fleetcarrier" AND st.type <> "Drake-Class Carrier"
		WITH s,
		     collect(st) AS stations,
		     [st IN collect(st) WHERE st.large_pads > 0 | st] AS large_pad_stations
		WITH s, stations,
		     size(stations) AS station_count,
		     size(large_pad_stations) > 0 AS has_large_pad,
		     CASE WHEN size(large_pad_stations) > 0
		          THEN reduce(nearest = large_pad_stations[0], st IN large_pad_stations |
		               CASE WHEN st.distance_ls < nearest.distance_ls THEN st ELSE nearest END)
		          ELSE null END AS nearest_large
		RETURN
			s.name AS system_name,
			s.controlling_power AS controlling_power,
			s.powers AS powers,
			s.powerplay_state AS powerplay_state,
			s.reinforcement AS reinforcement,
			s.undermining AS undermining,
			s.control_progress AS control_progress,
			s.powerplay_conflict_progress AS powerplay_conflict_progress,
			s.allegiance AS allegiance,
			s.government AS government,
			s.population AS population,
			s.controlling_faction AS controlling_faction,
			s.controlling_faction_state AS controlling_faction_state,
			s.last_eddn_update AS last_eddn_update,
			s.x AS x,
			s.y AS y,
			s.z AS z,
			s.security AS security,
			s.economy AS economy,
			has_large_pad,
			nearest_large.name AS nearest_station,
			nearest_large.distance_ls AS nearest_station_ls,
			station_count
		ORDER BY s.name
	`

	result, err := session.Run(ctx, query, map[string]any{"names": systemNames})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var systems []CGSystemData
	for result.Next(ctx) {
		record := result.Record()
		sys := CGSystemData{}

		if v, ok := record.Get("system_name"); ok && v != nil {
			sys.SystemName = v.(string)
		}
		if v, ok := record.Get("controlling_power"); ok && v != nil {
			sys.ControllingPower = v.(string)
		}
		if v, ok := record.Get("powers"); ok && v != nil {
			if powers, ok := v.([]any); ok {
				for _, p := range powers {
					if ps, ok := p.(string); ok {
						sys.Powers = append(sys.Powers, ps)
					}
				}
			}
		}
		if v, ok := record.Get("powerplay_state"); ok && v != nil {
			sys.PowerplayState = v.(string)
		}
		if v, ok := record.Get("reinforcement"); ok && v != nil {
			sys.Reinforcement = toInt64(v)
		}
		if v, ok := record.Get("undermining"); ok && v != nil {
			sys.Undermining = toInt64(v)
		}
		if v, ok := record.Get("control_progress"); ok && v != nil {
			cp := toFloat64(v)
			sys.ControlProgress = &cp
		}
		if v, ok := record.Get("powerplay_conflict_progress"); ok && v != nil {
			sys.PowerplayConflictProgress = parseConflictProgress(v)
		}
		if v, ok := record.Get("allegiance"); ok && v != nil {
			sys.Allegiance = v.(string)
		}
		if v, ok := record.Get("government"); ok && v != nil {
			sys.Government = v.(string)
		}
		if v, ok := record.Get("population"); ok && v != nil {
			sys.Population = toInt64(v)
		}
		if v, ok := record.Get("controlling_faction"); ok && v != nil {
			sys.ControllingFaction = v.(string)
		}
		if v, ok := record.Get("controlling_faction_state"); ok && v != nil {
			sys.ControllingFactionState = v.(string)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			sys.LastEDDNUpdate = toTime(v)
		}

		// Additional fields for CSV export
		if v, ok := record.Get("x"); ok && v != nil {
			sys.X = toFloat64(v)
		}
		if v, ok := record.Get("y"); ok && v != nil {
			sys.Y = toFloat64(v)
		}
		if v, ok := record.Get("z"); ok && v != nil {
			sys.Z = toFloat64(v)
		}
		if v, ok := record.Get("security"); ok && v != nil {
			sys.Security = v.(string)
		}
		if v, ok := record.Get("economy"); ok && v != nil {
			sys.Economy = v.(string)
		}
		if v, ok := record.Get("has_large_pad"); ok && v != nil {
			sys.HasLargePad = v.(bool)
		}
		if v, ok := record.Get("nearest_station"); ok && v != nil {
			sys.NearestStation = v.(string)
		}
		if v, ok := record.Get("nearest_station_ls"); ok && v != nil {
			sys.NearestStationLs = toFloat64(v)
		}
		if v, ok := record.Get("station_count"); ok && v != nil {
			sys.StationCount = int(toInt64(v))
		}

		systems = append(systems, sys)
	}

	if err = result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration error: %w", err)
	}

	return systems, nil
}

// GetAllPowerplaySystems fetches all systems involved in powerplay from Memgraph.
// This includes controlled systems (~15,900) AND expansion systems (~29,000) — any system
// with a powerplay_state set. Station data is omitted for performance at this scale.
func (c *Client) GetAllPowerplaySystems(ctx context.Context) ([]CGSystemData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.powerplay_state IS NOT NULL AND s.powerplay_state <> ""
		RETURN
			s.name AS system_name,
			s.controlling_power AS controlling_power,
			s.powers AS powers,
			s.powerplay_state AS powerplay_state,
			s.reinforcement AS reinforcement,
			s.undermining AS undermining,
			s.control_progress AS control_progress,
			s.powerplay_conflict_progress AS powerplay_conflict_progress,
			s.allegiance AS allegiance,
			s.government AS government,
			s.population AS population,
			s.controlling_faction AS controlling_faction,
			s.controlling_faction_state AS controlling_faction_state,
			s.last_eddn_update AS last_eddn_update,
			s.x AS x,
			s.y AS y,
			s.z AS z,
			s.security AS security,
			s.economy AS economy
		ORDER BY s.name
	`

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var systems []CGSystemData
	for result.Next(ctx) {
		record := result.Record()
		sys := CGSystemData{}

		if v, ok := record.Get("system_name"); ok && v != nil {
			sys.SystemName = v.(string)
		}
		if v, ok := record.Get("controlling_power"); ok && v != nil {
			sys.ControllingPower = v.(string)
		}
		if v, ok := record.Get("powers"); ok && v != nil {
			if powers, ok := v.([]any); ok {
				for _, p := range powers {
					if ps, ok := p.(string); ok {
						sys.Powers = append(sys.Powers, ps)
					}
				}
			}
		}
		if v, ok := record.Get("powerplay_state"); ok && v != nil {
			sys.PowerplayState = v.(string)
		}
		if v, ok := record.Get("reinforcement"); ok && v != nil {
			sys.Reinforcement = toInt64(v)
		}
		if v, ok := record.Get("undermining"); ok && v != nil {
			sys.Undermining = toInt64(v)
		}
		if v, ok := record.Get("control_progress"); ok && v != nil {
			cp := toFloat64(v)
			sys.ControlProgress = &cp
		}
		if v, ok := record.Get("powerplay_conflict_progress"); ok && v != nil {
			sys.PowerplayConflictProgress = parseConflictProgress(v)
		}
		if v, ok := record.Get("allegiance"); ok && v != nil {
			sys.Allegiance = v.(string)
		}
		if v, ok := record.Get("government"); ok && v != nil {
			sys.Government = v.(string)
		}
		if v, ok := record.Get("population"); ok && v != nil {
			sys.Population = toInt64(v)
		}
		if v, ok := record.Get("controlling_faction"); ok && v != nil {
			sys.ControllingFaction = v.(string)
		}
		if v, ok := record.Get("controlling_faction_state"); ok && v != nil {
			sys.ControllingFactionState = v.(string)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			sys.LastEDDNUpdate = toTime(v)
		}
		if v, ok := record.Get("x"); ok && v != nil {
			sys.X = toFloat64(v)
		}
		if v, ok := record.Get("y"); ok && v != nil {
			sys.Y = toFloat64(v)
		}
		if v, ok := record.Get("z"); ok && v != nil {
			sys.Z = toFloat64(v)
		}
		if v, ok := record.Get("security"); ok && v != nil {
			sys.Security = v.(string)
		}
		if v, ok := record.Get("economy"); ok && v != nil {
			sys.Economy = v.(string)
		}

		systems = append(systems, sys)
	}

	if err = result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration error: %w", err)
	}

	return systems, nil
}

// GetAllPowerStateCounts returns state counts per power across all powerplay systems.
// Global version of GetPowerStateCountsForSystems (no system name filter).
func (c *Client) GetAllPowerStateCounts(ctx context.Context) (map[string]*PowerStateCounts, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.powerplay_state IS NOT NULL AND s.powerplay_state <> ""
		      AND s.controlling_power IS NOT NULL AND s.controlling_power <> ""
		RETURN
			s.controlling_power AS power,
			s.powerplay_state AS state,
			count(*) AS count
		ORDER BY power, state
	`

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	counts := make(map[string]*PowerStateCounts)

	for result.Next(ctx) {
		record := result.Record()

		var power, state string
		var count int64

		if v, ok := record.Get("power"); ok && v != nil {
			power = v.(string)
		}
		if v, ok := record.Get("state"); ok && v != nil {
			state = v.(string)
		}
		if v, ok := record.Get("count"); ok && v != nil {
			count = toInt64(v)
		}

		if power == "" {
			continue
		}

		if counts[power] == nil {
			counts[power] = &PowerStateCounts{
				Power:  power,
				States: make(map[string]int),
			}
		}

		counts[power].States[state] = int(count)
		counts[power].Total += int(count)
	}

	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration failed: %w", err)
	}

	return counts, nil
}

// SystemData represents a single system with full data from Memgraph.
// Aligned with eddn-listener/MEMGRAPH-SCHEMA.md v3 (2026-01-06)
type SystemData struct {
	Name                      string           `json:"name"`
	ID64                      int64            `json:"id64,omitempty"`
	ControllingPower          string           `json:"controlling_power,omitempty"`
	Powers                    []string         `json:"powers,omitempty"`
	PowerplayState            string           `json:"powerplay_state,omitempty"`
	Reinforcement             int64            `json:"reinforcement"`
	Undermining               int64            `json:"undermining"`
	ControlProgress           *float64         `json:"control_progress,omitempty"`
	PowerplayConflictProgress []map[string]any `json:"powerplay_conflict_progress,omitempty"`
	Allegiance                string           `json:"allegiance,omitempty"`
	Government                string           `json:"government,omitempty"`
	Security                  string           `json:"security,omitempty"`
	Population                int64            `json:"population,omitempty"`
	Economy                   string           `json:"economy,omitempty"`
	SecondEconomy             string           `json:"second_economy,omitempty"`
	NeedsPermit               bool             `json:"needs_permit,omitempty"`
	ControllingFaction        string           `json:"controlling_faction,omitempty"`
	ControllingFactionState   string           `json:"controlling_faction_state,omitempty"`
	Coordinates               *Coords          `json:"coordinates,omitempty"`
	ThargoidState             string           `json:"thargoid_state,omitempty"`
	ThargoidProgress          float64          `json:"thargoid_progress,omitempty"`
	LastEDDNUpdate            time.Time        `json:"last_eddn_update,omitempty"`
}

// Coords represents system coordinates.
type Coords struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// GetSystem fetches a single system by name from Memgraph.
func (c *Client) GetSystem(ctx context.Context, systemName string) (*SystemData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.name = $name
		RETURN
			s.name AS name,
			s.id64 AS id64,
			s.controlling_power AS controlling_power,
			s.powers AS powers,
			s.powerplay_state AS powerplay_state,
			s.reinforcement AS reinforcement,
			s.undermining AS undermining,
			s.control_progress AS control_progress,
			s.powerplay_conflict_progress AS powerplay_conflict_progress,
			s.allegiance AS allegiance,
			s.government AS government,
			s.security AS security,
			s.population AS population,
			s.economy AS economy,
			s.second_economy AS second_economy,
			s.needs_permit AS needs_permit,
			s.controlling_faction AS controlling_faction,
			s.controlling_faction_state AS controlling_faction_state,
			s.x AS x, s.y AS y, s.z AS z,
			s.thargoid_state AS thargoid_state,
			s.thargoid_progress AS thargoid_progress,
			s.last_eddn_update AS last_eddn_update
		LIMIT 1
	`

	result, err := session.Run(ctx, query, map[string]any{"name": systemName})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	if !result.Next(ctx) {
		return nil, nil // System not found
	}

	record := result.Record()
	sys := &SystemData{}

	if v, ok := record.Get("name"); ok && v != nil {
		sys.Name = v.(string)
	}
	if v, ok := record.Get("id64"); ok && v != nil {
		sys.ID64 = toInt64(v)
	}
	if v, ok := record.Get("controlling_power"); ok && v != nil {
		sys.ControllingPower = v.(string)
	}
	if v, ok := record.Get("powers"); ok && v != nil {
		if powers, ok := v.([]any); ok {
			for _, p := range powers {
				if ps, ok := p.(string); ok {
					sys.Powers = append(sys.Powers, ps)
				}
			}
		}
	}
	if v, ok := record.Get("powerplay_state"); ok && v != nil {
		sys.PowerplayState = v.(string)
	}
	if v, ok := record.Get("reinforcement"); ok && v != nil {
		sys.Reinforcement = toInt64(v)
	}
	if v, ok := record.Get("undermining"); ok && v != nil {
		sys.Undermining = toInt64(v)
	}
	if v, ok := record.Get("control_progress"); ok && v != nil {
		cp := toFloat64(v)
		sys.ControlProgress = &cp
	}
	if v, ok := record.Get("powerplay_conflict_progress"); ok && v != nil {
		sys.PowerplayConflictProgress = parseConflictProgress(v)
	}
	if v, ok := record.Get("allegiance"); ok && v != nil {
		sys.Allegiance = v.(string)
	}
	if v, ok := record.Get("government"); ok && v != nil {
		sys.Government = v.(string)
	}
	if v, ok := record.Get("security"); ok && v != nil {
		sys.Security = v.(string)
	}
	if v, ok := record.Get("population"); ok && v != nil {
		sys.Population = toInt64(v)
	}
	if v, ok := record.Get("economy"); ok && v != nil {
		sys.Economy = v.(string)
	}
	if v, ok := record.Get("second_economy"); ok && v != nil {
		sys.SecondEconomy = v.(string)
	}
	if v, ok := record.Get("needs_permit"); ok && v != nil {
		sys.NeedsPermit = v.(bool)
	}
	if v, ok := record.Get("controlling_faction"); ok && v != nil {
		sys.ControllingFaction = v.(string)
	}
	if v, ok := record.Get("controlling_faction_state"); ok && v != nil {
		sys.ControllingFactionState = v.(string)
	}
	// Coordinates
	x, xOk := record.Get("x")
	y, yOk := record.Get("y")
	z, zOk := record.Get("z")
	if xOk && yOk && zOk && x != nil && y != nil && z != nil {
		sys.Coordinates = &Coords{
			X: toFloat64(x),
			Y: toFloat64(y),
			Z: toFloat64(z),
		}
	}
	if v, ok := record.Get("thargoid_state"); ok && v != nil {
		sys.ThargoidState = v.(string)
	}
	if v, ok := record.Get("thargoid_progress"); ok && v != nil {
		sys.ThargoidProgress = toFloat64(v)
	}
	if v, ok := record.Get("last_eddn_update"); ok && v != nil {
		sys.LastEDDNUpdate = toTime(v)
	}

	if err = result.Err(); err != nil {
		return nil, fmt.Errorf("result error: %w", err)
	}

	return sys, nil
}

// PowerplayUpdate holds powerplay data to write to Memgraph.
type PowerplayUpdate struct {
	SystemName    string
	ControlPower  string // Controlling power
	State         string // Exploited, Fortified, etc.
	Reinforcement int64
	Undermining   int64
	ChangePct     float64 // Expansion/control progress percentage
}

// UpdateSystemPowerplay updates powerplay data for a system in Memgraph.
// This is called by the Inara scraper to push fresh powerplay data.
func (c *Client) UpdateSystemPowerplay(ctx context.Context, update *PowerplayUpdate) error {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	// Update existing system's powerplay data
	// Only update if the system already exists (we don't create new systems from Inara)
	query := `
		MATCH (s:System {name: $name})
		SET
			s.controlling_power = $controlling_power,
			s.powerplay_state = $powerplay_state,
			s.reinforcement = $reinforcement,
			s.undermining = $undermining,
			s.control_progress = $control_progress,
			s.last_inara_update = datetime(),
			s.last_updated = datetime()
		RETURN s.name AS name
	`

	result, err := session.Run(ctx, query, map[string]any{
		"name":              update.SystemName,
		"controlling_power": update.ControlPower,
		"powerplay_state":   update.State,
		"reinforcement":     update.Reinforcement,
		"undermining":       update.Undermining,
		"control_progress":  update.ChangePct / 100.0, // Convert percentage to decimal
	})
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	// Check if we actually updated a system
	if !result.Next(ctx) {
		// System doesn't exist in Memgraph - that's okay, EDDN will create it
		return nil
	}

	return result.Err()
}

// UpdateSystemsPowerplay updates powerplay data for multiple systems in Memgraph.
func (c *Client) UpdateSystemsPowerplay(ctx context.Context, updates []*PowerplayUpdate) (int, error) {
	if len(updates) == 0 {
		return 0, nil
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	updated := 0
	for _, update := range updates {
		query := `
			MATCH (s:System {name: $name})
			SET
				s.controlling_power = $controlling_power,
				s.powerplay_state = $powerplay_state,
				s.reinforcement = $reinforcement,
				s.undermining = $undermining,
				s.control_progress = $control_progress,
				s.last_inara_update = datetime(),
				s.last_updated = datetime()
			RETURN s.name AS name
		`

		result, err := session.Run(ctx, query, map[string]any{
			"name":              update.SystemName,
			"controlling_power": update.ControlPower,
			"powerplay_state":   update.State,
			"reinforcement":     update.Reinforcement,
			"undermining":       update.Undermining,
			"control_progress":  update.ChangePct / 100.0,
		})
		if err != nil {
			log.Printf("⚠️ Failed to update %s in Memgraph: %v", update.SystemName, err)
			continue
		}

		if result.Next(ctx) {
			updated++
		}
	}

	return updated, nil
}

// SearchSystems searches for systems by name prefix (for autocomplete).
// Returns up to 10 matching systems with basic details.
func (c *Client) SearchSystems(ctx context.Context, query string, limit int) ([]SystemData, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Case-insensitive prefix search
	cypher := `
		MATCH (s:System)
		WHERE toLower(s.name) STARTS WITH toLower($query)
		RETURN
			s.name AS name,
			s.id64 AS id64,
			s.controlling_power AS controlling_power,
			s.powers AS powers,
			s.powerplay_state AS powerplay_state,
			s.reinforcement AS reinforcement,
			s.undermining AS undermining,
			s.control_progress AS control_progress,
			s.allegiance AS allegiance,
			s.government AS government,
			s.security AS security,
			s.population AS population,
			s.economy AS economy,
			s.second_economy AS second_economy,
			s.needs_permit AS needs_permit,
			s.controlling_faction AS controlling_faction,
			s.controlling_faction_state AS controlling_faction_state,
			s.x AS x, s.y AS y, s.z AS z,
			s.thargoid_state AS thargoid_state,
			s.thargoid_progress AS thargoid_progress,
			s.last_eddn_update AS last_eddn_update
		ORDER BY s.name
		LIMIT $limit
	`

	result, err := session.Run(ctx, cypher, map[string]any{"query": query, "limit": int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("search query failed: %w", err)
	}

	var systems []SystemData
	for result.Next(ctx) {
		record := result.Record()
		sys := SystemData{}

		if v, ok := record.Get("name"); ok && v != nil {
			sys.Name = v.(string)
		}
		if v, ok := record.Get("id64"); ok && v != nil {
			sys.ID64 = toInt64(v)
		}
		if v, ok := record.Get("controlling_power"); ok && v != nil {
			sys.ControllingPower = v.(string)
		}
		if v, ok := record.Get("powers"); ok && v != nil {
			if powers, ok := v.([]any); ok {
				for _, p := range powers {
					if ps, ok := p.(string); ok {
						sys.Powers = append(sys.Powers, ps)
					}
				}
			}
		}
		if v, ok := record.Get("powerplay_state"); ok && v != nil {
			sys.PowerplayState = v.(string)
		}
		if v, ok := record.Get("reinforcement"); ok && v != nil {
			sys.Reinforcement = toInt64(v)
		}
		if v, ok := record.Get("undermining"); ok && v != nil {
			sys.Undermining = toInt64(v)
		}
		if v, ok := record.Get("control_progress"); ok && v != nil {
			cp := toFloat64(v)
			sys.ControlProgress = &cp
		}
		if v, ok := record.Get("allegiance"); ok && v != nil {
			sys.Allegiance = v.(string)
		}
		if v, ok := record.Get("government"); ok && v != nil {
			sys.Government = v.(string)
		}
		if v, ok := record.Get("security"); ok && v != nil {
			sys.Security = v.(string)
		}
		if v, ok := record.Get("population"); ok && v != nil {
			sys.Population = toInt64(v)
		}
		if v, ok := record.Get("economy"); ok && v != nil {
			sys.Economy = v.(string)
		}
		if v, ok := record.Get("second_economy"); ok && v != nil {
			sys.SecondEconomy = v.(string)
		}
		if v, ok := record.Get("needs_permit"); ok && v != nil {
			sys.NeedsPermit = v.(bool)
		}
		if v, ok := record.Get("controlling_faction"); ok && v != nil {
			sys.ControllingFaction = v.(string)
		}
		if v, ok := record.Get("controlling_faction_state"); ok && v != nil {
			sys.ControllingFactionState = v.(string)
		}
		// Coordinates
		x, xOk := record.Get("x")
		y, yOk := record.Get("y")
		z, zOk := record.Get("z")
		if xOk && yOk && zOk && x != nil && y != nil && z != nil {
			sys.Coordinates = &Coords{
				X: toFloat64(x),
				Y: toFloat64(y),
				Z: toFloat64(z),
			}
		}
		if v, ok := record.Get("thargoid_state"); ok && v != nil {
			sys.ThargoidState = v.(string)
		}
		if v, ok := record.Get("thargoid_progress"); ok && v != nil {
			sys.ThargoidProgress = toFloat64(v)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			sys.LastEDDNUpdate = toTime(v)
		}

		systems = append(systems, sys)
	}

	if err = result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration error: %w", err)
	}

	return systems, nil
}

// GetSystemPowerStates fetches powerplay states for multiple systems by name.
// Returns a map of system_name -> powerplay_state. Missing systems are not included.
func (c *Client) GetSystemPowerStates(ctx context.Context, systemNames []string) (map[string]string, error) {
	if len(systemNames) == 0 {
		return map[string]string{}, nil
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		UNWIND $names AS name
		MATCH (s:System {name: name})
		RETURN s.name AS name, s.powerplay_state AS powerplay_state
	`

	result, err := session.Run(ctx, query, map[string]any{"names": systemNames})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	states := make(map[string]string)
	for result.Next(ctx) {
		record := result.Record()
		name, _ := record.Get("name")
		state, _ := record.Get("powerplay_state")

		if nameStr, ok := name.(string); ok {
			stateStr := ""
			if state != nil {
				stateStr, _ = state.(string)
			}
			states[nameStr] = stateStr
		}
	}

	return states, result.Err()
}

// GetSystemStats returns basic stats from Memgraph.
func (c *Client) GetSystemStats(ctx context.Context) (map[string]int64, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, `
		MATCH (n) 
		RETURN labels(n)[0] AS label, count(*) AS cnt
	`, nil)
	if err != nil {
		return nil, err
	}

	stats := make(map[string]int64)
	for result.Next(ctx) {
		record := result.Record()
		if label, ok := record.Get("label"); ok && label != nil {
			if cnt, ok := record.Get("cnt"); ok && cnt != nil {
				stats[label.(string)] = toInt64(cnt)
			}
		}
	}

	return stats, nil
}

// StationData represents a station from Memgraph.
// Aligned with eddn-listener/MEMGRAPH-SCHEMA.md v3 (2026-01-06)
type StationData struct {
	ID64               int64     `json:"id64"`
	Name               string    `json:"name"`
	Type               string    `json:"type,omitempty"`
	SystemID64         int64     `json:"system_id64,omitempty"` // From relationship, not node
	SystemName         string    `json:"system_name,omitempty"` // From relationship, not node
	DistanceLS         float64   `json:"distance_ls,omitempty"`
	MaxPad             string    `json:"max_pad,omitempty"` // "L", "M", "S" or empty
	IsPlanetary        bool      `json:"is_planetary,omitempty"`
	Services           []string  `json:"services,omitempty"`
	ControllingFaction string    `json:"controlling_faction,omitempty"`
	HasMarket          bool      `json:"has_market,omitempty"`     // Computed from relationships
	HasShipyard        bool      `json:"has_shipyard,omitempty"`   // Computed from relationships
	HasOutfitting      bool      `json:"has_outfitting,omitempty"` // Computed from relationships
	LastEDDNUpdate     time.Time `json:"last_eddn_update,omitempty"`
}

// BodyData represents a celestial body from Memgraph.
// Aligned with eddn-listener/MEMGRAPH-SCHEMA.md v3 (2026-01-06)
type BodyData struct {
	ID64                int64     `json:"id64"`
	BodyID              int       `json:"body_id,omitempty"`
	SystemID64          int64     `json:"system_id64,omitempty"`
	SystemName          string    `json:"system_name,omitempty"` // From relationship
	Name                string    `json:"name"`
	Type                string    `json:"type,omitempty"` // Star, Planet, Moon
	SubType             string    `json:"sub_type,omitempty"`
	DistanceFromArrival float64   `json:"distance_from_arrival,omitempty"`
	Radius              float64   `json:"radius,omitempty"`
	Gravity             float64   `json:"gravity,omitempty"`
	SurfaceTemp         int       `json:"surface_temp,omitempty"`
	SurfacePressure     float64   `json:"surface_pressure,omitempty"`
	IsLandable          bool      `json:"is_landable,omitempty"`
	TerraformState      string    `json:"terraform_state,omitempty"`
	AtmosphereType      string    `json:"atmosphere_type,omitempty"`
	Volcanism           string    `json:"volcanism,omitempty"`
	WasDiscovered       bool      `json:"was_discovered,omitempty"`
	WasMapped           bool      `json:"was_mapped,omitempty"`
	LastEDDNUpdate      time.Time `json:"last_eddn_update,omitempty"`
}

// FleetCarrierData represents a fleet carrier from Memgraph.
// Aligned with eddn-listener/MEMGRAPH-SCHEMA.md v3 (2026-01-06)
type FleetCarrierData struct {
	CarrierID         string    `json:"carrier_id"`
	Name              string    `json:"name,omitempty"`
	CurrentSystemID64 int64     `json:"current_system_id64,omitempty"`
	CurrentSystemName string    `json:"current_system_name,omitempty"`
	LastSeen          time.Time `json:"last_seen,omitempty"`
	FirstSeen         time.Time `json:"first_seen,omitempty"`
	JumpCount         int       `json:"jump_count,omitempty"`
}

// MarketData represents market commodity data from Memgraph.
type MarketData struct {
	MarketID       int64     `json:"market_id"`
	StationName    string    `json:"station_name,omitempty"`
	SystemName     string    `json:"system_name,omitempty"`
	CommodityCount int       `json:"commodity_count,omitempty"`
	TopExports     []string  `json:"top_exports,omitempty"`
	TopImports     []string  `json:"top_imports,omitempty"`
	LastEDDNUpdate time.Time `json:"last_eddn_update,omitempty"`
}

// SignalData represents bio/geo signals on a body.
type SignalData struct {
	BodyID64       int64     `json:"body_id64"`
	BodyName       string    `json:"body_name,omitempty"`
	SystemName     string    `json:"system_name,omitempty"`
	Type           string    `json:"type"`
	TypeLocalised  string    `json:"type_localised,omitempty"`
	Count          int       `json:"count"`
	LastEDDNUpdate time.Time `json:"last_eddn_update,omitempty"`
}

// SystemSignalData represents system-level signals (CZ, RES, USS, Titans).
// Aligned with eddn-listener/MEMGRAPH-SCHEMA.md v3 (2026-01-06)
type SystemSignalData struct {
	SystemID64      int64     `json:"system_id64"`
	SystemName      string    `json:"system_name,omitempty"` // From relationship
	SignalType      string    `json:"signal_type"`
	SignalName      string    `json:"signal_name,omitempty"`
	USSType         string    `json:"uss_type,omitempty"`
	IsStation       bool      `json:"is_station,omitempty"`
	SpawningFaction string    `json:"spawning_faction,omitempty"`
	SpawningState   string    `json:"spawning_state,omitempty"`
	Count           int       `json:"count,omitempty"`
	FirstSeen       time.Time `json:"first_seen,omitempty"`
	LastEDDNUpdate  time.Time `json:"last_eddn_update,omitempty"`
}

// FactionData represents a minor faction from Memgraph.
type FactionData struct {
	Name           string    `json:"name"`
	Allegiance     string    `json:"allegiance,omitempty"`
	Government     string    `json:"government,omitempty"`
	SystemCount    int       `json:"system_count,omitempty"`
	LastEDDNUpdate time.Time `json:"last_eddn_update,omitempty"`
}

// FactionPresence represents a faction's presence in a system.
// Aligned with PRESENT_IN relationship properties in Memgraph schema.
type FactionPresence struct {
	FactionName   string    `json:"faction_name"`
	SystemName    string    `json:"system_name"`
	Influence     float64   `json:"influence"`
	State         string    `json:"state,omitempty"`          // Primary/first active state
	ActiveStates  []string  `json:"active_states,omitempty"`  // All active states (e.g., ["Boom", "Expansion"])
	PendingStates []string  `json:"pending_states,omitempty"` // Pending states
	Happiness     string    `json:"happiness,omitempty"`
	LastEventTime time.Time `json:"last_event_time,omitempty"`
}

// PowerData represents a powerplay power from Memgraph.
type PowerData struct {
	Name                  string    `json:"name"`
	Allegiance            string    `json:"allegiance,omitempty"`
	ControlledSystemCount int       `json:"controlled_system_count,omitempty"`
	LastEDDNUpdate        time.Time `json:"last_eddn_update,omitempty"`
}

// SystemFull represents a system with all related data.
type SystemFull struct {
	System        *SystemData        `json:"system"`
	Stations      []StationData      `json:"stations,omitempty"`
	Bodies        []BodyData         `json:"bodies,omitempty"`
	Factions      []FactionPresence  `json:"factions,omitempty"`
	Signals       []SystemSignalData `json:"signals,omitempty"`
	FleetCarriers []FleetCarrierData `json:"fleet_carriers,omitempty"`
}

// GalaxyStats represents database statistics.
type GalaxyStats struct {
	NodeCounts         map[string]int64 `json:"node_counts"`
	RelationshipCounts map[string]int64 `json:"relationship_counts,omitempty"`
	LastUpdated        time.Time        `json:"last_updated,omitempty"`
}

// GetSystemFull fetches a system with all its relationships (stations, bodies, factions, signals).
func (c *Client) GetSystemFull(ctx context.Context, systemName string) (*SystemFull, error) {
	// First get the base system
	system, err := c.GetSystem(ctx, systemName)
	if err != nil {
		return nil, err
	}
	if system == nil {
		return nil, nil
	}

	result := &SystemFull{System: system}

	// Fetch related data in parallel
	type fetchResult struct {
		stations      []StationData
		bodies        []BodyData
		factions      []FactionPresence
		signals       []SystemSignalData
		fleetCarriers []FleetCarrierData
		err           error
	}

	ch := make(chan fetchResult, 1)
	go func() {
		var fr fetchResult
		fr.stations, _ = c.GetStationsInSystem(ctx, systemName)
		fr.bodies, _ = c.GetBodiesInSystem(ctx, systemName)
		fr.factions, _ = c.GetFactionsInSystem(ctx, systemName)
		fr.signals, _ = c.GetSystemSignals(ctx, systemName, "")
		fr.fleetCarriers, _ = c.GetFleetCarriersInSystem(ctx, systemName)
		ch <- fr
	}()

	fr := <-ch
	result.Stations = fr.stations
	result.Bodies = fr.bodies
	result.Factions = fr.factions
	result.Signals = fr.signals
	result.FleetCarriers = fr.fleetCarriers

	return result, nil
}

// GetStationsInSystem fetches all stations in a system.
// Computes max_pad from large_pads/medium_pads/small_pads fields per schema.
func (c *Client) GetStationsInSystem(ctx context.Context, systemName string) ([]StationData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System {name: $name})-[:HAS_STATION]->(st:Station)
		OPTIONAL MATCH (st)-[:HAS_MARKET]->(m:Market)
		OPTIONAL MATCH (st)-[:HAS_SHIPYARD]->(sh:Shipyard)
		OPTIONAL MATCH (st)-[:HAS_OUTFITTING]->(o:Outfitting)
		RETURN
			st.id64 AS id64,
			st.name AS name,
			st.type AS type,
			st.distance_ls AS distance_ls,
			CASE
				WHEN COALESCE(st.large_pads, 0) > 0 THEN 'L'
				WHEN COALESCE(st.medium_pads, 0) > 0 THEN 'M'
				WHEN COALESCE(st.small_pads, 0) > 0 THEN 'S'
				ELSE ''
			END AS max_pad,
			st.is_planetary AS is_planetary,
			st.services AS services,
			st.controlling_faction AS controlling_faction,
			st.last_eddn_update AS last_eddn_update,
			m IS NOT NULL AS has_market,
			sh IS NOT NULL AS has_shipyard,
			o IS NOT NULL AS has_outfitting
		ORDER BY st.distance_ls
	`

	result, err := session.Run(ctx, query, map[string]any{"name": systemName})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var stations []StationData
	for result.Next(ctx) {
		record := result.Record()
		st := StationData{SystemName: systemName}

		if v, ok := record.Get("id64"); ok && v != nil {
			st.ID64 = toInt64(v)
		}
		if v, ok := record.Get("name"); ok && v != nil {
			st.Name = v.(string)
		}
		if v, ok := record.Get("type"); ok && v != nil {
			st.Type = v.(string)
		}
		if v, ok := record.Get("distance_ls"); ok && v != nil {
			st.DistanceLS = toFloat64(v)
		}
		if v, ok := record.Get("max_pad"); ok && v != nil {
			st.MaxPad = v.(string)
		}
		if v, ok := record.Get("is_planetary"); ok && v != nil {
			st.IsPlanetary, _ = v.(bool)
		}
		if v, ok := record.Get("services"); ok && v != nil {
			st.Services = toStringSlice(v)
		}
		if v, ok := record.Get("controlling_faction"); ok && v != nil {
			st.ControllingFaction = v.(string)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			st.LastEDDNUpdate = toTime(v)
		}
		if v, ok := record.Get("has_market"); ok && v != nil {
			st.HasMarket, _ = v.(bool)
		}
		if v, ok := record.Get("has_shipyard"); ok && v != nil {
			st.HasShipyard, _ = v.(bool)
		}
		if v, ok := record.Get("has_outfitting"); ok && v != nil {
			st.HasOutfitting, _ = v.(bool)
		}

		stations = append(stations, st)
	}

	return stations, result.Err()
}

// GetStation fetches a single station by ID64.
func (c *Client) GetStation(ctx context.Context, id64 int64) (*StationData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Compute max_pad from large_pads/medium_pads/small_pads fields per schema
	query := `
		MATCH (st:Station {id64: $id64})
		OPTIONAL MATCH (s:System)-[:HAS_STATION]->(st)
		OPTIONAL MATCH (st)-[:HAS_MARKET]->(m:Market)
		OPTIONAL MATCH (st)-[:HAS_SHIPYARD]->(sh:Shipyard)
		OPTIONAL MATCH (st)-[:HAS_OUTFITTING]->(o:Outfitting)
		RETURN
			st.id64 AS id64,
			st.name AS name,
			st.type AS type,
			s.name AS system_name,
			s.id64 AS system_id64,
			st.distance_ls AS distance_ls,
			CASE
				WHEN COALESCE(st.large_pads, 0) > 0 THEN 'L'
				WHEN COALESCE(st.medium_pads, 0) > 0 THEN 'M'
				WHEN COALESCE(st.small_pads, 0) > 0 THEN 'S'
				ELSE ''
			END AS max_pad,
			st.is_planetary AS is_planetary,
			st.services AS services,
			st.controlling_faction AS controlling_faction,
			st.last_eddn_update AS last_eddn_update,
			m IS NOT NULL AS has_market,
			sh IS NOT NULL AS has_shipyard,
			o IS NOT NULL AS has_outfitting
		LIMIT 1
	`

	result, err := session.Run(ctx, query, map[string]any{"id64": id64})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	if !result.Next(ctx) {
		return nil, nil
	}

	record := result.Record()
	st := &StationData{ID64: id64}

	if v, ok := record.Get("name"); ok && v != nil {
		st.Name = v.(string)
	}
	if v, ok := record.Get("type"); ok && v != nil {
		st.Type = v.(string)
	}
	if v, ok := record.Get("system_name"); ok && v != nil {
		st.SystemName = v.(string)
	}
	if v, ok := record.Get("system_id64"); ok && v != nil {
		st.SystemID64 = toInt64(v)
	}
	if v, ok := record.Get("distance_ls"); ok && v != nil {
		st.DistanceLS = toFloat64(v)
	}
	if v, ok := record.Get("max_pad"); ok && v != nil {
		st.MaxPad = v.(string)
	}
	if v, ok := record.Get("is_planetary"); ok && v != nil {
		st.IsPlanetary, _ = v.(bool)
	}
	if v, ok := record.Get("services"); ok && v != nil {
		st.Services = toStringSlice(v)
	}
	if v, ok := record.Get("controlling_faction"); ok && v != nil {
		st.ControllingFaction = v.(string)
	}
	if v, ok := record.Get("last_eddn_update"); ok && v != nil {
		st.LastEDDNUpdate = toTime(v)
	}
	if v, ok := record.Get("has_market"); ok && v != nil {
		st.HasMarket, _ = v.(bool)
	}
	if v, ok := record.Get("has_shipyard"); ok && v != nil {
		st.HasShipyard, _ = v.(bool)
	}
	if v, ok := record.Get("has_outfitting"); ok && v != nil {
		st.HasOutfitting, _ = v.(bool)
	}

	return st, result.Err()
}

// GetBodiesInSystem fetches all bodies in a system.
func (c *Client) GetBodiesInSystem(ctx context.Context, systemName string) ([]BodyData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System {name: $name})-[:HAS_BODY]->(b:Body)
		RETURN
			b.id64 AS id64,
			b.body_id AS body_id,
			b.name AS name,
			b.type AS type,
			b.sub_type AS sub_type,
			b.distance_from_arrival AS distance_from_arrival,
			b.radius AS radius,
			b.gravity AS gravity,
			b.surface_temp AS surface_temp,
			b.surface_pressure AS surface_pressure,
			b.is_landable AS is_landable,
			b.terraform_state AS terraform_state,
			b.atmosphere_type AS atmosphere_type,
			b.volcanism AS volcanism,
			b.was_discovered AS was_discovered,
			b.was_mapped AS was_mapped,
			b.last_eddn_update AS last_eddn_update
		ORDER BY b.distance_from_arrival
	`

	result, err := session.Run(ctx, query, map[string]any{"name": systemName})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var bodies []BodyData
	for result.Next(ctx) {
		record := result.Record()
		b := BodyData{SystemName: systemName}

		if v, ok := record.Get("id64"); ok && v != nil {
			b.ID64 = toInt64(v)
		}
		if v, ok := record.Get("body_id"); ok && v != nil {
			b.BodyID = int(toInt64(v))
		}
		if v, ok := record.Get("name"); ok && v != nil {
			b.Name = v.(string)
		}
		if v, ok := record.Get("type"); ok && v != nil {
			b.Type = v.(string)
		}
		if v, ok := record.Get("sub_type"); ok && v != nil {
			b.SubType = v.(string)
		}
		if v, ok := record.Get("distance_from_arrival"); ok && v != nil {
			b.DistanceFromArrival = toFloat64(v)
		}
		if v, ok := record.Get("radius"); ok && v != nil {
			b.Radius = toFloat64(v)
		}
		if v, ok := record.Get("gravity"); ok && v != nil {
			b.Gravity = toFloat64(v)
		}
		if v, ok := record.Get("surface_temp"); ok && v != nil {
			b.SurfaceTemp = int(toInt64(v))
		}
		if v, ok := record.Get("surface_pressure"); ok && v != nil {
			b.SurfacePressure = toFloat64(v)
		}
		if v, ok := record.Get("is_landable"); ok && v != nil {
			b.IsLandable, _ = v.(bool)
		}
		if v, ok := record.Get("terraform_state"); ok && v != nil {
			b.TerraformState = v.(string)
		}
		if v, ok := record.Get("atmosphere_type"); ok && v != nil {
			b.AtmosphereType = v.(string)
		}
		if v, ok := record.Get("volcanism"); ok && v != nil {
			b.Volcanism = v.(string)
		}
		if v, ok := record.Get("was_discovered"); ok && v != nil {
			b.WasDiscovered, _ = v.(bool)
		}
		if v, ok := record.Get("was_mapped"); ok && v != nil {
			b.WasMapped, _ = v.(bool)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			b.LastEDDNUpdate = toTime(v)
		}

		bodies = append(bodies, b)
	}

	return bodies, result.Err()
}

// GetFleetCarrier fetches a fleet carrier by its carrier ID (e.g., "VHT-49Z").
// Uses COALESCE to handle both last_event_time (from EDDN) and last_seen (from legacy imports).
func (c *Client) GetFleetCarrier(ctx context.Context, carrierID string) (*FleetCarrierData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (fc:FleetCarrier {carrier_id: $carrier_id})
		OPTIONAL MATCH (fc)-[:DOCKED_AT]->(s:System)
		RETURN
			fc.carrier_id AS carrier_id,
			fc.name AS name,
			COALESCE(fc.last_event_time, fc.last_seen) AS last_seen,
			fc.first_seen AS first_seen,
			fc.jump_count AS jump_count,
			s.name AS current_system_name,
			s.id64 AS current_system_id64
		LIMIT 1
	`

	result, err := session.Run(ctx, query, map[string]any{"carrier_id": carrierID})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	if !result.Next(ctx) {
		return nil, nil
	}

	record := result.Record()
	fc := &FleetCarrierData{CarrierID: carrierID}

	if v, ok := record.Get("name"); ok && v != nil {
		fc.Name = v.(string)
	}
	if v, ok := record.Get("last_seen"); ok && v != nil {
		fc.LastSeen = toTime(v)
	}
	if v, ok := record.Get("first_seen"); ok && v != nil {
		fc.FirstSeen = toTime(v)
	}
	if v, ok := record.Get("jump_count"); ok && v != nil {
		fc.JumpCount = int(toInt64(v))
	}
	if v, ok := record.Get("current_system_name"); ok && v != nil {
		fc.CurrentSystemName = v.(string)
	}
	if v, ok := record.Get("current_system_id64"); ok && v != nil {
		fc.CurrentSystemID64 = toInt64(v)
	}

	return fc, result.Err()
}

// GetFleetCarriersInSystem fetches all fleet carriers currently in a system.
func (c *Client) GetFleetCarriersInSystem(ctx context.Context, systemName string) ([]FleetCarrierData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (fc:FleetCarrier)-[:DOCKED_AT]->(s:System {name: $name})
		RETURN
			fc.carrier_id AS carrier_id,
			fc.name AS name,
			fc.first_seen AS first_seen,
			COALESCE(fc.last_event_time, fc.last_seen) AS last_seen,
			fc.jump_count AS jump_count
		ORDER BY COALESCE(fc.last_event_time, fc.last_seen) DESC
	`

	result, err := session.Run(ctx, query, map[string]any{"name": systemName})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var carriers []FleetCarrierData
	for result.Next(ctx) {
		record := result.Record()
		fc := FleetCarrierData{CurrentSystemName: systemName}

		if v, ok := record.Get("carrier_id"); ok && v != nil {
			fc.CarrierID = v.(string)
		}
		if v, ok := record.Get("name"); ok && v != nil {
			fc.Name = v.(string)
		}
		if v, ok := record.Get("first_seen"); ok && v != nil {
			fc.FirstSeen = toTime(v)
		}
		if v, ok := record.Get("last_seen"); ok && v != nil {
			fc.LastSeen = toTime(v)
		}
		if v, ok := record.Get("jump_count"); ok && v != nil {
			fc.JumpCount = int(toInt64(v))
		}

		carriers = append(carriers, fc)
	}

	return carriers, result.Err()
}

// GetFactionsInSystem fetches all factions present in a system with their influence.
// Returns full PRESENT_IN relationship data including active_states and pending_states.
func (c *Client) GetFactionsInSystem(ctx context.Context, systemName string) ([]FactionPresence, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (f:Faction)-[r:PRESENT_IN]->(s:System {name: $name})
		RETURN
			f.name AS faction_name,
			r.influence AS influence,
			r.state AS state,
			r.active_states AS active_states,
			r.pending_states AS pending_states,
			r.happiness AS happiness,
			r.last_event_time AS last_event_time
		ORDER BY r.influence DESC
	`

	result, err := session.Run(ctx, query, map[string]any{"name": systemName})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var factions []FactionPresence
	for result.Next(ctx) {
		record := result.Record()
		fp := FactionPresence{SystemName: systemName}

		if v, ok := record.Get("faction_name"); ok && v != nil {
			fp.FactionName = v.(string)
		}
		if v, ok := record.Get("influence"); ok && v != nil {
			fp.Influence = toFloat64(v)
		}
		if v, ok := record.Get("state"); ok && v != nil {
			fp.State = v.(string)
		}
		if v, ok := record.Get("active_states"); ok && v != nil {
			fp.ActiveStates = toStringSlice(v)
		}
		if v, ok := record.Get("pending_states"); ok && v != nil {
			fp.PendingStates = toStringSlice(v)
		}
		if v, ok := record.Get("happiness"); ok && v != nil {
			fp.Happiness = v.(string)
		}
		if v, ok := record.Get("last_event_time"); ok && v != nil {
			fp.LastEventTime = toTime(v)
		}

		factions = append(factions, fp)
	}

	return factions, result.Err()
}

// GetSystemSignals fetches system-level signals (Combat Zones, RES sites, etc.).
// If signalType is empty, returns all signal types.
func (c *Client) GetSystemSignals(ctx context.Context, systemName string, signalType string) ([]SystemSignalData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	var query string
	params := map[string]any{"name": systemName}

	if signalType != "" {
		query = `
			MATCH (s:System {name: $name})-[:HAS_SYSTEM_SIGNAL]->(sig:SystemSignal {signal_type: $signal_type})
			RETURN
				sig.signal_type AS signal_type,
				sig.signal_name AS signal_name,
				sig.uss_type AS uss_type,
				sig.is_station AS is_station,
				sig.spawning_faction AS spawning_faction,
				sig.spawning_state AS spawning_state,
				sig.count AS count,
				sig.first_seen AS first_seen,
				sig.last_eddn_update AS last_eddn_update
			ORDER BY sig.count DESC
		`
		params["signal_type"] = signalType
	} else {
		query = `
			MATCH (s:System {name: $name})-[:HAS_SYSTEM_SIGNAL]->(sig:SystemSignal)
			RETURN
				sig.signal_type AS signal_type,
				sig.signal_name AS signal_name,
				sig.uss_type AS uss_type,
				sig.is_station AS is_station,
				sig.spawning_faction AS spawning_faction,
				sig.spawning_state AS spawning_state,
				sig.count AS count,
				sig.first_seen AS first_seen,
				sig.last_eddn_update AS last_eddn_update
			ORDER BY sig.signal_type, sig.count DESC
		`
	}

	result, err := session.Run(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var signals []SystemSignalData
	for result.Next(ctx) {
		record := result.Record()
		sig := SystemSignalData{SystemName: systemName}

		if v, ok := record.Get("signal_type"); ok && v != nil {
			sig.SignalType = v.(string)
		}
		if v, ok := record.Get("signal_name"); ok && v != nil {
			sig.SignalName = v.(string)
		}
		if v, ok := record.Get("uss_type"); ok && v != nil {
			sig.USSType = v.(string)
		}
		if v, ok := record.Get("is_station"); ok && v != nil {
			sig.IsStation, _ = v.(bool)
		}
		if v, ok := record.Get("spawning_faction"); ok && v != nil {
			sig.SpawningFaction = v.(string)
		}
		if v, ok := record.Get("spawning_state"); ok && v != nil {
			sig.SpawningState = v.(string)
		}
		if v, ok := record.Get("count"); ok && v != nil {
			sig.Count = int(toInt64(v))
		}
		if v, ok := record.Get("first_seen"); ok && v != nil {
			sig.FirstSeen = toTime(v)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			sig.LastEDDNUpdate = toTime(v)
		}

		signals = append(signals, sig)
	}

	return signals, result.Err()
}

// GetPower fetches a powerplay power with aggregated stats.
func (c *Client) GetPower(ctx context.Context, powerName string) (*PowerData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (p:Power {name: $name})
		OPTIONAL MATCH (p)-[:CONTROLS]->(s:System)
		RETURN
			p.name AS name,
			p.allegiance AS allegiance,
			p.last_eddn_update AS last_eddn_update,
			count(s) AS controlled_system_count
	`

	result, err := session.Run(ctx, query, map[string]any{"name": powerName})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	if !result.Next(ctx) {
		return nil, nil
	}

	record := result.Record()
	power := &PowerData{}

	if v, ok := record.Get("name"); ok && v != nil {
		power.Name = v.(string)
	}
	if v, ok := record.Get("allegiance"); ok && v != nil {
		power.Allegiance = v.(string)
	}
	if v, ok := record.Get("last_eddn_update"); ok && v != nil {
		power.LastEDDNUpdate = toTime(v)
	}
	if v, ok := record.Get("controlled_system_count"); ok && v != nil {
		power.ControlledSystemCount = int(toInt64(v))
	}

	return power, result.Err()
}

// GetPowerSystems fetches systems controlled by a power.
func (c *Client) GetPowerSystems(ctx context.Context, powerName string, limit int) ([]SystemData, error) {
	if limit <= 0 {
		limit = 100
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (p:Power {name: $name})-[:CONTROLS]->(s:System)
		RETURN
			s.name AS name,
			s.powerplay_state AS powerplay_state,
			s.reinforcement AS reinforcement,
			s.undermining AS undermining,
			s.population AS population,
			s.allegiance AS allegiance
		ORDER BY s.reinforcement DESC, s.undermining DESC
		LIMIT $limit
	`

	result, err := session.Run(ctx, query, map[string]any{"name": powerName, "limit": int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var systems []SystemData
	for result.Next(ctx) {
		record := result.Record()
		sys := SystemData{ControllingPower: powerName}

		if v, ok := record.Get("name"); ok && v != nil {
			sys.Name = v.(string)
		}
		if v, ok := record.Get("powerplay_state"); ok && v != nil {
			sys.PowerplayState = v.(string)
		}
		if v, ok := record.Get("reinforcement"); ok && v != nil {
			sys.Reinforcement = toInt64(v)
		}
		if v, ok := record.Get("undermining"); ok && v != nil {
			sys.Undermining = toInt64(v)
		}
		if v, ok := record.Get("population"); ok && v != nil {
			sys.Population = toInt64(v)
		}
		if v, ok := record.Get("allegiance"); ok && v != nil {
			sys.Allegiance = v.(string)
		}

		systems = append(systems, sys)
	}

	return systems, result.Err()
}

// GetFaction fetches a minor faction.
func (c *Client) GetFaction(ctx context.Context, factionName string) (*FactionData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (f:Faction {name: $name})
		OPTIONAL MATCH (f)-[:PRESENT_IN]->(s:System)
		RETURN
			f.name AS name,
			f.allegiance AS allegiance,
			f.government AS government,
			f.last_eddn_update AS last_eddn_update,
			count(s) AS system_count
	`

	result, err := session.Run(ctx, query, map[string]any{"name": factionName})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	if !result.Next(ctx) {
		return nil, nil
	}

	record := result.Record()
	faction := &FactionData{}

	if v, ok := record.Get("name"); ok && v != nil {
		faction.Name = v.(string)
	}
	if v, ok := record.Get("allegiance"); ok && v != nil {
		faction.Allegiance = v.(string)
	}
	if v, ok := record.Get("government"); ok && v != nil {
		faction.Government = v.(string)
	}
	if v, ok := record.Get("last_eddn_update"); ok && v != nil {
		faction.LastEDDNUpdate = toTime(v)
	}
	if v, ok := record.Get("system_count"); ok && v != nil {
		faction.SystemCount = int(toInt64(v))
	}

	return faction, result.Err()
}

// GetFactionSystems fetches systems where a faction is present.
// Returns full PRESENT_IN relationship data including active_states and pending_states.
func (c *Client) GetFactionSystems(ctx context.Context, factionName string, limit int) ([]FactionPresence, error) {
	if limit <= 0 {
		limit = 50
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (f:Faction {name: $name})-[r:PRESENT_IN]->(s:System)
		RETURN
			s.name AS system_name,
			r.influence AS influence,
			r.state AS state,
			r.active_states AS active_states,
			r.pending_states AS pending_states,
			r.happiness AS happiness,
			r.last_event_time AS last_event_time
		ORDER BY r.influence DESC
		LIMIT $limit
	`

	result, err := session.Run(ctx, query, map[string]any{"name": factionName, "limit": int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var presences []FactionPresence
	for result.Next(ctx) {
		record := result.Record()
		fp := FactionPresence{FactionName: factionName}

		if v, ok := record.Get("system_name"); ok && v != nil {
			fp.SystemName = v.(string)
		}
		if v, ok := record.Get("influence"); ok && v != nil {
			fp.Influence = toFloat64(v)
		}
		if v, ok := record.Get("state"); ok && v != nil {
			fp.State = v.(string)
		}
		if v, ok := record.Get("active_states"); ok && v != nil {
			fp.ActiveStates = toStringSlice(v)
		}
		if v, ok := record.Get("pending_states"); ok && v != nil {
			fp.PendingStates = toStringSlice(v)
		}
		if v, ok := record.Get("happiness"); ok && v != nil {
			fp.Happiness = v.(string)
		}
		if v, ok := record.Get("last_event_time"); ok && v != nil {
			fp.LastEventTime = toTime(v)
		}

		presences = append(presences, fp)
	}

	return presences, result.Err()
}

// FactionStateResult represents a system where a faction is in a specific state.
type FactionStateResult struct {
	SystemName       string    `json:"system_name"`
	FactionName      string    `json:"faction_name"`
	State            string    `json:"state"`
	ActiveStates     []string  `json:"active_states,omitempty"`
	PendingStates    []string  `json:"pending_states,omitempty"`
	Influence        float64   `json:"influence"`
	Happiness        string    `json:"happiness,omitempty"`
	Allegiance       string    `json:"allegiance,omitempty"`
	Government       string    `json:"government,omitempty"`
	Population       int64     `json:"population,omitempty"`
	ControllingPower string    `json:"controlling_power,omitempty"`
	LastEventTime    time.Time `json:"last_event_time,omitempty"`
	LastEDDNUpdate   time.Time `json:"last_eddn_update,omitempty"`
}

// FindSystemsByFactionState finds systems where any faction is in a given state.
// Common states: War, Civil War, Expansion, Boom, Bust, Famine, Outbreak, Lockdown, etc.
// If factionName is provided, filters to only that faction; otherwise returns all factions in that state.
// Checks both r.state (primary state) and r.active_states array for matches.
func (c *Client) FindSystemsByFactionState(ctx context.Context, state string, factionName string, limit int) ([]FactionStateResult, error) {
	if limit <= 0 {
		limit = 50
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	var query string
	params := map[string]any{"state": state, "limit": int64(limit)}

	// Check both r.state and r.active_states for the requested state
	// This ensures we find factions where the state is in active_states but r.state might differ
	stateCondition := `(toLower(r.state) = toLower($state) OR ANY(s IN COALESCE(r.active_states, []) WHERE toLower(s) = toLower($state)))`

	if factionName != "" {
		// Filter by specific faction
		params["faction_name"] = factionName
		query = `
			MATCH (f:Faction {name: $faction_name})-[r:PRESENT_IN]->(s:System)
			WHERE ` + stateCondition + `
			RETURN
				s.name AS system_name,
				f.name AS faction_name,
				r.state AS state,
				r.active_states AS active_states,
				r.pending_states AS pending_states,
				r.influence AS influence,
				r.happiness AS happiness,
				r.last_event_time AS last_event_time,
				s.allegiance AS allegiance,
				s.government AS government,
				s.population AS population,
				s.controlling_power AS controlling_power,
				s.last_eddn_update AS last_eddn_update
			ORDER BY r.influence DESC
			LIMIT $limit
		`
	} else {
		// All factions in the given state
		query = `
			MATCH (f:Faction)-[r:PRESENT_IN]->(s:System)
			WHERE ` + stateCondition + `
			RETURN
				s.name AS system_name,
				f.name AS faction_name,
				r.state AS state,
				r.active_states AS active_states,
				r.pending_states AS pending_states,
				r.influence AS influence,
				r.happiness AS happiness,
				r.last_event_time AS last_event_time,
				s.allegiance AS allegiance,
				s.government AS government,
				s.population AS population,
				s.controlling_power AS controlling_power,
				s.last_eddn_update AS last_eddn_update
			ORDER BY r.influence DESC
			LIMIT $limit
		`
	}

	result, err := session.Run(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var results []FactionStateResult
	for result.Next(ctx) {
		record := result.Record()
		fsr := FactionStateResult{}

		if v, ok := record.Get("system_name"); ok && v != nil {
			fsr.SystemName = v.(string)
		}
		if v, ok := record.Get("faction_name"); ok && v != nil {
			fsr.FactionName = v.(string)
		}
		if v, ok := record.Get("state"); ok && v != nil {
			fsr.State = v.(string)
		}
		if v, ok := record.Get("active_states"); ok && v != nil {
			fsr.ActiveStates = toStringSlice(v)
		}
		if v, ok := record.Get("pending_states"); ok && v != nil {
			fsr.PendingStates = toStringSlice(v)
		}
		if v, ok := record.Get("influence"); ok && v != nil {
			fsr.Influence = toFloat64(v)
		}
		if v, ok := record.Get("happiness"); ok && v != nil {
			fsr.Happiness = v.(string)
		}
		if v, ok := record.Get("last_event_time"); ok && v != nil {
			fsr.LastEventTime = toTime(v)
		}
		if v, ok := record.Get("allegiance"); ok && v != nil {
			fsr.Allegiance = v.(string)
		}
		if v, ok := record.Get("government"); ok && v != nil {
			fsr.Government = v.(string)
		}
		if v, ok := record.Get("population"); ok && v != nil {
			fsr.Population = toInt64(v)
		}
		if v, ok := record.Get("controlling_power"); ok && v != nil {
			fsr.ControllingPower = v.(string)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			fsr.LastEDDNUpdate = toTime(v)
		}

		results = append(results, fsr)
	}

	return results, result.Err()
}

// GetGalaxyStats returns comprehensive database statistics.
func (c *Client) GetGalaxyStats(ctx context.Context) (*GalaxyStats, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Get node counts
	nodeResult, err := session.Run(ctx, `
		MATCH (n)
		RETURN labels(n)[0] AS label, count(*) AS cnt
		ORDER BY cnt DESC
	`, nil)
	if err != nil {
		return nil, fmt.Errorf("node count query failed: %w", err)
	}

	stats := &GalaxyStats{
		NodeCounts:         make(map[string]int64),
		RelationshipCounts: make(map[string]int64),
		LastUpdated:        time.Now(),
	}

	for nodeResult.Next(ctx) {
		record := nodeResult.Record()
		if label, ok := record.Get("label"); ok && label != nil {
			if cnt, ok := record.Get("cnt"); ok && cnt != nil {
				stats.NodeCounts[label.(string)] = toInt64(cnt)
			}
		}
	}

	// Get relationship counts
	relResult, err := session.Run(ctx, `
		MATCH ()-[r]->()
		RETURN type(r) AS rel_type, count(*) AS cnt
		ORDER BY cnt DESC
	`, nil)
	if err != nil {
		return nil, fmt.Errorf("relationship count query failed: %w", err)
	}

	for relResult.Next(ctx) {
		record := relResult.Record()
		if relType, ok := record.Get("rel_type"); ok && relType != nil {
			if cnt, ok := record.Get("cnt"); ok && cnt != nil {
				stats.RelationshipCounts[relType.(string)] = toInt64(cnt)
			}
		}
	}

	return stats, nil
}

// SchemaInfo represents the full Memgraph schema: node labels, edge types, indexes, and constraints.
type SchemaInfo struct {
	NodeLabels    []string          `json:"node_labels"`
	EdgeTypes     []string          `json:"edge_types"`
	Indexes       []IndexInfo       `json:"indexes"`
	Constraints   []ConstraintInfo  `json:"constraints"`
	NodeCounts    map[string]int64  `json:"node_counts"`
	EdgeCounts    map[string]int64  `json:"edge_counts"`
}

// IndexInfo represents a single Memgraph index.
type IndexInfo struct {
	Label      string   `json:"label"`
	Properties []string `json:"properties,omitempty"` // Empty for label-only indexes
	Type       string   `json:"type"`                 // "label", "label+property", "point", etc.
	Count      int64    `json:"count"`
}

// ConstraintInfo represents a single Memgraph constraint.
type ConstraintInfo struct {
	Type       string   `json:"type"`       // "unique", "existence", "data_type"
	Label      string   `json:"label"`
	Properties []string `json:"properties"`
}

// GetSchema returns the current Memgraph schema: labels, edge types, indexes, constraints, and counts.
// Uses SHOW INDEX INFO, SHOW CONSTRAINT INFO, and count queries.
// Node labels and edge types come from count queries (always available).
// SHOW INDEX/CONSTRAINT INFO require appropriate Memgraph version.
func (c *Client) GetSchema(ctx context.Context) (*SchemaInfo, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	schema := &SchemaInfo{
		NodeCounts: make(map[string]int64),
		EdgeCounts: make(map[string]int64),
	}

	// Node counts by label (also gives us label list)
	nodeResult, err := session.Run(ctx, `
		MATCH (n)
		RETURN labels(n)[0] AS label, count(*) AS cnt
		ORDER BY cnt DESC
	`, nil)
	if err != nil {
		return nil, fmt.Errorf("node count query: %w", err)
	}
	for nodeResult.Next(ctx) {
		record := nodeResult.Record()
		if label, ok := record.Get("label"); ok && label != nil {
			labelStr := label.(string)
			schema.NodeLabels = append(schema.NodeLabels, labelStr)
			if cnt, ok := record.Get("cnt"); ok && cnt != nil {
				schema.NodeCounts[labelStr] = toInt64(cnt)
			}
		}
	}

	// Edge counts by type (also gives us edge type list)
	edgeResult, err := session.Run(ctx, `
		MATCH ()-[r]->()
		RETURN type(r) AS rel_type, count(*) AS cnt
		ORDER BY cnt DESC
	`, nil)
	if err != nil {
		return nil, fmt.Errorf("edge count query: %w", err)
	}
	for edgeResult.Next(ctx) {
		record := edgeResult.Record()
		if relType, ok := record.Get("rel_type"); ok && relType != nil {
			typeStr := relType.(string)
			schema.EdgeTypes = append(schema.EdgeTypes, typeStr)
			if cnt, ok := record.Get("cnt"); ok && cnt != nil {
				schema.EdgeCounts[typeStr] = toInt64(cnt)
			}
		}
	}

	// Indexes (SHOW INDEX INFO)
	indexResult, err := session.Run(ctx, `SHOW INDEX INFO`, nil)
	if err != nil {
		// Not fatal — may not be supported in all versions
		log.Printf("⚠️ SHOW INDEX INFO not available: %v", err)
	} else {
		for indexResult.Next(ctx) {
			record := indexResult.Record()
			idx := IndexInfo{}
			if v, ok := record.Get("index type"); ok && v != nil {
				idx.Type = v.(string)
			}
			if v, ok := record.Get("label"); ok && v != nil {
				idx.Label = v.(string)
			}
			if v, ok := record.Get("property"); ok && v != nil {
				switch prop := v.(type) {
				case string:
					if prop != "" {
						idx.Properties = []string{prop}
					}
				case []any:
					idx.Properties = toStringSlice(v)
				}
			}
			if v, ok := record.Get("count"); ok && v != nil {
				idx.Count = toInt64(v)
			}
			schema.Indexes = append(schema.Indexes, idx)
		}
	}

	// Constraints (SHOW CONSTRAINT INFO)
	constraintResult, err := session.Run(ctx, `SHOW CONSTRAINT INFO`, nil)
	if err != nil {
		log.Printf("⚠️ SHOW CONSTRAINT INFO not available: %v", err)
	} else {
		for constraintResult.Next(ctx) {
			record := constraintResult.Record()
			c := ConstraintInfo{}
			if v, ok := record.Get("constraint type"); ok && v != nil {
				c.Type = v.(string)
			}
			if v, ok := record.Get("label"); ok && v != nil {
				c.Label = v.(string)
			}
			if v, ok := record.Get("properties"); ok && v != nil {
				c.Properties = toStringSlice(v)
			}
			schema.Constraints = append(schema.Constraints, c)
		}
	}

	return schema, nil
}

// BodyWithSignals represents a body with computed signal counts from relationships.
type BodyWithSignals struct {
	BodyData
	BioSignalCount int `json:"bio_signal_count,omitempty"`
	GeoSignalCount int `json:"geo_signal_count,omitempty"`
}

// FindBodiesWithSignals finds bodies with biological or geological signals.
// Signal counts are computed from HAS_SIGNAL relationships, not stored on Body nodes.
func (c *Client) FindBodiesWithSignals(ctx context.Context, signalType string, minCount int, limit int) ([]BodyWithSignals, error) {
	if limit <= 0 {
		limit = 50
	}
	if minCount <= 0 {
		minCount = 1
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	var query string
	params := map[string]any{"min_count": int64(minCount), "limit": int64(limit)}

	switch signalType {
	case "bio", "biological", "Biological":
		query = `
			MATCH (s:System)-[:HAS_BODY]->(b:Body)-[:HAS_SIGNAL]->(sig:Signal)
			WHERE sig.type CONTAINS 'Biological'
			WITH b, s, SUM(sig.count) AS bio_count
			WHERE bio_count >= $min_count
			RETURN
				b.id64 AS id64,
				b.name AS name,
				s.name AS system_name,
				b.type AS type,
				b.sub_type AS sub_type,
				b.is_landable AS is_landable,
				bio_count AS bio_signal_count,
				0 AS geo_signal_count,
				b.last_eddn_update AS last_eddn_update
			ORDER BY bio_count DESC
			LIMIT $limit
		`
	case "geo", "geological", "Geological":
		query = `
			MATCH (s:System)-[:HAS_BODY]->(b:Body)-[:HAS_SIGNAL]->(sig:Signal)
			WHERE sig.type CONTAINS 'Geological'
			WITH b, s, SUM(sig.count) AS geo_count
			WHERE geo_count >= $min_count
			RETURN
				b.id64 AS id64,
				b.name AS name,
				s.name AS system_name,
				b.type AS type,
				b.sub_type AS sub_type,
				b.is_landable AS is_landable,
				0 AS bio_signal_count,
				geo_count AS geo_signal_count,
				b.last_eddn_update AS last_eddn_update
			ORDER BY geo_count DESC
			LIMIT $limit
		`
	default:
		query = `
			MATCH (s:System)-[:HAS_BODY]->(b:Body)-[:HAS_SIGNAL]->(sig:Signal)
			WITH b, s,
				SUM(CASE WHEN sig.type CONTAINS 'Biological' THEN sig.count ELSE 0 END) AS bio_count,
				SUM(CASE WHEN sig.type CONTAINS 'Geological' THEN sig.count ELSE 0 END) AS geo_count
			WHERE bio_count >= $min_count OR geo_count >= $min_count
			RETURN
				b.id64 AS id64,
				b.name AS name,
				s.name AS system_name,
				b.type AS type,
				b.sub_type AS sub_type,
				b.is_landable AS is_landable,
				bio_count AS bio_signal_count,
				geo_count AS geo_signal_count,
				b.last_eddn_update AS last_eddn_update
			ORDER BY (bio_count + geo_count) DESC
			LIMIT $limit
		`
	}

	result, err := session.Run(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var bodies []BodyWithSignals
	for result.Next(ctx) {
		record := result.Record()
		b := BodyWithSignals{}

		if v, ok := record.Get("id64"); ok && v != nil {
			b.ID64 = toInt64(v)
		}
		if v, ok := record.Get("name"); ok && v != nil {
			b.Name = v.(string)
		}
		if v, ok := record.Get("system_name"); ok && v != nil {
			b.SystemName = v.(string)
		}
		if v, ok := record.Get("type"); ok && v != nil {
			b.Type = v.(string)
		}
		if v, ok := record.Get("sub_type"); ok && v != nil {
			b.SubType = v.(string)
		}
		if v, ok := record.Get("is_landable"); ok && v != nil {
			b.IsLandable, _ = v.(bool)
		}
		if v, ok := record.Get("bio_signal_count"); ok && v != nil {
			b.BioSignalCount = int(toInt64(v))
		}
		if v, ok := record.Get("geo_signal_count"); ok && v != nil {
			b.GeoSignalCount = int(toInt64(v))
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			b.LastEDDNUpdate = toTime(v)
		}

		bodies = append(bodies, b)
	}

	return bodies, result.Err()
}

// SearchStations searches for stations by name prefix.
func (c *Client) SearchStations(ctx context.Context, query string, limit int) ([]StationData, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	cypher := `
		MATCH (s:System)-[:HAS_STATION]->(st:Station)
		WHERE toLower(st.name) STARTS WITH toLower($query)
		RETURN
			st.id64 AS id64,
			st.name AS name,
			st.type AS type,
			s.name AS system_name,
			s.id64 AS system_id64,
			st.distance_ls AS distance_ls,
			st.max_pad AS max_pad,
			st.is_planetary AS is_planetary,
			st.services AS services,
			st.last_eddn_update AS last_eddn_update
		ORDER BY st.name
		LIMIT $limit
	`

	result, err := session.Run(ctx, cypher, map[string]any{"query": query, "limit": int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("search query failed: %w", err)
	}

	var stations []StationData
	for result.Next(ctx) {
		record := result.Record()
		st := StationData{}

		if v, ok := record.Get("id64"); ok && v != nil {
			st.ID64 = toInt64(v)
		}
		if v, ok := record.Get("name"); ok && v != nil {
			st.Name = v.(string)
		}
		if v, ok := record.Get("type"); ok && v != nil {
			st.Type = v.(string)
		}
		if v, ok := record.Get("system_name"); ok && v != nil {
			st.SystemName = v.(string)
		}
		if v, ok := record.Get("system_id64"); ok && v != nil {
			st.SystemID64 = toInt64(v)
		}
		if v, ok := record.Get("distance_ls"); ok && v != nil {
			st.DistanceLS = toFloat64(v)
		}
		if v, ok := record.Get("max_pad"); ok && v != nil {
			st.MaxPad = v.(string)
		}
		if v, ok := record.Get("is_planetary"); ok && v != nil {
			st.IsPlanetary, _ = v.(bool)
		}
		if v, ok := record.Get("services"); ok && v != nil {
			st.Services = toStringSlice(v)
		}
		if v, ok := record.Get("last_eddn_update"); ok && v != nil {
			st.LastEDDNUpdate = toTime(v)
		}

		stations = append(stations, st)
	}

	return stations, result.Err()
}

// ExecuteQuery runs an arbitrary Cypher query and returns results as a slice of maps.
// This is used for ad-hoc queries from the galaxy_query tool.
func (c *Client) ExecuteQuery(ctx context.Context, query string, params map[string]any) ([]map[string]any, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, query, params)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	var rows []map[string]any
	for result.Next(ctx) {
		record := result.Record()
		row := make(map[string]any)

		keys := record.Keys
		for _, key := range keys {
			if v, ok := record.Get(key); ok {
				// Convert Neo4j types to standard Go types
				row[key] = convertNeo4jValue(v)
			}
		}
		rows = append(rows, row)
	}

	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration failed: %w", err)
	}

	return rows, nil
}

// convertNeo4jValue converts Neo4j types to standard Go types for JSON serialization.
func convertNeo4jValue(v any) any {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case int64:
		return val
	case float64:
		return val
	case bool:
		return val
	case string:
		return val
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = convertNeo4jValue(item)
		}
		return result
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		// Try to return as string representation
		return fmt.Sprintf("%v", val)
	}
}

// Helper functions
func toInt64(v any) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int:
		return int64(val)
	case float64:
		return int64(val)
	default:
		return 0
	}
}

func toFloat64(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int64:
		return float64(val)
	case int:
		return float64(val)
	default:
		return 0
	}
}

func toTime(v any) time.Time {
	switch val := v.(type) {
	case time.Time:
		return val
	case neo4j.LocalDateTime:
		return val.Time()
	case neo4j.Time:
		// neo4j.Time is used for ZONED_DATE_TIME values from Memgraph
		return time.Time(val)
	case neo4j.Date:
		return time.Time(val)
	case string:
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t
		}
		// Try parsing with timezone suffix like "2026-02-01T19:42:33.000000+00:00[Etc/UTC]"
		if idx := strings.Index(val, "["); idx > 0 {
			if t, err := time.Parse(time.RFC3339, val[:idx]); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		result := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// parseConflictProgress parses powerplay_conflict_progress from Memgraph.
// The data may come as either:
// - A JSON string: "[{\"Power\":\"Name\",\"Progress\":0.5}, ...]"
// - An already-parsed []any slice
func parseConflictProgress(v any) []map[string]any {
	if v == nil {
		return nil
	}

	// If it's already a slice, parse directly
	if pcp, ok := v.([]any); ok {
		var result []map[string]any
		for _, item := range pcp {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			}
		}
		return result
	}

	// If it's a string, unmarshal JSON
	if str, ok := v.(string); ok && str != "" {
		var entries []map[string]any
		if err := json.Unmarshal([]byte(str), &entries); err == nil {
			return entries
		}
	}

	return nil
}

// PowerStateCounts represents state counts for a power within a set of systems.
type PowerStateCounts struct {
	Power      string         `json:"power"`
	Allegiance string         `json:"allegiance,omitempty"`
	States     map[string]int `json:"states"` // e.g., {"Exploited": 5, "Fortified": 2}
	Total      int            `json:"total"`
}

// GetPowerStateCountsForSystems returns state counts per power for a specific list of systems.
// This is used for the HIP Thunderdome leaderboard to show how many systems each power
// has in each state (Exploited, Fortified, Stronghold, etc.) within the CG area.
func (c *Client) GetPowerStateCountsForSystems(ctx context.Context, systemNames []string) (map[string]*PowerStateCounts, error) {
	if len(systemNames) == 0 {
		return nil, nil
	}

	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.name IN $names AND s.controlling_power IS NOT NULL AND s.controlling_power <> ""
		RETURN
			s.controlling_power AS power,
			s.powerplay_state AS state,
			count(*) AS count
		ORDER BY power, state
	`

	result, err := session.Run(ctx, query, map[string]any{"names": systemNames})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	counts := make(map[string]*PowerStateCounts)

	for result.Next(ctx) {
		record := result.Record()

		var power, state string
		var count int64

		if v, ok := record.Get("power"); ok && v != nil {
			power = v.(string)
		}
		if v, ok := record.Get("state"); ok && v != nil {
			state = v.(string)
		}
		if v, ok := record.Get("count"); ok && v != nil {
			count = toInt64(v)
		}

		if power == "" {
			continue
		}

		if counts[power] == nil {
			counts[power] = &PowerStateCounts{
				Power:  power,
				States: make(map[string]int),
			}
		}

		counts[power].States[state] = int(count)
		counts[power].Total += int(count)
	}

	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration failed: %w", err)
	}

	return counts, nil
}

// MarketCommodity represents a single commodity's market data.
type MarketCommodity struct {
	Name      string `json:"name"`
	BuyPrice  int64  `json:"buy_price"`
	SellPrice int64  `json:"sell_price"`
	Demand    int64  `json:"demand"`
	Stock     int64  `json:"stock"`
	Category  string `json:"category,omitempty"`
}

// FactionState represents a BGS state for a faction present in a system.
type FactionState struct {
	FactionName string   `json:"faction_name"`
	States      []string `json:"states"` // Active BGS states (Boom, Expansion, etc.)
}

// StationMarketData represents full market data for a station.
type StationMarketData struct {
	StationName   string            `json:"station_name"`
	SystemName    string            `json:"system_name"`
	MarketID      int64             `json:"market_id,omitempty"`
	Commodities   []MarketCommodity `json:"commodities"`
	LastUpdate    time.Time         `json:"last_update,omitempty"`
	FactionStates []FactionState    `json:"faction_states,omitempty"` // BGS states for factions in the system
}

// GetStationMarket fetches all commodity data for a station's market.
func (c *Client) GetStationMarket(ctx context.Context, systemName, stationName string) (*StationMarketData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (sys:System {name: $systemName})-[:HAS_STATION]->(sta:Station {name: $stationName})
		MATCH (sta)-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
		RETURN
			m.market_id AS market_id,
			m.last_eddn_update AS last_update,
			c.name AS commodity_name,
			c.category AS category,
			t.buy_price AS buy_price,
			t.sell_price AS sell_price,
			t.demand AS demand,
			t.stock AS stock
		ORDER BY c.category, c.name
	`

	result, err := session.Run(ctx, query, map[string]any{
		"systemName":  systemName,
		"stationName": stationName,
	})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	market := &StationMarketData{
		StationName: stationName,
		SystemName:  systemName,
		Commodities: []MarketCommodity{},
	}

	for result.Next(ctx) {
		record := result.Record()

		// Get market-level info from first record
		if market.MarketID == 0 {
			if v, ok := record.Get("market_id"); ok && v != nil {
				market.MarketID = toInt64(v)
			}
			if v, ok := record.Get("last_update"); ok && v != nil {
				market.LastUpdate = toTime(v)
			}
		}

		// Get commodity data
		commodity := MarketCommodity{}
		if v, ok := record.Get("commodity_name"); ok && v != nil {
			commodity.Name = v.(string)
		}
		if v, ok := record.Get("category"); ok && v != nil {
			commodity.Category = v.(string)
		}
		if v, ok := record.Get("buy_price"); ok && v != nil {
			commodity.BuyPrice = toInt64(v)
		}
		if v, ok := record.Get("sell_price"); ok && v != nil {
			commodity.SellPrice = toInt64(v)
		}
		if v, ok := record.Get("demand"); ok && v != nil {
			commodity.Demand = toInt64(v)
		}
		if v, ok := record.Get("stock"); ok && v != nil {
			commodity.Stock = toInt64(v)
		}

		if commodity.Name != "" {
			market.Commodities = append(market.Commodities, commodity)
		}
	}

	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration failed: %w", err)
	}

	// Return nil if no market found
	if len(market.Commodities) == 0 && market.MarketID == 0 {
		return nil, nil
	}

	// Fetch faction states for the system (BGS states are on PRESENT_IN relationship)
	factionQuery := `
		MATCH (sys:System {name: $systemName})<-[pi:PRESENT_IN]-(f:Faction)
		WHERE pi.active_states IS NOT NULL AND size(pi.active_states) > 0
		RETURN f.name AS faction_name, pi.active_states AS states
		ORDER BY f.name
	`

	log.Printf("📊 Fetching faction states for system: %s", systemName)
	factionResult, err := session.Run(ctx, factionQuery, map[string]any{
		"systemName": systemName,
	})
	if err != nil {
		// Log but don't fail - faction states are supplementary
		log.Printf("⚠️ faction states query failed: %v", err)
	} else {
		factionCount := 0
		for factionResult.Next(ctx) {
			record := factionResult.Record()
			fs := FactionState{}
			if v, ok := record.Get("faction_name"); ok && v != nil {
				fs.FactionName = v.(string)
			}
			if v, ok := record.Get("states"); ok && v != nil {
				if states, ok := v.([]any); ok {
					for _, s := range states {
						if str, ok := s.(string); ok {
							fs.States = append(fs.States, str)
						}
					}
				}
			}
			if fs.FactionName != "" && len(fs.States) > 0 {
				market.FactionStates = append(market.FactionStates, fs)
				factionCount++
			}
		}
		log.Printf("📊 Found %d factions with active states for %s", factionCount, systemName)
		if factionResult.Err() != nil {
			log.Printf("⚠️ faction result iteration error: %v", factionResult.Err())
		}
	}

	return market, nil
}

// GetFleetCarrierMarket fetches market data for a fleet carrier.
func (c *Client) GetFleetCarrierMarket(ctx context.Context, carrierID string) (*StationMarketData, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (fc:FleetCarrier {carrier_id: $carrierID})-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
		OPTIONAL MATCH (fc)-[:DOCKED_AT]->(sys:System)
		RETURN
			fc.name AS carrier_name,
			sys.name AS system_name,
			m.market_id AS market_id,
			m.last_eddn_update AS last_update,
			c.name AS commodity_name,
			c.category AS category,
			t.buy_price AS buy_price,
			t.sell_price AS sell_price,
			t.demand AS demand,
			t.stock AS stock
		ORDER BY c.category, c.name
	`

	result, err := session.Run(ctx, query, map[string]any{"carrierID": carrierID})
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	market := &StationMarketData{
		Commodities: []MarketCommodity{},
	}

	for result.Next(ctx) {
		record := result.Record()

		// Get carrier/market-level info from first record
		if market.StationName == "" {
			if v, ok := record.Get("carrier_name"); ok && v != nil {
				market.StationName = v.(string)
			}
			if v, ok := record.Get("system_name"); ok && v != nil {
				market.SystemName = v.(string)
			}
			if v, ok := record.Get("market_id"); ok && v != nil {
				market.MarketID = toInt64(v)
			}
			if v, ok := record.Get("last_update"); ok && v != nil {
				market.LastUpdate = toTime(v)
			}
		}

		// Get commodity data
		commodity := MarketCommodity{}
		if v, ok := record.Get("commodity_name"); ok && v != nil {
			commodity.Name = v.(string)
		}
		if v, ok := record.Get("category"); ok && v != nil {
			commodity.Category = v.(string)
		}
		if v, ok := record.Get("buy_price"); ok && v != nil {
			commodity.BuyPrice = toInt64(v)
		}
		if v, ok := record.Get("sell_price"); ok && v != nil {
			commodity.SellPrice = toInt64(v)
		}
		if v, ok := record.Get("demand"); ok && v != nil {
			commodity.Demand = toInt64(v)
		}
		if v, ok := record.Get("stock"); ok && v != nil {
			commodity.Stock = toInt64(v)
		}

		if commodity.Name != "" {
			market.Commodities = append(market.Commodities, commodity)
		}
	}

	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration failed: %w", err)
	}

	// Return nil if no market found
	if len(market.Commodities) == 0 && market.MarketID == 0 {
		return nil, nil
	}

	return market, nil
}
