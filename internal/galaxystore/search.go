package galaxystore

import (
	"context"
	"fmt"
)

// SearchSystems returns system autocomplete rows matching prefix first, then
// substring matches. It keeps the graph-era JSON shape while reading galaxy.*.
func (s *Store) SearchSystems(ctx context.Context, query string, limit int) ([]SystemData, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(ctx, `
SELECT
	c.id64,
	COALESCE(sys.name, c.name) AS name,
	c.x::float8,
	c.y::float8,
	c.z::float8,
	sys.population,
	sys.allegiance,
	sys.government,
	sys.economy,
	sys.second_economy,
	sys.security,
	sys.thargoid_state,
	sys.thargoid_progress::float8,
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
		COALESCE(sys.last_event_time, '-infinity'::timestamptz),
		COALESCE(sys.last_faction_update, '-infinity'::timestamptz),
		COALESCE(sp.last_event_time, '-infinity'::timestamptz)
	), '-infinity'::timestamptz) AS last_eddn_update
FROM galaxy.system_catalog c
JOIN galaxy.system sys ON sys.id64 = c.id64
LEFT JOIN galaxy.faction cf ON cf.faction_id = sys.controlling_faction_id
LEFT JOIN galaxy.system_faction csf
	ON csf.system_id64 = sys.id64
	AND csf.faction_id = sys.controlling_faction_id
LEFT JOIN galaxy.system_power sp ON sp.system_id64 = sys.id64
WHERE lower(c.name) LIKE lower($1) || '%'
ORDER BY
	length(c.name),
	c.name
LIMIT $2`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("system search: %w", err)
	}
	defer rows.Close()

	var out []SystemData
	for rows.Next() {
		row, err := scanSystemRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *row.toSystemData())
	}
	return out, rows.Err()
}

// SearchStations returns station autocomplete rows matching station name.
func (s *Store) SearchStations(ctx context.Context, query string, limit int) ([]StationData, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(ctx, `
SELECT
	st.market_id,
	st.name,
	st.station_type,
	st.system_id64,
	COALESCE(sys.name, c.name, m.system_name) AS system_name,
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
LEFT JOIN galaxy.system sys ON sys.id64 = st.system_id64
LEFT JOIN galaxy.system_catalog c ON c.id64 = st.system_id64
LEFT JOIN galaxy.market m ON m.market_id = st.market_id
LEFT JOIN galaxy.faction cf ON cf.faction_id = st.controlling_faction_id
LEFT JOIN galaxy.shipyard sy ON sy.market_id = st.market_id
LEFT JOIN galaxy.outfitting o ON o.market_id = st.market_id
WHERE st.name ILIKE $1 || '%'
   OR st.name ILIKE '%' || $1 || '%'
ORDER BY
	CASE WHEN st.name ILIKE $1 || '%' THEN 0 ELSE 1 END,
	length(st.name),
	st.name
LIMIT $2`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("station search: %w", err)
	}
	defer rows.Close()

	var out []StationData
	for rows.Next() {
		st, err := scanStationData(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
