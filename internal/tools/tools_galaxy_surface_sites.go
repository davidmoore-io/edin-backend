package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// galaxySurfaceSites finds notable surface sites near a reference system.
func (e *Executor) galaxySurfaceSites(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	if systemName == "" {
		systemName = strings.TrimSpace(getString(args, "reference_system"))
	}
	if systemName == "" {
		return nil, errors.New("system_name is required")
	}
	radius := getFloatArg(args, "radius", 100)
	if radius <= 0 {
		radius = getFloatArg(args, "max_distance", 100)
	}
	if radius <= 0 {
		radius = 100
	}
	if radius > 500 {
		radius = 500
	}
	siteKind := strings.TrimSpace(getString(args, "site_kind"))
	nameQuery := strings.TrimSpace(getString(args, "name"))
	if nameQuery == "" {
		nameQuery = strings.TrimSpace(getString(args, "query"))
	}
	limit := getInt(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	argsSQL := pgx.NamedArgs{
		"system_name": systemName,
		"radius":      radius,
		"site_kind":   siteKind,
		"name_query":  nameQuery,
		"limit":       limit,
	}
	rows, err := store.Query(ctx, `
WITH ref AS (
	SELECT id64, name, x::float8 AS x, y::float8 AS y, z::float8 AS z
	FROM galaxy.system_catalog
	WHERE lower(name) = lower(@system_name)
	LIMIT 1
),
matches AS (
	SELECT
		ref.name AS reference_system,
		COALESCE(sys.name, c.name) AS system,
		ss.system_id64,
		ss.body_id,
		COALESCE(ss.body_name, b.name) AS body,
		ss.name AS site_name,
		ss.name_key,
		ss.site_kind,
		ss.latitude::float8,
		ss.longitude::float8,
		ss.first_seen,
		ss.last_event_time AS last_seen,
		sqrt(power(c.x::float8-ref.x, 2)+power(c.y::float8-ref.y, 2)+power(c.z::float8-ref.z, 2)) AS distance_ly
	FROM ref
	JOIN galaxy.system_catalog c
	  ON c.x BETWEEN ref.x - @radius AND ref.x + @radius
	 AND c.y BETWEEN ref.y - @radius AND ref.y + @radius
	 AND c.z BETWEEN ref.z - @radius AND ref.z + @radius
	JOIN galaxy.surface_site ss ON ss.system_id64 = c.id64
	LEFT JOIN galaxy.system sys ON sys.id64 = c.id64
	LEFT JOIN galaxy.body b ON b.system_id64 = ss.system_id64 AND b.body_id = ss.body_id
	WHERE (@site_kind = '' OR ss.site_kind ILIKE '%' || @site_kind || '%')
	  AND (@name_query = '' OR ss.name ILIKE '%' || @name_query || '%' OR ss.name_key ILIKE '%' || @name_query || '%')
)
SELECT *
FROM matches
WHERE distance_ly <= @radius
ORDER BY distance_ly, system, body_id, site_name
LIMIT @limit`, argsSQL)
	if err != nil {
		return nil, err
	}
	results, err := scanGalaxyRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		if _, ok, err := queryOneString(ctx, store, `SELECT name FROM galaxy.system_catalog WHERE lower(name)=lower($1) LIMIT 1`, systemName); err != nil {
			return nil, err
		} else if !ok {
			return map[string]any{"found": false, "error": "system not found", "system_name": systemName, "source": "postgres"}, nil
		}
	}
	for _, row := range results {
		row["distance_ly"] = round1(toFloat(row["distance_ly"]))
	}

	return map[string]any{
		"found":            len(results) > 0,
		"reference_system": systemName,
		"radius":           radius,
		"site_kind":        siteKind,
		"query":            nameQuery,
		"results":          results,
		"result_count":     len(results),
		"source":           "postgres",
	}, nil
}
