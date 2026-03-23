// Package discord provides Discord bot functionality.
// This file implements the /cg command for querying Col 359 sector powerplay status.
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/mark3labs/mcp-go/mcp"
)

// handleCG handles the /cg slash command for querying Col 359 sector powerplay status.
// It uses the MCP cg tool which returns SSG-EDDN data (real-time EDDN feed) + Inara attribution.
func (b *Bot) handleCG(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	prompt := optionValue(i, "prompt")

	// Defer response
	if err := deferResponse(s, i, false); err != nil {
		b.logger.Error("cg command defer failed", err)
		return
	}

	// Show initial status
	_ = editResponse(s, i, "🔍 Fetching CG sector data...")

	// Call the MCP cg tool (uses cached data - should be instant)
	result, err := b.mcp.CallTool(ctx, "cg", nil)
	if err != nil {
		b.logger.Error("cg tool call failed", err)
		_ = editResponse(s, i, fmt.Sprintf("⚠️ Failed to fetch CG data: %v", err))
		return
	}

	// Extract the JSON result
	if len(result.Content) == 0 {
		_ = editResponse(s, i, "⚠️ No data returned from CG tool")
		return
	}

	// Parse the result - extract text from MCP TextContent
	var cgData map[string]any
	textContent, ok := mcp.AsTextContent(result.Content[0])
	if !ok {
		_ = editResponse(s, i, "⚠️ Unexpected response format from CG tool")
		return
	}
	text := textContent.Text
	if err := json.Unmarshal([]byte(text), &cgData); err != nil {
		b.logger.Error("failed to parse cg result", err)
		_ = editResponse(s, i, fmt.Sprintf("⚠️ Failed to parse CG data: %v", err))
		return
	}

	// Check if data is still initializing
	if status, ok := cgData["status"].(string); ok && status == "initializing" {
		message := cgData["message"].(string)
		hint := ""
		if h, ok := cgData["hint"].(string); ok {
			hint = "\n\n_" + h + "_"
		}
		_ = editResponse(s, i, fmt.Sprintf("⏳ %s%s", message, hint))
		return
	}

	// If user provided a prompt, pass data to LLM for conversational response
	if strings.TrimSpace(prompt) != "" {
		_ = editResponse(s, i, "🧠 Analyzing CG data...")

		// Build context for the LLM
		llmPrompt := fmt.Sprintf(`The user is asking about the Elite Dangerous Community Goal in the Col 359 sector.

Here is the current CG data:
- Primary source: SSG-EDDN (real-time EDDN feed) provides merit numbers
- Supplementary source: Inara provides attribution (who is undermining/reinforcing)
%s

User's question: %s

Answer their question conversationally based on this data. Be concise and direct.`, text, prompt)

		userID := "discord:" + i.Member.User.ID
		_, reply, err := b.control.CreateLLMSession(ctx, "", userID, llmPrompt)
		if err != nil {
			b.logger.Error("cg llm query failed", err)
			_ = editResponse(s, i, fmt.Sprintf("⚠️ Failed to analyze data: %v", err))
			return
		}

		if err := editResponse(s, i, fmt.Sprintf("🤖 %s", reply)); err != nil {
			b.logger.Error("cg llm response failed", err)
		}
		return
	}

	// No prompt - format the data directly
	message := formatCGData(cgData)

	// Discord has a 2000 character limit, split if needed
	if len(message) > 1900 {
		messages := splitMessage(message, 1900)
		for idx, msg := range messages {
			if idx == 0 {
				if err := editResponse(s, i, msg); err != nil {
					b.logger.Error("cg command response edit failed", err)
				}
			} else {
				_, _ = s.ChannelMessageSend(i.ChannelID, msg)
			}
		}
	} else {
		if err := editResponse(s, i, message); err != nil {
			b.logger.Error("cg command response edit failed", err)
		}
	}
}

// formatCGData formats the CG tool response for Discord in Inara-style detailed view.
func formatCGData(data map[string]any) string {
	var sb strings.Builder

	// Header with data freshness
	sb.WriteString("# 🏛️ Col 359 Sector - CG Status\n")

	// Data freshness info
	if cached, ok := data["cached"].(bool); ok && cached {
		if age, ok := data["data_age_seconds"].(float64); ok {
			sb.WriteString(fmt.Sprintf("_Data ~%s old_\n\n", formatDuration(int(age))))
		}
	} else {
		sb.WriteString("_Fresh data_\n\n")
	}

	// Campaign progress if available (compact)
	if campaigns, ok := data["campaigns"].(map[string]any); ok {
		if campaignData, ok := campaigns["data"].([]any); ok && len(campaignData) > 0 {
			sb.WriteString("**Campaign Progress**\n")
			for _, c := range campaignData {
				camp := c.(map[string]any)
				name := getString(camp, "name")
				tier := getString(camp, "tier")
				progress := getString(camp, "progress")
				emoji := campaignEmoji(name)
				shortName := extractFactionName(name)
				sb.WriteString(fmt.Sprintf("%s%s T%s %s\n", emoji, shortName, tier, progress))
			}
			sb.WriteString("\n")
		}
	}

	// Group systems by controlling power and show detailed info
	if systems, ok := data["systems"].([]any); ok {
		// Build power -> systems map, separating expansion/contested systems
		byPower := make(map[string][]map[string]any)
		var expansionSystems []map[string]any
		var contestedSystems []map[string]any

		for _, s := range systems {
			sys := s.(map[string]any)
			power := "Uncontrolled"
			if p := getString(sys, "controlling_power"); p != "" {
				power = p
			}

			// Check for expansion/contested flags
			isExpansion, _ := sys["is_expansion"].(bool)
			isContested, _ := sys["is_contested"].(bool)
			state := strings.ToLower(getString(sys, "power_state"))

			if isExpansion || state == "expansion" {
				expansionSystems = append(expansionSystems, sys)
			} else if isContested || state == "contested" {
				contestedSystems = append(contestedSystems, sys)
			} else {
				byPower[power] = append(byPower[power], sys)
			}
		}

		// Sort powers by system count (controlled powers first, then uncontrolled)
		type powerGroup struct {
			name    string
			systems []map[string]any
		}
		var groups []powerGroup
		for name, sysList := range byPower {
			if name != "Uncontrolled" {
				groups = append(groups, powerGroup{name, sysList})
			}
		}
		sort.Slice(groups, func(i, j int) bool {
			return len(groups[i].systems) > len(groups[j].systems)
		})
		if uncontrolled, ok := byPower["Uncontrolled"]; ok {
			groups = append(groups, powerGroup{"Uncontrolled", uncontrolled})
		}

		// Show Expansion systems first (if any)
		if len(expansionSystems) > 0 {
			sb.WriteString(fmt.Sprintf("## 📈 Expansion (%d)\n", len(expansionSystems)))
			sort.Slice(expansionSystems, func(i, j int) bool {
				return getSystemActivity(expansionSystems[i]) > getSystemActivity(expansionSystems[j])
			})
			for _, sys := range expansionSystems {
				formatSystemLine(&sb, sys)
			}
			sb.WriteString("\n")
		}

		// Show Contested systems (if any)
		if len(contestedSystems) > 0 {
			sb.WriteString(fmt.Sprintf("## ⚔️ Contested (%d)\n", len(contestedSystems)))
			sort.Slice(contestedSystems, func(i, j int) bool {
				return getSystemActivity(contestedSystems[i]) > getSystemActivity(contestedSystems[j])
			})
			for _, sys := range contestedSystems {
				formatSystemLine(&sb, sys)
			}
			sb.WriteString("\n")
		}

		// Output each power's controlled systems
		for _, group := range groups {
			emoji := powerEmoji(group.name)
			sb.WriteString(fmt.Sprintf("## %s%s (%d)\n", emoji, group.name, len(group.systems)))

			sort.Slice(group.systems, func(i, j int) bool {
				return getSystemActivity(group.systems[i]) > getSystemActivity(group.systems[j])
			})

			for _, sys := range group.systems {
				formatSystemLine(&sb, sys)
			}
			sb.WriteString("\n")
		}
	}

	// Footer with data source info
	sb.WriteString("---\n_📡 SSG-EDDN + Memgraph | Use `/cg prompt:\"question\"` for analysis_")

	return sb.String()
}

// formatSystemLine formats a single system for Discord output.
func formatSystemLine(sb *strings.Builder, sys map[string]any) {
	name := getString(sys, "name")
	shortName := strings.Replace(name, "Col 359 Sector ", "", 1)

	state := getString(sys, "power_state")
	undermining, _ := sys["undermining"].(float64)
	reinforcement, _ := sys["reinforcement"].(float64)

	// Attribution from Inara
	underminingBy := getString(sys, "undermining_by")
	reinforcementBy := getString(sys, "reinforcement_by")
	expansionPct, _ := sys["expansion_pct"].(float64)

	// Powers involved (for expansion/contested)
	var powersStr string
	if powers, ok := sys["powers"].([]any); ok && len(powers) > 0 {
		var powerNames []string
		for _, p := range powers {
			if ps, ok := p.(string); ok {
				// Shorten power names for Discord
				short := ps
				if len(short) > 12 {
					parts := strings.Split(short, " ")
					if len(parts) > 1 {
						short = parts[len(parts)-1] // Use last name
					}
				}
				powerNames = append(powerNames, short)
			}
		}
		if len(powerNames) > 0 {
			powersStr = fmt.Sprintf(" _[%s]_", strings.Join(powerNames, ", "))
		}
	}

	// Conflict progress
	var conflictStr string
	if progress, ok := sys["conflict_progress"].([]any); ok && len(progress) > 0 {
		var parts []string
		for _, p := range progress {
			if pm, ok := p.(map[string]any); ok {
				power := getString(pm, "Power")
				pct, _ := pm["ConflictProgress"].(float64)
				if power != "" {
					shortPower := power
					if idx := strings.LastIndex(power, " "); idx > 0 {
						shortPower = power[idx+1:]
					}
					parts = append(parts, fmt.Sprintf("%s %.1f%%", shortPower, pct*100))
				}
			}
		}
		if len(parts) > 0 {
			conflictStr = fmt.Sprintf(" `%s`", strings.Join(parts, " | "))
		}
	}

	// Format state string
	stateStr := state
	if expansionPct != 0 {
		stateStr = fmt.Sprintf("%s %.1f%%", state, expansionPct)
	}

	// Build activity string
	var activity []string
	if undermining > 0 {
		uStr := fmt.Sprintf("🔴 %s", formatCompact(int(undermining)))
		if underminingBy != "" {
			uStr += fmt.Sprintf(" _%s_", underminingBy)
		}
		activity = append(activity, uStr)
	}
	if reinforcement > 0 {
		rStr := fmt.Sprintf("🟢 %s", formatCompact(int(reinforcement)))
		if reinforcementBy != "" {
			rStr += fmt.Sprintf(" _%s_", reinforcementBy)
		}
		activity = append(activity, rStr)
	}

	if len(activity) > 0 || powersStr != "" || conflictStr != "" {
		sb.WriteString(fmt.Sprintf("**%s** · %s%s%s\n", shortName, stateStr, powersStr, conflictStr))
		for _, a := range activity {
			sb.WriteString(fmt.Sprintf("  %s\n", a))
		}
	} else {
		sb.WriteString(fmt.Sprintf("**%s** · %s · _quiet_\n", shortName, stateStr))
	}
}

// getSystemActivity returns a score for sorting systems by activity level.
func getSystemActivity(sys map[string]any) float64 {
	var total float64
	// Data is now flat (from SSG-EDDN)
	if u, _ := sys["undermining"].(float64); u > 0 {
		total += u
	}
	if r, _ := sys["reinforcement"].(float64); r > 0 {
		total += r
	}
	return total
}

// extractFactionName extracts the faction from campaign names like "Opening Imperial Campaign..."
func extractFactionName(name string) string {
	nameLower := strings.ToLower(name)
	if strings.Contains(nameLower, "imperial") {
		return "Imperial"
	}
	if strings.Contains(nameLower, "federal") {
		return "Federal"
	}
	if strings.Contains(nameLower, "alliance") {
		return "Alliance"
	}
	if strings.Contains(nameLower, "independen") {
		return "Independent"
	}
	return name
}

// campaignEmoji returns an emoji for campaign faction.
func campaignEmoji(name string) string {
	nameLower := strings.ToLower(name)
	if strings.Contains(nameLower, "imperial") {
		return "👑 "
	}
	if strings.Contains(nameLower, "federal") {
		return "🦅 "
	}
	if strings.Contains(nameLower, "alliance") {
		return "🌿 "
	}
	if strings.Contains(nameLower, "independen") {
		return "⭐ "
	}
	return "📋 "
}

// powerEmoji returns an emoji for the given power.
func powerEmoji(power string) string {
	switch power {
	case "Aisling Duval":
		return "👸 "
	case "Archon Delaine":
		return "☠️ "
	case "A. Lavigny-Duval", "Arissa Lavigny-Duval":
		return "👑 "
	case "Denton Patreus":
		return "⚔️ "
	case "Edmund Mahon":
		return "🤝 "
	case "Felicia Winters":
		return "🕊️ "
	case "Jerome Archer":
		return "🦅 "
	case "Li Yong-Rui":
		return "💰 "
	case "Nakato Kaine":
		return "🌸 "
	case "Pranav Antal":
		return "🔮 "
	case "Yuri Grom":
		return "🐻 "
	case "Zemina Torval":
		return "💎 "
	case "Uncontrolled":
		return "⚪ "
	default:
		return "🏛️ "
	}
}

// formatDuration formats seconds into a human-readable duration.
func formatDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%dh %dm", seconds/3600, (seconds%3600)/60)
}

// formatCompact formats a number in compact form (e.g., 119728 -> 119.7k).
func formatCompact(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// splitMessage splits a message into chunks of maxLen characters at line boundaries.
func splitMessage(message string, maxLen int) []string {
	if len(message) <= maxLen {
		return []string{message}
	}

	var result []string
	lines := strings.Split(message, "\n")
	var current strings.Builder

	for _, line := range lines {
		if current.Len()+len(line)+1 > maxLen {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}

// Helper functions for safe type assertions
func getString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if v, ok := m[key].(int); ok {
		return v
	}
	return 0
}