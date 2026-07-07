package galaxystore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// MarketCommodity represents one commodity row in a market snapshot.
type MarketCommodity struct {
	Name      string `json:"name"`
	BuyPrice  int64  `json:"buy_price"`
	SellPrice int64  `json:"sell_price"`
	Demand    int64  `json:"demand"`
	Stock     int64  `json:"stock"`
	Category  string `json:"category,omitempty"`
}

// FactionState represents active BGS states for a faction present in a system.
type FactionState struct {
	FactionName string   `json:"faction_name"`
	States      []string `json:"states"`
}

// StationMarketData is the graph-era market payload backed by galaxy.*.
type StationMarketData struct {
	StationName   string            `json:"station_name"`
	SystemName    string            `json:"system_name"`
	MarketID      int64             `json:"market_id,omitempty"`
	Commodities   []MarketCommodity `json:"commodities"`
	LastUpdate    time.Time         `json:"last_update,omitempty"`
	FactionStates []FactionState    `json:"faction_states,omitempty"`
}

// GetStationMarket fetches the current market snapshot for a named station.
func (s *Store) GetStationMarket(ctx context.Context, systemName, stationName string) (*StationMarketData, error) {
	var marketID int64
	var lastUpdate time.Time
	var resolvedStation, resolvedSystem string
	err := s.db.QueryRow(ctx, `
WITH target_system AS (
	SELECT id64, name
	FROM galaxy.system_catalog
	WHERE lower(name) = lower($1)
	LIMIT 1
)
SELECT m.market_id, m.last_event_time, COALESCE(st.name, m.station_name), COALESCE(ts.name, m.system_name)
FROM galaxy.station st
JOIN target_system ts ON ts.id64 = st.system_id64
JOIN galaxy.market m ON m.market_id = st.market_id
WHERE lower(st.name) = lower($2)
LIMIT 1`, systemName, stationName).Scan(&marketID, &lastUpdate, &resolvedStation, &resolvedSystem)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("station market: %w", err)
	}
	return s.getMarketByID(ctx, marketID, resolvedStation, resolvedSystem, lastUpdate, true)
}

// GetFleetCarrierMarket fetches the current market snapshot for a carrier id.
func (s *Store) GetFleetCarrierMarket(ctx context.Context, carrierID string) (*StationMarketData, error) {
	var marketID int64
	var lastUpdate time.Time
	var stationName, systemName string
	err := s.db.QueryRow(ctx, `
SELECT m.market_id, m.last_event_time, COALESCE(fc.name, fc.carrier_id), COALESCE(sys.name, c.name, m.system_name, '')
FROM galaxy.fleet_carrier fc
JOIN galaxy.market m ON m.market_id = fc.market_id
LEFT JOIN galaxy.system sys ON sys.id64 = fc.current_system_id64
LEFT JOIN galaxy.system_catalog c ON c.id64 = fc.current_system_id64
WHERE fc.carrier_id = $1
LIMIT 1`, carrierID).Scan(&marketID, &lastUpdate, &stationName, &systemName)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("carrier market: %w", err)
	}
	return s.getMarketByID(ctx, marketID, stationName, systemName, lastUpdate, false)
}

func (s *Store) getMarketByID(ctx context.Context, marketID int64, stationName, systemName string, lastUpdate time.Time, includeFactionStates bool) (*StationMarketData, error) {
	rows, err := s.db.Query(ctx, `
SELECT c.name, c.category, mc.buy_price, mc.sell_price, mc.demand, mc.stock
FROM galaxy.market_commodity mc
JOIN galaxy.commodity c ON c.commodity_id = mc.commodity_id
WHERE mc.market_id = $1
ORDER BY c.category, c.name`, marketID)
	if err != nil {
		return nil, fmt.Errorf("market commodities: %w", err)
	}
	defer rows.Close()

	out := &StationMarketData{
		StationName: stationName,
		SystemName:  systemName,
		MarketID:    marketID,
		LastUpdate:  lastUpdate,
		Commodities: []MarketCommodity{},
	}
	for rows.Next() {
		var commodity MarketCommodity
		if err := rows.Scan(
			&commodity.Name,
			&commodity.Category,
			&commodity.BuyPrice,
			&commodity.SellPrice,
			&commodity.Demand,
			&commodity.Stock,
		); err != nil {
			return nil, fmt.Errorf("market commodities scan: %w", err)
		}
		out.Commodities = append(out.Commodities, commodity)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if includeFactionStates && systemName != "" {
		states, err := s.getFactionStates(ctx, systemName)
		if err != nil {
			return nil, err
		}
		out.FactionStates = states
	}
	return out, nil
}

func (s *Store) getFactionStates(ctx context.Context, systemName string) ([]FactionState, error) {
	rows, err := s.db.Query(ctx, `
SELECT f.name, sf.active_states
FROM galaxy.system_faction sf
JOIN galaxy.faction f ON f.faction_id = sf.faction_id
JOIN galaxy.system sys ON sys.id64 = sf.system_id64
WHERE lower(sys.name) = lower($1)
  AND cardinality(sf.active_states) > 0
ORDER BY f.name`, systemName)
	if err != nil {
		return nil, fmt.Errorf("market faction states: %w", err)
	}
	defer rows.Close()

	var out []FactionState
	for rows.Next() {
		var fs FactionState
		if err := rows.Scan(&fs.FactionName, &fs.States); err != nil {
			return nil, fmt.Errorf("market faction states scan: %w", err)
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}
