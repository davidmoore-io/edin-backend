package tools

import (
	"context"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
)

// normalizeCommodityName normalizes commodity names for fuzzy matching.
// Removes spaces and lowercases: "Power Generators" -> "powergenerators"
func normalizeCommodityName(name string) string {
	// Remove spaces and lowercase
	return strings.ToLower(strings.ReplaceAll(name, " ", ""))
}

// buildStationTypeFilter returns a Cypher WHERE clause fragment for station type filtering.
// stationType can be: "orbital" (Coriolis/Orbis/Ocellus), "outpost", "planetary", or "any" (default).
func buildStationTypeFilter(stationType string) string {
	switch strings.ToLower(stationType) {
	case "orbital":
		// Large orbital stations - handle both naming conventions in data
		return " AND (st.type IN ['Coriolis Starport', 'Orbis Starport', 'Ocellus Starport', 'Asteroid base', 'Asteroidbase', 'Coriolis', 'Orbis', 'Ocellus', 'Dodec', 'Dodec Starport'])"
	case "outpost":
		return " AND st.type = 'Outpost'"
	case "planetary":
		// Planetary stations - handle various naming conventions
		return " AND (st.type CONTAINS 'Planetary' OR st.type IN ['Settlement', 'Onfootsettlement', 'Crateroutpost', 'Craterport', 'Surfacestation', 'Dockableplanetstation'])"
	default:
		return "" // "any" - no filter
	}
}

// buildPadFilter returns a Cypher WHERE clause fragment for landing pad filtering.
// minPad can be: "L" (large only), "M" (medium or large), "S" (any pad size).
// Uses large_pads/medium_pads fields which contain counts of each pad type.
func buildPadFilter(minPad string) string {
	switch strings.ToUpper(minPad) {
	case "L":
		return " AND st.large_pads > 0"
	case "M":
		return " AND (st.large_pads > 0 OR st.medium_pads > 0)"
	default:
		return "" // "S" or empty - all stations have at least small pads
	}
}

// buildDistanceLSFilter returns a Cypher WHERE clause fragment for station distance from star.
// maxDistanceLS is the maximum distance in light-seconds (0 = no filter).
func buildDistanceLSFilter(maxDistanceLS float64) string {
	if maxDistanceLS > 0 {
		return " AND st.distance_ls <= $max_distance_ls"
	}
	return ""
}

// galaxyMarket queries commodity market data from Memgraph.
func (e *Executor) galaxyMarket(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope (market queries are public)
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}

	if e.memgraph == nil {
		return map[string]any{
			"error":  "memgraph not available",
			"source": "memgraph",
		}, nil
	}

	// Extract parameters
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
	excludeCarriers := getBool(args, "exclude_carriers", true) // Default: exclude fleet carriers

	// New filter parameters
	stationType := strings.ToLower(strings.TrimSpace(getString(args, "station_type"))) // "orbital", "outpost", "planetary", "any"
	maxDistanceLS := getFloatArg(args, "max_distance_ls", 0)                           // Max station distance from star (ls)
	minPad := strings.ToUpper(strings.TrimSpace(getString(args, "min_pad")))           // "L", "M", "S"

	if limit <= 0 || limit > 100 {
		limit = 20
	}

	// Default reference system to Sol
	if referenceSystem == "" {
		referenceSystem = "Sol"
	}

	// Default max distance for area searches
	if maxDistance <= 0 {
		maxDistance = 100
	}

	// Build combined station filter clauses (used by buy/sell/overview queries)
	stationFilters := buildStationTypeFilter(stationType) + buildPadFilter(minPad) + buildDistanceLSFilter(maxDistanceLS)

	// Determine query mode
	switch {
	case systemName != "" && stationName != "":
		// Mode 0: Specific station in specific system (most precise)
		return e.querySystemStationMarket(ctx, systemName, stationName, commodity, limit)

	case systemName != "":
		// Mode 1: System market overview (all stations in system)
		return e.querySystemMarket(ctx, systemName, commodity, limit)

	case stationName != "":
		// Mode 2: Station market data (search by station name)
		return e.queryStationMarket(ctx, stationName, commodity, limit)

	case commodity != "" && operation == "buy":
		// Mode 3: Find places to BUY commodity (lowest prices)
		return e.queryCommodityBuy(ctx, commodity, referenceSystem, maxDistance, minStock, maxPrice, limit, excludeCarriers, stationFilters, maxDistanceLS)

	case commodity != "" && operation == "sell":
		// Mode 4: Find places to SELL commodity (highest prices)
		return e.queryCommoditySell(ctx, commodity, referenceSystem, maxDistance, minDemand, minPrice, limit, excludeCarriers, stationFilters, maxDistanceLS)

	case commodity != "":
		// Default to showing both buy and sell options
		return e.queryCommodityOverview(ctx, commodity, referenceSystem, maxDistance, limit, excludeCarriers, stationFilters, maxDistanceLS)

	default:
		return map[string]any{
			"error":  "must provide system_name, station_name, or commodity",
			"source": "memgraph",
		}, nil
	}
}

// querySystemMarket returns all market data for a system
func (e *Executor) querySystemMarket(ctx context.Context, systemName, commodity string, limit int) (any, error) {
	var query string
	params := map[string]any{"system_name": systemName, "limit": limit}

	// Normalize commodity name: "power generators" -> "powergenerators"
	normalizedCommodity := normalizeCommodityName(commodity)

	if normalizedCommodity != "" {
		// Filter by commodity (normalized for fuzzy matching)
		query = `
			MATCH (sys:System {name: $system_name})-[:HAS_STATION]->(st:Station)-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
			WHERE c.name CONTAINS $commodity
			RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
			       st.type AS station_type, st.distance_ls AS distance_ls,
			       c.name AS commodity, c.category AS category,
			       t.buy_price AS buy_price, t.sell_price AS sell_price,
			       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
			       t.updated_at AS updated
			ORDER BY t.buy_price ASC
			LIMIT $limit
		`
		params["commodity"] = normalizedCommodity
	} else {
		// All commodities at all stations in system
		query = `
			MATCH (sys:System {name: $system_name})-[:HAS_STATION]->(st:Station)-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
			RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
			       st.type AS station_type, st.distance_ls AS distance_ls,
			       c.name AS commodity, c.category AS category,
			       t.buy_price AS buy_price, t.sell_price AS sell_price,
			       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
			       t.updated_at AS updated
			ORDER BY st.name, c.name
			LIMIT $limit
		`
	}

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return map[string]any{
			"error":       err.Error(),
			"system_name": systemName,
			"source":      "memgraph",
		}, nil
	}

	return map[string]any{
		"found":        len(results) > 0,
		"system_name":  systemName,
		"commodity":    commodity,
		"results":      results,
		"result_count": len(results),
		"source":       "memgraph",
	}, nil
}

// querySystemStationMarket returns market data for a specific station in a specific system
func (e *Executor) querySystemStationMarket(ctx context.Context, systemName, stationName, commodity string, limit int) (any, error) {
	var query string
	params := map[string]any{"system_name": systemName, "station_name": stationName, "limit": limit}

	// Normalize commodity name: "power generators" -> "powergenerators"
	normalizedCommodity := normalizeCommodityName(commodity)

	if normalizedCommodity != "" {
		// Filter by specific commodity (normalized for fuzzy matching)
		query = `
			MATCH (sys:System {name: $system_name})-[:HAS_STATION]->(st:Station)-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
			WHERE st.name = $station_name AND c.name CONTAINS $commodity
			RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
			       st.type AS station_type, st.distance_ls AS distance_ls,
			       c.name AS commodity, c.category AS category,
			       t.buy_price AS buy_price, t.sell_price AS sell_price,
			       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
			       t.updated_at AS updated
			LIMIT $limit
		`
		params["commodity"] = normalizedCommodity
	} else {
		// All commodities at the specific station
		query = `
			MATCH (sys:System {name: $system_name})-[:HAS_STATION]->(st:Station)-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
			WHERE st.name = $station_name
			RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
			       st.type AS station_type, st.distance_ls AS distance_ls,
			       c.name AS commodity, c.category AS category,
			       t.buy_price AS buy_price, t.sell_price AS sell_price,
			       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
			       t.updated_at AS updated
			ORDER BY c.name
			LIMIT $limit
		`
	}

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return map[string]any{
			"error":        err.Error(),
			"system_name":  systemName,
			"station_name": stationName,
			"source":       "memgraph",
		}, nil
	}

	return map[string]any{
		"found":        len(results) > 0,
		"system_name":  systemName,
		"station_name": stationName,
		"commodity":    commodity,
		"results":      results,
		"result_count": len(results),
		"source":       "memgraph",
	}, nil
}

// queryStationMarket returns market data for a specific station
func (e *Executor) queryStationMarket(ctx context.Context, stationName, commodity string, limit int) (any, error) {
	var query string
	params := map[string]any{"station_name": stationName, "limit": limit}

	// Normalize commodity name: "power generators" -> "powergenerators"
	normalizedCommodity := normalizeCommodityName(commodity)

	if normalizedCommodity != "" {
		query = `
			MATCH (sys:System)-[:HAS_STATION]->(st:Station)-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
			WHERE st.name CONTAINS $station_name AND c.name CONTAINS $commodity
			RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
			       st.type AS station_type, st.distance_ls AS distance_ls,
			       c.name AS commodity, c.category AS category,
			       t.buy_price AS buy_price, t.sell_price AS sell_price,
			       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
			       t.updated_at AS updated
			LIMIT $limit
		`
		params["commodity"] = normalizedCommodity
	} else {
		query = `
			MATCH (sys:System)-[:HAS_STATION]->(st:Station)-[:HAS_MARKET]->(m:Market)-[t:TRADES]->(c:Commodity)
			WHERE st.name CONTAINS $station_name
			RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
			       st.type AS station_type, st.distance_ls AS distance_ls,
			       c.name AS commodity, c.category AS category,
			       t.buy_price AS buy_price, t.sell_price AS sell_price,
			       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
			       t.updated_at AS updated
			ORDER BY c.name
			LIMIT $limit
		`
	}

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return map[string]any{
			"error":        err.Error(),
			"station_name": stationName,
			"source":       "memgraph",
		}, nil
	}

	return map[string]any{
		"found":        len(results) > 0,
		"station_name": stationName,
		"commodity":    commodity,
		"results":      results,
		"result_count": len(results),
		"source":       "memgraph",
	}, nil
}

// queryCommodityBuy finds best places to BUY a commodity (lowest prices)
func (e *Executor) queryCommodityBuy(ctx context.Context, commodity, refSystem string, maxDist float64, minStock, maxPrice, limit int, excludeCarriers bool, stationFilters string, maxDistanceLS float64) (any, error) {
	// Normalize commodity name: "power generators" -> "powergenerators"
	normalizedCommodity := normalizeCommodityName(commodity)

	// Build carrier filter clause if requested
	carrierFilter := ""
	if excludeCarriers {
		carrierFilter = " AND st.type <> 'Fleetcarrier' AND NOT st.type CONTAINS 'Carrier'"
	}

	query := `
		MATCH (ref:System {name: $reference_system})
		MATCH (c:Commodity)<-[t:TRADES]-(m:Market)<-[:HAS_MARKET]-(st:Station)<-[:HAS_STATION]-(sys:System)
		WHERE c.name CONTAINS $commodity AND t.stock > $min_stock AND t.buy_price > 0` + carrierFilter + stationFilters + `
		WITH sys, st, m, t, c, ref,
		     sqrt((sys.x - ref.x)*(sys.x - ref.x) +
		          (sys.y - ref.y)*(sys.y - ref.y) +
		          (sys.z - ref.z)*(sys.z - ref.z)) AS distance
		WHERE distance <= $max_distance
	`

	params := map[string]any{
		"reference_system": refSystem,
		"commodity":        normalizedCommodity,
		"min_stock":        minStock,
		"max_distance":     maxDist,
		"limit":            limit,
	}

	// Add max_distance_ls parameter if filter is used
	if maxDistanceLS > 0 {
		params["max_distance_ls"] = maxDistanceLS
	}

	if maxPrice > 0 {
		query += " AND t.buy_price <= $max_price"
		params["max_price"] = maxPrice
	}

	query += `
		RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
		       st.type AS station_type, st.distance_ls AS distance_ls,
		       c.name AS commodity, c.category AS category,
		       t.buy_price AS buy_price, t.sell_price AS sell_price,
		       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
		       toInteger(distance) AS distance_ly, t.updated_at AS updated
		ORDER BY t.buy_price ASC
		LIMIT $limit
	`

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return map[string]any{
			"error":            err.Error(),
			"commodity":        commodity,
			"operation":        "buy",
			"reference_system": refSystem,
			"source":           "memgraph",
		}, nil
	}

	return map[string]any{
		"found":            len(results) > 0,
		"commodity":        commodity,
		"operation":        "buy",
		"reference_system": refSystem,
		"max_distance":     maxDist,
		"results":          results,
		"result_count":     len(results),
		"source":           "memgraph",
	}, nil
}

// queryCommoditySell finds best places to SELL a commodity (highest prices)
func (e *Executor) queryCommoditySell(ctx context.Context, commodity, refSystem string, maxDist float64, minDemand, minPrice, limit int, excludeCarriers bool, stationFilters string, maxDistanceLS float64) (any, error) {
	// Normalize commodity name: "power generators" -> "powergenerators"
	normalizedCommodity := normalizeCommodityName(commodity)

	// Build carrier filter clause if requested
	carrierFilter := ""
	if excludeCarriers {
		carrierFilter = " AND st.type <> 'Fleetcarrier' AND NOT st.type CONTAINS 'Carrier'"
	}

	query := `
		MATCH (ref:System {name: $reference_system})
		MATCH (c:Commodity)<-[t:TRADES]-(m:Market)<-[:HAS_MARKET]-(st:Station)<-[:HAS_STATION]-(sys:System)
		WHERE c.name CONTAINS $commodity AND t.demand > $min_demand AND t.sell_price > 0` + carrierFilter + stationFilters + `
		WITH sys, st, m, t, c, ref,
		     sqrt((sys.x - ref.x)*(sys.x - ref.x) +
		          (sys.y - ref.y)*(sys.y - ref.y) +
		          (sys.z - ref.z)*(sys.z - ref.z)) AS distance
		WHERE distance <= $max_distance
	`

	params := map[string]any{
		"reference_system": refSystem,
		"commodity":        normalizedCommodity,
		"min_demand":       minDemand,
		"max_distance":     maxDist,
		"limit":            limit,
	}

	// Add max_distance_ls parameter if filter is used
	if maxDistanceLS > 0 {
		params["max_distance_ls"] = maxDistanceLS
	}

	if minPrice > 0 {
		query += " AND t.sell_price >= $min_price"
		params["min_price"] = minPrice
	}

	query += `
		RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
		       st.type AS station_type, st.distance_ls AS distance_ls,
		       c.name AS commodity, c.category AS category,
		       t.buy_price AS buy_price, t.sell_price AS sell_price,
		       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
		       toInteger(distance) AS distance_ly, t.updated_at AS updated
		ORDER BY t.sell_price DESC
		LIMIT $limit
	`

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return map[string]any{
			"error":            err.Error(),
			"commodity":        commodity,
			"operation":        "sell",
			"reference_system": refSystem,
			"source":           "memgraph",
		}, nil
	}

	return map[string]any{
		"found":            len(results) > 0,
		"commodity":        commodity,
		"operation":        "sell",
		"reference_system": refSystem,
		"max_distance":     maxDist,
		"results":          results,
		"result_count":     len(results),
		"source":           "memgraph",
	}, nil
}

// queryCommodityOverview shows both buy and sell options for a commodity
func (e *Executor) queryCommodityOverview(ctx context.Context, commodity, refSystem string, maxDist float64, limit int, excludeCarriers bool, stationFilters string, maxDistanceLS float64) (any, error) {
	// Normalize commodity name: "power generators" -> "powergenerators"
	normalizedCommodity := normalizeCommodityName(commodity)

	// Build carrier filter clause if requested
	carrierFilter := ""
	if excludeCarriers {
		carrierFilter = " AND st.type <> 'Fleetcarrier' AND NOT st.type CONTAINS 'Carrier'"
	}

	query := `
		MATCH (ref:System {name: $reference_system})
		MATCH (c:Commodity)<-[t:TRADES]-(m:Market)<-[:HAS_MARKET]-(st:Station)<-[:HAS_STATION]-(sys:System)
		WHERE c.name CONTAINS $commodity AND (t.buy_price > 0 OR t.sell_price > 0)` + carrierFilter + stationFilters + `
		WITH sys, st, m, t, c, ref,
		     sqrt((sys.x - ref.x)*(sys.x - ref.x) +
		          (sys.y - ref.y)*(sys.y - ref.y) +
		          (sys.z - ref.z)*(sys.z - ref.z)) AS distance
		WHERE distance <= $max_distance
		RETURN sys.name AS system, st.name AS station, CASE WHEN st.large_pads > 0 THEN 'L' WHEN st.medium_pads > 0 THEN 'M' ELSE 'S' END AS pad,
		       st.type AS station_type, st.distance_ls AS distance_ls,
		       c.name AS commodity, c.category AS category,
		       t.buy_price AS buy_price, t.sell_price AS sell_price,
		       t.stock AS stock, t.demand AS demand, t.mean_price AS mean_price,
		       toInteger(distance) AS distance_ly, t.updated_at AS updated
		ORDER BY distance ASC
		LIMIT $limit
	`

	params := map[string]any{
		"reference_system": refSystem,
		"commodity":        normalizedCommodity,
		"max_distance":     maxDist,
		"limit":            limit,
	}

	// Add max_distance_ls parameter if filter is used
	if maxDistanceLS > 0 {
		params["max_distance_ls"] = maxDistanceLS
	}

	results, err := e.memgraph.ExecuteQuery(ctx, query, params)
	if err != nil {
		return map[string]any{
			"error":            err.Error(),
			"commodity":        commodity,
			"reference_system": refSystem,
			"source":           "memgraph",
		}, nil
	}

	return map[string]any{
		"found":            len(results) > 0,
		"commodity":        commodity,
		"reference_system": refSystem,
		"max_distance":     maxDist,
		"results":          results,
		"result_count":     len(results),
		"source":           "memgraph",
	}, nil
}
