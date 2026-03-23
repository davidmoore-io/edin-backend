package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/memgraph"
)

// systemProfile queries EDIN (Elite Dangerous Intel Network) for system data.
// EDIN is the authoritative source for real-time galaxy data.
func (e *Executor) systemProfile(ctx context.Context, args map[string]any) (any, error) {
	if err := requireScope(ctx, authz.ScopeLlmOperator); err != nil {
		return nil, err
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	systemID := strings.TrimSpace(getString(args, "system_id"))
	if systemName == "" && systemID == "" {
		return nil, errors.New("system_name or system_id is required")
	}

	name := firstNonEmpty(systemName, systemID)
	generatedAt := time.Now().UTC()

	// Query EDIN (authoritative source)
	if e.memgraph == nil {
		return map[string]any{
			"system":       name,
			"generated_at": generatedAt.Format(time.RFC3339),
			"error":        "EDIN not available",
			"markdown":     fmt.Sprintf("# System Profile: %s\n\n⚠️ EDIN database not available.", name),
		}, nil
	}

	sys, err := e.memgraph.GetSystem(ctx, name)
	if err != nil {
		return map[string]any{
			"system":       name,
			"generated_at": generatedAt.Format(time.RFC3339),
			"error":        err.Error(),
			"markdown":     fmt.Sprintf("# System Profile: %s\n\n⚠️ Failed to query EDIN: %v", name, err),
		}, nil
	}

	if sys == nil {
		return map[string]any{
			"system":       name,
			"generated_at": generatedAt.Format(time.RFC3339),
			"not_found":    true,
			"markdown":     fmt.Sprintf("# System Profile: %s\n\nℹ️ System not found in EDIN.\n\nThis system may not have been visited recently by connected players.", name),
		}, nil
	}

	// Build response
	markdown := buildMemgraphSystemMarkdown(sys, generatedAt)

	response := map[string]any{
		"system":       sys.Name,
		"generated_at": generatedAt.Format(time.RFC3339),
		"markdown":     markdown,
		"data":         sys,
	}

	// Add data freshness info
	if !sys.LastEDDNUpdate.IsZero() {
		age := time.Since(sys.LastEDDNUpdate)
		response["data_age_seconds"] = int(age.Seconds())
		response["last_update"] = sys.LastEDDNUpdate.Format(time.RFC3339)
	}

	return response, nil
}

func buildMemgraphSystemMarkdown(sys *memgraph.SystemData, generatedAt time.Time) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# System Profile: %s\n\n", sys.Name))
	sb.WriteString(fmt.Sprintf("_Generated %s · Source: EDIN_\n\n", generatedAt.Format(time.RFC3339)))

	// Data freshness
	var sources []string
	if !sys.LastEDDNUpdate.IsZero() {
		age := time.Since(sys.LastEDDNUpdate)
		if age < time.Hour {
			sources = append(sources, fmt.Sprintf("%d min ago", int(age.Minutes())))
		} else if age < 24*time.Hour {
			sources = append(sources, fmt.Sprintf("%.1fh ago", age.Hours()))
		} else {
			sources = append(sources, fmt.Sprintf("⚠️ %.1fd old", age.Hours()/24))
		}
	}
	if len(sources) > 0 {
		sb.WriteString(fmt.Sprintf("_Last update: %s_\n\n", strings.Join(sources, " · ")))
	}

	// System Details
	sb.WriteString("## System Details\n\n")

	if sys.Allegiance != "" {
		sb.WriteString(fmt.Sprintf("- **Allegiance**: %s\n", sys.Allegiance))
	}
	if sys.Government != "" {
		sb.WriteString(fmt.Sprintf("- **Government**: %s\n", sys.Government))
	}
	if sys.Security != "" {
		sb.WriteString(fmt.Sprintf("- **Security**: %s\n", sys.Security))
	}
	if sys.Economy != "" {
		if sys.SecondEconomy != "" {
			sb.WriteString(fmt.Sprintf("- **Economy**: %s + %s\n", sys.Economy, sys.SecondEconomy))
		} else {
			sb.WriteString(fmt.Sprintf("- **Economy**: %s\n", sys.Economy))
		}
	}
	if sys.Population > 0 {
		sb.WriteString(fmt.Sprintf("- **Population**: %s\n", formatInteger(int64(sys.Population))))
	}
	if sys.Coordinates != nil {
		sb.WriteString(fmt.Sprintf("- **Coordinates**: (%.2f, %.2f, %.2f)\n", sys.Coordinates.X, sys.Coordinates.Y, sys.Coordinates.Z))
	}

	// Controlling Faction
	if sys.ControllingFaction != "" {
		sb.WriteString(fmt.Sprintf("\n### Controlling Faction\n"))
		state := sys.ControllingFactionState
		if state == "" {
			state = "None"
		}
		sb.WriteString(fmt.Sprintf("- **%s** — %s\n", sys.ControllingFaction, state))
	}

	// Powerplay
	if sys.ControllingPower != "" || len(sys.Powers) > 0 || sys.PowerplayState != "" {
		sb.WriteString("\n## Powerplay\n\n")

		if sys.ControllingPower != "" {
			sb.WriteString(fmt.Sprintf("- **Controlling**: %s", sys.ControllingPower))
			if sys.PowerplayState != "" {
				sb.WriteString(fmt.Sprintf(" · State: **%s**\n", sys.PowerplayState))
			} else {
				sb.WriteString("\n")
			}
		}

		if sys.ControlProgress != nil && *sys.ControlProgress > 0 {
			sb.WriteString(fmt.Sprintf("- **Control Progress**: %.1f%%\n", *sys.ControlProgress*100))
		}

		if sys.Reinforcement > 0 || sys.Undermining > 0 {
			sb.WriteString(fmt.Sprintf("- **Reinforcement**: %s merits · **Undermining**: %s merits\n",
				formatInteger(sys.Reinforcement),
				formatInteger(sys.Undermining),
			))
		}

		if len(sys.Powers) > 0 {
			sb.WriteString(fmt.Sprintf("- **Powers present**: %s\n", strings.Join(sys.Powers, ", ")))
		}

		// Conflict progress if available
		if len(sys.PowerplayConflictProgress) > 0 {
			sb.WriteString("\n### Conflict Progress\n")
			for _, cp := range sys.PowerplayConflictProgress {
				power, _ := cp["Power"].(string)
				progress, _ := cp["ConflictProgress"].(float64)
				if power != "" {
					sb.WriteString(fmt.Sprintf("- %s: %.1f%%\n", power, progress*100))
				}
			}
		}
	}

	sb.WriteString("\n---\n_📡 Data from EDIN (Elite Dangerous Intel Network)_\n")

	return sb.String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func formatInteger(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	str := strconv.FormatInt(value, 10)
	for i := len(str) - 3; i > 0; i -= 3 {
		str = str[:i] + "," + str[i:]
	}
	if negative {
		return "-" + str
	}
	return str
}
