package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// galaxyPower queries powerplay power data from the relational galaxy store.
func (e *Executor) galaxyPower(ctx context.Context, args map[string]any) (any, error) {
	if e.galaxyStore == nil {
		return nil, errors.New("galaxy relational store not available")
	}

	powerName := strings.TrimSpace(getString(args, "power_name"))
	if powerName == "" {
		powerName = strings.TrimSpace(getString(args, "power"))
	}
	if powerName == "" {
		return nil, errors.New("power_name parameter is required")
	}

	includeSystems := getBool(args, "include_systems", false)
	limit := getInt(args, "limit", 50)

	power, err := e.galaxyStore.GetPower(ctx, powerName)
	if err != nil {
		return nil, err
	}
	if power == nil {
		return map[string]any{
			"found":   false,
			"message": fmt.Sprintf("Power '%s' not found", powerName),
		}, nil
	}

	response := map[string]any{
		"found":  true,
		"power":  power,
		"source": "postgres",
	}

	if includeSystems {
		systems, err := e.galaxyStore.GetPowerSystems(ctx, powerName, limit)
		if err == nil {
			response["systems"] = systems
			response["system_count"] = len(systems)
		}
	}

	return response, nil
}

// galaxyFaction queries minor faction data from the relational galaxy store.
func (e *Executor) galaxyFaction(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	factionName := strings.TrimSpace(getString(args, "faction_name"))
	if factionName == "" {
		factionName = strings.TrimSpace(getString(args, "faction"))
	}
	systemName := strings.TrimSpace(getString(args, "system_name"))
	factionState := strings.TrimSpace(getString(args, "faction_state"))

	if factionName != "" {
		faction, err := queryFaction(ctx, store, factionName)
		if err != nil {
			return nil, err
		}
		if faction == nil {
			return map[string]any{
				"found":   false,
				"message": fmt.Sprintf("Faction '%s' not found", factionName),
			}, nil
		}

		response := map[string]any{
			"found":   true,
			"faction": faction,
			"source":  "postgres",
		}

		if factionState != "" {
			limit := getInt(args, "limit", 50)
			results, err := querySystemsByFactionState(ctx, store, factionState, factionName, limit)
			if err == nil {
				response["systems_in_state"] = results
				response["state_count"] = len(results)
				response["state_filter"] = factionState
			}
		} else if getBool(args, "include_systems", false) {
			limit := getInt(args, "limit", 50)
			presences, err := queryFactionSystems(ctx, store, factionName, limit)
			if err == nil {
				response["systems"] = presences
				response["system_count"] = len(presences)
			}
		}

		return response, nil
	}

	if systemName != "" {
		full, err := store.GetSystemFull(ctx, systemName)
		if err != nil {
			return nil, err
		}
		var factions any = []any{}
		count := 0
		if full != nil {
			factions = full.Factions
			count = len(full.Factions)
			systemName = full.System.Name
		}
		return map[string]any{
			"found":    count > 0,
			"factions": factions,
			"count":    count,
			"system":   systemName,
			"source":   "postgres",
		}, nil
	}

	if factionState != "" {
		limit := getInt(args, "limit", 50)
		results, err := querySystemsByFactionState(ctx, store, factionState, "", limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"found":         len(results) > 0,
			"systems":       results,
			"count":         len(results),
			"faction_state": factionState,
			"source":        "postgres",
		}, nil
	}

	return nil, errors.New("faction_name, system_name, or faction_state parameter is required")
}

// galaxyStats returns relational galaxy database statistics.
func (e *Executor) galaxyStats(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	stats, err := queryGalaxyStats(ctx, store)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"stats":  stats,
		"source": "postgres",
	}, nil
}

// galaxySchema returns a relational schema inventory for galaxy.*.
func (e *Executor) galaxySchema(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	schema, err := queryGalaxySchema(ctx, store)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"schema": schema,
		"source": "postgres",
	}, nil
}

func queryFaction(ctx context.Context, store interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, factionName string) (map[string]any, error) {
	var name string
	var allegiance, government *string
	var lastEvent *time.Time
	var systemCount int
	err := store.QueryRow(ctx, `
SELECT f.name, f.allegiance, f.government, f.last_event_time, COUNT(sf.system_id64)::int
FROM galaxy.faction f
LEFT JOIN galaxy.system_faction sf ON sf.faction_id = f.faction_id
WHERE lower(f.name) = lower($1)
GROUP BY f.faction_id, f.name, f.allegiance, f.government, f.last_event_time
LIMIT 1`, factionName).Scan(&name, &allegiance, &government, &lastEvent, &systemCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return map[string]any{
		"name":             name,
		"allegiance":       derefString(allegiance),
		"government":       derefString(government),
		"last_eddn_update": timeOrNil(derefTimePtr(lastEvent)),
		"system_count":     systemCount,
	}, nil
}

func queryFactionSystems(ctx context.Context, store interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, factionName string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := store.Query(ctx, `
SELECT
	COALESCE(sys.name, c.name) AS system_name,
	f.name AS faction_name,
	sf.influence::float8,
	sf.state,
	sf.active_states,
	sf.pending_states,
	sf.happiness,
	sf.last_event_time
FROM galaxy.system_faction sf
JOIN galaxy.faction f ON f.faction_id = sf.faction_id
JOIN galaxy.system_catalog c ON c.id64 = sf.system_id64
LEFT JOIN galaxy.system sys ON sys.id64 = sf.system_id64
WHERE lower(f.name) = lower($1)
ORDER BY sf.influence DESC, c.name
LIMIT $2`, factionName, limit)
	if err != nil {
		return nil, err
	}
	return scanGalaxyRows(ctx, rows)
}

func querySystemsByFactionState(ctx context.Context, store interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, state string, factionName string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	factionFilter := ""
	args := pgx.NamedArgs{"state": state, "limit": limit}
	if strings.TrimSpace(factionName) != "" {
		factionFilter = " AND lower(f.name) = lower(@faction_name)"
		args["faction_name"] = factionName
	}
	rows, err := store.Query(ctx, `
SELECT
	COALESCE(sys.name, c.name) AS system_name,
	f.name AS faction_name,
	sf.state,
	sf.active_states,
	sf.pending_states,
	sf.influence::float8,
	sf.happiness,
	sf.last_event_time,
	sys.allegiance,
	sys.government,
	sys.population,
	sp.power_name AS controlling_power,
	NULLIF(GREATEST(
		COALESCE(sys.last_event_time, '-infinity'::timestamptz),
		COALESCE(sys.last_faction_update, '-infinity'::timestamptz),
		COALESCE(sp.last_event_time, '-infinity'::timestamptz)
	), '-infinity'::timestamptz) AS last_eddn_update
FROM galaxy.system_faction sf
JOIN galaxy.faction f ON f.faction_id = sf.faction_id
JOIN galaxy.system_catalog c ON c.id64 = sf.system_id64
LEFT JOIN galaxy.system sys ON sys.id64 = sf.system_id64
LEFT JOIN galaxy.system_power sp ON sp.system_id64 = sf.system_id64
WHERE (lower(sf.state) = lower(@state) OR lower(@state) = ANY(SELECT lower(x) FROM unnest(sf.active_states) AS x))
`+factionFilter+`
ORDER BY sf.influence DESC, c.name
LIMIT @limit`, args)
	if err != nil {
		return nil, err
	}
	return scanGalaxyRows(ctx, rows)
}

func queryGalaxyStats(ctx context.Context, store interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (map[string]any, error) {
	rows, err := store.Query(ctx, `
SELECT relname AS table_name, n_live_tup::bigint AS rows
FROM pg_stat_user_tables
WHERE schemaname = 'galaxy'
ORDER BY n_live_tup DESC, relname`)
	if err != nil {
		return nil, err
	}
	countRows, err := scanGalaxyRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	tableCounts := make(map[string]int64, len(countRows))
	for _, row := range countRows {
		tableCounts[row["table_name"].(string)] = int64(toFloat(row["rows"]))
	}
	return map[string]any{
		"table_counts": tableCounts,
		"last_updated": time.Now(),
	}, nil
}

func queryGalaxySchema(ctx context.Context, store interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (map[string]any, error) {
	columnsRows, err := store.Query(ctx, `
SELECT table_name, column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema = 'galaxy'
ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, err
	}
	columns, err := scanGalaxyRows(ctx, columnsRows)
	if err != nil {
		return nil, err
	}
	indexRows, err := store.Query(ctx, `
SELECT tablename AS table_name, indexname AS index_name, indexdef
FROM pg_indexes
WHERE schemaname = 'galaxy'
ORDER BY tablename, indexname`)
	if err != nil {
		return nil, err
	}
	indexes, err := scanGalaxyRows(ctx, indexRows)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tables":  columns,
		"indexes": indexes,
	}, nil
}

func derefTimePtr(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
