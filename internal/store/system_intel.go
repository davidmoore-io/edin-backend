// Package store provides database access for system intel queries.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemIntelStore provides queries for system intelligence data from EDDN raw feed.
// All queries use the event timestamp from message_data, not received_at.
type SystemIntelStore struct {
	pool *pgxpool.Pool
}

// NewSystemIntelStore creates a new system intel store.
func NewSystemIntelStore(pool *pgxpool.Pool) *SystemIntelStore {
	return &SystemIntelStore{pool: pool}
}

// TrafficBucket represents aggregated traffic data for a time bucket.
type TrafficBucket struct {
	Bucket          time.Time `json:"bucket"`
	EventType       string    `json:"event_type"`
	EventCount      int64     `json:"event_count"`
	UniqueUploaders int64     `json:"unique_uploaders"`
}

// CarrierEvent represents a fleet carrier arrival or departure.
type CarrierEvent struct {
	CarrierID   string    `json:"carrier_id"`
	CarrierName string    `json:"carrier_name,omitempty"`
	EventTime   time.Time `json:"event_time"`
	Action      string    `json:"action"` // "arrival" or "departure"
	FromSystem  string    `json:"from_system,omitempty"`
	ToSystem    string    `json:"to_system,omitempty"`
}

// ActivityBucket represents commander activity for a day/hour.
type ActivityBucket struct {
	Day             string `json:"day"`
	Hour            int    `json:"hour"`
	UniqueUploaders int64  `json:"unique_uploaders"`
}

// MarketUpdate represents a market update event.
type MarketUpdate struct {
	StationName  string    `json:"station_name"`
	EventTime    time.Time `json:"event_time"`
	SoftwareName string    `json:"software_name,omitempty"`
}

// EDDNEvent represents a recent EDDN event with full details.
type EDDNEvent struct {
	ID           int64     `json:"id"`
	EventTime    time.Time `json:"event_time"`
	ReceivedAt   time.Time `json:"received_at"`
	EventType    string    `json:"event_type"`
	SoftwareName string    `json:"software_name,omitempty"`
	StationName  string    `json:"station_name,omitempty"`
	SchemaRef    string    `json:"schema_ref,omitempty"`
}

// EDDNEventFull represents a complete EDDN event with message data.
type EDDNEventFull struct {
	EDDNEvent
	MessageData json.RawMessage `json:"message_data"`
	HeaderData  json.RawMessage `json:"header_data,omitempty"`
	UploaderID  string          `json:"uploader_id,omitempty"`
}

// EventStats represents overall event statistics for a system.
type EventStats struct {
	TotalEvents     int64            `json:"total_events"`
	UniqueUploaders int64            `json:"unique_uploaders"`
	EventBreakdown  map[string]int64 `json:"event_breakdown"`
	SchemaBreakdown map[string]int64 `json:"schema_breakdown"`
	FirstEvent      *time.Time       `json:"first_event,omitempty"`
	LastEvent       *time.Time       `json:"last_event,omitempty"`
}

// EventsPage represents a paginated list of events.
type EventsPage struct {
	Events     []EDDNEvent `json:"events"`
	TotalCount int64       `json:"total_count"`
	Offset     int         `json:"offset"`
	Limit      int         `json:"limit"`
	HasMore    bool        `json:"has_more"`
}

// TrafficStats represents overall traffic statistics (legacy, maps to EventStats).
type TrafficStats = EventStats

// deriveEventType extracts a human-readable event type from schema_ref or event field.
// Schema refs like "https://eddn.edcd.io/schemas/commodity/3" become "Market Update"
func deriveEventType(schemaRef, eventField string) string {
	// If we have an explicit event field, use it
	if eventField != "" {
		return eventField
	}

	// Derive from schema ref
	schemaMap := map[string]string{
		"commodity":          "Market Update",
		"shipyard":           "Shipyard Update",
		"outfitting":         "Outfitting Update",
		"journal":            "Journal Event",
		"navroute":           "Nav Route",
		"navbeaconscan":      "Nav Beacon Scan",
		"fssdiscoveryscan":   "FSS Discovery",
		"fsssignaldiscovered": "FSS Signal",
		"fssallbodiesfound":  "FSS Complete",
		"codexentry":         "Codex Entry",
		"approachsettlement": "Settlement Approach",
		"fcmaterials":        "FC Materials",
	}

	for key, name := range schemaMap {
		if strings.Contains(strings.ToLower(schemaRef), key) {
			return name
		}
	}

	return "EDDN Event"
}

// buildTimeFilter returns SQL condition and parameter for time filtering.
// hours <= 0 means all time (no filter).
func buildTimeFilter(hours int, paramNum int) (string, bool) {
	if hours <= 0 {
		return "", false // No time filter
	}
	return fmt.Sprintf("AND received_at > NOW() - $%d * INTERVAL '1 hour'", paramNum), true
}

// GetEventStats returns overall event statistics for a system.
// hours <= 0 means all time.
func (s *SystemIntelStore) GetEventStats(ctx context.Context, systemName string, hours int) (*EventStats, error) {
	// Build time filter
	timeFilter, hasTimeParam := buildTimeFilter(hours, 2)

	// Get breakdown by event type (using derived event type)
	// Note: PostgreSQL requires the full expression in GROUP BY, not the alias
	query := fmt.Sprintf(`
		SELECT
			COALESCE(
				message_data->>'event',
				CASE
					WHEN schema_ref LIKE '%%commodity%%' THEN 'Market Update'
					WHEN schema_ref LIKE '%%shipyard%%' THEN 'Shipyard Update'
					WHEN schema_ref LIKE '%%outfitting%%' THEN 'Outfitting Update'
					WHEN schema_ref LIKE '%%fsssignaldiscovered%%' THEN 'FSS Signal'
					WHEN schema_ref LIKE '%%navbeaconscan%%' THEN 'Nav Beacon Scan'
					WHEN schema_ref LIKE '%%codexentry%%' THEN 'Codex Entry'
					ELSE 'EDDN Event'
				END
			) AS event_type,
			schema_ref,
			COUNT(*) AS event_count
		FROM feed.messages
		WHERE system_name = $1
		  %s
		GROUP BY COALESCE(
			message_data->>'event',
			CASE
				WHEN schema_ref LIKE '%%commodity%%' THEN 'Market Update'
				WHEN schema_ref LIKE '%%shipyard%%' THEN 'Shipyard Update'
				WHEN schema_ref LIKE '%%outfitting%%' THEN 'Outfitting Update'
				WHEN schema_ref LIKE '%%fsssignaldiscovered%%' THEN 'FSS Signal'
				WHEN schema_ref LIKE '%%navbeaconscan%%' THEN 'Nav Beacon Scan'
				WHEN schema_ref LIKE '%%codexentry%%' THEN 'Codex Entry'
				ELSE 'EDDN Event'
			END
		), schema_ref
		ORDER BY event_count DESC
	`, timeFilter)

	var rows interface{ Close(); Next() bool; Scan(...any) error; Err() error }
	var err error
	if hasTimeParam {
		rows, err = s.pool.Query(ctx, query, systemName, hours)
	} else {
		rows, err = s.pool.Query(ctx, query, systemName)
	}
	if err != nil {
		return nil, fmt.Errorf("query event stats: %w", err)
	}
	defer rows.Close()

	stats := &EventStats{
		EventBreakdown:  make(map[string]int64),
		SchemaBreakdown: make(map[string]int64),
	}

	for rows.Next() {
		var eventType, schemaRef string
		var count int64
		if err := rows.Scan(&eventType, &schemaRef, &count); err != nil {
			return nil, fmt.Errorf("scan event stat: %w", err)
		}
		stats.TotalEvents += count
		stats.EventBreakdown[eventType] += count

		// Extract schema name for breakdown
		parts := strings.Split(schemaRef, "/")
		if len(parts) >= 5 {
			schemaName := parts[4] // e.g., "commodity", "journal"
			stats.SchemaBreakdown[schemaName] += count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Get totals including unique uploaders and time range
	totalsQuery := fmt.Sprintf(`
		SELECT
			COUNT(DISTINCT uploader_id),
			MIN((message_data->>'timestamp')::timestamptz),
			MAX((message_data->>'timestamp')::timestamptz)
		FROM feed.messages
		WHERE system_name = $1
		  %s
	`, timeFilter)

	var uniqueUploaders int64
	var firstEvent, lastEvent *time.Time
	if hasTimeParam {
		err = s.pool.QueryRow(ctx, totalsQuery, systemName, hours).Scan(&uniqueUploaders, &firstEvent, &lastEvent)
	} else {
		err = s.pool.QueryRow(ctx, totalsQuery, systemName).Scan(&uniqueUploaders, &firstEvent, &lastEvent)
	}
	if err != nil {
		return nil, fmt.Errorf("query event totals: %w", err)
	}

	stats.UniqueUploaders = uniqueUploaders
	stats.FirstEvent = firstEvent
	stats.LastEvent = lastEvent

	return stats, nil
}

// GetTrafficTimeline returns traffic data bucketed by hour for a system.
// Uses the event timestamp from message_data, not received_at.
// hours <= 0 means all time.
func (s *SystemIntelStore) GetTrafficTimeline(ctx context.Context, systemName string, hours int) ([]TrafficBucket, error) {
	timeFilter, hasTimeParam := buildTimeFilter(hours, 2)

	query := fmt.Sprintf(`
		SELECT
			time_bucket('1 hour', (message_data->>'timestamp')::timestamptz) AS bucket,
			COALESCE(
				message_data->>'event',
				CASE
					WHEN schema_ref LIKE '%%commodity%%' THEN 'Market Update'
					WHEN schema_ref LIKE '%%shipyard%%' THEN 'Shipyard Update'
					WHEN schema_ref LIKE '%%outfitting%%' THEN 'Outfitting Update'
					WHEN schema_ref LIKE '%%fsssignaldiscovered%%' THEN 'FSS Signal'
					ELSE 'EDDN Event'
				END
			) AS event_type,
			COUNT(*) AS event_count,
			COUNT(DISTINCT uploader_id) AS unique_uploaders
		FROM feed.messages
		WHERE system_name = $1
		  %s
		  AND message_data->>'timestamp' IS NOT NULL
		GROUP BY bucket, event_type
		ORDER BY bucket DESC, event_count DESC
	`, timeFilter)

	var rows interface{ Close(); Next() bool; Scan(...any) error; Err() error }
	var err error
	if hasTimeParam {
		rows, err = s.pool.Query(ctx, query, systemName, hours)
	} else {
		rows, err = s.pool.Query(ctx, query, systemName)
	}
	if err != nil {
		return nil, fmt.Errorf("query traffic timeline: %w", err)
	}
	defer rows.Close()

	var buckets []TrafficBucket
	for rows.Next() {
		var b TrafficBucket
		if err := rows.Scan(&b.Bucket, &b.EventType, &b.EventCount, &b.UniqueUploaders); err != nil {
			return nil, fmt.Errorf("scan traffic bucket: %w", err)
		}
		buckets = append(buckets, b)
	}

	return buckets, rows.Err()
}

// GetTrafficStats returns overall traffic statistics for a system.
// Deprecated: Use GetEventStats instead.
func (s *SystemIntelStore) GetTrafficStats(ctx context.Context, systemName string, hours int) (*TrafficStats, error) {
	return s.GetEventStats(ctx, systemName, hours)
}

// GetFleetCarrierActivity returns fleet carrier arrivals and departures for a system.
// hours <= 0 means all time.
func (s *SystemIntelStore) GetFleetCarrierActivity(ctx context.Context, systemName string, hours int) ([]CarrierEvent, error) {
	timeFilter, hasTimeParam := buildTimeFilter(hours, 2)

	query := fmt.Sprintf(`
		SELECT
			COALESCE(message_data->>'CarrierID', 'Unknown') AS carrier_id,
			COALESCE(message_data->>'Name', message_data->>'CarrierID', 'Unknown') AS carrier_name,
			(message_data->>'timestamp')::timestamptz AS event_time,
			COALESCE(message_data->>'StarSystem', '') AS to_system
		FROM feed.messages
		WHERE schema_ref = 'https://eddn.edcd.io/schemas/journal/1'
		  AND message_data->>'event' = 'CarrierJump'
		  AND (message_data->>'StarSystem' = $1 OR system_name = $1)
		  %s
		  AND message_data->>'timestamp' IS NOT NULL
		  AND message_data->>'CarrierID' IS NOT NULL
		ORDER BY event_time DESC
		LIMIT 100
	`, timeFilter)

	var rows interface{ Close(); Next() bool; Scan(...any) error; Err() error }
	var err error
	if hasTimeParam {
		rows, err = s.pool.Query(ctx, query, systemName, hours)
	} else {
		rows, err = s.pool.Query(ctx, query, systemName)
	}
	if err != nil {
		return nil, fmt.Errorf("query carrier activity: %w", err)
	}
	defer rows.Close()

	var events []CarrierEvent
	for rows.Next() {
		var e CarrierEvent
		var toSystem string
		if err := rows.Scan(&e.CarrierID, &e.CarrierName, &e.EventTime, &toSystem); err != nil {
			return nil, fmt.Errorf("scan carrier event: %w", err)
		}

		// Determine if arrival or departure
		if toSystem == systemName {
			e.Action = "arrival"
			e.ToSystem = toSystem
		} else {
			e.Action = "departure"
			e.FromSystem = systemName
			e.ToSystem = toSystem
		}

		events = append(events, e)
	}

	return events, rows.Err()
}

// GetCommanderActivity returns a heatmap of commander activity by day/hour.
func (s *SystemIntelStore) GetCommanderActivity(ctx context.Context, systemName string, days int) ([]ActivityBucket, error) {
	if days <= 0 {
		days = 7
	}

	query := `
		SELECT
			TO_CHAR(DATE((message_data->>'timestamp')::timestamptz), 'YYYY-MM-DD') AS day,
			EXTRACT(HOUR FROM (message_data->>'timestamp')::timestamptz)::int AS hour,
			COUNT(DISTINCT uploader_id) AS unique_uploaders
		FROM feed.messages
		WHERE system_name = $1
		  AND received_at > NOW() - $2 * INTERVAL '1 day'
		  AND message_data->>'timestamp' IS NOT NULL
		GROUP BY day, hour
		ORDER BY day DESC, hour
	`

	rows, err := s.pool.Query(ctx, query, systemName, days)
	if err != nil {
		return nil, fmt.Errorf("query commander activity: %w", err)
	}
	defer rows.Close()

	var buckets []ActivityBucket
	for rows.Next() {
		var b ActivityBucket
		if err := rows.Scan(&b.Day, &b.Hour, &b.UniqueUploaders); err != nil {
			return nil, fmt.Errorf("scan activity bucket: %w", err)
		}
		buckets = append(buckets, b)
	}

	return buckets, rows.Err()
}

// GetMarketHistory returns market update events for stations in a system.
// hours <= 0 means all time.
func (s *SystemIntelStore) GetMarketHistory(ctx context.Context, systemName string, hours int) ([]MarketUpdate, error) {
	timeFilter, hasTimeParam := buildTimeFilter(hours, 2)

	query := fmt.Sprintf(`
		SELECT
			station_name,
			(message_data->>'timestamp')::timestamptz AS event_time,
			software_name
		FROM feed.messages
		WHERE system_name = $1
		  AND schema_ref = 'https://eddn.edcd.io/schemas/commodity/3'
		  %s
		  AND station_name IS NOT NULL
		  AND message_data->>'timestamp' IS NOT NULL
		ORDER BY event_time DESC
		LIMIT 100
	`, timeFilter)

	var rows interface{ Close(); Next() bool; Scan(...any) error; Err() error }
	var err error
	if hasTimeParam {
		rows, err = s.pool.Query(ctx, query, systemName, hours)
	} else {
		rows, err = s.pool.Query(ctx, query, systemName)
	}
	if err != nil {
		return nil, fmt.Errorf("query market history: %w", err)
	}
	defer rows.Close()

	var updates []MarketUpdate
	for rows.Next() {
		var u MarketUpdate
		if err := rows.Scan(&u.StationName, &u.EventTime, &u.SoftwareName); err != nil {
			return nil, fmt.Errorf("scan market update: %w", err)
		}
		updates = append(updates, u)
	}

	return updates, rows.Err()
}

// GetRecentEvents returns paginated EDDN events for a system.
// hours <= 0 means all time. Orders by event_time DESC.
func (s *SystemIntelStore) GetRecentEvents(ctx context.Context, systemName string, hours int, limit int, offset int) (*EventsPage, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	timeFilter, hasTimeParam := buildTimeFilter(hours, 2)

	// Get total count first
	countQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM feed.messages
		WHERE system_name = $1
		  %s
		  AND message_data->>'timestamp' IS NOT NULL
	`, timeFilter)

	var totalCount int64
	var err error
	if hasTimeParam {
		err = s.pool.QueryRow(ctx, countQuery, systemName, hours).Scan(&totalCount)
	} else {
		err = s.pool.QueryRow(ctx, countQuery, systemName).Scan(&totalCount)
	}
	if err != nil {
		return nil, fmt.Errorf("count events: %w", err)
	}

	// Get events with proper event type derivation
	query := fmt.Sprintf(`
		SELECT
			id,
			(message_data->>'timestamp')::timestamptz AS event_time,
			received_at,
			COALESCE(
				message_data->>'event',
				CASE
					WHEN schema_ref LIKE '%%commodity%%' THEN 'Market Update'
					WHEN schema_ref LIKE '%%shipyard%%' THEN 'Shipyard Update'
					WHEN schema_ref LIKE '%%outfitting%%' THEN 'Outfitting Update'
					WHEN schema_ref LIKE '%%fsssignaldiscovered%%' THEN 'FSS Signal'
					WHEN schema_ref LIKE '%%navbeaconscan%%' THEN 'Nav Beacon Scan'
					WHEN schema_ref LIKE '%%codexentry%%' THEN 'Codex Entry'
					ELSE 'EDDN Event'
				END
			) AS event_type,
			software_name,
			station_name,
			schema_ref
		FROM feed.messages
		WHERE system_name = $1
		  %s
		  AND message_data->>'timestamp' IS NOT NULL
		ORDER BY (message_data->>'timestamp')::timestamptz DESC
		LIMIT $%d OFFSET $%d
	`, timeFilter, func() int {
		if hasTimeParam {
			return 3
		}
		return 2
	}(), func() int {
		if hasTimeParam {
			return 4
		}
		return 3
	}())

	var rows interface{ Close(); Next() bool; Scan(...any) error; Err() error }
	if hasTimeParam {
		rows, err = s.pool.Query(ctx, query, systemName, hours, limit, offset)
	} else {
		rows, err = s.pool.Query(ctx, query, systemName, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("query recent events: %w", err)
	}
	defer rows.Close()

	var events []EDDNEvent
	for rows.Next() {
		var e EDDNEvent
		if err := rows.Scan(&e.ID, &e.EventTime, &e.ReceivedAt, &e.EventType, &e.SoftwareName, &e.StationName, &e.SchemaRef); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &EventsPage{
		Events:     events,
		TotalCount: totalCount,
		Offset:     offset,
		Limit:      limit,
		HasMore:    int64(offset+len(events)) < totalCount,
	}, nil
}

// GetEventByID returns a single event with full message data.
func (s *SystemIntelStore) GetEventByID(ctx context.Context, eventID int64) (*EDDNEventFull, error) {
	query := `
		SELECT
			id,
			(message_data->>'timestamp')::timestamptz AS event_time,
			received_at,
			COALESCE(
				message_data->>'event',
				CASE
					WHEN schema_ref LIKE '%commodity%' THEN 'Market Update'
					WHEN schema_ref LIKE '%shipyard%' THEN 'Shipyard Update'
					WHEN schema_ref LIKE '%outfitting%' THEN 'Outfitting Update'
					WHEN schema_ref LIKE '%fsssignaldiscovered%' THEN 'FSS Signal'
					WHEN schema_ref LIKE '%navbeaconscan%' THEN 'Nav Beacon Scan'
					WHEN schema_ref LIKE '%codexentry%' THEN 'Codex Entry'
					ELSE 'EDDN Event'
				END
			) AS event_type,
			software_name,
			station_name,
			schema_ref,
			message_data,
			header_data,
			uploader_id
		FROM feed.messages
		WHERE id = $1
	`

	var e EDDNEventFull
	err := s.pool.QueryRow(ctx, query, eventID).Scan(
		&e.ID, &e.EventTime, &e.ReceivedAt, &e.EventType,
		&e.SoftwareName, &e.StationName, &e.SchemaRef,
		&e.MessageData, &e.HeaderData, &e.UploaderID,
	)
	if err != nil {
		return nil, fmt.Errorf("get event by id: %w", err)
	}

	return &e, nil
}

// SoftwareStats represents software client distribution.
type SoftwareStats struct {
	SoftwareName string `json:"software_name"`
	EventCount   int64  `json:"event_count"`
}

// CommodityDataPoint represents a single commodity price/demand data point.
type CommodityDataPoint struct {
	EventTime     time.Time `json:"event_time"`
	StationName   string    `json:"station_name"`
	CommodityName string    `json:"commodity_name"`
	Demand        int64     `json:"demand"`
	Supply        int64     `json:"supply"`
	BuyPrice      int64     `json:"buy_price"`
	SellPrice     int64     `json:"sell_price"`
}

// CommodityHistory represents market history for charting.
type CommodityHistory struct {
	DataPoints  []CommodityDataPoint `json:"data_points"`
	Commodities []string             `json:"commodities"` // Unique commodity names found
}

// GetCommodityHistory returns commodity demand/supply history for charting.
// hours <= 0 means all time. Returns up to 5000 data points.
func (s *SystemIntelStore) GetCommodityHistory(ctx context.Context, systemName string, hours int) (*CommodityHistory, error) {
	timeFilter, hasTimeParam := buildTimeFilter(hours, 2)

	// Query commodity events and extract individual commodities from the JSON array
	// This uses jsonb_array_elements to unnest the commodities array
	// INITCAP converts lowercase names like "advancedcatalysers" to "Advancedcatalysers"
	// We also handle common commodity name formatting
	query := fmt.Sprintf(`
		SELECT
			(message_data->>'timestamp')::timestamptz AS event_time,
			station_name,
			INITCAP(comm->>'name') AS commodity_name,
			COALESCE((comm->>'demand')::bigint, 0) AS demand,
			COALESCE((comm->>'stock')::bigint, 0) AS supply,
			COALESCE((comm->>'buyPrice')::bigint, 0) AS buy_price,
			COALESCE((comm->>'sellPrice')::bigint, 0) AS sell_price
		FROM feed.messages,
		     jsonb_array_elements(message_data->'commodities') AS comm
		WHERE system_name = $1
		  AND schema_ref = 'https://eddn.edcd.io/schemas/commodity/3'
		  %s
		  AND message_data->>'timestamp' IS NOT NULL
		  AND station_name IS NOT NULL
		ORDER BY event_time DESC
		LIMIT 5000
	`, timeFilter)

	var rows interface{ Close(); Next() bool; Scan(...any) error; Err() error }
	var err error
	if hasTimeParam {
		rows, err = s.pool.Query(ctx, query, systemName, hours)
	} else {
		rows, err = s.pool.Query(ctx, query, systemName)
	}
	if err != nil {
		return nil, fmt.Errorf("query commodity history: %w", err)
	}
	defer rows.Close()

	commoditySet := make(map[string]bool)
	var dataPoints []CommodityDataPoint

	for rows.Next() {
		var dp CommodityDataPoint
		if err := rows.Scan(&dp.EventTime, &dp.StationName, &dp.CommodityName, &dp.Demand, &dp.Supply, &dp.BuyPrice, &dp.SellPrice); err != nil {
			return nil, fmt.Errorf("scan commodity data: %w", err)
		}
		dataPoints = append(dataPoints, dp)
		commoditySet[dp.CommodityName] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Extract unique commodity names
	commodities := make([]string, 0, len(commoditySet))
	for name := range commoditySet {
		commodities = append(commodities, name)
	}

	return &CommodityHistory{
		DataPoints:  dataPoints,
		Commodities: commodities,
	}, nil
}

// GetSoftwareStats returns the distribution of software clients for a system.
// hours <= 0 means all time.
func (s *SystemIntelStore) GetSoftwareStats(ctx context.Context, systemName string, hours int) ([]SoftwareStats, error) {
	timeFilter, hasTimeParam := buildTimeFilter(hours, 2)

	query := fmt.Sprintf(`
		SELECT
			COALESCE(software_name, 'Unknown') AS software_name,
			COUNT(*) AS event_count
		FROM feed.messages
		WHERE system_name = $1
		  %s
		GROUP BY software_name
		ORDER BY event_count DESC
		LIMIT 20
	`, timeFilter)

	var rows interface{ Close(); Next() bool; Scan(...any) error; Err() error }
	var err error
	if hasTimeParam {
		rows, err = s.pool.Query(ctx, query, systemName, hours)
	} else {
		rows, err = s.pool.Query(ctx, query, systemName)
	}
	if err != nil {
		return nil, fmt.Errorf("query software stats: %w", err)
	}
	defer rows.Close()

	var stats []SoftwareStats
	for rows.Next() {
		var s SoftwareStats
		if err := rows.Scan(&s.SoftwareName, &s.EventCount); err != nil {
			return nil, fmt.Errorf("scan software stat: %w", err)
		}
		stats = append(stats, s)
	}

	return stats, rows.Err()
}
