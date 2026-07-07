package galaxystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CGSystemData represents a system with powerplay data for the powerplay APIs.
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
	X                         float64          `json:"x"`
	Y                         float64          `json:"y"`
	Z                         float64          `json:"z"`
	Security                  string           `json:"security,omitempty"`
	Economy                   string           `json:"economy,omitempty"`
	HasLargePad               bool             `json:"has_large_pad"`
	NearestStation            string           `json:"nearest_station,omitempty"`
	NearestStationLs          float64          `json:"nearest_station_ls,omitempty"`
	StationCount              int              `json:"station_count"`
}

// PowerData represents a powerplay power with aggregate current-state counts.
type PowerData struct {
	Name                  string    `json:"name"`
	Allegiance            string    `json:"allegiance,omitempty"`
	ControlledSystemCount int       `json:"controlled_system_count,omitempty"`
	LastEDDNUpdate        time.Time `json:"last_eddn_update,omitempty"`
}

// PowerStateCounts holds state counts per power.
type PowerStateCounts struct {
	Power      string         `json:"power"`
	Allegiance string         `json:"allegiance,omitempty"`
	States     map[string]int `json:"states"`
	Total      int            `json:"total"`
}

const powerplaySystemSelect = `
SELECT
	COALESCE(s.name, c.name) AS system_name,
	sp.power_name,
	sp.powers_present,
	sp.powerplay_state,
	sp.reinforcement,
	sp.undermining,
	sp.control_progress::float8,
	sp.conflict_progress,
	s.allegiance,
	s.government,
	s.population,
	cf.name AS controlling_faction,
	csf.state AS controlling_faction_state,
	GREATEST(
		COALESCE(s.last_event_time, '-infinity'::timestamptz),
		COALESCE(s.last_faction_update, '-infinity'::timestamptz),
		sp.last_event_time
	) AS last_eddn_update,
	c.x::float8,
	c.y::float8,
	c.z::float8,
	s.security,
	s.economy,
	COALESCE(st.station_count, 0),
	nearest.name,
	nearest.dist_from_star_ls::float8,
	nearest.market_id IS NOT NULL
FROM galaxy.system_power sp
JOIN galaxy.system s ON s.id64 = sp.system_id64
JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
LEFT JOIN galaxy.faction cf ON cf.faction_id = s.controlling_faction_id
LEFT JOIN galaxy.system_faction csf
	ON csf.system_id64 = s.id64
	AND csf.faction_id = s.controlling_faction_id
LEFT JOIN LATERAL (
	SELECT count(*)::int AS station_count
	FROM galaxy.station station_count
	WHERE station_count.system_id64 = s.id64
	  AND COALESCE(station_count.station_type, '') NOT IN ('Fleetcarrier', 'Drake-Class Carrier')
) st ON true
LEFT JOIN LATERAL (
	SELECT station_nearest.market_id, station_nearest.name, station_nearest.dist_from_star_ls
	FROM galaxy.station station_nearest
	WHERE station_nearest.system_id64 = s.id64
	  AND station_nearest.large_pads > 0
	  AND COALESCE(station_nearest.station_type, '') NOT IN ('Fleetcarrier', 'Drake-Class Carrier')
	ORDER BY station_nearest.dist_from_star_ls NULLS LAST, station_nearest.name
	LIMIT 1
) nearest ON true
`

// GetAllPowerplaySystems returns every current system with a powerplay row.
func (s *Store) GetAllPowerplaySystems(ctx context.Context) ([]CGSystemData, error) {
	rows, err := s.db.Query(ctx, powerplaySystemSelect+`ORDER BY COALESCE(s.name, c.name)`)
	if err != nil {
		return nil, fmt.Errorf("powerplay systems: %w", err)
	}
	defer rows.Close()

	var out []CGSystemData
	for rows.Next() {
		sys, err := scanCGSystem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sys)
	}
	return out, rows.Err()
}

// GetCGSystems returns powerplay rows for the requested system names.
func (s *Store) GetCGSystems(ctx context.Context, systemNames []string) ([]CGSystemData, error) {
	if len(systemNames) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, powerplaySystemSelect+`
WHERE COALESCE(s.name, c.name) = ANY($1)
ORDER BY COALESCE(s.name, c.name)`, systemNames)
	if err != nil {
		return nil, fmt.Errorf("cg systems: %w", err)
	}
	defer rows.Close()

	var out []CGSystemData
	for rows.Next() {
		sys, err := scanCGSystem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sys)
	}
	return out, rows.Err()
}

// GetPower fetches one power and counts its current controlled systems.
func (s *Store) GetPower(ctx context.Context, powerName string) (*PowerData, error) {
	var out PowerData
	var allegiance *string
	var lastUpdate *time.Time
	err := s.db.QueryRow(ctx, `
SELECT
	p.name,
	p.allegiance,
	max(sp.last_event_time),
	count(sp.system_id64)::int
FROM galaxy.power p
LEFT JOIN galaxy.system_power sp ON sp.power_name = p.name
WHERE lower(p.name) = lower($1)
GROUP BY p.name, p.allegiance`, powerName).Scan(
		&out.Name,
		&allegiance,
		&lastUpdate,
		&out.ControlledSystemCount,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("power lookup: %w", err)
	}
	if allegiance != nil {
		out.Allegiance = *allegiance
	}
	if lastUpdate != nil {
		out.LastEDDNUpdate = *lastUpdate
	}
	return &out, nil
}

// GetPowerSystems returns current systems controlled by the named power.
func (s *Store) GetPowerSystems(ctx context.Context, powerName string, limit int) ([]SystemData, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
SELECT
	COALESCE(sys.name, c.name),
	sp.powerplay_state,
	sp.reinforcement,
	sp.undermining,
	sys.population,
	sys.allegiance
FROM galaxy.system_power sp
JOIN galaxy.system sys ON sys.id64 = sp.system_id64
JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
WHERE lower(sp.power_name) = lower($1)
ORDER BY sp.reinforcement DESC, sp.undermining DESC
LIMIT $2`, powerName, limit)
	if err != nil {
		return nil, fmt.Errorf("power systems: %w", err)
	}
	defer rows.Close()

	var out []SystemData
	for rows.Next() {
		sys := SystemData{ControllingPower: powerName}
		var allegiance *string
		if err := rows.Scan(
			&sys.Name,
			&sys.PowerplayState,
			&sys.Reinforcement,
			&sys.Undermining,
			&sys.Population,
			&allegiance,
		); err != nil {
			return nil, fmt.Errorf("power systems scan: %w", err)
		}
		if allegiance != nil {
			sys.Allegiance = *allegiance
		}
		out = append(out, sys)
	}
	return out, rows.Err()
}

// GetPowerStateCountsForSystems returns state counts per power for system names.
func (s *Store) GetPowerStateCountsForSystems(ctx context.Context, systemNames []string) (map[string]*PowerStateCounts, error) {
	if len(systemNames) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT sp.power_name, sp.powerplay_state, count(*)::int
FROM galaxy.system_power sp
JOIN galaxy.system sys ON sys.id64 = sp.system_id64
JOIN galaxy.system_catalog c ON c.id64 = sp.system_id64
WHERE sp.power_name IS NOT NULL
  AND COALESCE(sys.name, c.name) = ANY($1)
GROUP BY sp.power_name, sp.powerplay_state
ORDER BY sp.power_name, sp.powerplay_state`, systemNames)
	if err != nil {
		return nil, fmt.Errorf("power state counts: %w", err)
	}
	defer rows.Close()

	return scanPowerStateCounts(rows)
}

func scanCGSystem(rows interface {
	Scan(dest ...any) error
}) (CGSystemData, error) {
	var sys CGSystemData
	var controllingPower *string
	var controlProgress *float64
	var conflictProgress []byte
	var allegiance, government, controllingFaction, controllingFactionState *string
	var security, economy, nearestStation *string
	var nearestStationLs *float64
	var lastUpdate time.Time
	if err := rows.Scan(
		&sys.SystemName,
		&controllingPower,
		&sys.Powers,
		&sys.PowerplayState,
		&sys.Reinforcement,
		&sys.Undermining,
		&controlProgress,
		&conflictProgress,
		&allegiance,
		&government,
		&sys.Population,
		&controllingFaction,
		&controllingFactionState,
		&lastUpdate,
		&sys.X,
		&sys.Y,
		&sys.Z,
		&security,
		&economy,
		&sys.StationCount,
		&nearestStation,
		&nearestStationLs,
		&sys.HasLargePad,
	); err != nil {
		return CGSystemData{}, fmt.Errorf("powerplay system scan: %w", err)
	}
	if controllingPower != nil {
		sys.ControllingPower = *controllingPower
	}
	if controlProgress != nil {
		sys.ControlProgress = controlProgress
	}
	if len(conflictProgress) > 0 {
		_ = json.Unmarshal(conflictProgress, &sys.PowerplayConflictProgress)
	}
	if allegiance != nil {
		sys.Allegiance = *allegiance
	}
	if government != nil {
		sys.Government = *government
	}
	if controllingFaction != nil {
		sys.ControllingFaction = *controllingFaction
	}
	if controllingFactionState != nil {
		sys.ControllingFactionState = *controllingFactionState
	}
	if security != nil {
		sys.Security = *security
	}
	if economy != nil {
		sys.Economy = *economy
	}
	if nearestStation != nil {
		sys.NearestStation = *nearestStation
	}
	if nearestStationLs != nil {
		sys.NearestStationLs = *nearestStationLs
	}
	sys.LastEDDNUpdate = lastUpdate
	return sys, nil
}

func scanPowerStateCounts(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) (map[string]*PowerStateCounts, error) {
	counts := make(map[string]*PowerStateCounts)
	for rows.Next() {
		var power, state string
		var count int
		if err := rows.Scan(&power, &state, &count); err != nil {
			return nil, fmt.Errorf("power state counts scan: %w", err)
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
		counts[power].States[state] = count
		counts[power].Total += count
	}
	return counts, rows.Err()
}
