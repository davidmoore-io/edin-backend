// Package store provides TimescaleDB client for EDIN (Elite Dangerous Intel Network).
package store

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds database configuration.
type Config struct {
	Enabled  bool
	Host     string
	Port     int
	User     string
	Password string
	Database string
	Schema   string
	PoolSize int
}

// Client wraps a pgxpool connection pool for EDIN database access.
type Client struct {
	pool   *pgxpool.Pool
	schema string
	logger func(string)
}

// Option configures the Client.
type Option func(*Client)

// WithLogger sets a logging function.
func WithLogger(fn func(string)) Option {
	return func(c *Client) {
		c.logger = fn
	}
}

// New creates a new EDIN database client.
func New(ctx context.Context, cfg Config, opts ...Option) (*Client, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	client := &Client{
		schema: cfg.Schema,
	}
	for _, opt := range opts {
		opt(client)
	}

	// URL-encode password to handle special characters
	encodedPassword := url.QueryEscape(cfg.Password)

	// Build connection string with search_path for schema
	// Include 'public' schema for TimescaleDB functions (time_bucket, etc.)
	// sslmode=disable for internal Docker network (no TLS needed)
	connStr := fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?pool_max_conns=%d&pool_min_conns=1&search_path=%s,public&sslmode=disable",
		cfg.User, encodedPassword, cfg.Host, cfg.Port, cfg.Database, cfg.PoolSize, cfg.Schema,
	)

	// Parse config for more control
	poolConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("parse connection string: %w", err)
	}

	// Connection settings
	poolConfig.ConnConfig.ConnectTimeout = 10 * time.Second
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = time.Minute

	// Create pool
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	// Verify connection
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	client.pool = pool
	client.log("connected to EDIN database at %s:%d/%s (schema: %s)", cfg.Host, cfg.Port, cfg.Database, cfg.Schema)

	return client, nil
}

// Close closes the connection pool.
func (c *Client) Close() {
	if c != nil && c.pool != nil {
		c.pool.Close()
		c.log("closed EDIN database connection")
	}
}

// Health checks database connectivity.
func (c *Client) Health(ctx context.Context) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("database client not initialized")
	}
	return c.pool.Ping(ctx)
}

// Pool returns the underlying connection pool for advanced queries.
func (c *Client) Pool() *pgxpool.Pool {
	if c == nil {
		return nil
	}
	return c.pool
}

// Schema returns the configured schema name.
func (c *Client) Schema() string {
	if c == nil {
		return ""
	}
	return c.schema
}

func (c *Client) log(format string, args ...any) {
	if c != nil && c.logger != nil {
		c.logger(fmt.Sprintf(format, args...))
	}
}
