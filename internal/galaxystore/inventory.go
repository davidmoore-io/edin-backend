package galaxystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SystemInventory is the compact, complete current-state projection used by
// galaxy_system. It deliberately excludes bulky physical JSON and commodity
// rows; callers can drill into a market by market id.
type SystemInventory struct {
	System          *SystemData
	Facilities      []InventoryFacility
	Bodies          []InventoryBody
	UnassignedRings []InventoryRing
}

type InventoryFacility struct {
	Kind          string
	Identity      string
	MarketID      *int64
	Name          string
	Type          string
	DistanceLS    *float64
	Services      []string
	HasMarket     bool
	HasShipyard   bool
	HasOutfitting bool
	LastEventTime *time.Time
}

type InventoryBody struct {
	BodyID        int
	Name          string
	Type          string
	SubType       string
	DistanceLS    *float64
	LastEventTime *time.Time
	Rings         []InventoryRing
}

type InventoryRing struct {
	BodyID        *int
	Name          string
	Class         string
	ReserveLevel  string
	HotspotCount  int
	LastEventTime *time.Time
}

type MarketInventory struct {
	MarketID               int64
	OwnerKind              string
	OwnerIdentity          string
	StationName            string
	SystemName             string
	LastEventTime          time.Time
	ReportedCommodityCount int
	Prohibited             []string
	Commodities            []MarketCommodity
}

// GetSystemInventory returns every map-visible body and facility class held by
// the relational schema for one system.
func (s *Store) GetSystemInventory(ctx context.Context, systemName string) (*SystemInventory, error) {
	row, err := s.querySystemRow(ctx, systemLookupSelect+`WHERE lower(c.name) = lower($1) LIMIT 1`, systemName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	out := &SystemInventory{System: row.toSystemData()}
	out.Facilities, err = s.getInventoryFacilities(ctx, row.id64)
	if err != nil {
		return nil, err
	}
	out.Bodies, out.UnassignedRings, err = s.getInventoryBodies(ctx, row.id64)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) getInventoryFacilities(ctx context.Context, systemID64 int64) ([]InventoryFacility, error) {
	rows, err := s.db.Query(ctx, `
WITH facilities AS (
	SELECT
		st.kind::text AS kind,
		st.market_id::text AS identity,
		st.market_id,
		st.name,
		COALESCE(st.station_type, st.kind)::text AS type,
		st.dist_from_star_ls::float8 AS distance_ls,
		st.services,
		m.market_id IS NOT NULL AS has_market,
		sy.market_id IS NOT NULL AS has_shipyard,
		o.market_id IS NOT NULL AS has_outfitting,
		NULLIF(GREATEST(
			st.last_event_time,
			COALESCE(m.last_event_time, '-infinity'::timestamptz),
			COALESCE(sy.last_event_time, '-infinity'::timestamptz),
			COALESCE(o.last_event_time, '-infinity'::timestamptz)
		), '-infinity'::timestamptz) AS last_event_time
	FROM galaxy.station st
	LEFT JOIN galaxy.market m ON m.market_id = st.market_id
	LEFT JOIN galaxy.shipyard sy ON sy.market_id = st.market_id
	LEFT JOIN galaxy.outfitting o ON o.market_id = st.market_id
	WHERE st.system_id64 = $1

	UNION ALL

	SELECT
		'settlement',
		se.market_id::text,
		se.market_id,
		COALESCE(se.name, 'Unnamed settlement'),
		'Settlement',
		se.dist_from_star_ls::float8,
		se.services,
		m.market_id IS NOT NULL,
		sy.market_id IS NOT NULL,
		o.market_id IS NOT NULL,
		NULLIF(GREATEST(
			se.last_event_time,
			COALESCE(m.last_event_time, '-infinity'::timestamptz),
			COALESCE(sy.last_event_time, '-infinity'::timestamptz),
			COALESCE(o.last_event_time, '-infinity'::timestamptz)
		), '-infinity'::timestamptz) AS last_event_time
	FROM galaxy.settlement se
	LEFT JOIN galaxy.market m ON m.market_id = se.market_id
	LEFT JOIN galaxy.shipyard sy ON sy.market_id = se.market_id
	LEFT JOIN galaxy.outfitting o ON o.market_id = se.market_id
	WHERE se.system_id64 = $1

	UNION ALL

	SELECT
		'station_stub',
		$1::text || '/' || ss.name,
		NULL::bigint,
		ss.name,
		COALESCE(ss.type, 'Station stub'),
		NULL::float8,
		'{}'::text[],
		false,
		false,
		false,
		ss.last_event_time
	FROM galaxy.station_stub ss
	WHERE ss.system_id64 = $1
	  AND NOT EXISTS (
		SELECT 1 FROM galaxy.station st
		WHERE st.system_id64 = ss.system_id64 AND st.name = ss.name
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM galaxy.settlement se
		WHERE se.system_id64 = ss.system_id64 AND se.name = ss.name
	  )

	UNION ALL

	SELECT
		'installation',
		$1::text || '/' || i.name,
		NULL::bigint,
		i.name,
		'Installation',
		NULL::float8,
		'{}'::text[],
		false,
		false,
		false,
		i.last_event_time
	FROM galaxy.installation i
	WHERE i.system_id64 = $1

	UNION ALL

	SELECT
		'fleet_carrier',
		fc.carrier_id,
		fc.market_id,
		COALESCE(fc.name, fc.carrier_id),
		'Fleet Carrier',
		fc.dist_from_star_ls::float8,
		fc.services,
		m.market_id IS NOT NULL,
		sy.market_id IS NOT NULL,
		o.market_id IS NOT NULL,
		NULLIF(GREATEST(
			fc.last_event_time,
			COALESCE(m.last_event_time, '-infinity'::timestamptz),
			COALESCE(sy.last_event_time, '-infinity'::timestamptz),
			COALESCE(o.last_event_time, '-infinity'::timestamptz)
		), '-infinity'::timestamptz) AS last_event_time
	FROM galaxy.fleet_carrier fc
	LEFT JOIN galaxy.market m ON m.market_id = fc.market_id
	LEFT JOIN galaxy.shipyard sy ON sy.market_id = fc.market_id
	LEFT JOIN galaxy.outfitting o ON o.market_id = fc.market_id
	WHERE fc.current_system_id64 = $1

	UNION ALL

	SELECT
		'stronghold_carrier',
		$1::text,
		sc.market_id,
		'Stronghold Carrier',
		'Stronghold Carrier',
		sc.dist_from_star_ls::float8,
		sc.services,
		m.market_id IS NOT NULL,
		sy.market_id IS NOT NULL,
		o.market_id IS NOT NULL,
		NULLIF(GREATEST(
			sc.last_seen,
			COALESCE(m.last_event_time, '-infinity'::timestamptz),
			COALESCE(sy.last_event_time, '-infinity'::timestamptz),
			COALESCE(o.last_event_time, '-infinity'::timestamptz)
		), '-infinity'::timestamptz) AS last_event_time
	FROM galaxy.stronghold_carrier sc
	LEFT JOIN galaxy.market m ON m.market_id = sc.market_id
	LEFT JOIN galaxy.shipyard sy ON sy.market_id = sc.market_id
	LEFT JOIN galaxy.outfitting o ON o.market_id = sc.market_id
	WHERE sc.system_id64 = $1

	UNION ALL

	SELECT
		'megaship',
		ms.name,
		NULL::bigint,
		ms.name,
		'Megaship',
		NULL::float8,
		'{}'::text[],
		false,
		false,
		false,
		ms.last_event_time
	FROM galaxy.megaship ms
	WHERE ms.current_system_id64 = $1
)
SELECT kind, identity, market_id, name, type, distance_ls, services,
       has_market, has_shipyard, has_outfitting, last_event_time
FROM facilities
ORDER BY kind, name, identity`, systemID64)
	if err != nil {
		return nil, fmt.Errorf("system inventory facilities: %w", err)
	}
	defer rows.Close()

	out := make([]InventoryFacility, 0)
	for rows.Next() {
		var f InventoryFacility
		if err := rows.Scan(
			&f.Kind,
			&f.Identity,
			&f.MarketID,
			&f.Name,
			&f.Type,
			&f.DistanceLS,
			&f.Services,
			&f.HasMarket,
			&f.HasShipyard,
			&f.HasOutfitting,
			&f.LastEventTime,
		); err != nil {
			return nil, fmt.Errorf("system inventory facility scan: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) getInventoryBodies(ctx context.Context, systemID64 int64) ([]InventoryBody, []InventoryRing, error) {
	rows, err := s.db.Query(ctx, `
SELECT body_id, COALESCE(name, 'Body ' || body_id::text), COALESCE(type, ''),
       COALESCE(sub_type, ''), distance_from_arrival::float8, last_event_time
FROM galaxy.body
WHERE system_id64 = $1
ORDER BY distance_from_arrival NULLS LAST, body_id`, systemID64)
	if err != nil {
		return nil, nil, fmt.Errorf("system inventory bodies: %w", err)
	}

	bodies := make([]InventoryBody, 0)
	bodyIndexes := make(map[int]int)
	for rows.Next() {
		var body InventoryBody
		if err := rows.Scan(
			&body.BodyID,
			&body.Name,
			&body.Type,
			&body.SubType,
			&body.DistanceLS,
			&body.LastEventTime,
		); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("system inventory body scan: %w", err)
		}
		bodyIndexes[body.BodyID] = len(bodies)
		bodies = append(bodies, body)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()

	ringRows, err := s.db.Query(ctx, `
SELECT
	r.body_id,
	r.name,
	COALESCE(r.ring_class, ''),
	COALESCE(r.reserve_level, ''),
	COALESCE(SUM(rh.count), 0)::int,
	NULLIF(GREATEST(
		COALESCE(r.last_event_time, '-infinity'::timestamptz),
		COALESCE(r.hotspots_updated, '-infinity'::timestamptz),
		COALESCE(MAX(rh.hotspots_updated), '-infinity'::timestamptz)
	), '-infinity'::timestamptz)
FROM galaxy.ring r
LEFT JOIN galaxy.ring_hotspot rh
	ON rh.system_id64 = r.system_id64
	AND rh.ring_name = r.name
WHERE r.system_id64 = $1
GROUP BY r.body_id, r.name, r.ring_class, r.reserve_level,
         r.last_event_time, r.hotspots_updated
ORDER BY r.body_id NULLS LAST, r.name`, systemID64)
	if err != nil {
		return nil, nil, fmt.Errorf("system inventory rings: %w", err)
	}
	defer ringRows.Close()

	unassigned := make([]InventoryRing, 0)
	for ringRows.Next() {
		var ring InventoryRing
		if err := ringRows.Scan(
			&ring.BodyID,
			&ring.Name,
			&ring.Class,
			&ring.ReserveLevel,
			&ring.HotspotCount,
			&ring.LastEventTime,
		); err != nil {
			return nil, nil, fmt.Errorf("system inventory ring scan: %w", err)
		}
		if ring.BodyID != nil {
			if index, ok := bodyIndexes[*ring.BodyID]; ok {
				bodies[index].Rings = append(bodies[index].Rings, ring)
				continue
			}
		}
		unassigned = append(unassigned, ring)
	}
	return bodies, unassigned, ringRows.Err()
}

// GetMarketInventoryByID returns the complete current commodity snapshot for
// one market. No row limit is applied.
func (s *Store) GetMarketInventoryByID(ctx context.Context, marketID int64) (*MarketInventory, error) {
	out := &MarketInventory{MarketID: marketID}
	err := s.db.QueryRow(ctx, `
SELECT
	m.market_id,
	CASE
		WHEN st.market_id IS NOT NULL THEN st.kind
		WHEN se.market_id IS NOT NULL THEN 'settlement'
		WHEN fc.market_id IS NOT NULL THEN 'fleet_carrier'
		WHEN sc.market_id IS NOT NULL THEN 'stronghold_carrier'
		ELSE 'unlinked_market'
	END,
	COALESCE(
		st.market_id::text,
		se.market_id::text,
		fc.carrier_id,
		sc.system_id64::text,
		m.market_id::text
	),
	COALESCE(st.name, se.name, fc.name, fc.carrier_id, m.station_name, 'Unknown market'),
	COALESCE(sys.name, m.system_name, ''),
	m.last_event_time,
	m.commodity_count,
	m.prohibited
FROM galaxy.market m
LEFT JOIN galaxy.station st ON st.market_id = m.market_id
LEFT JOIN galaxy.settlement se ON se.market_id = m.market_id
LEFT JOIN galaxy.fleet_carrier fc ON fc.market_id = m.market_id
LEFT JOIN galaxy.stronghold_carrier sc ON sc.market_id = m.market_id
LEFT JOIN galaxy.system_catalog sys ON sys.id64 = COALESCE(
	st.system_id64,
	se.system_id64,
	fc.current_system_id64,
	sc.system_id64
)
WHERE m.market_id = $1
LIMIT 1`, marketID).Scan(
		&out.MarketID,
		&out.OwnerKind,
		&out.OwnerIdentity,
		&out.StationName,
		&out.SystemName,
		&out.LastEventTime,
		&out.ReportedCommodityCount,
		&out.Prohibited,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("market inventory header: %w", err)
	}

	rows, err := s.db.Query(ctx, `
SELECT c.name, mc.buy_price, mc.sell_price, mc.demand, mc.stock, c.category
FROM galaxy.market_commodity mc
JOIN galaxy.commodity c ON c.commodity_id = mc.commodity_id
WHERE mc.market_id = $1
ORDER BY c.category, c.name`, marketID)
	if err != nil {
		return nil, fmt.Errorf("market inventory commodities: %w", err)
	}
	defer rows.Close()

	out.Commodities = make([]MarketCommodity, 0)
	for rows.Next() {
		var commodity MarketCommodity
		if err := rows.Scan(
			&commodity.Name,
			&commodity.BuyPrice,
			&commodity.SellPrice,
			&commodity.Demand,
			&commodity.Stock,
			&commodity.Category,
		); err != nil {
			return nil, fmt.Errorf("market inventory commodity scan: %w", err)
		}
		out.Commodities = append(out.Commodities, commodity)
	}
	return out, rows.Err()
}
