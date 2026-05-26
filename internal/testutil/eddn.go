package testutil

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartEDDNTestDB spins up a TimescaleDB container, creates the feed.messages
// hypertable (matching production schema), and returns a pool plus cleanup.
// Mirrors StartTestDB exactly — same image, same startup strategy.
func StartEDDNTestDB(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()

	ctx := context.Background()

	ctr, err := postgres.Run(ctx, testDBImage,
		postgres.WithDatabase("eddn_test"),
		postgres.WithUsername("eddn_admin"),
		postgres.WithPassword("testpass"),
		testcontainers.WithEnv(map[string]string{"NO_TS_TUNE": "1"}),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("StartEDDNTestDB: start container: %v", err)
	}

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = ctr.Terminate(ctx)
		t.Fatalf("StartEDDNTestDB: connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		_ = ctr.Terminate(ctx)
		t.Fatalf("StartEDDNTestDB: create pool: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		_ = ctr.Terminate(ctx)
		t.Fatalf("StartEDDNTestDB: ping: %v", err)
	}

	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS timescaledb"); err != nil {
		pool.Close()
		_ = ctr.Terminate(ctx)
		t.Fatalf("StartEDDNTestDB: enable timescaledb: %v", err)
	}

	if err := createFeedSchema(ctx, pool); err != nil {
		pool.Close()
		_ = ctr.Terminate(ctx)
		t.Fatalf("StartEDDNTestDB: create schema: %v", err)
	}

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			pool.Close()
			termCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = ctr.Terminate(termCtx)
		})
	}
	t.Cleanup(cleanup)
	return pool, cleanup
}

// createFeedSchema creates the minimal feed schema required by CacheStore history
// queries. Must match the production schema in eddn-init.sql.j2 exactly.
func createFeedSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS feed;

		CREATE TABLE IF NOT EXISTS feed.messages (
			received_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			id                BIGSERIAL,
			schema_ref        TEXT NOT NULL,
			gateway_timestamp TIMESTAMPTZ,
			software_name     TEXT,
			software_version  TEXT,
			uploader_id       TEXT,
			system_name       TEXT,
			station_name      TEXT,
			event_type        TEXT,
			message_data      JSONB NOT NULL,
			header_data       JSONB,
			data_size         INTEGER,
			CONSTRAINT pk_messages PRIMARY KEY (received_at, id)
		);

		SELECT create_hypertable('feed.messages', 'received_at',
			if_not_exists => TRUE,
			chunk_time_interval => INTERVAL '1 day'
		);

		CREATE INDEX IF NOT EXISTS idx_messages_system
			ON feed.messages (system_name, received_at DESC)
			WHERE system_name IS NOT NULL;

		CREATE INDEX IF NOT EXISTS idx_messages_event
			ON feed.messages (event_type, received_at DESC)
			WHERE event_type IS NOT NULL;
	`)
	return err
}

// SeedEDDNMessagesFromCSV loads rows from the named CSV file into feed.messages.
// CSV file must be in internal/store/testdata/ relative to this source file.
// The CSV must have the header row produced by the fixture-pull commands.
// Returns the number of rows inserted.
//
// Fixture files:
//   - feed_messages_alpha_centauri.csv — Alpha Centauri FSDJump rows with PowerplayState
//   - feed_messages_expansion.csv      — Arietis Sector CQ-Y c5 FSDJump rows with PowerplayConflictProgress
func SeedEDDNMessagesFromCSV(t *testing.T, pool *pgxpool.Pool, csvFilename string) int {
	t.Helper()

	_, thisFile, _, _ := runtime.Caller(0)
	csvPath := filepath.Join(filepath.Dir(thisFile), "..", "store", "testdata", csvFilename)

	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("SeedEDDNMessagesFromCSV: open %s: %v", csvPath, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("SeedEDDNMessagesFromCSV: parse CSV: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("SeedEDDNMessagesFromCSV: %s has no data rows", csvFilename)
	}

	header := records[0]
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[h] = i
	}
	for _, c := range []string{"received_at", "schema_ref", "system_name", "event_type", "message_data"} {
		if _, ok := col[c]; !ok {
			t.Fatalf("SeedEDDNMessagesFromCSV: CSV missing required column %q", c)
		}
	}

	ctx := context.Background()
	inserted := 0
	for lineNum, row := range records[1:] {
		receivedAt, err := parseTimestamp(strings.TrimSpace(row[col["received_at"]]))
		if err != nil {
			t.Logf("SeedEDDNMessagesFromCSV: skip line %d — bad received_at %q: %v",
				lineNum+2, row[col["received_at"]], err)
			continue
		}

		_, err = pool.Exec(ctx, `
			INSERT INTO feed.messages
				(received_at, schema_ref, software_name, system_name, event_type, message_data)
			VALUES ($1, $2, $3, $4, $5, $6)
		`,
			receivedAt,
			strings.TrimSpace(row[col["schema_ref"]]),
			nullableStr(row, col, "software_name"),
			nullableStr(row, col, "system_name"),
			nullableStr(row, col, "event_type"),
			row[col["message_data"]],
		)
		if err != nil {
			t.Fatalf("SeedEDDNMessagesFromCSV: insert line %d: %v", lineNum+2, err)
		}
		inserted++
	}

	if inserted == 0 {
		t.Fatalf("SeedEDDNMessagesFromCSV: inserted 0 rows from %s", csvFilename)
	}
	return inserted
}

// EDDNMessageRow holds fields for a single programmatically-inserted test row.
type EDDNMessageRow struct {
	ReceivedAt  time.Time
	SystemName  string
	EventType   string
	MessageData string // raw JSON string
}

// InsertEDDNMessages inserts a slice of EDDNMessageRow into feed.messages.
func InsertEDDNMessages(t *testing.T, pool *pgxpool.Pool, rows []EDDNMessageRow) {
	t.Helper()
	ctx := context.Background()
	for i, r := range rows {
		_, err := pool.Exec(ctx, `
			INSERT INTO feed.messages (received_at, schema_ref, system_name, event_type, message_data)
			VALUES ($1, $2, $3, $4, $5)
		`,
			r.ReceivedAt,
			"https://eddn.edcd.io/schemas/journal/1",
			r.SystemName, r.EventType, r.MessageData,
		)
		if err != nil {
			t.Fatalf("InsertEDDNMessages: row %d: %v", i, err)
		}
	}
}

// PowerplayMessageData returns a FSDJump JSON payload with the given powerplay fields.
func PowerplayMessageData(system, power, state string, reinforcement, undermining int64, eventTime time.Time) string {
	return fmt.Sprintf(
		`{"event":"FSDJump","timestamp":%q,"StarSystem":%q,"ControllingPower":%q,`+
			`"PowerplayState":%q,"PowerplayStateReinforcement":%d,"PowerplayStateUndermining":%d}`,
		eventTime.UTC().Format(time.RFC3339),
		system, power, state, reinforcement, undermining,
	)
}

func parseTimestamp(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05.999999+00",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05+00",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp format: %q", s)
}

func nullableStr(row []string, col map[string]int, name string) *string {
	i, ok := col[name]
	if !ok || i >= len(row) {
		return nil
	}
	s := strings.TrimSpace(row[i])
	if s == "" {
		return nil
	}
	return &s
}

// nullableFloat64 parses a CSV cell as float64, returning nil for empty.
func nullableFloat64(row []string, col map[string]int, name string) *float64 {
	i, ok := col[name]
	if !ok || i >= len(row) {
		return nil
	}
	s := strings.TrimSpace(row[i])
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
