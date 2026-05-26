package store

import "github.com/jackc/pgx/v5/pgxpool"

// SetEDDNClientForTest injects a raw pool as the EDDN client for integration testing.
// This file is excluded from production builds (Go only compiles _test.go files during testing).
func (s *CacheStore) SetEDDNClientForTest(pool *pgxpool.Pool) {
	s.eddnClient = &Client{pool: pool}
}
