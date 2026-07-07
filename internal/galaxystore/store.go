// Package galaxystore is the relational read layer for the galaxy.* schema.
package galaxystore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSystemNotFound is returned when a name or slug lookup has no matching
// galaxy.system_catalog row.
var ErrSystemNotFound = errors.New("system not found")

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
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
