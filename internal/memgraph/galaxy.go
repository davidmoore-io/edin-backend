// Package memgraph provides galaxy visualization queries for the 3D galaxy map.
// These methods support the galaxy view API endpoints for spatial queries,
// system detail, and orrery view data.
package memgraph

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// ErrSystemNotFound is returned when a system cannot be found.
var ErrSystemNotFound = errors.New("system not found")

// GalaxyViewRequest represents the viewport parameters for spatial queries.
type GalaxyViewRequest struct {
	MinX  float64 `json:"min_x"`
	MaxX  float64 `json:"max_x"`
	MinY  float64 `json:"min_y"`
	MaxY  float64 `json:"max_y"`
	MinZ  float64 `json:"min_z"`
	MaxZ  float64 `json:"max_z"`
	Limit int     `json:"limit"`

	// Optional filters
	Power      string `json:"power,omitempty"`
	Allegiance string `json:"allegiance,omitempty"`
	State      string `json:"state,omitempty"`
}

// GalaxySystem represents a system for visualization.
type GalaxySystem struct {
	ID64             int64   `json:"id64"`
	Name             string  `json:"name"`
	X                float64 `json:"x"`
	Y                float64 `json:"y"`
	Z                float64 `json:"z"`
	ControllingPower string  `json:"controlling_power,omitempty"`
	PowerplayState   string  `json:"powerplay_state,omitempty"`
	Allegiance       string  `json:"allegiance,omitempty"`
	Population       int64   `json:"population,omitempty"`
}

// GalaxyViewResponse contains the systems in view.
type GalaxyViewResponse struct {
	Systems     []GalaxySystem `json:"systems"`
	TotalCount  int            `json:"total_count"`
	Truncated   bool           `json:"truncated"`
	QueryTimeMs int64          `json:"query_time_ms"`
}

// SystemDetailResponse contains full system information for orrery view.
type SystemDetailResponse struct {
	System   SystemInfo    `json:"system"`
	Bodies   []BodyInfo    `json:"bodies"`
	Stations []StationInfo `json:"stations"`
}

// SystemInfo represents core system metadata.
type SystemInfo struct {
	ID64                    int64   `json:"id64"`
	Name                    string  `json:"name"`
	X                       float64 `json:"x"`
	Y                       float64 `json:"y"`
	Z                       float64 `json:"z"`
	Population              int64   `json:"population,omitempty"`
	Allegiance              string  `json:"allegiance,omitempty"`
	Government              string  `json:"government,omitempty"`
	Economy                 string  `json:"economy,omitempty"`
	SecondEconomy           string  `json:"second_economy,omitempty"`
	Security                string  `json:"security,omitempty"`
	ControllingFaction      string  `json:"controlling_faction,omitempty"`
	ControllingFactionState string  `json:"controlling_faction_state,omitempty"`
	ControllingPower        string    `json:"controlling_power,omitempty"`
	PowerplayState          string    `json:"powerplay_state,omitempty"`
	LastEDDNUpdate          time.Time `json:"last_eddn_update,omitempty"`
}

// BodyInfo represents a celestial body with orbital parameters.
type BodyInfo struct {
	ID64                int64    `json:"id64"`
	BodyID              int      `json:"body_id"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	SubType             string   `json:"sub_type,omitempty"`
	DistanceFromArrival float64  `json:"distance_from_arrival"`
	Radius              float64  `json:"radius,omitempty"`

	// Stellar properties
	SpectralClass    string  `json:"spectral_class,omitempty"`
	Luminosity       string  `json:"luminosity,omitempty"`
	StellarMass      float64 `json:"stellar_mass,omitempty"`
	SolarRadius      float64 `json:"solar_radius,omitempty"`
	AbsoluteMagnitude float64 `json:"absolute_magnitude,omitempty"`
	Age              float64 `json:"age,omitempty"` // millions of years

	// Planetary properties
	EarthMasses     float64 `json:"earth_masses,omitempty"`
	Gravity         float64 `json:"gravity,omitempty"`
	SurfaceTemp     float64 `json:"surface_temp,omitempty"`     // Kelvin
	SurfacePressure float64 `json:"surface_pressure,omitempty"` // Atmospheres
	AtmosphereType  string  `json:"atmosphere_type,omitempty"`
	Volcanism       string  `json:"volcanism,omitempty"`
	TerraformState  string  `json:"terraform_state,omitempty"`

	// Orbital parameters
	SemiMajorAxis       *float64 `json:"semi_major_axis,omitempty"`
	OrbitalPeriod       *float64 `json:"orbital_period,omitempty"`
	OrbitalEccentricity *float64 `json:"orbital_eccentricity,omitempty"`
	AxialTilt           *float64 `json:"axial_tilt,omitempty"`
	RotationPeriod      *float64 `json:"rotation_period,omitempty"`
	TidallyLocked       bool     `json:"tidally_locked,omitempty"`

	// Discovery
	IsMainStar    bool `json:"is_main_star,omitempty"`
	IsLandable    bool `json:"is_landable,omitempty"`
	WasDiscovered bool `json:"was_discovered,omitempty"`
	WasMapped     bool `json:"was_mapped,omitempty"`

	Rings []RingInfo `json:"rings,omitempty"`
}

// RingInfo represents a planetary ring.
type RingInfo struct {
	Name      string  `json:"name"`
	RingClass string  `json:"ring_class"`
	InnerRad  float64 `json:"inner_rad"` // meters
	OuterRad  float64 `json:"outer_rad"` // meters
}

// StationInfo represents a space station.
type StationInfo struct {
	MarketID    int64          `json:"market_id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	DistanceLS  float64        `json:"distance_ls"`
	LandingPads map[string]int `json:"landing_pads"`
	Services    []string       `json:"services,omitempty"`
}

// GalaxyViewStats contains aggregate statistics for the galaxy visualization.
type GalaxyViewStats struct {
	TotalSystems    int   `json:"total_systems"`
	TotalPopulation int64 `json:"total_population"`
	TotalPowers     int   `json:"total_powers"`
	TotalStations   int   `json:"total_stations"`
}

// GetSystemsInBounds returns systems within a 3D bounding box for galaxy visualization.
// This is the core spatial query for the galaxy map chunk loader.
// Uses point.withinbbox() with Memgraph's spatial point index for efficient lookup.
func (c *Client) GetSystemsInBounds(ctx context.Context, req GalaxyViewRequest) ([]GalaxySystem, int, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Build the query with optional filters
	query := `
		MATCH (s:System)
		WHERE point.withinbbox(
		    s.location,
		    point({x: $minX, y: $minY, z: $minZ}),
		    point({x: $maxX, y: $maxY, z: $maxZ})
		  )
	`

	// Add optional filters
	params := map[string]any{
		"minX":  req.MinX,
		"maxX":  req.MaxX,
		"minY":  req.MinY,
		"maxY":  req.MaxY,
		"minZ":  req.MinZ,
		"maxZ":  req.MaxZ,
		"limit": req.Limit,
	}

	if req.Power != "" {
		query += "  AND s.controlling_power = $power\n"
		params["power"] = req.Power
	}
	if req.Allegiance != "" {
		query += "  AND s.allegiance = $allegiance\n"
		params["allegiance"] = req.Allegiance
	}
	if req.State != "" {
		query += "  AND s.powerplay_state = $state\n"
		params["state"] = req.State
	}

	// First, get total count for the region (without limit)
	countQuery := query + `
		RETURN count(s) AS total
	`
	countResult, err := session.Run(ctx, countQuery, params)
	if err != nil {
		return nil, 0, fmt.Errorf("count query failed: %w", err)
	}

	var totalCount int
	if countResult.Next(ctx) {
		if v, ok := countResult.Record().Get("total"); ok && v != nil {
			totalCount = int(toInt64(v))
		}
	}
	if err = countResult.Err(); err != nil {
		return nil, 0, fmt.Errorf("count result error: %w", err)
	}

	// Now get the actual systems with limit
	dataQuery := query + `
		RETURN s.id64 AS id64,
		       s.name AS name,
		       s.location.x AS x,
		       s.location.y AS y,
		       s.location.z AS z,
		       s.controlling_power AS controlling_power,
		       s.powerplay_state AS powerplay_state,
		       s.allegiance AS allegiance,
		       s.population AS population
		LIMIT $limit
	`

	result, err := session.Run(ctx, dataQuery, params)
	if err != nil {
		return nil, 0, fmt.Errorf("data query failed: %w", err)
	}

	systems := make([]GalaxySystem, 0, min(totalCount, req.Limit))
	for result.Next(ctx) {
		record := result.Record()
		sys := GalaxySystem{}

		if v, ok := record.Get("id64"); ok && v != nil {
			sys.ID64 = toInt64(v)
		}
		if v, ok := record.Get("name"); ok && v != nil {
			sys.Name = v.(string)
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
		if v, ok := record.Get("controlling_power"); ok && v != nil {
			sys.ControllingPower = v.(string)
		}
		if v, ok := record.Get("powerplay_state"); ok && v != nil {
			sys.PowerplayState = v.(string)
		}
		if v, ok := record.Get("allegiance"); ok && v != nil {
			sys.Allegiance = v.(string)
		}
		if v, ok := record.Get("population"); ok && v != nil {
			sys.Population = toInt64(v)
		}

		systems = append(systems, sys)
	}

	if err = result.Err(); err != nil {
		return nil, 0, fmt.Errorf("result iteration error: %w", err)
	}

	return systems, totalCount, nil
}

// GetSystemDetail returns full system information including bodies, stations,
// and settlements for the orrery view.
func (c *Client) GetSystemDetail(ctx context.Context, systemID int64) (*SystemDetailResponse, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// First, get the system info
	sysQuery := `
		MATCH (s:System)
		WHERE s.id64 = $id64
		RETURN s.id64 AS id64,
		       s.name AS name,
		       s.location.x AS x,
		       s.location.y AS y,
		       s.location.z AS z,
		       s.population AS population,
		       s.allegiance AS allegiance,
		       s.government AS government,
		       s.economy AS economy,
		       s.second_economy AS second_economy,
		       s.security AS security,
		       s.controlling_faction AS controlling_faction,
		       s.controlling_faction_state AS controlling_faction_state,
		       s.controlling_power AS controlling_power,
		       s.powerplay_state AS powerplay_state,
		       s.last_eddn_update AS last_eddn_update
	`

	sysResult, err := session.Run(ctx, sysQuery, map[string]any{"id64": systemID})
	if err != nil {
		return nil, fmt.Errorf("system query failed: %w", err)
	}

	if !sysResult.Next(ctx) {
		return nil, ErrSystemNotFound
	}

	sysRecord := sysResult.Record()
	sysInfo := SystemInfo{}

	if v, ok := sysRecord.Get("id64"); ok && v != nil {
		sysInfo.ID64 = toInt64(v)
	}
	if v, ok := sysRecord.Get("name"); ok && v != nil {
		sysInfo.Name = v.(string)
	}
	if v, ok := sysRecord.Get("x"); ok && v != nil {
		sysInfo.X = toFloat64(v)
	}
	if v, ok := sysRecord.Get("y"); ok && v != nil {
		sysInfo.Y = toFloat64(v)
	}
	if v, ok := sysRecord.Get("z"); ok && v != nil {
		sysInfo.Z = toFloat64(v)
	}
	if v, ok := sysRecord.Get("population"); ok && v != nil {
		sysInfo.Population = toInt64(v)
	}
	if v, ok := sysRecord.Get("allegiance"); ok && v != nil {
		sysInfo.Allegiance = v.(string)
	}
	if v, ok := sysRecord.Get("government"); ok && v != nil {
		sysInfo.Government = v.(string)
	}
	if v, ok := sysRecord.Get("economy"); ok && v != nil {
		sysInfo.Economy = v.(string)
	}
	if v, ok := sysRecord.Get("second_economy"); ok && v != nil {
		sysInfo.SecondEconomy = v.(string)
	}
	if v, ok := sysRecord.Get("security"); ok && v != nil {
		sysInfo.Security = v.(string)
	}
	if v, ok := sysRecord.Get("controlling_faction"); ok && v != nil {
		sysInfo.ControllingFaction = v.(string)
	}
	if v, ok := sysRecord.Get("controlling_faction_state"); ok && v != nil {
		sysInfo.ControllingFactionState = v.(string)
	}
	if v, ok := sysRecord.Get("controlling_power"); ok && v != nil {
		sysInfo.ControllingPower = v.(string)
	}
	if v, ok := sysRecord.Get("powerplay_state"); ok && v != nil {
		sysInfo.PowerplayState = v.(string)
	}
	if v, ok := sysRecord.Get("last_eddn_update"); ok && v != nil {
		sysInfo.LastEDDNUpdate = toTime(v)
	}

	// Get bodies with orbital parameters
	bodies, err := c.getSystemBodies(ctx, session, systemID)
	if err != nil {
		return nil, fmt.Errorf("bodies query failed: %w", err)
	}

	// Get stations
	stations, err := c.getSystemStations(ctx, session, systemID)
	if err != nil {
		return nil, fmt.Errorf("stations query failed: %w", err)
	}

	return &SystemDetailResponse{
		System:   sysInfo,
		Bodies:   bodies,
		Stations: stations,
	}, nil
}

// GetSystemDetailByName returns full system information by name.
func (c *Client) GetSystemDetailByName(ctx context.Context, systemName string) (*SystemDetailResponse, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// First, get the system id64 by name
	idQuery := `
		MATCH (s:System)
		WHERE s.name = $name
		RETURN s.id64 AS id64
		LIMIT 1
	`

	result, err := session.Run(ctx, idQuery, map[string]any{"name": systemName})
	if err != nil {
		return nil, fmt.Errorf("id lookup query failed: %w", err)
	}

	if !result.Next(ctx) {
		return nil, ErrSystemNotFound
	}

	record := result.Record()
	var systemID int64
	if v, ok := record.Get("id64"); ok && v != nil {
		systemID = toInt64(v)
	}

	// Close this session and use GetSystemDetail for the rest
	session.Close(ctx)

	return c.GetSystemDetail(ctx, systemID)
}

// getSystemBodies fetches all bodies for a system with orbital parameters.
func (c *Client) getSystemBodies(ctx context.Context, session neo4j.SessionWithContext, systemID int64) ([]BodyInfo, error) {
	query := `
		MATCH (s:System)-[:HAS_BODY]->(b:Body)
		WHERE s.id64 = $id64
		OPTIONAL MATCH (b)-[:HAS_RING]->(r:Ring)
		WITH b, collect(r) AS rings
		RETURN b.id64 AS id64,
		       b.body_id AS body_id,
		       b.name AS name,
		       b.type AS type,
		       b.sub_type AS sub_type,
		       b.distance_from_arrival AS distance_from_arrival,
		       b.radius AS radius,
		       b.spectral_class AS spectral_class,
		       b.luminosity AS luminosity,
		       b.stellar_mass AS stellar_mass,
		       b.solar_radius AS solar_radius,
		       b.absolute_magnitude AS absolute_magnitude,
		       b.age AS age,
		       b.earth_masses AS earth_masses,
		       b.gravity AS gravity,
		       b.surface_temp AS surface_temp,
		       b.surface_pressure AS surface_pressure,
		       b.atmosphere_type AS atmosphere_type,
		       b.volcanism AS volcanism,
		       b.terraform_state AS terraform_state,
		       b.semi_major_axis AS semi_major_axis,
		       b.orbital_period AS orbital_period,
		       b.orbital_eccentricity AS orbital_eccentricity,
		       b.axial_tilt AS axial_tilt,
		       b.rotation_period AS rotation_period,
		       b.tidally_locked AS tidally_locked,
		       b.is_main_star AS is_main_star,
		       b.is_landable AS is_landable,
		       b.was_discovered AS was_discovered,
		       b.was_mapped AS was_mapped,
		       rings
		ORDER BY b.body_id
	`

	result, err := session.Run(ctx, query, map[string]any{"id64": systemID})
	if err != nil {
		return nil, err
	}

	var bodies []BodyInfo
	for result.Next(ctx) {
		record := result.Record()
		body := BodyInfo{}

		if v, ok := record.Get("id64"); ok && v != nil {
			body.ID64 = toInt64(v)
		}
		if v, ok := record.Get("body_id"); ok && v != nil {
			body.BodyID = int(toInt64(v))
		}
		if v, ok := record.Get("name"); ok && v != nil {
			body.Name = v.(string)
		}
		if v, ok := record.Get("type"); ok && v != nil {
			body.Type = v.(string)
		}
		if v, ok := record.Get("sub_type"); ok && v != nil {
			body.SubType = v.(string)
		}
		if v, ok := record.Get("distance_from_arrival"); ok && v != nil {
			body.DistanceFromArrival = toFloat64(v)
		}
		if v, ok := record.Get("radius"); ok && v != nil {
			body.Radius = toFloat64(v)
		}

		// Stellar properties
		if v, ok := record.Get("spectral_class"); ok && v != nil {
			body.SpectralClass = v.(string)
		}
		if v, ok := record.Get("luminosity"); ok && v != nil {
			body.Luminosity = v.(string)
		}
		if v, ok := record.Get("stellar_mass"); ok && v != nil {
			body.StellarMass = toFloat64(v)
		}
		if v, ok := record.Get("solar_radius"); ok && v != nil {
			body.SolarRadius = toFloat64(v)
		}
		if v, ok := record.Get("absolute_magnitude"); ok && v != nil {
			body.AbsoluteMagnitude = toFloat64(v)
		}
		if v, ok := record.Get("age"); ok && v != nil {
			body.Age = toFloat64(v)
		}

		// Planetary properties
		if v, ok := record.Get("earth_masses"); ok && v != nil {
			body.EarthMasses = toFloat64(v)
		}
		if v, ok := record.Get("gravity"); ok && v != nil {
			body.Gravity = toFloat64(v)
		}
		if v, ok := record.Get("surface_temp"); ok && v != nil {
			body.SurfaceTemp = toFloat64(v)
		}
		if v, ok := record.Get("surface_pressure"); ok && v != nil {
			body.SurfacePressure = toFloat64(v)
		}
		if v, ok := record.Get("atmosphere_type"); ok && v != nil {
			body.AtmosphereType = v.(string)
		}
		if v, ok := record.Get("volcanism"); ok && v != nil {
			body.Volcanism = v.(string)
		}
		if v, ok := record.Get("terraform_state"); ok && v != nil {
			body.TerraformState = v.(string)
		}

		// Orbital parameters (nullable)
		if v, ok := record.Get("semi_major_axis"); ok && v != nil {
			val := toFloat64(v)
			body.SemiMajorAxis = &val
		}
		if v, ok := record.Get("orbital_period"); ok && v != nil {
			val := toFloat64(v)
			body.OrbitalPeriod = &val
		}
		if v, ok := record.Get("orbital_eccentricity"); ok && v != nil {
			val := toFloat64(v)
			body.OrbitalEccentricity = &val
		}
		if v, ok := record.Get("axial_tilt"); ok && v != nil {
			val := toFloat64(v)
			body.AxialTilt = &val
		}
		if v, ok := record.Get("rotation_period"); ok && v != nil {
			val := toFloat64(v)
			body.RotationPeriod = &val
		}
		if v, ok := record.Get("tidally_locked"); ok && v != nil {
			body.TidallyLocked = v.(bool)
		}
		if v, ok := record.Get("is_main_star"); ok && v != nil {
			body.IsMainStar = v.(bool)
		}
		if v, ok := record.Get("is_landable"); ok && v != nil {
			body.IsLandable = v.(bool)
		}
		if v, ok := record.Get("was_discovered"); ok && v != nil {
			body.WasDiscovered = v.(bool)
		}
		if v, ok := record.Get("was_mapped"); ok && v != nil {
			body.WasMapped = v.(bool)
		}

		// Parse rings
		if v, ok := record.Get("rings"); ok && v != nil {
			if ringNodes, ok := v.([]any); ok {
				for _, ringNode := range ringNodes {
					if rn, ok := ringNode.(neo4j.Node); ok {
						ring := RingInfo{}
						if name, ok := rn.Props["name"].(string); ok {
							ring.Name = name
						}
						if ringClass, ok := rn.Props["ring_class"].(string); ok {
							ring.RingClass = ringClass
						}
						if innerRad, ok := rn.Props["inner_rad"]; ok {
							ring.InnerRad = toFloat64(innerRad)
						}
						if outerRad, ok := rn.Props["outer_rad"]; ok {
							ring.OuterRad = toFloat64(outerRad)
						}
						body.Rings = append(body.Rings, ring)
					}
				}
			}
		}

		bodies = append(bodies, body)
	}

	if err = result.Err(); err != nil {
		return nil, err
	}

	return bodies, nil
}

// getSystemStations fetches all stations for a system.
func (c *Client) getSystemStations(ctx context.Context, session neo4j.SessionWithContext, systemID int64) ([]StationInfo, error) {
	query := `
		MATCH (s:System)-[:HAS_STATION]->(st:Station)
		WHERE s.id64 = $id64
		  AND st.type <> "Fleetcarrier"
		  AND st.type <> "Drake-Class Carrier"
		RETURN st.market_id AS market_id,
		       st.name AS name,
		       st.type AS type,
		       st.distance_ls AS distance_ls,
		       st.large_pads AS large_pads,
		       st.medium_pads AS medium_pads,
		       st.small_pads AS small_pads,
		       st.services AS services
		ORDER BY st.distance_ls
	`

	result, err := session.Run(ctx, query, map[string]any{"id64": systemID})
	if err != nil {
		return nil, err
	}

	var stations []StationInfo
	for result.Next(ctx) {
		record := result.Record()
		station := StationInfo{}

		if v, ok := record.Get("market_id"); ok && v != nil {
			station.MarketID = toInt64(v)
		}
		if v, ok := record.Get("name"); ok && v != nil {
			station.Name = v.(string)
		}
		if v, ok := record.Get("type"); ok && v != nil {
			station.Type = v.(string)
		}
		if v, ok := record.Get("distance_ls"); ok && v != nil {
			station.DistanceLS = toFloat64(v)
		}

		// Landing pads
		station.LandingPads = make(map[string]int)
		if v, ok := record.Get("large_pads"); ok && v != nil {
			station.LandingPads["large"] = int(toInt64(v))
		}
		if v, ok := record.Get("medium_pads"); ok && v != nil {
			station.LandingPads["medium"] = int(toInt64(v))
		}
		if v, ok := record.Get("small_pads"); ok && v != nil {
			station.LandingPads["small"] = int(toInt64(v))
		}

		// Services
		if v, ok := record.Get("services"); ok && v != nil {
			if services, ok := v.([]any); ok {
				for _, svc := range services {
					if s, ok := svc.(string); ok {
						station.Services = append(station.Services, s)
					}
				}
			}
		}

		stations = append(stations, station)
	}

	if err = result.Err(); err != nil {
		return nil, err
	}

	return stations, nil
}

// GetSystemIDByName looks up a system ID by name for navigation commands.
func (c *Client) GetSystemIDByName(ctx context.Context, systemName string) (int64, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.name = $name
		RETURN s.id64 AS id64
		LIMIT 1
	`

	result, err := session.Run(ctx, query, map[string]any{"name": systemName})
	if err != nil {
		return 0, fmt.Errorf("query failed: %w", err)
	}

	if !result.Next(ctx) {
		return 0, ErrSystemNotFound
	}

	record := result.Record()
	if v, ok := record.Get("id64"); ok && v != nil {
		return toInt64(v), nil
	}

	return 0, ErrSystemNotFound
}

// GetSystemCoordinates returns the x, y, z coordinates for a system.
func (c *Client) GetSystemCoordinates(ctx context.Context, systemID int64) (x, y, z float64, err error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WHERE s.id64 = $id64
		RETURN s.location.x AS x, s.location.y AS y, s.location.z AS z
		LIMIT 1
	`

	result, err := session.Run(ctx, query, map[string]any{"id64": systemID})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("query failed: %w", err)
	}

	if !result.Next(ctx) {
		return 0, 0, 0, ErrSystemNotFound
	}

	record := result.Record()
	if v, ok := record.Get("x"); ok && v != nil {
		x = toFloat64(v)
	}
	if v, ok := record.Get("y"); ok && v != nil {
		y = toFloat64(v)
	}
	if v, ok := record.Get("z"); ok && v != nil {
		z = toFloat64(v)
	}

	return x, y, z, nil
}

// GetGalaxyViewStats returns aggregate statistics for the galaxy view summary.
func (c *Client) GetGalaxyViewStats(ctx context.Context) (*GalaxyViewStats, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	query := `
		MATCH (s:System)
		WITH count(s) AS total_systems,
		     sum(s.population) AS total_population
		MATCH (p:Power)
		WITH total_systems, total_population, count(p) AS total_powers
		MATCH (st:Station)
		WHERE st.type <> "Fleetcarrier" AND st.type <> "Drake-Class Carrier"
		RETURN total_systems, total_population, total_powers, count(st) AS total_stations
	`

	result, err := session.Run(ctx, query, nil)
	if err != nil {
		return nil, fmt.Errorf("stats query failed: %w", err)
	}

	if !result.Next(ctx) {
		return nil, fmt.Errorf("no stats returned")
	}

	record := result.Record()
	stats := &GalaxyViewStats{}

	if v, ok := record.Get("total_systems"); ok && v != nil {
		stats.TotalSystems = int(toInt64(v))
	}
	if v, ok := record.Get("total_population"); ok && v != nil {
		stats.TotalPopulation = toInt64(v)
	}
	if v, ok := record.Get("total_powers"); ok && v != nil {
		stats.TotalPowers = int(toInt64(v))
	}
	if v, ok := record.Get("total_stations"); ok && v != nil {
		stats.TotalStations = int(toInt64(v))
	}

	return stats, nil
}

// MinimalSystem represents a system with only the data needed for binary export.
// Used by the galaxy-exporter to generate static visualization files.
type MinimalSystem struct {
	ID64             int64   `json:"id64"`
	X                float64 `json:"x"`
	Y                float64 `json:"y"`
	Z                float64 `json:"z"`
	ControllingPower string  `json:"controlling_power,omitempty"`
	PowerplayState   string  `json:"powerplay_state,omitempty"`
	Allegiance       string  `json:"allegiance,omitempty"`
}

// GetAllSystemsMinimal returns all systems with minimal data for binary export.
// This is optimized for the galaxy-exporter to generate static visualization files.
// Returns systems in batches via a callback to avoid holding 1M+ systems in memory.
func (c *Client) GetAllSystemsMinimal(ctx context.Context, batchSize int, callback func([]MinimalSystem) error) (int, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Get total count first
	countQuery := `MATCH (s:System) RETURN count(s) AS total`
	countResult, err := session.Run(ctx, countQuery, nil)
	if err != nil {
		return 0, fmt.Errorf("count query failed: %w", err)
	}

	var totalCount int
	if countResult.Next(ctx) {
		if v, ok := countResult.Record().Get("total"); ok && v != nil {
			totalCount = int(toInt64(v))
		}
	}
	if err = countResult.Err(); err != nil {
		return 0, fmt.Errorf("count result error: %w", err)
	}

	// Stream all systems with SKIP/LIMIT pagination
	// Memgraph handles this efficiently in-memory
	query := `
		MATCH (s:System)
		RETURN s.id64 AS id64,
		       s.location.x AS x,
		       s.location.y AS y,
		       s.location.z AS z,
		       s.controlling_power AS controlling_power,
		       s.powerplay_state AS powerplay_state,
		       s.allegiance AS allegiance
		ORDER BY s.id64
		SKIP $skip
		LIMIT $limit
	`

	totalProcessed := 0
	skip := 0

	for skip < totalCount {
		result, err := session.Run(ctx, query, map[string]any{
			"skip":  skip,
			"limit": batchSize,
		})
		if err != nil {
			return totalProcessed, fmt.Errorf("batch query failed at offset %d: %w", skip, err)
		}

		batch := make([]MinimalSystem, 0, batchSize)
		for result.Next(ctx) {
			record := result.Record()
			sys := MinimalSystem{}

			if v, ok := record.Get("id64"); ok && v != nil {
				sys.ID64 = toInt64(v)
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
			if v, ok := record.Get("controlling_power"); ok && v != nil {
				sys.ControllingPower = v.(string)
			}
			if v, ok := record.Get("powerplay_state"); ok && v != nil {
				sys.PowerplayState = v.(string)
			}
			if v, ok := record.Get("allegiance"); ok && v != nil {
				sys.Allegiance = v.(string)
			}

			batch = append(batch, sys)
		}

		if err = result.Err(); err != nil {
			return totalProcessed, fmt.Errorf("result iteration error at offset %d: %w", skip, err)
		}

		if len(batch) > 0 {
			if err := callback(batch); err != nil {
				return totalProcessed, fmt.Errorf("callback error at offset %d: %w", skip, err)
			}
			totalProcessed += len(batch)
		}

		skip += batchSize
	}

	return totalProcessed, nil
}

// SearchSystemsByPrefix returns systems matching a name prefix for autocomplete.
func (c *Client) SearchSystemsByPrefix(ctx context.Context, prefix string, limit int) ([]GalaxySystem, error) {
	session := c.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)

	// Use case-insensitive prefix match
	query := `
		MATCH (s:System)
		WHERE toLower(s.name) STARTS WITH toLower($prefix)
		RETURN s.id64 AS id64,
		       s.name AS name,
		       s.location.x AS x,
		       s.location.y AS y,
		       s.location.z AS z,
		       s.controlling_power AS controlling_power,
		       s.powerplay_state AS powerplay_state,
		       s.allegiance AS allegiance,
		       s.population AS population
		ORDER BY s.population DESC
		LIMIT $limit
	`

	result, err := session.Run(ctx, query, map[string]any{"prefix": prefix, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("search query failed: %w", err)
	}

	var systems []GalaxySystem
	for result.Next(ctx) {
		record := result.Record()
		sys := GalaxySystem{}

		if v, ok := record.Get("id64"); ok && v != nil {
			sys.ID64 = toInt64(v)
		}
		if v, ok := record.Get("name"); ok && v != nil {
			sys.Name = v.(string)
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
		if v, ok := record.Get("controlling_power"); ok && v != nil {
			sys.ControllingPower = v.(string)
		}
		if v, ok := record.Get("powerplay_state"); ok && v != nil {
			sys.PowerplayState = v.(string)
		}
		if v, ok := record.Get("allegiance"); ok && v != nil {
			sys.Allegiance = v.(string)
		}
		if v, ok := record.Get("population"); ok && v != nil {
			sys.Population = toInt64(v)
		}

		systems = append(systems, sys)
	}

	if err = result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration error: %w", err)
	}

	return systems, nil
}
