package tools

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// normalizeCommodityName normalizes commodity names for fuzzy matching.
// Removes spaces and lowercases: "Power Generators" -> "powergenerators".
func normalizeCommodityName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
}

// galaxyMarket queries current commodity market data from galaxy.*.
func (e *Executor) galaxyMarket(ctx context.Context, args map[string]any) (any, error) {
	if _, err := e.requireGalaxyStore(); err != nil {
		return map[string]any{"error": err.Error(), "source": "postgres"}, nil
	}

	commodity := strings.ToLower(strings.TrimSpace(getString(args, "commodity")))
	operation := strings.ToLower(strings.TrimSpace(getString(args, "operation")))
	systemName := strings.TrimSpace(getString(args, "system_name"))
	stationName := strings.TrimSpace(getString(args, "station_name"))
	referenceSystem := strings.TrimSpace(getString(args, "reference_system"))
	maxDistance := getFloatArg(args, "max_distance", 0)
	minPrice := getInt(args, "min_price", 0)
	maxPrice := getInt(args, "max_price", 0)
	minDemand := getInt(args, "min_demand", 0)
	minStock := getInt(args, "min_stock", 0)
	limit := getInt(args, "limit", 0)
	excludeCarriers := getBool(args, "exclude_carriers", true)
	stationType := strings.ToLower(strings.TrimSpace(getString(args, "station_type")))
	maxDistanceLS := getFloatArg(args, "max_distance_ls", 0)
	minPad := strings.ToUpper(strings.TrimSpace(getString(args, "min_pad")))

	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if referenceSystem == "" {
		referenceSystem = "Sol"
	}
	if maxDistance <= 0 {
		maxDistance = 100
	}

	switch {
	case systemName != "" && stationName != "":
		return e.querySystemStationMarket(ctx, systemName, stationName, commodity, limit)
	case systemName != "":
		return e.querySystemMarket(ctx, systemName, commodity, limit)
	case stationName != "":
		return e.queryStationMarket(ctx, stationName, commodity, limit)
	case commodity != "" && operation == "buy":
		return e.queryCommodityBuy(ctx, commodity, referenceSystem, maxDistance, minStock, maxPrice, limit, excludeCarriers, stationType, minPad, maxDistanceLS)
	case commodity != "" && operation == "sell":
		return e.queryCommoditySell(ctx, commodity, referenceSystem, maxDistance, minDemand, minPrice, limit, excludeCarriers, stationType, minPad, maxDistanceLS)
	case commodity != "":
		return e.queryCommodityOverview(ctx, commodity, referenceSystem, maxDistance, limit, excludeCarriers, stationType, minPad, maxDistanceLS)
	default:
		return map[string]any{"error": "must provide system_name, station_name, or commodity", "source": "postgres"}, nil
	}
}

func (e *Executor) querySystemMarket(ctx context.Context, systemName, commodity string, limit int) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	whereCommodity := ""
	args := pgx.NamedArgs{"system_name": systemName, "limit": limit}
	if normalized := normalizeCommodityName(commodity); normalized != "" {
		whereCommodity = " AND replace(c.name, ' ', '') ILIKE '%' || @commodity || '%'"
		args["commodity"] = normalized
	}

	rows, err := store.Query(ctx, galaxyMarketSelectSQL(false)+galaxyMarketFromSQL()+`
WHERE lower(sys.name) = lower(@system_name)`+whereCommodity+`
ORDER BY st.name, c.name
LIMIT @limit`, args)
	if err != nil {
		return map[string]any{"error": err.Error(), "system_name": systemName, "source": "postgres"}, nil
	}
	results, err := scanMarketMaps(ctx, rows)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"found":        len(results) > 0,
		"system_name":  systemName,
		"commodity":    commodity,
		"results":      results,
		"result_count": len(results),
		"source":       "postgres",
	}, nil
}

func (e *Executor) querySystemStationMarket(ctx context.Context, systemName, stationName, commodity string, limit int) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	whereCommodity := ""
	args := pgx.NamedArgs{"system_name": systemName, "station_name": stationName, "limit": limit}
	if normalized := normalizeCommodityName(commodity); normalized != "" {
		whereCommodity = " AND replace(c.name, ' ', '') ILIKE '%' || @commodity || '%'"
		args["commodity"] = normalized
	}

	rows, err := store.Query(ctx, galaxyMarketSelectSQL(false)+galaxyMarketFromSQL()+`
WHERE lower(sys.name) = lower(@system_name)
  AND lower(st.name) = lower(@station_name)`+whereCommodity+`
ORDER BY c.name
LIMIT @limit`, args)
	if err != nil {
		return map[string]any{"error": err.Error(), "system_name": systemName, "station_name": stationName, "source": "postgres"}, nil
	}
	results, err := scanMarketMaps(ctx, rows)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"found":        len(results) > 0,
		"system_name":  systemName,
		"station_name": stationName,
		"commodity":    commodity,
		"results":      results,
		"result_count": len(results),
		"source":       "postgres",
	}, nil
}

func (e *Executor) queryStationMarket(ctx context.Context, stationName, commodity string, limit int) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	whereCommodity := ""
	args := pgx.NamedArgs{"station_name": stationName, "limit": limit}
	if normalized := normalizeCommodityName(commodity); normalized != "" {
		whereCommodity = " AND replace(c.name, ' ', '') ILIKE '%' || @commodity || '%'"
		args["commodity"] = normalized
	}

	rows, err := store.Query(ctx, galaxyMarketSelectSQL(false)+galaxyMarketFromSQL()+`
WHERE st.name ILIKE '%' || @station_name || '%'`+whereCommodity+`
ORDER BY st.name, c.name
LIMIT @limit`, args)
	if err != nil {
		return map[string]any{"error": err.Error(), "station_name": stationName, "source": "postgres"}, nil
	}
	results, err := scanMarketMaps(ctx, rows)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"found":        len(results) > 0,
		"station_name": stationName,
		"commodity":    commodity,
		"results":      results,
		"result_count": len(results),
		"source":       "postgres",
	}, nil
}

func (e *Executor) queryCommodityBuy(ctx context.Context, commodity, refSystem string, maxDist float64, minStock, maxPrice, limit int, excludeCarriers bool, stationType, minPad string, maxDistanceLS float64) (any, error) {
	extra := ""
	args := pgx.NamedArgs{
		"reference_system": refSystem,
		"commodity":        normalizeCommodityName(commodity),
		"max_distance":     maxDist,
		"min_stock":        minStock,
		"limit":            limit,
		"max_distance_ls":  maxDistanceLS,
	}
	if maxPrice > 0 {
		extra += " AND mc.buy_price <= @max_price"
		args["max_price"] = maxPrice
	}
	results, err := e.queryCommodityDistanceMarket(ctx, args, `
  AND mc.stock > @min_stock
  AND mc.buy_price > 0`+extra, "mc.buy_price ASC", excludeCarriers, stationType, minPad, maxDistanceLS)
	if err != nil {
		return map[string]any{"error": err.Error(), "commodity": commodity, "operation": "buy", "reference_system": refSystem, "source": "postgres"}, nil
	}
	return map[string]any{
		"found":            len(results) > 0,
		"commodity":        commodity,
		"operation":        "buy",
		"reference_system": refSystem,
		"max_distance":     maxDist,
		"results":          results,
		"result_count":     len(results),
		"source":           "postgres",
	}, nil
}

func (e *Executor) queryCommoditySell(ctx context.Context, commodity, refSystem string, maxDist float64, minDemand, minPrice, limit int, excludeCarriers bool, stationType, minPad string, maxDistanceLS float64) (any, error) {
	extra := ""
	args := pgx.NamedArgs{
		"reference_system": refSystem,
		"commodity":        normalizeCommodityName(commodity),
		"max_distance":     maxDist,
		"min_demand":       minDemand,
		"limit":            limit,
		"max_distance_ls":  maxDistanceLS,
	}
	if minPrice > 0 {
		extra += " AND mc.sell_price >= @min_price"
		args["min_price"] = minPrice
	}
	results, err := e.queryCommodityDistanceMarket(ctx, args, `
  AND mc.demand > @min_demand
  AND mc.sell_price > 0`+extra, "mc.sell_price DESC", excludeCarriers, stationType, minPad, maxDistanceLS)
	if err != nil {
		return map[string]any{"error": err.Error(), "commodity": commodity, "operation": "sell", "reference_system": refSystem, "source": "postgres"}, nil
	}
	return map[string]any{
		"found":            len(results) > 0,
		"commodity":        commodity,
		"operation":        "sell",
		"reference_system": refSystem,
		"max_distance":     maxDist,
		"results":          results,
		"result_count":     len(results),
		"source":           "postgres",
	}, nil
}

func (e *Executor) queryCommodityOverview(ctx context.Context, commodity, refSystem string, maxDist float64, limit int, excludeCarriers bool, stationType, minPad string, maxDistanceLS float64) (any, error) {
	args := pgx.NamedArgs{
		"reference_system": refSystem,
		"commodity":        normalizeCommodityName(commodity),
		"max_distance":     maxDist,
		"limit":            limit,
		"max_distance_ls":  maxDistanceLS,
	}
	results, err := e.queryCommodityDistanceMarket(ctx, args, `
  AND (mc.buy_price > 0 OR mc.sell_price > 0)`, "distance ASC", excludeCarriers, stationType, minPad, maxDistanceLS)
	if err != nil {
		return map[string]any{"error": err.Error(), "commodity": commodity, "reference_system": refSystem, "source": "postgres"}, nil
	}
	return map[string]any{
		"found":            len(results) > 0,
		"commodity":        commodity,
		"reference_system": refSystem,
		"max_distance":     maxDist,
		"results":          results,
		"result_count":     len(results),
		"source":           "postgres",
	}, nil
}

func (e *Executor) queryCommodityDistanceMarket(ctx context.Context, args pgx.NamedArgs, predicate, orderBy string, excludeCarriers bool, stationType, minPad string, maxDistanceLS float64) ([]map[string]any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	distance := floatDistanceExpr("sys", "ref")
	sql := galaxyMarketSelectSQL(true) + galaxyMarketFromSQL() + `
JOIN galaxy.system_catalog ref ON lower(ref.name) = lower(@reference_system)
WHERE replace(c.name, ' ', '') ILIKE '%' || @commodity || '%'
` + predicate + galaxyCarrierExclusionSQL(excludeCarriers) + galaxyStationFilterSQL(stationType, minPad, maxDistanceLS) + `
  AND sys.x BETWEEN ref.x - @max_distance AND ref.x + @max_distance
  AND sys.y BETWEEN ref.y - @max_distance AND ref.y + @max_distance
  AND sys.z BETWEEN ref.z - @max_distance AND ref.z + @max_distance
  AND ` + distance + ` <= @max_distance
ORDER BY ` + orderBy + `
LIMIT @limit`

	rows, err := store.Query(ctx, sql, args)
	if err != nil {
		return nil, err
	}
	return scanMarketMaps(ctx, rows)
}
