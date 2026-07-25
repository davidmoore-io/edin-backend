package galaxystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SurveyStation is a dockable large-pad station used by the survey route.
type SurveyStation struct {
	Name       string
	Type       string
	DistanceLS float64
}

// SurveyCandidate is a populated system near a qualifying mining-map anchor.
type SurveyCandidate struct {
	Name       string
	X          float64
	Y          float64
	Z          float64
	LastUpdate *time.Time
	Stations   []SurveyStation
}

// SurveyProjection contains the stable-snapshot inputs needed by the HTTP
// route's existing stale-first and nearest-neighbour logic.
type SurveyProjection struct {
	AnchorsUsed int
	Candidates  []SurveyCandidate
	Start       *Coords
}

// GetSurveyProjection resolves all anchors and candidates set-wise in one
// repeatable-read, read-only transaction.
func (s *Store) GetSurveyProjection(ctx context.Context, mapSystems []string, startSystem string) (*SurveyProjection, error) {
	tx, err := s.BeginRepeatableReadOnly(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := newWithQuerier(tx)
	out, err := q.querySurveyCandidates(ctx, mapSystems)
	if err != nil {
		return nil, err
	}
	if startSystem != "" {
		var start Coords
		err := tx.QueryRow(ctx, `
SELECT x::float8, y::float8, z::float8
FROM galaxy.system_catalog
WHERE name = $1
  AND x IS NOT NULL AND y IS NOT NULL AND z IS NOT NULL
LIMIT 1`, startSystem).Scan(&start.X, &start.Y, &start.Z)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSystemNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("survey start lookup: %w", err)
		}
		out.Start = &start
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("survey snapshot commit: %w", err)
	}
	return out, nil
}

func (s *Store) querySurveyCandidates(ctx context.Context, mapSystems []string) (*SurveyProjection, error) {
	rows, err := s.db.Query(ctx, `
WITH requested(name) AS (
	SELECT unnest($1::text[])
),
anchors AS (
	SELECT c.id64, c.name, c.x, c.y, c.z,
	       CASE sp.powerplay_state
	           WHEN 'Fortified' THEN 20::float8
	           WHEN 'Stronghold' THEN 30::float8
	       END AS radius
	FROM requested r
	JOIN galaxy.system_catalog c ON c.name = r.name
	JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
	WHERE c.x IS NOT NULL AND c.y IS NOT NULL AND c.z IS NOT NULL
	  AND sp.powerplay_state IN ('Fortified', 'Stronghold')
),
candidate_systems AS (
	SELECT DISTINCT ON (c.name)
	       c.id64, c.name, c.x, c.y, c.z,
	       NULLIF(GREATEST(
	           COALESCE(sys.last_event_time, '-infinity'::timestamptz),
	           COALESCE(sys.last_faction_update, '-infinity'::timestamptz),
	           COALESCE(sp.last_event_time, '-infinity'::timestamptz)
	       ), '-infinity'::timestamptz) AS last_update
	FROM anchors a
	CROSS JOIN LATERAL (
		SELECT sc.id64, sc.name, sc.x, sc.y, sc.z
		FROM galaxy.system_catalog sc
		WHERE cube(ARRAY[sc.x,sc.y,sc.z]) <@
		      cube_enlarge(cube(ARRAY[a.x,a.y,a.z]::float8[]), a.radius, 3)
		  AND sc.x IS NOT NULL AND sc.y IS NOT NULL AND sc.z IS NOT NULL
		  AND sqrt(
		      power(sc.x::float8-a.x::float8, 2) +
		      power(sc.y::float8-a.y::float8, 2) +
		      power(sc.z::float8-a.z::float8, 2)
		  ) <= a.radius
	) c
	JOIN galaxy.system sys ON sys.id64 = c.id64 AND sys.population > 0
	LEFT JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
	ORDER BY c.name, c.id64
),
eligible AS (
	SELECT cs.*, st.name AS station_name, st.station_type,
	       COALESCE(st.dist_from_star_ls, 0)::float8 AS distance_ls
	FROM candidate_systems cs
	JOIN galaxy.station st ON st.system_id64 = cs.id64
	WHERE st.kind = 'station'
	  AND st.large_pads > 0
	  AND st.station_type IN ('Coriolis','Orbis','Ocellus','Dodec','Asteroidbase')
)
SELECT (SELECT count(*) FROM anchors) AS anchor_count,
       id64, name, x::float8, y::float8, z::float8, last_update,
       station_name, station_type, distance_ls
FROM eligible
ORDER BY name, distance_ls, station_name`, mapSystems)
	if err != nil {
		return nil, fmt.Errorf("survey candidates: %w", err)
	}
	defer rows.Close()

	out := &SurveyProjection{Candidates: []SurveyCandidate{}}
	index := make(map[string]int)
	for rows.Next() {
		var anchorCount int
		var id64 int64
		var candidate SurveyCandidate
		var station SurveyStation
		if err := rows.Scan(
			&anchorCount,
			&id64,
			&candidate.Name,
			&candidate.X,
			&candidate.Y,
			&candidate.Z,
			&candidate.LastUpdate,
			&station.Name,
			&station.Type,
			&station.DistanceLS,
		); err != nil {
			return nil, fmt.Errorf("survey candidate scan: %w", err)
		}
		out.AnchorsUsed = anchorCount
		i, exists := index[candidate.Name]
		if !exists {
			i = len(out.Candidates)
			index[candidate.Name] = i
			out.Candidates = append(out.Candidates, candidate)
		}
		out.Candidates[i].Stations = append(out.Candidates[i].Stations, station)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Preserve the anchor count when no candidate rows exist.
	if len(out.Candidates) == 0 {
		if err := s.db.QueryRow(ctx, `
WITH requested(name) AS (SELECT unnest($1::text[]))
SELECT count(*)
FROM requested r
JOIN galaxy.system_catalog c ON c.name = r.name
JOIN galaxy.system_power sp ON sp.system_id64 = c.id64
WHERE c.x IS NOT NULL AND c.y IS NOT NULL AND c.z IS NOT NULL
  AND sp.powerplay_state IN ('Fortified', 'Stronghold')`, mapSystems).Scan(&out.AnchorsUsed); err != nil {
			return nil, fmt.Errorf("survey anchor count: %w", err)
		}
	}
	return out, nil
}
