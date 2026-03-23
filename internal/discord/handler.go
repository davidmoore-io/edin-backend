package discord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/ops"
)

var (
	errServiceNameRequired = errors.New("service name is required")
	errUnknownService      = errors.New("unknown service")
)

func (b *Bot) registerInteractionHandler() {
	b.session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		ctx := context.Background()

		switch i.Type {
		case discordgo.InteractionApplicationCommandAutocomplete:
			b.handleAutocomplete(ctx, s, i)
		case discordgo.InteractionApplicationCommand:
			switch i.ApplicationCommandData().Name {
			case commandStatus:
				b.handleStatus(ctx, s, i)
			case commandRestart:
				b.handleRestart(ctx, s, i)
			case commandLogs:
				b.handleLogs(ctx, s, i)
			case commandOpsAssist:
				b.handleOpsAssist(ctx, s, i)
			case commandCG:
				b.handleCG(ctx, s, i)
			case commandDayzEconomy:
				b.handleDayzEconomy(ctx, s, i)
			default:
				_ = respondEphemeral(s, i, "Unknown command")
			}
		}
	})
}

func (b *Bot) handleStatus(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	service, label, err := b.resolveService(optionValue(i, "service"))
	if err != nil {
		_ = respondEphemeral(s, i, err.Error())
		return
	}

	status, err := b.control.Status(ctx, service)
	if err != nil {
		b.logger.Error("status command failed", err)
		_ = respondError(s, i, err)
		return
	}

	_ = respond(s, i, formatStatusMessage(status, label))
}

func (b *Bot) handleRestart(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	service, label, err := b.resolveService(optionValue(i, "service"))
	if err != nil {
		_ = respondEphemeral(s, i, err.Error())
		return
	}

	if !b.userHasServiceRoles(i.Member, service) {
		_ = respondEphemeral(s, i, "You do not have permission to restart that service.")
		return
	}

	if err := deferResponse(s, i, false); err != nil {
		b.logger.Error("restart command defer failed", err)
		return
	}

	result, err := b.control.Restart(ctx, service)
	if err != nil {
		b.logger.Error("restart command failed", err)
		_ = editResponse(s, i, fmt.Sprintf("⚠️ %v", err))
		return
	}

	message := fmt.Sprintf("🔄 Restarted %s (`%s`) at %s", label, result.Container, result.RestartedAt.Format(timeFormat))
	if err := editResponse(s, i, message); err != nil {
		b.logger.Error("restart command response edit failed", err)
	}
}

func (b *Bot) handleLogs(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	service, label, err := b.resolveService(optionValue(i, "service"))
	if err != nil {
		_ = respondEphemeral(s, i, err.Error())
		return
	}

	if !b.userHasServiceRoles(i.Member, service) {
		_ = respondEphemeral(s, i, "You do not have permission to view logs for that service.")
		return
	}

	tail := optionIntValue(i, "tail", b.cfg.Operations.LogTailDefault)

	entries, err := b.control.Logs(ctx, service, tail)
	if err != nil {
		b.logger.Error("logs command failed", err)
		_ = respondError(s, i, err)
		return
	}

	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("Latest logs for %s (`%s`) – showing %d line(s):\n```\n", label, service, len(entries)))
	for _, entry := range entries {
		builder.WriteString(fmt.Sprintf("%s %s\n", entry.Timestamp.Format(timeFormat), entry.Message))
	}
	builder.WriteString("```")
	content := builder.String()
	if len(content) > 1900 {
		content = content[:1900] + "…```"
	}
	_ = respond(s, i, content)
}

func (b *Bot) handleDayzEconomy(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	// Check if user has permission (dayz service role or admin)
	if !b.userHasServiceRoles(i.Member, "dayz") {
		_ = respondEphemeral(s, i, "You do not have permission to view DayZ economy stats.")
		return
	}

	// Check if item search parameter was provided
	itemSearch := optionValue(i, "item")

	// Defer response since parsing might take a moment
	if err := deferResponse(s, i, false); err != nil {
		b.logger.Error("dayz-economy defer failed", err)
		return
	}

	// If item search provided, do item lookup instead of general stats
	if itemSearch != "" {
		result, err := b.control.DayzItemSearch(ctx, itemSearch)
		if err != nil {
			b.logger.Error("dayz-economy item search failed", err)
			_ = editResponse(s, i, fmt.Sprintf("⚠️ Failed to search items: %v", err))
			return
		}

		message := formatDayzItemSearchMessage(result)
		if err := editResponse(s, i, message); err != nil {
			b.logger.Error("dayz-economy item search response edit failed", err)
		}
		return
	}

	// Default: show economy stats
	stats, err := b.control.DayzEconomy(ctx)
	if err != nil {
		b.logger.Error("dayz-economy command failed", err)
		_ = editResponse(s, i, fmt.Sprintf("⚠️ Failed to fetch economy stats: %v", err))
		return
	}

	message := formatDayzEconomyMessage(stats)
	if err := editResponse(s, i, message); err != nil {
		b.logger.Error("dayz-economy response edit failed", err)
	}
}

func formatDayzEconomyMessage(stats *ops.DayZEconomyStats) string {
	var sb strings.Builder

	sb.WriteString("📊 **DayZ Sakhal Economy Stats**\n\n")

	// Loot overview with visual bar
	fillBar := generateFillBar(stats.FillPercent, 20)
	sb.WriteString("**Loot Economy**\n")
	sb.WriteString(fmt.Sprintf("> Items in world: **%d** / %d nominal\n", stats.TotalInMap, stats.NominalItems))
	sb.WriteString(fmt.Sprintf("> Fill level: %s **%.1f%%**\n", fillBar, stats.FillPercent))
	sb.WriteString(fmt.Sprintf("> Vehicles spawned: **%d**\n\n", stats.VehicleCount))

	// Events
	sb.WriteString("**Dynamic Events**\n")
	sb.WriteString(fmt.Sprintf("> Active event types: **%d**\n", stats.DynamicEvents))
	sb.WriteString(fmt.Sprintf("> Spawn positions: **%d**\n\n", stats.EventSpawns))

	// Configuration summary
	sb.WriteString("**Server Config**\n")
	sb.WriteString(fmt.Sprintf("> Item types configured: **%d**\n", stats.TypesConfigured))

	// Timestamp
	sb.WriteString(fmt.Sprintf("\n_Updated: %s_", stats.ParsedAt.Format("15:04:05 UTC")))

	return sb.String()
}

func formatDayzItemSearchMessage(result *ops.DayZItemSearchResult) string {
	var sb strings.Builder

	if len(result.Items) == 0 {
		sb.WriteString(fmt.Sprintf("🔍 No items found matching **%s**\n", result.Query))
		sb.WriteString(fmt.Sprintf("\n_Searched %d item types_", result.TotalTypes))
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("🔍 **Items matching \"%s\"** (%d found)\n\n", result.Query, len(result.Items)))

	// Show up to 10 items in detail
	showCount := len(result.Items)
	if showCount > 10 {
		showCount = 10
	}

	for i := 0; i < showCount; i++ {
		item := result.Items[i]
		sb.WriteString(fmt.Sprintf("**%s**\n", item.Name))
		sb.WriteString(fmt.Sprintf("> Nominal: **%d** | Min: **%d**\n", item.Nominal, item.Min))
		sb.WriteString(fmt.Sprintf("> Lifetime: **%s** | Restock: **%s**\n",
			formatDayzDuration(item.Lifetime), formatDayzDuration(item.Restock)))

		if item.Category != "" {
			sb.WriteString(fmt.Sprintf("> Category: %s\n", item.Category))
		}
		if len(item.Usages) > 0 {
			sb.WriteString(fmt.Sprintf("> Spawn: %s\n", strings.Join(item.Usages, ", ")))
		}
		if len(item.Values) > 0 {
			sb.WriteString(fmt.Sprintf("> Value: %s\n", strings.Join(item.Values, ", ")))
		}
		sb.WriteString("\n")
	}

	if len(result.Items) > 10 {
		sb.WriteString(fmt.Sprintf("_... and %d more items_\n", len(result.Items)-10))
	}

	return sb.String()
}

// formatDayzDuration converts seconds to a human-readable duration for DayZ items.
func formatDayzDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	if seconds < 86400 {
		hours := seconds / 3600
		mins := (seconds % 3600) / 60
		if mins > 0 {
			return fmt.Sprintf("%dh%dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	if hours > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}

// generateFillBar creates a visual progress bar for Discord
func generateFillBar(percent float64, width int) string {
	filled := int(percent / 100 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	empty := width - filled
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", empty) + "]"
}

func optionValue(i *discordgo.InteractionCreate, name string) string {
	if i == nil {
		return ""
	}
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == name {
			return opt.StringValue()
		}
	}
	return ""
}

func optionIntValue(i *discordgo.InteractionCreate, name string, def int) int {
	for _, opt := range i.ApplicationCommandData().Options {
		if opt.Name == name {
			return int(opt.IntValue())
		}
	}
	return def
}

const timeFormat = "2006-01-02 15:04:05"

func (b *Bot) resolveService(input string) (string, string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		//nolint:ST1005 // user-facing error message mirrors command style
		return "", "", fmt.Errorf("%w (options: %s)", errServiceNameRequired, b.availableServices())
	}
	// Check if it's a real container service
	if _, ok := b.cfg.Operations.Services[name]; ok {
		label := serviceLabel(b.cfg.Operations.ServiceLabels, name)
		return name, label, nil
	}
	// Check if it's a virtual log-only service
	if label, ok := ops.VirtualLogServices[name]; ok {
		return name, label, nil
	}
	//nolint:ST1005 // user-facing error message mirrors command style
	return "", "", fmt.Errorf("%w %q (options: %s)", errUnknownService, name, b.availableServices())
}

func (b *Bot) availableServices() string {
	if len(b.cfg.Operations.Services) == 0 && len(ops.VirtualLogServices) == 0 {
		return "none configured"
	}
	names := make([]string, 0, len(b.cfg.Operations.Services)+len(ops.VirtualLogServices))
	for name := range b.cfg.Operations.Services {
		label := serviceLabel(b.cfg.Operations.ServiceLabels, name)
		names = append(names, fmt.Sprintf("%s (`%s`)", label, name))
	}
	// Include virtual log services
	for name, label := range ops.VirtualLogServices {
		names = append(names, fmt.Sprintf("%s (`%s`)", label, name))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (b *Bot) handleAutocomplete(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	if len(b.cfg.Operations.Services) == 0 {
		b.logger.Warn("autocomplete requested with no configured services")
		return
	}
	options := i.ApplicationCommandData().Options
	if len(options) == 0 {
		b.logger.Warn("autocomplete requested with no options payload")
		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionApplicationCommandAutocompleteResult,
			Data: &discordgo.InteractionResponseData{
				Choices: b.filteredServiceChoices("", 25),
			},
		}); err != nil {
			b.logger.Error("autocomplete response failed", err)
		}
		return
	}
	current := strings.TrimSpace(options[0].StringValue())
	choices := b.filteredServiceChoices(current, 25)
	if len(choices) == 0 {
		b.logger.Info(fmt.Sprintf("autocomplete no matches for %q, returning full list", current))
		choices = b.filteredServiceChoices("", 25)
	}
	b.logger.Info(fmt.Sprintf("autocomplete query %q -> %d choice(s)", current, len(choices)))
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{
			Choices: choices,
		},
	}); err != nil {
		b.logger.Error("autocomplete response failed", err)
	}
}

func (b *Bot) filteredServiceChoices(query string, limit int) []*discordgo.ApplicationCommandOptionChoice {
	names := make([]string, 0, len(b.cfg.Operations.Services)+len(ops.VirtualLogServices))
	for name := range b.cfg.Operations.Services {
		names = append(names, name)
	}
	// Include virtual log services for autocomplete
	for name := range ops.VirtualLogServices {
		names = append(names, name)
	}
	sort.Strings(names)

	lowerQuery := strings.ToLower(query)
	choices := make([]*discordgo.ApplicationCommandOptionChoice, 0, limit)
	for _, name := range names {
		label := serviceLabel(b.cfg.Operations.ServiceLabels, name)
		if lowerQuery != "" && !strings.Contains(strings.ToLower(name), lowerQuery) && !strings.Contains(strings.ToLower(label), lowerQuery) {
			continue
		}
		choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
			Name:  label,
			Value: name,
		})
		if len(choices) >= limit {
			break
		}
	}
	return choices
}

func (b *Bot) userHasRoles(member *discordgo.Member, allowed []string) bool {
	if len(allowed) == 0 || member == nil {
		return false
	}
	roleSet := make(map[string]struct{}, len(member.Roles))
	for _, id := range member.Roles {
		roleSet[id] = struct{}{}
	}
	for _, allowedID := range allowed {
		if _, ok := roleSet[allowedID]; ok {
			return true
		}
	}
	return false
}

func (b *Bot) userHasServiceRoles(member *discordgo.Member, service string) bool {
	allowed := make([]string, 0, len(b.cfg.Discord.AdminRoleIDs)+len(b.cfg.Discord.ServiceRoleIDs[service]))
	allowed = append(allowed, b.cfg.Discord.AdminRoleIDs...)
	allowed = append(allowed, b.cfg.Discord.ServiceRoleIDs[service]...)
	filtered := make([]string, 0, len(allowed))
	for _, role := range allowed {
		if strings.TrimSpace(role) != "" {
			filtered = append(filtered, strings.TrimSpace(role))
		}
	}
	return b.userHasRoles(member, filtered)
}

func formatStatusMessage(status *ops.ServiceStatus, label string) string {
	message := fmt.Sprintf("**%s** (`%s`)\nState: `%s`\nHealth: `%s`\nRunning: `%t`",
		label, status.Service, status.State, status.Health, status.Running)
	if status.Detail != "" {
		message += fmt.Sprintf("\nDetail: `%s`", status.Detail)
	}
	if status.Running && !status.StartedAt.IsZero() {
		message += fmt.Sprintf("\nUptime: `%s` (since %s)", humanizeDuration(time.Since(status.StartedAt)), status.StartedAt.Format(timeFormat))
	} else if !status.StartedAt.IsZero() {
		message += fmt.Sprintf("\nLast started: `%s`", status.StartedAt.Format(timeFormat))
	}
	return message
}

func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd%dh", days, hours)
}
