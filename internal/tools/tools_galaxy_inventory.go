package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edin-space/edin-backend/internal/galaxystore"
)

func (e *Executor) galaxySystem(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	systemName := strings.TrimSpace(getString(args, "system_name"))
	if systemName == "" {
		return nil, errors.New("system_name parameter is required")
	}

	inventory, err := store.GetSystemInventory(ctx, systemName)
	if err != nil {
		return nil, err
	}
	if inventory == nil {
		return fmt.Sprintf("# System not found\n\nNo relational galaxy record exists for **%s**.", markdownText(systemName)), nil
	}
	return formatSystemInventoryMarkdown(inventory), nil
}

func formatSystemInventoryMarkdown(inventory *galaxystore.SystemInventory) string {
	system := inventory.System
	var out strings.Builder

	fmt.Fprintf(&out, "# %s\n\n", markdownText(system.Name))
	fmt.Fprintf(&out, "- **System ID64:** `%d`\n", system.ID64)
	if system.Coordinates != nil {
		fmt.Fprintf(&out, "- **Coordinates:** %.3f, %.3f, %.3f\n",
			system.Coordinates.X, system.Coordinates.Y, system.Coordinates.Z)
	}
	writeMarkdownField(&out, "Allegiance", system.Allegiance)
	writeMarkdownField(&out, "Government", system.Government)
	writeMarkdownField(&out, "Security", system.Security)
	if system.Population > 0 {
		fmt.Fprintf(&out, "- **Population:** %s\n", formatInteger(system.Population))
	}
	economies := compactStrings(system.Economy, system.SecondEconomy)
	if len(economies) > 0 {
		fmt.Fprintf(&out, "- **Economies:** %s\n", markdownText(strings.Join(economies, " / ")))
	}
	writeMarkdownField(&out, "Controlling faction", system.ControllingFaction)
	if system.ControllingPower != "" || system.PowerplayState != "" {
		power := compactStrings(system.ControllingPower, system.PowerplayState)
		fmt.Fprintf(&out, "- **Powerplay:** %s\n", markdownText(strings.Join(power, " - ")))
	}
	if !system.LastEDDNUpdate.IsZero() {
		fmt.Fprintf(&out, "- **System updated:** %s\n", markdownTime(system.LastEDDNUpdate))
	}

	fmt.Fprintf(&out, "\n## Facilities (%d)\n\n", len(inventory.Facilities))
	if len(inventory.Facilities) == 0 {
		out.WriteString("_None recorded._\n")
	} else {
		for _, facility := range inventory.Facilities {
			fmt.Fprintf(&out, "- **%s** - %s; %s; distance %s",
				markdownText(facility.Name),
				markdownText(facilityType(facility)),
				facilityIdentifier(facility),
				markdownDistance(facility.DistanceLS))
			if services := facilityServices(facility); len(services) > 0 {
				fmt.Fprintf(&out, "; services: %s", markdownText(strings.Join(services, ", ")))
			}
			if facility.LastEventTime != nil {
				fmt.Fprintf(&out, "; updated %s", markdownTime(*facility.LastEventTime))
			}
			out.WriteString("\n")
		}
	}

	fmt.Fprintf(&out, "\n## Bodies (%d)\n\n", len(inventory.Bodies))
	if len(inventory.Bodies) == 0 {
		out.WriteString("_None recorded._\n")
	} else {
		for _, body := range inventory.Bodies {
			bodyType := strings.TrimSpace(strings.Join(compactStrings(body.Type, body.SubType), " - "))
			if bodyType == "" {
				bodyType = "Unknown body"
			}
			fmt.Fprintf(&out, "- **%s** - body ID `%d`; %s; distance %s",
				markdownText(body.Name), body.BodyID, markdownText(bodyType), markdownDistance(body.DistanceLS))
			if body.LastEventTime != nil {
				fmt.Fprintf(&out, "; updated %s", markdownTime(*body.LastEventTime))
			}
			out.WriteString("\n")
			for _, ring := range body.Rings {
				writeRingMarkdown(&out, ring)
			}
		}
	}

	if len(inventory.UnassignedRings) > 0 {
		fmt.Fprintf(&out, "\n## Unassigned rings (%d)\n\n", len(inventory.UnassignedRings))
		for _, ring := range inventory.UnassignedRings {
			writeRingMarkdown(&out, ring)
		}
	}

	return strings.TrimSpace(out.String())
}

func writeRingMarkdown(out *strings.Builder, ring galaxystore.InventoryRing) {
	ringClass := humanizeRingClass(ring.Class)
	if ringClass == "" {
		ringClass = "Unknown class"
	}
	fmt.Fprintf(out, "  - Ring **%s** - %s", markdownText(ring.Name), markdownText(ringClass))
	if ring.ReserveLevel != "" {
		fmt.Fprintf(out, "; reserve %s", markdownText(ring.ReserveLevel))
	}
	fmt.Fprintf(out, "; hotspots %d", ring.HotspotCount)
	if ring.LastEventTime != nil {
		fmt.Fprintf(out, "; updated %s", markdownTime(*ring.LastEventTime))
	}
	out.WriteString("\n")
}

func facilityType(facility galaxystore.InventoryFacility) string {
	kind := strings.TrimSpace(facility.Kind)
	kindLabel := strings.ReplaceAll(kind, "_", " ")
	displayType := strings.TrimSpace(facility.Type)
	if displayType == "" {
		return kindLabel
	}
	normalizedKind := strings.ReplaceAll(strings.ToLower(kindLabel), " ", "")
	normalizedType := strings.ReplaceAll(strings.ToLower(displayType), " ", "")
	if kind == "" || normalizedKind == normalizedType {
		return displayType
	}
	return displayType + " (" + kindLabel + ")"
}

func facilityIdentifier(facility galaxystore.InventoryFacility) string {
	if facility.MarketID != nil {
		return fmt.Sprintf("market ID `%d`", *facility.MarketID)
	}
	label := "key"
	switch facility.Kind {
	case "fleet_carrier":
		label = "carrier ID"
	case "megaship":
		label = "megaship ID"
	}
	return fmt.Sprintf("%s `%s`", label, markdownCode(facility.Identity))
}

func facilityServices(facility galaxystore.InventoryFacility) []string {
	services := append([]string(nil), facility.Services...)
	if facility.HasMarket {
		services = append(services, "Market")
	}
	if facility.HasShipyard {
		services = append(services, "Shipyard")
	}
	if facility.HasOutfitting {
		services = append(services, "Outfitting")
	}
	seen := make(map[string]string)
	for _, service := range services {
		service = strings.TrimSpace(service)
		if service != "" {
			seen[strings.ToLower(service)] = service
		}
	}
	services = services[:0]
	for _, service := range seen {
		services = append(services, service)
	}
	sort.Slice(services, func(i, j int) bool {
		return strings.ToLower(services[i]) < strings.ToLower(services[j])
	})
	return services
}

func humanizeRingClass(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "eRingClass_"))
	switch value {
	case "MetalRich":
		return "Metal-rich"
	case "Rocky":
		return "Rocky"
	case "Icy":
		return "Icy"
	case "Metallic":
		return "Metallic"
	default:
		return value
	}
}

func writeMarkdownField(out *strings.Builder, label, value string) {
	if strings.TrimSpace(value) != "" {
		fmt.Fprintf(out, "- **%s:** %s\n", label, markdownText(value))
	}
}

func compactStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func markdownDistance(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatFloat(*value, 'f', 1, 64) + " Ls"
}

func markdownTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func markdownText(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"*", "\\*",
		"_", "\\_",
		"|", "\\|",
		"\n", " ",
		"\r", " ",
	)
	return replacer.Replace(strings.TrimSpace(value))
}

func markdownCode(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "`", "'")
}

func formatInteger(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := strconv.FormatInt(value, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}
