package tools

import (
	"context"
	"os"
	"testing"

	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestGalaxyRelationalToolsSmoke(t *testing.T) {
	dsn := os.Getenv("GALAXY_TEST_DSN")
	if dsn == "" {
		t.Skip("set GALAXY_TEST_DSN to run relational galaxy tool smoke")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	store := galaxystore.New(pool)
	exec := NewExecutor(nil, nil, nil, nil).WithGalaxyStore(store)

	systemName := requireSampleString(t, ctx, store, `SELECT name FROM galaxy.system_catalog WHERE x IS NOT NULL ORDER BY id64 LIMIT 1`)
	system, err := exec.galaxySystem(ctx, map[string]any{"system_name": systemName, "include": []any{"system"}})
	require.NoError(t, err)
	require.True(t, system.(map[string]any)["found"].(bool))

	marketSystem := requireSampleString(t, ctx, store, `
SELECT c.name
FROM galaxy.market_commodity mc
JOIN galaxy.market m ON m.market_id = mc.market_id
JOIN galaxy.station st ON st.market_id = m.market_id
JOIN galaxy.system_catalog c ON c.id64 = st.system_id64
LIMIT 1`)
	market, err := exec.galaxyMarket(ctx, map[string]any{"system_name": marketSystem, "limit": 5})
	require.NoError(t, err)
	require.Equal(t, "postgres", market.(map[string]any)["source"])

	surfaceRef := requireSampleString(t, ctx, store, `
SELECT c.name
FROM galaxy.surface_site ss
JOIN galaxy.system_catalog c ON c.id64 = ss.system_id64
ORDER BY ss.last_event_time DESC
LIMIT 1`)
	sites, err := exec.galaxySurfaceSites(ctx, map[string]any{"system_name": surfaceRef, "radius": 1, "limit": 5})
	require.NoError(t, err)
	require.True(t, sites.(map[string]any)["found"].(bool))

	factionName := requireSampleString(t, ctx, store, `SELECT name FROM galaxy.faction ORDER BY last_event_time DESC LIMIT 1`)
	faction, err := exec.galaxyFaction(ctx, map[string]any{"faction_name": factionName, "include_systems": true, "limit": 5})
	require.NoError(t, err)
	require.True(t, faction.(map[string]any)["found"].(bool))

	stats, err := exec.galaxyStats(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "postgres", stats.(map[string]any)["source"])

	schema, err := exec.galaxySchema(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "postgres", schema.(map[string]any)["source"])
}

func requireSampleString(t *testing.T, ctx context.Context, store *galaxystore.Store, sql string) string {
	t.Helper()
	var out string
	require.NoError(t, store.QueryRow(ctx, sql).Scan(&out))
	require.NotEmpty(t, out)
	return out
}
