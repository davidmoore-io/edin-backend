package store

import (
	"context"
	"fmt"
	"time"
)

// PowerplayCycle represents a weekly powerplay cycle bounded by Thursday 07:00 UTC ticks.
type PowerplayCycle struct {
	CycleNumber int       `json:"cycle_number"` // 0 = current, -1 = previous, etc.
	StartTime   time.Time `json:"start_time"`   // Thursday 07:00 UTC
	EndTime     time.Time `json:"end_time"`     // Following Thursday 07:00 UTC (or now for current)
	IsCurrent   bool      `json:"is_current"`
}

// TickTime is the weekly powerplay tick: Thursday 07:00 UTC
const (
	TickDayOfWeek = time.Thursday
	TickHourUTC   = 7
	TickMinuteUTC = 0
)

// GetCurrentCycleStart returns the start time of the current powerplay cycle.
// The cycle starts every Thursday at 07:00 UTC.
func GetCurrentCycleStart(now time.Time) time.Time {
	// Convert to UTC for calculations
	now = now.UTC()

	// Find the most recent Thursday 07:00 UTC
	tickTime := time.Date(now.Year(), now.Month(), now.Day(), TickHourUTC, TickMinuteUTC, 0, 0, time.UTC)

	// Adjust to the most recent Thursday
	daysSinceThursday := int(now.Weekday()) - int(TickDayOfWeek)
	if daysSinceThursday < 0 {
		daysSinceThursday += 7
	}

	// If today is Thursday but before the tick time, go back to last Thursday
	if daysSinceThursday == 0 && now.Before(tickTime) {
		daysSinceThursday = 7
	}

	return tickTime.AddDate(0, 0, -daysSinceThursday)
}

// GetCycleBoundaries returns the start and end times for a given cycle offset.
// offset=0 means current cycle, offset=-1 means previous cycle, etc.
func GetCycleBoundaries(now time.Time, offset int) PowerplayCycle {
	currentStart := GetCurrentCycleStart(now)

	// Apply offset (negative = past cycles)
	cycleStart := currentStart.AddDate(0, 0, offset*7)
	cycleEnd := cycleStart.AddDate(0, 0, 7)

	isCurrent := offset == 0

	// For current cycle, end time is now (not next Thursday)
	if isCurrent {
		cycleEnd = now.UTC()
	}

	return PowerplayCycle{
		CycleNumber: offset,
		StartTime:   cycleStart,
		EndTime:     cycleEnd,
		IsCurrent:   isCurrent,
	}
}

// GetCycleForTime determines which cycle a given timestamp belongs to.
// Returns the cycle number (0 = current, -1 = last week, -2 = two weeks ago, etc.)
func GetCycleForTime(eventTime time.Time, now time.Time) int {
	currentStart := GetCurrentCycleStart(now)
	eventTime = eventTime.UTC()

	if eventTime.After(currentStart) || eventTime.Equal(currentStart) {
		return 0 // Current cycle
	}

	// Calculate how many weeks ago
	diff := currentStart.Sub(eventTime)
	weeksAgo := int(diff.Hours()/24/7) + 1

	return -weeksAgo
}

// IsInMaintenanceWindow checks if a timestamp falls within the unreliable
// maintenance window (Thursday 07:00-08:30 UTC).
func IsInMaintenanceWindow(t time.Time) bool {
	t = t.UTC()

	if t.Weekday() != TickDayOfWeek {
		return false
	}

	hour := t.Hour()
	minute := t.Minute()

	// Maintenance window: 07:00 - 08:30 UTC
	startMinutes := TickHourUTC*60 + TickMinuteUTC // 420
	endMinutes := 8*60 + 30                        // 510
	currentMinutes := hour*60 + minute

	return currentMinutes >= startMinutes && currentMinutes <= endMinutes
}

// CycleSystemData represents powerplay data for a system within a specific cycle.
type CycleSystemData struct {
	SystemName         string    `json:"system_name"`
	CycleNumber        int       `json:"cycle_number"`
	CycleStart         time.Time `json:"cycle_start"`
	CycleEnd           time.Time `json:"cycle_end"`
	MaxReinforcement   int64     `json:"max_reinforcement"`
	MaxUndermining     int64     `json:"max_undermining"`
	StartReinforcement int64     `json:"start_reinforcement,omitempty"` // First value after tick
	StartUndermining   int64     `json:"start_undermining,omitempty"`   // First value after tick
	EndReinforcement   int64     `json:"end_reinforcement,omitempty"`   // Latest value
	EndUndermining     int64     `json:"end_undermining,omitempty"`     // Latest value
	ControllingPower   string    `json:"controlling_power,omitempty"`
	PowerplayState     string    `json:"powerplay_state,omitempty"`
	ObservationCount   int       `json:"observations"`
	FirstObservation   time.Time `json:"first_observation,omitempty"`
	LastObservation    time.Time `json:"last_observation,omitempty"`
}

// GetPowerplayCycleData retrieves powerplay data for systems bounded by cycle.
func (s *CacheStore) GetPowerplayCycleData(ctx context.Context, systemNames []string, cycleOffset int) ([]CycleSystemData, error) {
	if s.eddnClient == nil || s.eddnClient.pool == nil {
		return nil, fmt.Errorf("EDDN raw database not configured")
	}

	if len(systemNames) == 0 {
		return nil, fmt.Errorf("at least one system name required")
	}

	// Limit cycles to available data (60 days = ~8 weeks)
	if cycleOffset < -8 {
		cycleOffset = -8
	}
	if cycleOffset > 0 {
		cycleOffset = 0
	}

	// Get cycle boundaries
	cycle := GetCycleBoundaries(time.Now(), cycleOffset)

	// Query for cycle-bounded data
	// Skip first 90 minutes after tick to avoid stale cached data
	safeStart := cycle.StartTime.Add(90 * time.Minute)

	query := `
		WITH cycle_data AS (
			SELECT
				system_name,
				(message_data->>'PowerplayStateReinforcement')::bigint AS reinforcement,
				(message_data->>'PowerplayStateUndermining')::bigint AS undermining,
				message_data->>'ControllingPower' AS controlling_power,
				message_data->>'PowerplayState' AS powerplay_state,
				(message_data->>'timestamp')::timestamp AS event_time
			FROM feed.messages
			WHERE schema_ref = 'https://eddn.edcd.io/schemas/journal/1'
			  AND message_data->>'event' IN ('FSDJump', 'Location')
			  AND system_name = ANY($1)
			  AND message_data->>'ControllingPower' IS NOT NULL
			  AND (message_data->>'timestamp')::timestamp >= $2
			  AND (message_data->>'timestamp')::timestamp < $3
		),
		aggregated AS (
			SELECT
				system_name,
				MAX(reinforcement) AS max_reinforcement,
				MAX(undermining) AS max_undermining,
				-- First observation (start of cycle)
				(array_agg(reinforcement ORDER BY event_time ASC) FILTER (WHERE reinforcement IS NOT NULL))[1] AS start_reinforcement,
				(array_agg(undermining ORDER BY event_time ASC) FILTER (WHERE undermining IS NOT NULL))[1] AS start_undermining,
				-- Last observation (end of cycle / current)
				(array_agg(reinforcement ORDER BY event_time DESC) FILTER (WHERE reinforcement IS NOT NULL))[1] AS end_reinforcement,
				(array_agg(undermining ORDER BY event_time DESC) FILTER (WHERE undermining IS NOT NULL))[1] AS end_undermining,
				-- Latest state info
				(array_agg(controlling_power ORDER BY event_time DESC) FILTER (WHERE controlling_power IS NOT NULL))[1] AS controlling_power,
				(array_agg(powerplay_state ORDER BY event_time DESC) FILTER (WHERE powerplay_state IS NOT NULL))[1] AS powerplay_state,
				COUNT(*) AS observation_count,
				MIN(event_time) AS first_observation,
				MAX(event_time) AS last_observation
			FROM cycle_data
			GROUP BY system_name
		)
		SELECT * FROM aggregated
		ORDER BY system_name
	`

	rows, err := s.eddnClient.pool.Query(ctx, query, systemNames, safeStart, cycle.EndTime)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	var results []CycleSystemData
	for rows.Next() {
		var d CycleSystemData
		var firstObs, lastObs *time.Time

		err := rows.Scan(
			&d.SystemName,
			&d.MaxReinforcement,
			&d.MaxUndermining,
			&d.StartReinforcement,
			&d.StartUndermining,
			&d.EndReinforcement,
			&d.EndUndermining,
			&d.ControllingPower,
			&d.PowerplayState,
			&d.ObservationCount,
			&firstObs,
			&lastObs,
		)
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}

		d.CycleNumber = cycleOffset
		d.CycleStart = cycle.StartTime
		d.CycleEnd = cycle.EndTime

		if firstObs != nil {
			d.FirstObservation = *firstObs
		}
		if lastObs != nil {
			d.LastObservation = *lastObs
		}

		results = append(results, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}

	return results, nil
}
