package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CacheStore provides read/write operations for EDIN powerplay cache tables.
type CacheStore struct {
	client     *Client // EDIN database (powerplay schema)
	eddnClient *Client // Raw EDDN feed database (feed schema)
}

// NewCacheStore creates a new cache store.
func NewCacheStore(client *Client) *CacheStore {
	return &CacheStore{client: client}
}

// SetEDDNClient sets the EDDN raw feed database client for historical queries.
func (s *CacheStore) SetEDDNClient(client *Client) {
	s.eddnClient = client
}

// SpanshSystemData represents cached Spansh data for a single system.
type SpanshSystemData struct {
	SystemName         string         `json:"system_name"`
	ScrapedAt          time.Time      `json:"scraped_at"`
	SpanshUpdatedAt    *time.Time     `json:"spansh_updated_at,omitempty"`
	ControllingPower   string         `json:"controlling_power,omitempty"`
	Powers             []string       `json:"powers,omitempty"`
	PowerState         string         `json:"power_state,omitempty"`
	ControlProgress    *float64       `json:"control_progress,omitempty"`
	Reinforcement      int64          `json:"reinforcement"`
	Undermining        int64          `json:"undermining"`
	ControllingFaction string         `json:"controlling_faction,omitempty"`
	FactionState       string         `json:"faction_state,omitempty"`
	Allegiance         string         `json:"allegiance,omitempty"`
	RawData            map[string]any `json:"raw_data,omitempty"`
}

// InaraLink represents a mapping from system name to Inara ID for direct links.
// This is the only data we need from Inara - all powerplay data comes from EDDN.
type InaraLink struct {
	SystemName string `json:"system_name"`
	InaraID    int    `json:"inara_id"`
}

// InaraPowerplayData represents Inara powerplay data stored in the cache.
// DEPRECATED: Inara scraping has been deprecated. This type is preserved for
// the legacy write path. Use InaraLink and GetAllInaraLinks() for reads.
type InaraPowerplayData struct {
	SystemName       string    `json:"system_name"`
	ScrapedAt        time.Time `json:"scraped_at"`
	InaraID          int       `json:"inara_id"`
	ControllingPower string    `json:"controlling_power"`
	PowerState       string    `json:"power_state"`
	ChangePct        float64   `json:"change_pct"`
	Pct              float64   `json:"pct"` // For expansion systems
	Undermining      int64     `json:"undermining"`
	UnderminingBy    *string   `json:"undermining_by,omitempty"`
	Reinforcement    int64     `json:"reinforcement"`
	ReinforcementBy  *string   `json:"reinforcement_by,omitempty"`
}

// WriteSpanshData writes Spansh system data to the database.
// Uses batch insert for efficiency (50 systems = 1 round trip).
func (s *CacheStore) WriteSpanshData(ctx context.Context, systems map[string]map[string]any) error {
	if s.client == nil || s.client.pool == nil || len(systems) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	now := time.Now().UTC()

	for systemName, data := range systems {
		// Skip error entries
		if _, hasErr := data["error"]; hasErr {
			continue
		}

		rawJSON, err := json.Marshal(data)
		if err != nil {
			s.client.log("failed to marshal spansh data for %s: %v", systemName, err)
			continue
		}

		// Parse spansh_updated_at if present
		var spanshUpdatedAt *time.Time
		if updatedStr, ok := data["updated_at"].(string); ok && updatedStr != "" {
			if t, err := time.Parse(time.RFC3339, updatedStr); err == nil {
				spanshUpdatedAt = &t
			}
		}

		// Extract powers as JSONB
		var powersJSON []byte
		if powers, ok := data["powers"]; ok && powers != nil {
			powersJSON, _ = json.Marshal(powers)
		}

		// Extract numeric values (JSON decodes as float64)
		reinforcement := int64(getFloat64(data, "reinforcement"))
		undermining := int64(getFloat64(data, "undermining"))
		var controlProgress *float64
		if cp, ok := data["control_progress"].(float64); ok {
			controlProgress = &cp
		}

		batch.Queue(`
			INSERT INTO spansh_cache 
			(scraped_at, system_name, spansh_updated_at, controlling_power, powers, power_state, 
			 control_progress, reinforcement, undermining, controlling_faction, faction_state, allegiance, raw_data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		`,
			now,
			systemName,
			spanshUpdatedAt,
			getStr(data, "controlling_power"),
			powersJSON,
			getStr(data, "power_state"),
			controlProgress,
			reinforcement,
			undermining,
			getStr(data, "controlling_faction"),
			getStr(data, "controlling_faction_state"),
			getStr(data, "allegiance"),
			rawJSON,
		)
	}

	if batch.Len() == 0 {
		return nil
	}

	br := s.client.pool.SendBatch(ctx, batch)
	defer br.Close()

	// Execute all queued statements
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			s.client.log("batch insert error at index %d: %v", i, err)
		}
	}

	s.client.log("wrote %d Spansh system records to database", batch.Len())
	return nil
}

// WriteInaraPowerplay writes Inara powerplay data to the database.
// NOTE: Inara scraping has been deprecated - this method is preserved for backward compatibility.
func (s *CacheStore) WriteInaraPowerplay(ctx context.Context, data map[string]*InaraPowerplayData) error {
	if s.client == nil || s.client.pool == nil || len(data) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	now := time.Now().UTC()

	for systemName, pp := range data {
		if pp == nil {
			continue
		}

		rawJSON, _ := json.Marshal(pp)

		batch.Queue(`
			INSERT INTO inara_cache
			(scraped_at, system_name, inara_id, controlling_power, power_state, change_pct,
			 undermining, undermining_by, reinforcement, reinforcement_by, raw_data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`,
			now,
			systemName,
			pp.InaraID,
			pp.ControllingPower,
			pp.PowerState,
			pp.ChangePct,
			pp.Undermining,
			pp.UnderminingBy,
			pp.Reinforcement,
			pp.ReinforcementBy,
			rawJSON,
		)
	}

	if batch.Len() == 0 {
		return nil
	}

	br := s.client.pool.SendBatch(ctx, batch)
	defer br.Close()

	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			s.client.log("inara batch insert error at index %d: %v", i, err)
		}
	}

	s.client.log("wrote %d Inara powerplay records to database", batch.Len())
	return nil
}

// GetLatestSpanshData returns the most recent Spansh data for each system.
func (s *CacheStore) GetLatestSpanshData(ctx context.Context, systems []string) (map[string]map[string]any, time.Time, error) {
	result := make(map[string]map[string]any)
	var latestTime time.Time

	if s.client == nil || s.client.pool == nil || len(systems) == 0 {
		return result, latestTime, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT DISTINCT ON (system_name) 
			system_name, scraped_at, raw_data
		FROM spansh_cache
		WHERE system_name = ANY($1)
		ORDER BY system_name, scraped_at DESC
	`, systems)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("query spansh cache: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var systemName string
		var scrapedAt time.Time
		var rawData []byte

		if err := rows.Scan(&systemName, &scrapedAt, &rawData); err != nil {
			s.client.log("scan error: %v", err)
			continue
		}

		var data map[string]any
		if err := json.Unmarshal(rawData, &data); err != nil {
			s.client.log("unmarshal error for %s: %v", systemName, err)
			continue
		}

		result[systemName] = data
		if scrapedAt.After(latestTime) {
			latestTime = scrapedAt
		}
	}

	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("rows iteration: %w", err)
	}

	return result, latestTime, nil
}

// GetLatestInaraPowerplay returns the most recent Inara data for each system.
func (s *CacheStore) GetLatestInaraPowerplay(ctx context.Context, systems []string) (map[string]*InaraPowerplayData, time.Time, error) {
	result := make(map[string]*InaraPowerplayData)
	var latestTime time.Time

	if s.client == nil || s.client.pool == nil || len(systems) == 0 {
		return result, latestTime, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT DISTINCT ON (system_name)
			system_name, scraped_at, inara_id, controlling_power, power_state, change_pct,
			undermining, undermining_by, reinforcement, reinforcement_by
		FROM inara_cache
		WHERE system_name = ANY($1)
		ORDER BY system_name, scraped_at DESC
	`, systems)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("query inara cache: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var systemName string
		var scrapedAt time.Time
		var inaraID int
		var controllingPower, powerState string
		var changePct float64
		var undermining, reinforcement int64
		var underminingBy, reinforcementBy *string

		if err := rows.Scan(
			&systemName,
			&scrapedAt,
			&inaraID,
			&controllingPower,
			&powerState,
			&changePct,
			&undermining,
			&underminingBy,
			&reinforcement,
			&reinforcementBy,
		); err != nil {
			s.client.log("scan error: %v", err)
			continue
		}

		pp := &InaraPowerplayData{
			SystemName:       systemName,
			ScrapedAt:        scrapedAt,
			InaraID:          inaraID,
			ControllingPower: controllingPower,
			PowerState:       powerState,
			ChangePct:        changePct,
			Undermining:      undermining,
			Reinforcement:    reinforcement,
			UnderminingBy:    underminingBy,
			ReinforcementBy:  reinforcementBy,
		}
		result[systemName] = pp

		if scrapedAt.After(latestTime) {
			latestTime = scrapedAt
		}
	}

	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("rows iteration: %w", err)
	}

	return result, latestTime, nil
}

// GetLastRefreshTime returns when data was last written for a source.
func (s *CacheStore) GetLastRefreshTime(ctx context.Context, source string) (time.Time, error) {
	if s.client == nil || s.client.pool == nil {
		return time.Time{}, nil
	}

	var query string
	switch source {
	case "ssg-eddn", "eddn":
		// Use new current_state table for SSG-EDDN data
		query = `SELECT COALESCE(MAX(last_updated), '1970-01-01') FROM current_state`
	case "spansh":
		// Legacy: spansh_cache table
		query = `SELECT COALESCE(MAX(scraped_at), '1970-01-01') FROM spansh_cache`
	case "inara":
		query = `SELECT COALESCE(MAX(scraped_at), '1970-01-01') FROM inara_cache`
	default:
		return time.Time{}, fmt.Errorf("unknown source: %s", source)
	}

	var lastTime time.Time
	err := s.client.pool.QueryRow(ctx, query).Scan(&lastTime)
	if err != nil {
		return time.Time{}, err
	}

	return lastTime, nil
}

// HasData returns true if there's any data in the cache tables.
func (s *CacheStore) HasData(ctx context.Context) (spanshReady, inaraReady bool) {
	if s.client == nil || s.client.pool == nil {
		return false, false
	}

	var spanshCount, inaraCount int
	_ = s.client.pool.QueryRow(ctx, `SELECT COUNT(*) FROM spansh_cache LIMIT 1`).Scan(&spanshCount)
	_ = s.client.pool.QueryRow(ctx, `SELECT COUNT(*) FROM inara_cache LIMIT 1`).Scan(&inaraCount)

	return spanshCount > 0, inaraCount > 0
}

// GetAllLatestSpanshData returns the most recent Spansh data for ALL systems.
func (s *CacheStore) GetAllLatestSpanshData(ctx context.Context) ([]map[string]any, time.Time, error) {
	var result []map[string]any
	var latestTime time.Time

	if s.client == nil || s.client.pool == nil {
		return result, latestTime, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT DISTINCT ON (system_name) 
			system_name, scraped_at, raw_data
		FROM spansh_cache
		ORDER BY system_name, scraped_at DESC
	`)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("query spansh cache: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var systemName string
		var scrapedAt time.Time
		var rawData []byte

		if err := rows.Scan(&systemName, &scrapedAt, &rawData); err != nil {
			continue
		}

		var data map[string]any
		if err := json.Unmarshal(rawData, &data); err != nil {
			continue
		}

		// Add metadata
		data["system_name"] = systemName
		data["scraped_at"] = scrapedAt.Format(time.RFC3339)
		result = append(result, data)

		if scrapedAt.After(latestTime) {
			latestTime = scrapedAt
		}
	}

	return result, latestTime, rows.Err()
}

// GetInaraSystemIDs returns a map of system names to their Inara IDs from the cache.
// This is used to pre-populate the scraper's system ID cache on startup.
func (s *CacheStore) GetInaraSystemIDs(ctx context.Context) (map[string]int, error) {
	result := make(map[string]int)

	if s.client == nil || s.client.pool == nil {
		return result, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT DISTINCT system_name, inara_id 
		FROM inara_cache 
		WHERE inara_id IS NOT NULL AND inara_id > 0
	`)
	if err != nil {
		return nil, fmt.Errorf("query inara system IDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var systemName string
		var inaraID int
		if err := rows.Scan(&systemName, &inaraID); err != nil {
			continue
		}
		result[systemName] = inaraID
	}

	return result, rows.Err()
}

// GetAllLatestInaraPowerplay returns the most recent Inara data for ALL systems.
func (s *CacheStore) GetAllLatestInaraPowerplay(ctx context.Context) ([]*InaraPowerplayData, time.Time, error) {
	var result []*InaraPowerplayData
	var latestTime time.Time

	if s.client == nil || s.client.pool == nil {
		return result, latestTime, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT DISTINCT ON (system_name)
			system_name, scraped_at, inara_id, controlling_power, power_state, change_pct,
			undermining, undermining_by, reinforcement, reinforcement_by
		FROM inara_cache
		ORDER BY system_name, scraped_at DESC
	`)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("query inara cache: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var systemName string
		var scrapedAt time.Time
		var inaraID int
		var controllingPower, powerState string
		var changePct float64
		var undermining, reinforcement int64
		var underminingBy, reinforcementBy *string

		if err := rows.Scan(
			&systemName, &scrapedAt, &inaraID, &controllingPower, &powerState, &changePct,
			&undermining, &underminingBy, &reinforcement, &reinforcementBy,
		); err != nil {
			continue
		}

		pp := &InaraPowerplayData{
			SystemName:       systemName,
			ScrapedAt:        scrapedAt,
			InaraID:          inaraID,
			ControllingPower: controllingPower,
			PowerState:       powerState,
			ChangePct:        changePct,
			Undermining:      undermining,
			Reinforcement:    reinforcement,
			UnderminingBy:    underminingBy,
			ReinforcementBy:  reinforcementBy,
		}
		result = append(result, pp)

		if scrapedAt.After(latestTime) {
			latestTime = scrapedAt
		}
	}

	return result, latestTime, rows.Err()
}

// GetAllInaraLinks returns Inara IDs for all systems in the cache.
// This is a lightweight query that only fetches the data needed for Inara direct links.
func (s *CacheStore) GetAllInaraLinks(ctx context.Context) ([]*InaraLink, error) {
	var result []*InaraLink

	if s.client == nil || s.client.pool == nil {
		return result, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT DISTINCT ON (system_name)
			system_name, inara_id
		FROM inara_cache
		WHERE inara_id > 0
		ORDER BY system_name, scraped_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query inara links: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var link InaraLink
		if err := rows.Scan(&link.SystemName, &link.InaraID); err != nil {
			continue
		}
		result = append(result, &link)
	}

	return result, rows.Err()
}

// SystemHistoryEntry represents a single historical data point.
type SystemHistoryEntry struct {
	Timestamp        time.Time `json:"timestamp"`
	Reinforcement    int64     `json:"reinforcement"`
	Undermining      int64     `json:"undermining"`
	PowerplayState   string    `json:"powerplay_state,omitempty"`
	ControllingPower string    `json:"controlling_power,omitempty"`
	Source           string    `json:"source"` // Software that submitted the data
}

// CurrentSystemState represents the current state of a system from EDDN.
type CurrentSystemState struct {
	SystemName       string    `json:"system_name"`
	ControllingPower *string   `json:"controlling_power,omitempty"`
	Powers           []string  `json:"powers,omitempty"`
	PowerState       string    `json:"power_state"`
	IsExpansion      bool      `json:"is_expansion"`
	ControlProgress  *float64  `json:"control_progress,omitempty"`
	Reinforcement    int64     `json:"reinforcement"`
	Undermining      int64     `json:"undermining"`
	LastUpdated      time.Time `json:"last_updated"`
	UpdateCount      int64     `json:"update_count"`
	Source           string    `json:"source"` // Always "ssg-eddn" for this table
}

// GetSystemHistory returns historical data for a specific system within a time window.
// Queries the raw EDDN feed for FSDJump events with powerplay data.
func (s *CacheStore) GetSystemHistory(ctx context.Context, systemName string, hours int) ([]SystemHistoryEntry, error) {
	var result []SystemHistoryEntry

	// Use EDDN raw client if available, otherwise return empty
	if s.eddnClient == nil || s.eddnClient.pool == nil {
		return result, nil
	}

	if hours <= 0 {
		hours = 24
	}
	// Cap at 30 days (raw feed has ~60 day retention)
	if hours > 720 {
		hours = 720
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	// Query raw EDDN feed for FSDJump events with powerplay data
	// Extract reinforcement and undermining from message_data JSON
	// Use the event timestamp from message_data, not received_at (when EDDN got it)
	// This handles late journal uploads correctly
	rows, err := s.eddnClient.pool.Query(ctx, `
		SELECT
			(message_data->>'timestamp')::timestamptz as event_time,
			COALESCE((message_data->>'PowerplayStateReinforcement')::bigint, 0) as reinforcement,
			COALESCE((message_data->>'PowerplayStateUndermining')::bigint, 0) as undermining,
			COALESCE(message_data->>'PowerplayState', '') as powerplay_state,
			COALESCE(message_data->>'ControllingPower', '') as controlling_power,
			COALESCE(software_name, 'unknown') as source
		FROM feed.messages
		WHERE system_name = $1
			AND event_type = 'FSDJump'
			AND message_data->>'PowerplayState' IS NOT NULL
			AND (message_data->>'timestamp')::timestamptz >= $2
		ORDER BY (message_data->>'timestamp')::timestamptz ASC
	`, systemName, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query raw EDDN feed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var timestamp time.Time
		var reinforcement, undermining int64
		var powerplayState, controllingPower, source string
		if err := rows.Scan(&timestamp, &reinforcement, &undermining, &powerplayState, &controllingPower, &source); err == nil {
			result = append(result, SystemHistoryEntry{
				Timestamp:        timestamp,
				Reinforcement:    reinforcement,
				Undermining:      undermining,
				PowerplayState:   powerplayState,
				ControllingPower: controllingPower,
				Source:           source,
			})
		}
	}

	return result, rows.Err()
}

// ExpansionHistoryEntry represents a single historical data point for expansion conflict progress.
type ExpansionHistoryEntry struct {
	Timestamp        time.Time          `json:"timestamp"`
	ConflictProgress map[string]float64 `json:"conflict_progress"` // Power name -> progress (0.0-1.0+)
}

// GetExpansionHistory returns historical conflict progress data for expansion systems.
// Queries the raw EDDN feed for FSDJump events with PowerplayConflictProgress data.
func (s *CacheStore) GetExpansionHistory(ctx context.Context, systemName string, hours int) ([]ExpansionHistoryEntry, error) {
	var result []ExpansionHistoryEntry

	// Use EDDN raw client if available, otherwise return empty
	if s.eddnClient == nil || s.eddnClient.pool == nil {
		return result, nil
	}

	if hours <= 0 {
		hours = 24
	}
	// Cap at 30 days (raw feed has ~60 day retention)
	if hours > 720 {
		hours = 720
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	// Query raw EDDN feed for FSDJump events with PowerplayConflictProgress
	// The field contains an array like: [{"Power": "A. Lavigny-Duval", "ConflictProgress": 0.906925}, ...]
	rows, err := s.eddnClient.pool.Query(ctx, `
		SELECT
			(message_data->>'timestamp')::timestamptz as event_time,
			message_data->'PowerplayConflictProgress' as conflict_progress
		FROM feed.messages
		WHERE system_name = $1
			AND event_type = 'FSDJump'
			AND message_data->'PowerplayConflictProgress' IS NOT NULL
			AND jsonb_array_length(message_data->'PowerplayConflictProgress') > 0
			AND (message_data->>'timestamp')::timestamptz >= $2
		ORDER BY (message_data->>'timestamp')::timestamptz ASC
	`, systemName, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query raw EDDN feed for expansion history: %w", err)
	}
	defer rows.Close()

	type conflictEntry struct {
		Power            string  `json:"Power"`
		ConflictProgress float64 `json:"ConflictProgress"`
	}

	for rows.Next() {
		var timestamp time.Time
		var progressJSON []byte
		if err := rows.Scan(&timestamp, &progressJSON); err != nil {
			continue
		}

		// Parse the JSON array
		var entries []conflictEntry
		if err := json.Unmarshal(progressJSON, &entries); err != nil {
			continue
		}

		// Convert to map for easier frontend consumption
		progressMap := make(map[string]float64)
		for _, e := range entries {
			progressMap[e.Power] = e.ConflictProgress
		}

		if len(progressMap) > 0 {
			result = append(result, ExpansionHistoryEntry{
				Timestamp:        timestamp,
				ConflictProgress: progressMap,
			})
		}
	}

	return result, rows.Err()
}

// GetAllCurrentState returns current state for all systems from the current_state table.
// This is the primary data source for the Thunderdome frontend, powered by real-time EDDN.
func (s *CacheStore) GetAllCurrentState(ctx context.Context) ([]*CurrentSystemState, time.Time, error) {
	var result []*CurrentSystemState
	var latestTime time.Time

	if s.client == nil || s.client.pool == nil {
		return result, latestTime, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT 
			system_name, controlling_power, powers, power_state, is_expansion,
			control_progress, reinforcement, undermining, last_updated, update_count
		FROM current_state
		ORDER BY system_name
	`)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("query current_state: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		state := &CurrentSystemState{Source: "ssg-eddn"}
		var powers []string

		if err := rows.Scan(
			&state.SystemName,
			&state.ControllingPower,
			&powers,
			&state.PowerState,
			&state.IsExpansion,
			&state.ControlProgress,
			&state.Reinforcement,
			&state.Undermining,
			&state.LastUpdated,
			&state.UpdateCount,
		); err != nil {
			s.client.log("scan current_state error: %v", err)
			continue
		}

		state.Powers = powers
		result = append(result, state)

		if state.LastUpdated.After(latestTime) {
			latestTime = state.LastUpdated
		}
	}

	return result, latestTime, rows.Err()
}

// GetCurrentStateForSystems returns current state data for a specific set of systems.
func (s *CacheStore) GetCurrentStateForSystems(ctx context.Context, systems []string) ([]*CurrentSystemState, time.Time, error) {
	var result []*CurrentSystemState
	var latestTime time.Time

	if s.client == nil || s.client.pool == nil || len(systems) == 0 {
		return result, latestTime, nil
	}

	rows, err := s.client.pool.Query(ctx, `
		SELECT 
			system_name, controlling_power, powers, power_state, is_expansion,
			control_progress, reinforcement, undermining, last_updated, update_count
		FROM current_state
		WHERE system_name = ANY($1)
		ORDER BY system_name
	`, systems)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("query current_state for systems: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		state := &CurrentSystemState{Source: "ssg-eddn"}
		var powers []string

		if err := rows.Scan(
			&state.SystemName,
			&state.ControllingPower,
			&powers,
			&state.PowerState,
			&state.IsExpansion,
			&state.ControlProgress,
			&state.Reinforcement,
			&state.Undermining,
			&state.LastUpdated,
			&state.UpdateCount,
		); err != nil {
			s.client.log("scan current_state error: %v", err)
			continue
		}

		state.Powers = powers
		result = append(result, state)

		if state.LastUpdated.After(latestTime) {
			latestTime = state.LastUpdated
		}
	}

	return result, latestTime, rows.Err()
}

// GetCurrentStateCount returns the number of systems in current_state.
func (s *CacheStore) GetCurrentStateCount(ctx context.Context) (int, error) {
	if s.client == nil || s.client.pool == nil {
		return 0, nil
	}

	var count int
	err := s.client.pool.QueryRow(ctx, `SELECT COUNT(*) FROM current_state`).Scan(&count)
	return count, err
}

// CacheStatus holds status information about the cache.
type CacheStatus struct {
	// EDDN native data (primary source)
	EDDNLastRefresh  time.Time `json:"eddn_last_refresh"`
	EDDNSystemCount  int       `json:"eddn_system_count"`
	EDDNTotalUpdates int64     `json:"eddn_total_updates"`
	EDDNHistoryCount int64     `json:"eddn_history_count"`

	// Inara supplemental data (for attribution)
	InaraLastRefresh time.Time `json:"inara_last_refresh"`
	InaraSystemCount int       `json:"inara_system_count"`

	// Legacy (deprecated, will be removed)
	SpanshLastRefresh time.Time `json:"spansh_last_refresh,omitempty"`
	SpanshSystemCount int       `json:"spansh_system_count,omitempty"`
}

// GetStatus returns cache status information.
func (s *CacheStore) GetStatus(ctx context.Context) (*CacheStatus, error) {
	status := &CacheStatus{}

	if s.client == nil || s.client.pool == nil {
		return status, nil
	}

	// EDDN native data (current_state + system_history)
	_ = s.client.pool.QueryRow(ctx, `SELECT COALESCE(MAX(last_updated), '1970-01-01') FROM current_state`).Scan(&status.EDDNLastRefresh)
	_ = s.client.pool.QueryRow(ctx, `SELECT COUNT(*) FROM current_state`).Scan(&status.EDDNSystemCount)
	_ = s.client.pool.QueryRow(ctx, `SELECT COALESCE(SUM(update_count), 0) FROM current_state`).Scan(&status.EDDNTotalUpdates)
	_ = s.client.pool.QueryRow(ctx, `SELECT COUNT(*) FROM system_history`).Scan(&status.EDDNHistoryCount)

	// Inara supplemental data
	_ = s.client.pool.QueryRow(ctx, `SELECT COALESCE(MAX(scraped_at), '1970-01-01') FROM inara_cache`).Scan(&status.InaraLastRefresh)
	_ = s.client.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT system_name) FROM inara_cache`).Scan(&status.InaraSystemCount)

	// Legacy spansh (still populated by EDDN listener for backward compatibility)
	_ = s.client.pool.QueryRow(ctx, `SELECT COALESCE(MAX(scraped_at), '1970-01-01') FROM spansh_cache`).Scan(&status.SpanshLastRefresh)
	_ = s.client.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT system_name) FROM spansh_cache`).Scan(&status.SpanshSystemCount)

	return status, nil
}

// PowerplayHistoryEntry represents a daily snapshot of powerplay activity.
type PowerplayHistoryEntry struct {
	Date             time.Time `json:"date"`
	Reinforcement    int64     `json:"reinforcement"`
	Undermining      int64     `json:"undermining"`
	ControllingPower string    `json:"controlling_power,omitempty"`
	PowerplayState   string    `json:"powerplay_state,omitempty"`
	ControlProgress  *float64  `json:"control_progress,omitempty"`
	ObservationCount int       `json:"observations"`
}

// SystemPowerplayHistory holds historical powerplay data for a system.
type SystemPowerplayHistory struct {
	SystemName string                  `json:"system_name"`
	History    []PowerplayHistoryEntry `json:"history"`
	DaysSpan   int                     `json:"days_span"`
}

// GetPowerplayHistory retrieves compressed daily powerplay history for one or more systems.
// Returns the max reinforcement/undermining values seen each day (to capture peak activity).
func (s *CacheStore) GetPowerplayHistory(ctx context.Context, systemNames []string, days int) ([]SystemPowerplayHistory, error) {
	if s.eddnClient == nil || s.eddnClient.pool == nil {
		return nil, fmt.Errorf("EDDN raw database not configured")
	}

	if len(systemNames) == 0 {
		return nil, fmt.Errorf("at least one system name required")
	}

	if days <= 0 || days > 30 {
		days = 14 // Default to 2 weeks
	}

	// Query for daily aggregates with peak values per day
	query := `
		WITH daily_data AS (
			SELECT
				system_name,
				DATE(message_data->>'timestamp') AS day,
				MAX((message_data->>'PowerplayStateReinforcement')::bigint) AS reinforcement,
				MAX((message_data->>'PowerplayStateUndermining')::bigint) AS undermining,
				(array_agg(message_data->>'ControllingPower' ORDER BY message_data->>'timestamp' DESC))[1] AS controlling_power,
				(array_agg(message_data->>'PowerplayState' ORDER BY message_data->>'timestamp' DESC))[1] AS powerplay_state,
				(array_agg((message_data->>'PowerplayStateControlProgress')::float ORDER BY message_data->>'timestamp' DESC))[1] AS control_progress,
				COUNT(*) AS observations
			FROM feed.messages
			WHERE schema_ref = 'https://eddn.edcd.io/schemas/journal/1'
			  AND message_data->>'event' = 'FSDJump'
			  AND system_name = ANY($1)
			  AND message_data->>'ControllingPower' IS NOT NULL
			  AND received_at >= NOW() - INTERVAL '1 day' * $2
			GROUP BY system_name, DATE(message_data->>'timestamp')
		)
		SELECT system_name, day, reinforcement, undermining, controlling_power, powerplay_state, control_progress, observations
		FROM daily_data
		ORDER BY system_name, day DESC
	`

	rows, err := s.eddnClient.pool.Query(ctx, query, systemNames, days)
	if err != nil {
		return nil, fmt.Errorf("query powerplay history: %w", err)
	}
	defer rows.Close()

	// Group results by system
	systemData := make(map[string][]PowerplayHistoryEntry)
	for rows.Next() {
		var (
			systemName       string
			day              time.Time
			reinforcement    int64
			undermining      int64
			controllingPower *string
			powerplayState   *string
			controlProgress  *float64
			observations     int
		)

		if err := rows.Scan(&systemName, &day, &reinforcement, &undermining, &controllingPower, &powerplayState, &controlProgress, &observations); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		entry := PowerplayHistoryEntry{
			Date:             day,
			Reinforcement:    reinforcement,
			Undermining:      undermining,
			ControlProgress:  controlProgress,
			ObservationCount: observations,
		}
		if controllingPower != nil {
			entry.ControllingPower = *controllingPower
		}
		if powerplayState != nil {
			entry.PowerplayState = *powerplayState
		}

		systemData[systemName] = append(systemData[systemName], entry)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	// Build result preserving input order
	result := make([]SystemPowerplayHistory, 0, len(systemData))
	for _, name := range systemNames {
		history, found := systemData[name]
		if !found {
			history = []PowerplayHistoryEntry{}
		}
		result = append(result, SystemPowerplayHistory{
			SystemName: name,
			History:    history,
			DaysSpan:   days,
		})
	}

	return result, nil
}

// Helper functions

func getStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getFloat64(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	if v, ok := m[key].(int); ok {
		return float64(v)
	}
	if v, ok := m[key].(int64); ok {
		return float64(v)
	}
	return 0
}
