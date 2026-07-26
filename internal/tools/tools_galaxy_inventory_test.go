package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestFormatSystemInventoryMarkdownIsCompleteAndCompact(t *testing.T) {
	updated := time.Date(2026, 7, 27, 14, 5, 6, 0, time.UTC)
	distance := 42.5
	marketID := int64(128666762)
	bodyID := 7

	inventory := &galaxystore.SystemInventory{
		System: &galaxystore.SystemData{
			Name:           "Test | System",
			ID64:           123456789,
			Population:     3500000000,
			Economy:        "Industrial",
			SecondEconomy:  "Refinery",
			LastEDDNUpdate: updated,
		},
		Facilities: []galaxystore.InventoryFacility{
			{Kind: "station", Identity: fmt.Sprint(marketID), MarketID: &marketID, Name: "Alpha Station", Type: "Orbis", DistanceLS: &distance, Services: []string{"Dock", "Market"}, HasMarket: true, LastEventTime: &updated},
			{Kind: "settlement", Identity: "2", Name: "Beta Settlement", Type: "Settlement", LastEventTime: &updated},
			{Kind: "station_stub", Identity: "123456789/Stub", Name: "Stub", Type: "Outpost", LastEventTime: &updated},
			{Kind: "installation", Identity: "123456789/Array", Name: "Array", Type: "Installation", LastEventTime: &updated},
			{Kind: "fleet_carrier", Identity: "ABC-123", Name: "Carrier", Type: "Fleet Carrier", LastEventTime: &updated},
			{Kind: "stronghold_carrier", Identity: "123456789", Name: "Stronghold Carrier", Type: "Stronghold Carrier", LastEventTime: &updated},
			{Kind: "megaship", Identity: "Megaship One", Name: "Megaship One", Type: "Megaship", LastEventTime: &updated},
		},
		Bodies: []galaxystore.InventoryBody{
			{
				BodyID:        bodyID,
				Name:          "Test System 7",
				Type:          "Planet",
				SubType:       "Rocky body",
				DistanceLS:    &distance,
				LastEventTime: &updated,
				Rings: []galaxystore.InventoryRing{{
					BodyID:        &bodyID,
					Name:          "Test System 7 A Ring",
					Class:         "eRingClass_Metallic",
					ReserveLevel:  "Pristine",
					HotspotCount:  6,
					LastEventTime: &updated,
				}},
			},
		},
	}

	got := formatSystemInventoryMarkdown(inventory)
	for _, want := range []string{
		"# Test \\| System",
		"System ID64:** `123456789`",
		"Population:** 3,500,000,000",
		"Facilities (7)",
		"market ID `128666762`",
		"Beta Settlement",
		"Outpost (station stub)",
		"123456789/Array",
		"carrier ID `ABC-123`",
		"Stronghold Carrier",
		"Megaship One",
		"Bodies (1)",
		"body ID `7`",
		"Metallic",
		"hotspots 6",
		"2026-07-27T14:05:06Z",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{`"physical"`, `"found"`, "```json"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected bulky/JSON output %q in:\n%s", unwanted, got)
		}
	}
}

func TestFormatMarketInventoryMarkdownDoesNotTruncate(t *testing.T) {
	commodities := make([]galaxystore.MarketCommodity, 0, 250)
	for i := 0; i < 250; i++ {
		commodities = append(commodities, galaxystore.MarketCommodity{
			Name:      fmt.Sprintf("Commodity %03d", i),
			Category:  "Chemicals",
			BuyPrice:  int64(i + 1),
			SellPrice: int64(i + 2),
			Stock:     int64(i + 3),
			Demand:    int64(i + 4),
		})
	}
	market := &galaxystore.MarketInventory{
		MarketID:               987654321,
		OwnerKind:              "fleet_carrier",
		OwnerIdentity:          "ABC-123",
		StationName:            "Complete Market",
		SystemName:             "Sol",
		LastEventTime:          time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC),
		ReportedCommodityCount: len(commodities),
		Commodities:            commodities,
	}

	got := formatMarketInventoryMarkdown(market)
	for _, want := range []string{
		"# Market: Complete Market",
		"Market ID:** `987654321`",
		"fleet carrier `ABC-123`",
		"Last updated:** 2026-07-27T15:00:00Z",
		"Commodities:** 250",
		"Commodity 000",
		"Commodity 249",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in market output", want)
		}
	}
	if strings.Contains(strings.ToLower(got), "truncat") {
		t.Fatalf("market output claims truncation:\n%s", got)
	}
}

func TestGalaxyInventoryToolsIntegration(t *testing.T) {
	dsn := os.Getenv("GALAXY_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("TSDB_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("set GALAXY_TEST_DSN or TSDB_TEST_DSN")
	}

	ctx := authz.ContextWithScopes(context.Background(), authz.ScopeGalaxyRead)
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	var systemName string
	require.NoError(t, pool.QueryRow(ctx, `
SELECT c.name
FROM galaxy.system_catalog c
WHERE EXISTS (SELECT 1 FROM galaxy.body b WHERE b.system_id64 = c.id64)
  AND (
	EXISTS (SELECT 1 FROM galaxy.station st WHERE st.system_id64 = c.id64)
	OR EXISTS (SELECT 1 FROM galaxy.settlement se WHERE se.system_id64 = c.id64)
  )
ORDER BY c.id64
LIMIT 1`).Scan(&systemName))

	executor := NewExecutor(nil, nil, nil, nil).WithGalaxyStore(galaxystore.New(pool))
	systemResult, err := executor.Invoke(ctx, string(ToolGalaxySystem), map[string]any{
		"system_name": systemName,
	})
	require.NoError(t, err)
	systemMarkdown, ok := systemResult.(string)
	require.True(t, ok)
	require.Contains(t, systemMarkdown, "## Facilities")
	require.Contains(t, systemMarkdown, "## Bodies")

	var marketID int64
	require.NoError(t, pool.QueryRow(ctx, `
SELECT market_id
FROM galaxy.market
ORDER BY market_id
LIMIT 1`).Scan(&marketID))
	marketResult, err := executor.Invoke(ctx, string(ToolGalaxyMarket), map[string]any{
		"market_id": marketID,
	})
	require.NoError(t, err)
	marketMarkdown, ok := marketResult.(string)
	require.True(t, ok)
	require.Contains(t, marketMarkdown, fmt.Sprintf("Market ID:** `%d`", marketID))
	require.Contains(t, marketMarkdown, "| Commodity |")
}
