package galaxystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/edin-space/edin-backend/internal/galaxy"
	"github.com/jackc/pgx/v5"
)

// Coords represents system coordinates.
type Coords struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// SystemData is the relational equivalent of the graph-era system payload.
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

// StationData represents a station in a system.
type StationData struct {
	ID64               int64     `json:"id64"`
	Name               string    `json:"name"`
	Type               string    `json:"type,omitempty"`
	SystemID64         int64     `json:"system_id64,omitempty"`
	SystemName         string    `json:"system_name,omitempty"`
	DistanceLS         float64   `json:"distance_ls,omitempty"`
	MaxPad             string    `json:"max_pad,omitempty"`
	IsPlanetary        bool      `json:"is_planetary,omitempty"`
	Services           []string  `json:"services,omitempty"`
	ControllingFaction string    `json:"controlling_faction,omitempty"`
	HasMarket          bool      `json:"has_market,omitempty"`
	HasShipyard        bool      `json:"has_shipyard,omitempty"`
	HasOutfitting      bool      `json:"has_outfitting,omitempty"`
	LastEDDNUpdate     time.Time `json:"last_eddn_update,omitempty"`
}

// FactionPresence represents a faction's presence in a system.
type FactionPresence struct {
	FactionName   string    `json:"faction_name"`
	SystemName    string    `json:"system_name"`
	Influence     float64   `json:"influence"`
	State         string    `json:"state,omitempty"`
	ActiveStates  []string  `json:"active_states,omitempty"`
	PendingStates []string  `json:"pending_states,omitempty"`
	Happiness     string    `json:"happiness,omitempty"`
	LastEventTime time.Time `json:"last_event_time,omitempty"`
}

// FleetCarrierData represents a fleet carrier currently reported in a system.
type FleetCarrierData struct {
	CarrierID         string    `json:"carrier_id"`
	Name              string    `json:"name,omitempty"`
	CurrentSystemID64 int64     `json:"current_system_id64,omitempty"`
	CurrentSystemName string    `json:"current_system_name,omitempty"`
	LastSeen          time.Time `json:"last_seen,omitempty"`
	FirstSeen         time.Time `json:"first_seen,omitempty"`
	JumpCount         int       `json:"jump_count,omitempty"`
}

// SystemFull represents a system with the W5.1 related slices needed by
// system-intel and watch surfaces. Bodies, signals, and market detail move in
// the later W5 surface tasks.
type SystemFull struct {
	System        *SystemData        `json:"system"`
	Stations      []StationData      `json:"stations,omitempty"`
	Factions      []FactionPresence  `json:"factions,omitempty"`
	FleetCarriers []FleetCarrierData `json:"fleet_carriers,omitempty"`
}

// SystemWatchSnapshot is the lean payload used by the bot watch endpoint.
type SystemWatchSnapshot struct {
	Slug                      string          `json:"slug"`
	Name                      string          `json:"name"`
	Allegiance                string          `json:"allegiance,omitempty"`
	ControllingFaction        string          `json:"controlling_faction,omitempty"`
	ControllingWatchFaction   string          `json:"controlling_faction_state,omitempty"`
	ControllingPower          string          `json:"controlling_power,omitempty"`
	PowerplayState            string          `json:"powerplay_state,omitempty"`
	Powers                    []string        `json:"powers,omitempty"`
	ControlProgress           *float64        `json:"control_progress,omitempty"`
	Reinforcement             *int64          `json:"reinforcement,omitempty"`
	Undermining               *int64          `json:"undermining,omitempty"`
	PowerplayConflictProgress json.RawMessage `json:"powerplay_conflict_progress,omitempty"`
	Factions                  []WatchFaction  `json:"factions"`
	LastUpdatedAt             time.Time       `json:"last_updated_at"`
}

// WatchFaction pairs a faction with its current state and influence.
type WatchFaction struct {
	Name      string  `json:"name"`
	State     string  `json:"state,omitempty"`
	Influence float64 `json:"influence,omitempty"`
}

type systemRow struct {
	id64                    int64
	name                    string
	x, y, z                 *float64
	population              int64
	allegiance              *string
	government              *string
	economy                 *string
	secondEconomy           *string
	security                *string
	thargoidState           *string
	thargoidProgress        float64
	controllingFaction      *string
	controllingFactionState *string
	powerName               *string
	powerplayState          *string
	powers                  []string
	reinforcement           int64
	undermining             int64
	controlProgress         float64
	conflictProgress        []byte
	lastEDDNUpdate          *time.Time
}

const systemLookupSelect = `
SELECT
	c.id64,
	COALESCE(s.name, c.name) AS name,
	c.x::float8,
	c.y::float8,
	c.z::float8,
	s.population,
	s.allegiance,
	s.government,
	s.economy,
	s.second_economy,
	s.security,
	s.thargoid_state,
	s.thargoid_progress::float8,
	cf.name AS controlling_faction,
	csf.state AS controlling_faction_state,
	sp.power_name,
	sp.powerplay_state,
	COALESCE(sp.powers_present, '{}'),
	COALESCE(sp.reinforcement, 0),
	COALESCE(sp.undermining, 0),
	COALESCE(sp.control_progress, 0)::float8,
	sp.conflict_progress,
	NULLIF(GREATEST(
		COALESCE(s.last_event_time, '-infinity'::timestamptz),
		COALESCE(s.last_faction_update, '-infinity'::timestamptz),
		COALESCE(sp.last_event_time, '-infinity'::timestamptz)
	), '-infinity'::timestamptz) AS last_eddn_update
FROM galaxy.system_catalog c
JOIN galaxy.system s ON s.id64 = c.id64
LEFT JOIN galaxy.faction cf ON cf.faction_id = s.controlling_faction_id
LEFT JOIN galaxy.system_faction csf
	ON csf.system_id64 = s.id64
	AND csf.faction_id = s.controlling_faction_id
LEFT JOIN galaxy.system_power sp ON sp.system_id64 = s.id64
`

// GetSystemFull fetches the core relational system-detail snapshot by exact
// system name, case-insensitive.
func (s *Store) GetSystemFull(ctx context.Context, systemName string) (*SystemFull, error) {
	return s.getSystemFull(ctx, systemLookupSelect+`WHERE lower(c.name) = lower($1) LIMIT 1`, systemName)
}

// GetFactionsInSystem returns the legacy modal faction projection for an exact,
// case-sensitive system name.
func (s *Store) GetFactionsInSystem(ctx context.Context, systemName string) ([]FactionPresence, error) {
	systemID64, found, err := s.resolveExactSystem(ctx, systemName)
	if err != nil {
		return nil, err
	}
	if !found {
		return []FactionPresence{}, nil
	}
	out, err := s.getFactions(ctx, systemID64, systemName)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []FactionPresence{}
	}
	return out, nil
}

// GetStationsInSystem returns the legacy modal station projection for an
// exact, case-sensitive system name. Construction depots are excluded and FSS
// station stubs are included unless a full station row with the same name
// exists.
func (s *Store) GetStationsInSystem(ctx context.Context, systemName string) ([]StationData, error) {
	systemID64, found, err := s.resolveExactSystem(ctx, systemName)
	if err != nil {
		return nil, err
	}
	if !found {
		return []StationData{}, nil
	}

	rows, err := s.db.Query(ctx, `
WITH modal_stations AS (
	SELECT
		st.market_id AS id64,
		st.name,
		st.station_type AS type,
		st.dist_from_star_ls::float8 AS distance_ls,
		CASE
			WHEN st.large_pads > 0 THEN 'L'
			WHEN st.medium_pads > 0 THEN 'M'
			WHEN st.small_pads > 0 THEN 'S'
			ELSE ''
		END AS max_pad,
		st.services,
		cf.name AS controlling_faction,
		st.last_event_time,
		m.market_id IS NOT NULL AS has_market,
		sy.market_id IS NOT NULL AS has_shipyard,
		o.market_id IS NOT NULL AS has_outfitting,
		0 AS source_priority
	FROM galaxy.station st
	LEFT JOIN galaxy.faction cf ON cf.faction_id = st.controlling_faction_id
	LEFT JOIN galaxy.market m ON m.market_id = st.market_id
	LEFT JOIN galaxy.shipyard sy ON sy.market_id = st.market_id
	LEFT JOIN galaxy.outfitting o ON o.market_id = st.market_id
	WHERE st.system_id64 = $1
	  AND st.kind = 'station'

	UNION ALL

	SELECT
		0::bigint,
		ss.name,
		ss.type,
		NULL::float8,
		''::text,
		'{}'::text[],
		NULL::text,
		ss.last_event_time,
		false,
		false,
		false,
		1
	FROM galaxy.station_stub ss
	WHERE ss.system_id64 = $1
	  AND NOT EXISTS (
		SELECT 1
		FROM galaxy.station st
		WHERE st.system_id64 = ss.system_id64
		  AND st.name = ss.name
		  AND st.kind = 'station'
	  )
)
SELECT id64, name, type, distance_ls, max_pad, services,
       controlling_faction, last_event_time, has_market, has_shipyard,
       has_outfitting
FROM modal_stations
ORDER BY COALESCE(distance_ls, 0), name, source_priority`, systemID64)
	if err != nil {
		return nil, fmt.Errorf("modal stations: %w", err)
	}
	defer rows.Close()

	out := make([]StationData, 0)
	for rows.Next() {
		var st StationData
		var stationType *string
		var distance *float64
		var controllingFaction *string
		if err := rows.Scan(
			&st.ID64,
			&st.Name,
			&stationType,
			&distance,
			&st.MaxPad,
			&st.Services,
			&controllingFaction,
			&st.LastEDDNUpdate,
			&st.HasMarket,
			&st.HasShipyard,
			&st.HasOutfitting,
		); err != nil {
			return nil, fmt.Errorf("modal station scan: %w", err)
		}
		st.SystemName = systemName
		if stationType != nil {
			st.Type = *stationType
		}
		if distance != nil {
			st.DistanceLS = *distance
		}
		if controllingFaction != nil {
			st.ControllingFaction = *controllingFaction
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) resolveExactSystem(ctx context.Context, systemName string) (int64, bool, error) {
	var id64 int64
	err := s.db.QueryRow(ctx, `
SELECT id64
FROM galaxy.system_catalog
WHERE name = $1
LIMIT 1`, systemName).Scan(&id64)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("exact system lookup: %w", err)
	}
	return id64, true, nil
}

// GetSystemFullBySlug fetches the core relational system-detail snapshot by
// the canonical no-spaces slug used by Kaine URLs and the bot watch route.
func (s *Store) GetSystemFullBySlug(ctx context.Context, slug string) (*SystemFull, error) {
	return s.getSystemFull(ctx, systemLookupSelect+`WHERE replace(btrim(c.name), ' ', '') = $1 LIMIT 1`, slug)
}

func (s *Store) getSystemFull(ctx context.Context, query string, arg string) (*SystemFull, error) {
	row, err := s.querySystemRow(ctx, query, arg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	out := &SystemFull{System: row.toSystemData()}
	out.Factions, err = s.getFactions(ctx, row.id64, row.name)
	if err != nil {
		return nil, err
	}
	out.Stations, err = s.getStations(ctx, row.id64, row.name)
	if err != nil {
		return nil, err
	}
	out.FleetCarriers, err = s.getFleetCarriers(ctx, row.id64, row.name)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetSystemWatchSnapshot fetches the powerplay + faction snapshot by slug.
func (s *Store) GetSystemWatchSnapshot(ctx context.Context, slug string) (*SystemWatchSnapshot, error) {
	row, err := s.querySystemRow(ctx, systemLookupSelect+`WHERE replace(btrim(c.name), ' ', '') = $1 LIMIT 1`, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSystemNotFound
		}
		return nil, err
	}

	factions, err := s.getWatchFactions(ctx, row.id64)
	if err != nil {
		return nil, err
	}

	out := &SystemWatchSnapshot{
		Slug:            galaxy.Slugify(row.name),
		Name:            row.name,
		Powers:          row.powers,
		Factions:        factions,
		LastUpdatedAt:   derefTime(row.lastEDDNUpdate),
		ControlProgress: &row.controlProgress,
	}
	if row.allegiance != nil {
		out.Allegiance = *row.allegiance
	}
	if row.controllingFaction != nil {
		out.ControllingFaction = *row.controllingFaction
	}
	if row.controllingFactionState != nil {
		out.ControllingWatchFaction = *row.controllingFactionState
	}
	if row.powerName != nil {
		out.ControllingPower = *row.powerName
	}
	if row.powerplayState != nil {
		out.PowerplayState = *row.powerplayState
	}
	if row.powerplayState != nil {
		out.Reinforcement = &row.reinforcement
		out.Undermining = &row.undermining
	}
	if len(row.conflictProgress) > 0 {
		out.PowerplayConflictProgress = append(json.RawMessage(nil), row.conflictProgress...)
	}
	if row.powerplayState == nil {
		out.ControlProgress = nil
	}
	return out, nil
}

func (s *Store) querySystemRow(ctx context.Context, query string, arg string) (*systemRow, error) {
	row, err := scanSystemRow(s.db.QueryRow(ctx, query, arg))
	if err != nil {
		return nil, fmt.Errorf("system lookup: %w", err)
	}
	return row, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSystemRow(rowScanner scanner) (*systemRow, error) {
	var row systemRow
	err := rowScanner.Scan(
		&row.id64,
		&row.name,
		&row.x,
		&row.y,
		&row.z,
		&row.population,
		&row.allegiance,
		&row.government,
		&row.economy,
		&row.secondEconomy,
		&row.security,
		&row.thargoidState,
		&row.thargoidProgress,
		&row.controllingFaction,
		&row.controllingFactionState,
		&row.powerName,
		&row.powerplayState,
		&row.powers,
		&row.reinforcement,
		&row.undermining,
		&row.controlProgress,
		&row.conflictProgress,
		&row.lastEDDNUpdate,
	)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *systemRow) toSystemData() *SystemData {
	out := &SystemData{
		Name:             r.name,
		ID64:             r.id64,
		Powers:           r.powers,
		Population:       r.population,
		Reinforcement:    r.reinforcement,
		Undermining:      r.undermining,
		ThargoidProgress: r.thargoidProgress,
		LastEDDNUpdate:   derefTime(r.lastEDDNUpdate),
		ControlProgress:  &r.controlProgress,
	}
	if r.x != nil && r.y != nil && r.z != nil {
		out.Coordinates = &Coords{X: *r.x, Y: *r.y, Z: *r.z}
	}
	if r.allegiance != nil {
		out.Allegiance = *r.allegiance
	}
	if r.government != nil {
		out.Government = *r.government
	}
	if r.economy != nil {
		out.Economy = *r.economy
	}
	if r.secondEconomy != nil {
		out.SecondEconomy = *r.secondEconomy
	}
	if r.security != nil {
		out.Security = *r.security
	}
	if r.thargoidState != nil {
		out.ThargoidState = *r.thargoidState
	}
	if r.controllingFaction != nil {
		out.ControllingFaction = *r.controllingFaction
	}
	if r.controllingFactionState != nil {
		out.ControllingFactionState = *r.controllingFactionState
	}
	if r.powerName != nil {
		out.ControllingPower = *r.powerName
	}
	if r.powerplayState != nil {
		out.PowerplayState = *r.powerplayState
	} else {
		out.ControlProgress = nil
	}
	if len(r.conflictProgress) > 0 {
		_ = json.Unmarshal(r.conflictProgress, &out.PowerplayConflictProgress)
	}
	return out
}

func (s *Store) getFactions(ctx context.Context, systemID64 int64, systemName string) ([]FactionPresence, error) {
	rows, err := s.db.Query(ctx, `
SELECT
	f.name,
	sf.influence::float8,
	sf.state,
	sf.active_states,
	sf.pending_states,
	COALESCE(sf.happiness, ''),
	sf.last_event_time
FROM galaxy.system_faction sf
JOIN galaxy.faction f ON f.faction_id = sf.faction_id
WHERE sf.system_id64 = $1
ORDER BY sf.influence DESC, f.name ASC`, systemID64)
	if err != nil {
		return nil, fmt.Errorf("system factions: %w", err)
	}
	defer rows.Close()

	var out []FactionPresence
	for rows.Next() {
		var fp FactionPresence
		fp.SystemName = systemName
		if err := rows.Scan(
			&fp.FactionName,
			&fp.Influence,
			&fp.State,
			&fp.ActiveStates,
			&fp.PendingStates,
			&fp.Happiness,
			&fp.LastEventTime,
		); err != nil {
			return nil, fmt.Errorf("system factions scan: %w", err)
		}
		out = append(out, fp)
	}
	return out, rows.Err()
}

func (s *Store) getWatchFactions(ctx context.Context, systemID64 int64) ([]WatchFaction, error) {
	rows, err := s.db.Query(ctx, `
SELECT f.name, sf.state, sf.influence::float8
FROM galaxy.system_faction sf
JOIN galaxy.faction f ON f.faction_id = sf.faction_id
WHERE sf.system_id64 = $1
ORDER BY sf.influence DESC, f.name ASC`, systemID64)
	if err != nil {
		return nil, fmt.Errorf("watch factions: %w", err)
	}
	defer rows.Close()

	var out []WatchFaction
	for rows.Next() {
		var wf WatchFaction
		if err := rows.Scan(&wf.Name, &wf.State, &wf.Influence); err != nil {
			return nil, fmt.Errorf("watch factions scan: %w", err)
		}
		out = append(out, wf)
	}
	return out, rows.Err()
}

func (s *Store) getStations(ctx context.Context, systemID64 int64, systemName string) ([]StationData, error) {
	rows, err := s.db.Query(ctx, `
SELECT
	st.market_id,
	st.name,
	st.station_type,
	st.system_id64,
	$2::text AS system_name,
	st.dist_from_star_ls::float8,
	CASE
		WHEN st.large_pads > 0 THEN 'L'
		WHEN st.medium_pads > 0 THEN 'M'
		WHEN st.small_pads > 0 THEN 'S'
		ELSE ''
	END AS max_pad,
	st.kind = 'planetary_depot' AS is_planetary,
	st.services,
	cf.name AS controlling_faction,
	st.last_event_time,
	m.market_id IS NOT NULL AS has_market,
	sy.market_id IS NOT NULL AS has_shipyard,
	o.market_id IS NOT NULL AS has_outfitting
FROM galaxy.station st
LEFT JOIN galaxy.faction cf ON cf.faction_id = st.controlling_faction_id
LEFT JOIN galaxy.market m ON m.market_id = st.market_id
LEFT JOIN galaxy.shipyard sy ON sy.market_id = st.market_id
LEFT JOIN galaxy.outfitting o ON o.market_id = st.market_id
WHERE st.system_id64 = $1
ORDER BY st.dist_from_star_ls NULLS LAST, st.name`, systemID64, systemName)
	if err != nil {
		return nil, fmt.Errorf("system stations: %w", err)
	}
	defer rows.Close()

	var out []StationData
	for rows.Next() {
		st, err := scanStationData(rows)
		if err != nil {
			return nil, err
		}
		st.SystemID64 = systemID64
		st.SystemName = systemName
		out = append(out, st)
	}
	return out, rows.Err()
}

func scanStationData(rowScanner scanner) (StationData, error) {
	var st StationData
	var distance *float64
	var stationType *string
	var controllingFaction *string
	if err := rowScanner.Scan(
		&st.ID64,
		&st.Name,
		&stationType,
		&st.SystemID64,
		&st.SystemName,
		&distance,
		&st.MaxPad,
		&st.IsPlanetary,
		&st.Services,
		&controllingFaction,
		&st.LastEDDNUpdate,
		&st.HasMarket,
		&st.HasShipyard,
		&st.HasOutfitting,
	); err != nil {
		return StationData{}, fmt.Errorf("station scan: %w", err)
	}
	if stationType != nil {
		st.Type = *stationType
	}
	if distance != nil {
		st.DistanceLS = *distance
	}
	if controllingFaction != nil {
		st.ControllingFaction = *controllingFaction
	}
	return st, nil
}

func (s *Store) getFleetCarriers(ctx context.Context, systemID64 int64, systemName string) ([]FleetCarrierData, error) {
	rows, err := s.db.Query(ctx, `
SELECT carrier_id, name, current_system_id64, first_seen, last_event_time, jump_count
FROM galaxy.fleet_carrier
WHERE current_system_id64 = $1
ORDER BY carrier_id`, systemID64)
	if err != nil {
		return nil, fmt.Errorf("system fleet carriers: %w", err)
	}
	defer rows.Close()

	var out []FleetCarrierData
	for rows.Next() {
		var fc FleetCarrierData
		var name *string
		var firstSeen *time.Time
		if err := rows.Scan(&fc.CarrierID, &name, &fc.CurrentSystemID64, &firstSeen, &fc.LastSeen, &fc.JumpCount); err != nil {
			return nil, fmt.Errorf("system fleet carriers scan: %w", err)
		}
		if name != nil {
			fc.Name = *name
		}
		if firstSeen != nil {
			fc.FirstSeen = *firstSeen
		}
		fc.CurrentSystemName = systemName
		out = append(out, fc)
	}
	return out, rows.Err()
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
