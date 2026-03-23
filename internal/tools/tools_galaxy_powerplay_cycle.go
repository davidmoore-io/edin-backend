package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/store"
)

// galaxyPowerplayCycle retrieves cycle-aware powerplay data for systems.
// It understands the weekly Thursday 07:00 UTC tick and can compare cycles.
func (e *Executor) galaxyPowerplayCycle(ctx context.Context, args map[string]any) (any, error) {
	// Allow both full ops scope and limited Kaine scope
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		if err2 := requireScope(ctx, authz.ScopeKaineChat); err2 != nil {
			return nil, err
		}
	}

	if e.historyClient == nil {
		return nil, errors.New("historical data not available (EDDN raw database not configured)")
	}

	// Parse system_names (can be a single string or array)
	var systemNames []string
	if names, ok := args["system_names"].([]any); ok {
		for _, n := range names {
			if s, ok := n.(string); ok && strings.TrimSpace(s) != "" {
				systemNames = append(systemNames, strings.TrimSpace(s))
			}
		}
	} else if name, ok := args["system_name"].(string); ok && strings.TrimSpace(name) != "" {
		systemNames = []string{strings.TrimSpace(name)}
	}

	if len(systemNames) == 0 {
		return nil, errors.New("system_name or system_names parameter required")
	}

	// Limit to 10 systems max to avoid overloading
	if len(systemNames) > 10 {
		systemNames = systemNames[:10]
	}

	// Parse cycle offset (0 = current, -1 = previous, etc.)
	cycle := getInt(args, "cycle", 0)
	if cycle > 0 {
		cycle = 0 // Can't query future cycles
	}
	if cycle < -8 {
		cycle = -8 // Limited to ~60 days of data
	}

	// Check if comparison is requested
	compare := getBool(args, "compare", false)

	// Get current cycle info for context
	now := time.Now().UTC()
	currentCycle := store.GetCycleBoundaries(now, 0)

	// Query the requested cycle
	cycleData, err := e.historyClient.GetPowerplayCycleData(ctx, systemNames, cycle)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycle data: %w", err)
	}

	response := map[string]any{
		"cycle_info": map[string]any{
			"cycle_number":        cycle,
			"cycle_start":         store.GetCycleBoundaries(now, cycle).StartTime.Format(time.RFC3339),
			"cycle_end":           store.GetCycleBoundaries(now, cycle).EndTime.Format(time.RFC3339),
			"is_current":          cycle == 0,
			"current_cycle_start": currentCycle.StartTime.Format(time.RFC3339),
			"tick_day":            "Thursday",
			"tick_time_utc":       "07:00",
		},
		"systems": cycleData,
		"source":  "eddn_raw",
	}

	// If comparison requested, also get previous cycle
	if compare && cycle == 0 {
		prevCycleData, err := e.historyClient.GetPowerplayCycleData(ctx, systemNames, -1)
		if err == nil {
			response["previous_cycle"] = map[string]any{
				"cycle_number": -1,
				"cycle_start":  store.GetCycleBoundaries(now, -1).StartTime.Format(time.RFC3339),
				"cycle_end":    store.GetCycleBoundaries(now, -1).EndTime.Format(time.RFC3339),
				"systems":      prevCycleData,
			}

			// Calculate deltas for systems present in both cycles
			deltas := calculateCycleDeltas(cycleData, prevCycleData)
			if len(deltas) > 0 {
				response["week_over_week_change"] = deltas
			}
		}
	}

	return response, nil
}

// CycleDelta represents the change between two cycles for a system.
type CycleDelta struct {
	SystemName            string `json:"system_name"`
	ReinforcementChange   int64  `json:"reinforcement_change"`
	UnderminingChange     int64  `json:"undermining_change"`
	ReinforcementPrevious int64  `json:"reinforcement_previous"`
	ReinforcementCurrent  int64  `json:"reinforcement_current"`
	UnderminingPrevious   int64  `json:"undermining_previous"`
	UnderminingCurrent    int64  `json:"undermining_current"`
	Trend                 string `json:"trend"` // "improving", "declining", "stable"
}

func calculateCycleDeltas(current, previous []store.CycleSystemData) []CycleDelta {
	// Build map of previous cycle data
	prevMap := make(map[string]store.CycleSystemData)
	for _, p := range previous {
		prevMap[p.SystemName] = p
	}

	var deltas []CycleDelta
	for _, curr := range current {
		prev, ok := prevMap[curr.SystemName]
		if !ok {
			continue
		}

		delta := CycleDelta{
			SystemName:            curr.SystemName,
			ReinforcementCurrent:  curr.EndReinforcement,
			ReinforcementPrevious: prev.EndReinforcement,
			ReinforcementChange:   curr.EndReinforcement - prev.EndReinforcement,
			UnderminingCurrent:    curr.EndUndermining,
			UnderminingPrevious:   prev.EndUndermining,
			UnderminingChange:     curr.EndUndermining - prev.EndUndermining,
		}

		// Determine trend based on net change
		// Positive reinforcement change OR negative undermining change = improving
		netChange := delta.ReinforcementChange - delta.UnderminingChange
		if netChange > 100000 {
			delta.Trend = "improving"
		} else if netChange < -100000 {
			delta.Trend = "declining"
		} else {
			delta.Trend = "stable"
		}

		deltas = append(deltas, delta)
	}

	return deltas
}
