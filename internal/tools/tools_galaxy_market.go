package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/edin-space/edin-backend/internal/galaxystore"
)

// galaxyMarket returns one complete current market snapshot selected by the
// stable market id exposed by galaxy_system.
func (e *Executor) galaxyMarket(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	marketID := getInt64(args, "market_id", 0)
	if marketID <= 0 {
		return nil, errors.New("market_id parameter is required and must be positive")
	}

	market, err := store.GetMarketInventoryByID(ctx, marketID)
	if err != nil {
		return nil, err
	}
	if market == nil {
		return fmt.Sprintf("# Market not found\n\nNo relational market record exists for market ID `%d`.", marketID), nil
	}
	return formatMarketInventoryMarkdown(market), nil
}

func formatMarketInventoryMarkdown(market *galaxystore.MarketInventory) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Market: %s\n\n", markdownText(market.StationName))
	fmt.Fprintf(&out, "- **Market ID:** `%d`\n", market.MarketID)
	ownerKind := strings.ReplaceAll(market.OwnerKind, "_", " ")
	fmt.Fprintf(&out, "- **Owner:** %s `%s`\n", markdownText(ownerKind), markdownCode(market.OwnerIdentity))
	if market.SystemName != "" {
		fmt.Fprintf(&out, "- **System:** %s\n", markdownText(market.SystemName))
	}
	fmt.Fprintf(&out, "- **Last updated:** %s\n", markdownTime(market.LastEventTime))
	fmt.Fprintf(&out, "- **Commodities:** %d", len(market.Commodities))
	if market.ReportedCommodityCount != len(market.Commodities) {
		fmt.Fprintf(&out, " (source event reported %d)", market.ReportedCommodityCount)
	}
	out.WriteString("\n")
	if len(market.Prohibited) > 0 {
		fmt.Fprintf(&out, "- **Prohibited:** %s\n", markdownText(strings.Join(market.Prohibited, ", ")))
	}

	out.WriteString("\n| Commodity | Category | Buy | Sell | Stock | Demand |\n")
	out.WriteString("|---|---|---:|---:|---:|---:|\n")
	for _, commodity := range market.Commodities {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s | %s |\n",
			markdownText(commodity.Name),
			markdownText(commodity.Category),
			formatInteger(commodity.BuyPrice),
			formatInteger(commodity.SellPrice),
			formatInteger(commodity.Stock),
			formatInteger(commodity.Demand),
		)
	}
	if len(market.Commodities) == 0 {
		out.WriteString("| _No commodities recorded_ |  |  |  |  |  |\n")
	}
	return strings.TrimSpace(out.String())
}
