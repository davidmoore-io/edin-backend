package tools

import (
	"context"
	"errors"
	"fmt"
	"math"
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
