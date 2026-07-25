// Package galaxystore is the relational read layer for the galaxy.* schema.
package galaxystore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSystemNotFound is returned when a name or slug lookup has no matching
// galaxy.system_catalog row.
var ErrSystemNotFound = errors.New("system not found")

// ErrSurveyStartLookup marks a database failure while resolving the optional
// survey-route origin. The legacy HTTP contract maps this class to status 400.
var ErrSurveyStartLookup = errors.New("survey start lookup failed")

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txStarter interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Store executes read-only queries against the galaxy relational state schema.
type Store struct {
	db querier
}

// New returns a relational galaxy read store backed by pool.
func New(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return nil
	}
	return &Store{db: pool}
}

func newWithQuerier(db querier) *Store {
	return &Store{db: db}
}

// Query exposes the underlying read-only querier for domain packages that own
// their result types but read from galaxy.*.
func (s *Store) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return s.db.Query(ctx, sql, args...)
}

// QueryRow exposes the underlying read-only querier for domain packages that
// own their result types but read from galaxy.*.
func (s *Store) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.db.QueryRow(ctx, sql, args...)
}

// BeginReadOnly starts a transaction for ad-hoc read tools that must set
// transaction-local safety limits before executing caller SQL.
func (s *Store) BeginReadOnly(ctx context.Context) (pgx.Tx, error) {
	starter, ok := s.db.(txStarter)
	if !ok {
		return nil, errors.New("galaxy store does not support transactions")
	}
	return starter.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
}

// BeginRepeatableReadOnly starts a stable-snapshot transaction for projections
// assembled from more than one statement.
func (s *Store) BeginRepeatableReadOnly(ctx context.Context) (pgx.Tx, error) {
	starter, ok := s.db.(txStarter)
	if !ok {
		return nil, errors.New("galaxy store does not support transactions")
	}
	return starter.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
}

// ProbeReader verifies that the configured connection is the least-privilege
// galaxy reader and can read the catalog.
func (s *Store) ProbeReader(ctx context.Context) error {
	var user string
	var readable bool
	if err := s.db.QueryRow(ctx, `
SELECT current_user,
       EXISTS (SELECT 1 FROM galaxy.system_catalog LIMIT 1)`).Scan(&user, &readable); err != nil {
		return fmt.Errorf("galaxy reader probe: %w", err)
	}
	if user != "galaxy_reader" {
		return fmt.Errorf("galaxy reader probe: current_user=%q, want galaxy_reader", user)
	}
	if !readable {
		return errors.New("galaxy reader probe: system catalog is empty")
	}
	return nil
}
