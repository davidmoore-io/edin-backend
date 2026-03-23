package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/edin-space/edin-backend/internal/authz"
)

// galaxyHistory retrieves historical powerplay data from the EDDN raw feed.
func (e *Executor) galaxyHistory(ctx context.Context, args map[string]any) (any, error) {
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

	// Parse days (default 14, max 30)
	days := getInt(args, "days", 14)
	if days <= 0 {
		days = 14
	}
	if days > 30 {
		days = 30
	}

	// Query historical data
	history, err := e.historyClient.GetPowerplayHistory(ctx, systemNames, days)
	if err != nil {
		return nil, err
	}

	// Build response
	response := map[string]any{
		"systems":      history,
		"days_queried": days,
		"source":       "eddn_raw",
	}

	// Add summary stats
	totalObs := 0
	for _, sys := range history {
		for _, entry := range sys.History {
			totalObs += entry.ObservationCount
		}
	}
	response["total_observations"] = totalObs

	return response, nil
}
