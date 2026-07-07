package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/jackc/pgx/v5"
)

func (e *Executor) requireGalaxyStore() (*galaxystore.Store, error) {
	if e.galaxyStore == nil {
		return nil, errors.New("galaxy relational store not available")
	}
	return e.galaxyStore, nil
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func scanGalaxyRows(ctx context.Context, rows pgx.Rows) ([]map[string]any, error) {
	defer rows.Close()
	out, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func galaxyStationFilterSQL(stationType, minPad string, maxDistanceLS float64) string {
	var clauses []string
	switch strings.ToLower(stationType) {
	case "orbital":
		clauses = append(clauses, "st.station_type IN ('Coriolis Starport','Orbis Starport','Ocellus Starport','Asteroid base','Asteroidbase','Coriolis','Orbis','Ocellus','Dodec','Dodec Starport')")
	case "outpost":
		clauses = append(clauses, "st.station_type = 'Outpost'")
	case "planetary":
		clauses = append(clauses, "(st.station_type ILIKE '%Planetary%' OR st.station_type IN ('Settlement','Onfootsettlement','Crateroutpost','Craterport','Surfacestation','Dockableplanetstation') OR st.kind = 'planetary_depot')")
	}
	switch strings.ToUpper(minPad) {
	case "L":
		clauses = append(clauses, "st.large_pads > 0")
	case "M":
		clauses = append(clauses, "(st.large_pads > 0 OR st.medium_pads > 0)")
	}
	if maxDistanceLS > 0 {
		clauses = append(clauses, "st.dist_from_star_ls <= @max_distance_ls")
	}
	if len(clauses) == 0 {
		return ""
	}
	return " AND " + strings.Join(clauses, " AND ")
}

func galaxyCarrierExclusionSQL(exclude bool) string {
	if !exclude {
		return ""
	}
	return " AND COALESCE(st.station_type, '') NOT ILIKE '%carrier%' AND COALESCE(st.kind, '') <> 'fleet_carrier'"
}

func galaxyMarketSelectSQL(includeDistance bool) string {
	distanceCol := ""
	if includeDistance {
		distanceCol = ", round(distance::numeric, 1)::float8 AS distance_ly"
	}
	return `
SELECT
	sys.name AS system,
	st.name AS station,
	CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
	st.station_type,
	st.dist_from_star_ls::float8 AS distance_ls,
	c.name AS commodity,
	c.category,
	mc.buy_price,
	mc.sell_price,
	mc.stock,
	mc.demand,
	0::int AS mean_price,
	mc.last_event_time AS updated` + distanceCol + `
`
}

func galaxyMarketFromSQL() string {
	return `
FROM galaxy.market_commodity mc
JOIN galaxy.commodity c ON c.commodity_id = mc.commodity_id
JOIN galaxy.market m ON m.market_id = mc.market_id
JOIN galaxy.station st ON st.market_id = m.market_id
JOIN galaxy.system_catalog sys ON sys.id64 = st.system_id64
`
}

func scanMarketMaps(ctx context.Context, rows pgx.Rows) ([]map[string]any, error) {
	return scanGalaxyRows(ctx, rows)
}

func queryOneString(ctx context.Context, store *galaxystore.Store, sql string, args ...any) (string, bool, error) {
	var out string
	err := store.QueryRow(ctx, sql, args...).Scan(&out)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}

func floatDistanceExpr(aliasA, aliasB string) string {
	return fmt.Sprintf("sqrt(power(%s.x::float8-%s.x::float8, 2)+power(%s.y::float8-%s.y::float8, 2)+power(%s.z::float8-%s.z::float8, 2))", aliasA, aliasB, aliasA, aliasB, aliasA, aliasB)
}
