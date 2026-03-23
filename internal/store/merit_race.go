package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// PowerStandingResult contains the full power standing analysis response.
// Note: The values shown are "control points" (aggregate scores from EDDN), not personal merits.
// Personal merits earned by commanders are 4x the control points shown here.
type PowerStandingResult struct {
	TimeSeries  []PowerStandingBucket `json:"time_series"`
	Summary     map[string]PowerStats `json:"summary"`
	GapAnalysis *GapAnalysis          `json:"gap_analysis,omitempty"`
	TickInfo    TickInfo              `json:"tick_info"`
	QueryParams QueryParams           `json:"query_params"`
	DataThrough time.Time             `json:"data_through"`
	GeneratedAt time.Time             `json:"generated_at"`
}

// PowerStandingBucket represents aggregated control point data at a point in time.
type PowerStandingBucket struct {
	Bucket time.Time                `json:"bucket"`
	Powers map[string]ControlTotals `json:"powers"`
}

// ControlTotals contains control point values for a power.
// These are aggregate scores from EDDN PowerplayStateReinforcement/Undermining fields.
type ControlTotals struct {
	Reinforcement int64 `json:"reinforcement"`
	Undermining   int64 `json:"undermining"`
}

// PowerStats contains summary statistics for a power.
type PowerStats struct {
	CurrentTotal       int64            `json:"current_total"`
	FirstTotal         int64            `json:"first_total,omitempty"`
	Delta              int64            `json:"delta"`
	RatePerHour        float64          `json:"rate_per_hour"`
	FirstObservation   time.Time        `json:"first_observation,omitempty"`
	LastObservation    time.Time        `json:"last_observation,omitempty"`
	SystemCount        int              `json:"system_count"`
	ReinforcementTotal int64            `json:"reinforcement_total"`
	UnderminingTotal   int64            `json:"undermining_total"`
	NetMerits          int64            `json:"net_merits"`
	StateCounts        PowerStateCounts `json:"state_counts"`
}

// PowerStateCounts captures current system counts by powerplay state.
type PowerStateCounts struct {
	Expansion  int `json:"expansion"`
	Contested  int `json:"contested"`
	Exploited  int `json:"exploited"`
	Fortified  int `json:"fortified"`
	Stronghold int `json:"stronghold"`
	HomeSystem int `json:"home_system"`
	Controlled int `json:"controlled"`
}

// GapAnalysis contains competitive analysis between powers.
type GapAnalysis struct {
	Leader                string  `json:"leader"`
	LeaderTotal           int64   `json:"leader_total"`
	Second                string  `json:"second"`
	SecondTotal           int64   `json:"second_total"`
	Gap                   int64   `json:"gap"`
	LeaderRate            float64 `json:"leader_rate"`
	SecondRate            float64 `json:"second_rate"`
	ProjectedHoursToClose float64 `json:"projected_hours_to_close,omitempty"`
}

// TickInfo contains powerplay tick information.
type TickInfo struct {
	TickNumber    int       `json:"tick_number"`
	TickStart     time.Time `json:"tick_start"`
	TickEnd       time.Time `json:"tick_end"`
	HoursIntoTick float64   `json:"hours_into_tick"`
}

// QueryParams records the query parameters used.
type QueryParams struct {
	Granularity string `json:"granularity"`
	Hours       int    `json:"hours"`
}

// ValidGranularities defines allowed granularity values.
var ValidGranularities = map[string]string{
	"15m": "15 minutes",
	"30m": "30 minutes",
	"1h":  "1 hour",
	"6h":  "6 hours",
	"1d":  "1 day",
}

// GetPowerStandingData retrieves time-bucketed control point aggregations by power.
func (s *CacheStore) GetPowerStandingData(ctx context.Context, systems []string, granularity string, hours int) (*PowerStandingResult, error) {
	if s.eddnClient == nil || s.eddnClient.pool == nil {
		return nil, fmt.Errorf("EDDN client not configured")
	}

	if len(systems) == 0 {
		return nil, fmt.Errorf("no systems specified")
	}

	// Validate and convert granularity
	intervalStr, ok := ValidGranularities[granularity]
	if !ok {
		intervalStr = "1 hour"
		granularity = "1h"
	}

	// Cap hours
	if hours <= 0 {
		hours = 168 // Default 7 days
	}
	if hours > 720 {
		hours = 720 // Max 30 days
	}

	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)

	// Query: bucket by time and aggregate per-system max values, then sum by power
	// Use COALESCE to handle NULL values (events without reinforcement/undermining data)
	// Include current bucket - each power's line will end at their last observation
	query := `
		WITH bucketed_data AS (
			SELECT
				time_bucket($1::interval, (message_data->>'timestamp')::timestamptz) AS bucket,
				message_data->>'ControllingPower' AS controlling_power,
				system_name,
				COALESCE(MAX((message_data->>'PowerplayStateReinforcement')::bigint), 0) AS reinforcement,
				COALESCE(MAX((message_data->>'PowerplayStateUndermining')::bigint), 0) AS undermining
			FROM feed.messages
			WHERE schema_ref = 'https://eddn.edcd.io/schemas/journal/1'
			  AND message_data->>'event' IN ('FSDJump', 'Location')
			  AND message_data->>'ControllingPower' IS NOT NULL
			  AND system_name = ANY($2)
			  AND (message_data->>'timestamp')::timestamptz >= $3
			GROUP BY bucket, controlling_power, system_name
		),
		power_totals AS (
			SELECT
				bucket,
				controlling_power,
				COALESCE(SUM(reinforcement), 0) AS total_reinforcement,
				COALESCE(SUM(undermining), 0) AS total_undermining,
				COUNT(DISTINCT system_name) AS system_count
			FROM bucketed_data
			GROUP BY bucket, controlling_power
		)
		SELECT bucket, controlling_power, total_reinforcement, total_undermining, system_count
		FROM power_totals
		ORDER BY bucket ASC, controlling_power
	`

	rows, err := s.eddnClient.pool.Query(ctx, query, intervalStr, systems, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query power standing data: %w", err)
	}
	defer rows.Close()

	// Parse results into time buckets
	bucketMap := make(map[time.Time]map[string]ControlTotals)
	systemCounts := make(map[string]int)
	var allBuckets []time.Time

	for rows.Next() {
		var bucket time.Time
		var power string
		var reinforcement, undermining int64
		var sysCount int

		if err := rows.Scan(&bucket, &power, &reinforcement, &undermining, &sysCount); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		if _, exists := bucketMap[bucket]; !exists {
			bucketMap[bucket] = make(map[string]ControlTotals)
			allBuckets = append(allBuckets, bucket)
		}

		bucketMap[bucket][power] = ControlTotals{
			Reinforcement: reinforcement,
			Undermining:   undermining,
		}
		systemCounts[power] = sysCount
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	// Sort buckets chronologically
	sort.Slice(allBuckets, func(i, j int) bool {
		return allBuckets[i].Before(allBuckets[j])
	})

	// Build time series
	timeSeries := make([]PowerStandingBucket, 0, len(allBuckets))
	for _, bucket := range allBuckets {
		timeSeries = append(timeSeries, PowerStandingBucket{
			Bucket: bucket,
			Powers: bucketMap[bucket],
		})
	}

	// Calculate summary stats for each power
	summary := calculatePowerSummaries(timeSeries, systemCounts)

	// Merge current-state counts and totals when available
	if currentStates, _, err := s.GetCurrentStateForSystems(ctx, systems); err == nil {
		applyCurrentStateSummaries(summary, currentStates)
	}

	// Calculate gap analysis
	gapAnalysis := calculateGapAnalysis(summary)

	// Calculate tick info
	now := time.Now().UTC()
	tickInfo := calculateTickInfo(now)

	// Query for the actual latest timestamp in the data
	dataThrough := now
	maxTsQuery := `
		SELECT MAX((message_data->>'timestamp')::timestamptz)
		FROM feed.messages
		WHERE schema_ref = 'https://eddn.edcd.io/schemas/journal/1'
		  AND message_data->>'event' IN ('FSDJump', 'Location')
		  AND message_data->>'ControllingPower' IS NOT NULL
		  AND system_name = ANY($1)
		  AND (message_data->>'timestamp')::timestamptz >= $2
	`
	var maxTs *time.Time
	if err := s.eddnClient.pool.QueryRow(ctx, maxTsQuery, systems, cutoff).Scan(&maxTs); err == nil && maxTs != nil {
		dataThrough = *maxTs
	}

	return &PowerStandingResult{
		TimeSeries:  timeSeries,
		Summary:     summary,
		GapAnalysis: gapAnalysis,
		TickInfo:    tickInfo,
		QueryParams: QueryParams{
			Granularity: granularity,
			Hours:       hours,
		},
		DataThrough: dataThrough,
		GeneratedAt: now,
	}, nil
}

type powerStateAggregation struct {
	counts           PowerStateCounts
	reinforcementSum int64
	underminingSum   int64
}

func applyCurrentStateSummaries(summary map[string]PowerStats, states []*CurrentSystemState) {
	if len(states) == 0 {
		return
	}

	aggregates := make(map[string]*powerStateAggregation)
	getAgg := func(power string) *powerStateAggregation {
		agg, ok := aggregates[power]
		if !ok {
			agg = &powerStateAggregation{}
			aggregates[power] = agg
		}
		return agg
	}

	for _, state := range states {
		if state == nil {
			continue
		}
		stateLabel := normalisePowerplayState(state.PowerState, state.IsExpansion)
		if stateLabel == "" {
			continue
		}

		if stateLabel == "expansion" || stateLabel == "contested" {
			for _, power := range state.Powers {
				if power == "" {
					continue
				}
				agg := getAgg(power)
				incrementStateCount(&agg.counts, stateLabel)
			}
			continue
		}

		if state.ControllingPower == nil || *state.ControllingPower == "" {
			continue
		}
		power := *state.ControllingPower
		agg := getAgg(power)
		incrementStateCount(&agg.counts, stateLabel)
		agg.reinforcementSum += state.Reinforcement
		agg.underminingSum += state.Undermining
	}

	for power, agg := range aggregates {
		stats := summary[power]
		stats.StateCounts = agg.counts
		stats.ReinforcementTotal = agg.reinforcementSum
		stats.UnderminingTotal = agg.underminingSum
		stats.NetMerits = agg.reinforcementSum - agg.underminingSum
		summary[power] = stats
	}
}

func normalisePowerplayState(state string, isExpansion bool) string {
	if isExpansion {
		return "expansion"
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "contested":
		return "contested"
	case "exploited":
		return "exploited"
	case "fortified":
		return "fortified"
	case "stronghold":
		return "stronghold"
	case "homesystem":
		return "home_system"
	case "controlled":
		return "controlled"
	case "unoccupied":
		return "expansion"
	default:
		return ""
	}
}

func incrementStateCount(counts *PowerStateCounts, stateLabel string) {
	switch stateLabel {
	case "expansion":
		counts.Expansion++
	case "contested":
		counts.Contested++
	case "exploited":
		counts.Exploited++
	case "fortified":
		counts.Fortified++
	case "stronghold":
		counts.Stronghold++
	case "home_system":
		counts.HomeSystem++
	case "controlled":
		counts.Controlled++
	}
}

// calculatePowerSummaries computes summary statistics for each power.
func calculatePowerSummaries(timeSeries []PowerStandingBucket, systemCounts map[string]int) map[string]PowerStats {
	summary := make(map[string]PowerStats)

	if len(timeSeries) == 0 {
		return summary
	}

	// Collect all powers
	powers := make(map[string]bool)
	for _, bucket := range timeSeries {
		for power := range bucket.Powers {
			powers[power] = true
		}
	}

	firstBucket := timeSeries[0]
	lastBucket := timeSeries[len(timeSeries)-1]
	elapsedHours := lastBucket.Bucket.Sub(firstBucket.Bucket).Hours()
	if elapsedHours < 1 {
		elapsedHours = 1 // Avoid division by zero
	}

	for power := range powers {
		var firstTotal, lastTotal int64
		var firstTime, lastTime time.Time

		// Find first observation
		for _, bucket := range timeSeries {
			if totals, ok := bucket.Powers[power]; ok {
				firstTotal = totals.Reinforcement
				firstTime = bucket.Bucket
				break
			}
		}

		// Find last observation
		for i := len(timeSeries) - 1; i >= 0; i-- {
			if totals, ok := timeSeries[i].Powers[power]; ok {
				lastTotal = totals.Reinforcement
				lastTime = timeSeries[i].Bucket
				break
			}
		}

		delta := lastTotal - firstTotal
		ratePerHour := float64(delta) / elapsedHours

		summary[power] = PowerStats{
			CurrentTotal:     lastTotal,
			FirstTotal:       firstTotal,
			Delta:            delta,
			RatePerHour:      ratePerHour,
			FirstObservation: firstTime,
			LastObservation:  lastTime,
			SystemCount:      systemCounts[power],
		}
	}

	return summary
}

// calculateGapAnalysis computes competitive analysis between top powers.
func calculateGapAnalysis(summary map[string]PowerStats) *GapAnalysis {
	if len(summary) < 2 {
		return nil
	}

	// Sort powers by current total (reinforcement)
	type powerRank struct {
		name  string
		stats PowerStats
	}
	ranks := make([]powerRank, 0, len(summary))
	for name, stats := range summary {
		ranks = append(ranks, powerRank{name, stats})
	}
	sort.Slice(ranks, func(i, j int) bool {
		return ranks[i].stats.CurrentTotal > ranks[j].stats.CurrentTotal
	})

	leader := ranks[0]
	second := ranks[1]

	gap := leader.stats.CurrentTotal - second.stats.CurrentTotal

	ga := &GapAnalysis{
		Leader:      leader.name,
		LeaderTotal: leader.stats.CurrentTotal,
		Second:      second.name,
		SecondTotal: second.stats.CurrentTotal,
		Gap:         gap,
		LeaderRate:  leader.stats.RatePerHour,
		SecondRate:  second.stats.RatePerHour,
	}

	// Calculate projected time to close gap (if second is gaining faster)
	rateDiff := second.stats.RatePerHour - leader.stats.RatePerHour
	if rateDiff > 0 && gap > 0 {
		ga.ProjectedHoursToClose = float64(gap) / rateDiff
	}

	return ga
}

// calculateTickInfo computes current powerplay tick information.
func calculateTickInfo(now time.Time) TickInfo {
	// Epoch: First tick of Powerplay 2.0 (Thursday 2024-10-31 07:00 UTC)
	epoch := time.Date(2024, 10, 31, 7, 0, 0, 0, time.UTC)

	daysSinceEpoch := now.Sub(epoch).Hours() / 24
	tickNumber := int(daysSinceEpoch / 7)

	tickStart := epoch.Add(time.Duration(tickNumber*7*24) * time.Hour)
	tickEnd := tickStart.Add(7 * 24 * time.Hour)
	hoursIntoTick := now.Sub(tickStart).Hours()

	return TickInfo{
		TickNumber:    tickNumber,
		TickStart:     tickStart,
		TickEnd:       tickEnd,
		HoursIntoTick: hoursIntoTick,
	}
}
