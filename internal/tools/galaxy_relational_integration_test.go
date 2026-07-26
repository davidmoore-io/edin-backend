package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/edin-space/edin-backend/internal/kaine"
	"github.com/edin-space/edin-backend/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestGalaxyRelationalToolsSmoke(t *testing.T) {
	if os.Getenv("GALAXY_TEST_DSN") == "" ||
		os.Getenv("EDIN_TEST_DSN") == "" ||
		os.Getenv("EDDN_HISTORY_TEST_DSN") == "" {
		t.Skip("set GALAXY_TEST_DSN, EDIN_TEST_DSN, and EDDN_HISTORY_TEST_DSN for the MR7 smoke")
	}
	galaxyDSN := requireIntegrationDSN(t, "GALAXY_TEST_DSN")
	edinDSN := requireIntegrationDSN(t, "EDIN_TEST_DSN")
	historyDSN := requireIntegrationDSN(t, "EDDN_HISTORY_TEST_DSN")

	ctx := authz.ContextWithScopes(
		context.Background(),
		authz.ScopeGalaxyRead,
		authz.ScopeKaineMining,
	)

	galaxyPool, err := pgxpool.New(ctx, galaxyDSN)
	require.NoError(t, err)
	defer galaxyPool.Close()
	require.NoError(t, galaxyPool.Ping(ctx))

	edinPool, err := pgxpool.New(ctx, edinDSN)
	require.NoError(t, err)
	defer edinPool.Close()
	require.NoError(t, edinPool.Ping(ctx))

	historyClient := newStoreClientFromDSN(t, historyDSN, "feed")
	defer historyClient.Close()
	historyStore := store.NewCacheStore(nil)
	historyStore.SetEDDNClient(historyClient)

	galaxyStore := galaxystore.New(galaxyPool)
	exec := NewExecutor(nil, nil, nil, nil).
		WithGalaxyStore(galaxyStore).
		WithKaineStore(kaine.NewStore(edinPool)).
		WithHistoryClient(historyStore)

	samples := loadGalaxySmokeSamples(t, ctx, galaxyStore)

	currentGalaxy := []struct {
		name ToolName
		args map[string]any
	}{
		{ToolGalaxySystem, map[string]any{"system_name": samples.system}},
		{ToolGalaxyStation, map[string]any{"station_name": samples.station, "limit": 1}},
		{ToolGalaxyFleetCarrier, map[string]any{"carrier_id": samples.carrier}},
		{ToolGalaxyBodies, map[string]any{"system_name": samples.bodySystem}},
		{ToolGalaxySignals, map[string]any{"system_name": samples.signalSystem}},
		{ToolGalaxySurfaceSites, map[string]any{"system_name": samples.surfaceSystem, "radius": 1, "limit": 1}},
		{ToolGalaxyPower, map[string]any{"power_name": samples.power, "include_systems": true, "limit": 1}},
		{ToolGalaxyFaction, map[string]any{"faction_name": samples.faction, "include_systems": true, "limit": 1}},
		{ToolGalaxyStats, nil},
		{ToolGalaxyMarket, map[string]any{"market_id": samples.marketID}},
		{ToolGalaxyExpansionCheck, map[string]any{"system_name": samples.powerSystem, "power_name": samples.power}},
		{ToolGalaxyNearbyPowerplay, map[string]any{"system_name": samples.powerSystem, "power_name": samples.power, "max_distance": 10}},
		{ToolGalaxyExpansionFrontier, map[string]any{"control_system": samples.controlSystem, "power_name": samples.controlPower}},
		{ToolGalaxyPlasmiumBuyers, nil},
		{ToolGalaxyLTDBuyers, nil},
		{ToolGalaxyExpansionTargets, nil},
		{ToolGalaxySchema, nil},
	}

	for _, tc := range currentGalaxy {
		t.Run(string(tc.name), func(t *testing.T) {
			result, err := exec.Invoke(ctx, string(tc.name), tc.args)
			require.NoError(t, err)
			require.NotNil(t, result)
			_, err = json.Marshal(result)
			require.NoError(t, err)
		})
	}

	t.Run("galaxy_query_reader_boundary", func(t *testing.T) {
		result := invokeMap(t, exec, ctx, ToolGalaxyQuery, map[string]any{"query": "SELECT current_user"})
		require.NotContains(t, result, "error")
		rows := result["rows"].([]map[string]any)
		require.Equal(t, "galaxy_reader", rows[0]["current_user"])

		result = invokeMap(t, exec, ctx, ToolGalaxyQuery, map[string]any{"query": "SELECT count(*) FROM galaxy.system_catalog"})
		require.NotContains(t, result, "error")
		require.Equal(t, 1, result["row_count"])

		result = invokeMap(t, exec, ctx, ToolGalaxyQuery, map[string]any{"query": "SELECT count(*) FROM feed.messages"})
		require.Contains(t, result, "error")
	})

	historySystem := requireSampleString(t, ctx, historyClient.Pool(), `
SELECT system_name
FROM feed.powerplay_hourly
ORDER BY bucket DESC
LIMIT 1`)

	for _, tc := range []struct {
		name ToolName
		args map[string]any
	}{
		{ToolGalaxyHistory, map[string]any{"system_name": historySystem, "days": 1}},
		{ToolGalaxyPowerplayCycle, map[string]any{"system_name": historySystem, "cycle": 0}},
	} {
		t.Run(string(tc.name), func(t *testing.T) {
			result, err := exec.Invoke(ctx, string(tc.name), tc.args)
			require.NoError(t, err)
			require.NotNil(t, result)
		})
	}

	t.Run("history_requires_separate_client", func(t *testing.T) {
		noHistory := NewExecutor(nil, nil, nil, nil).WithGalaxyStore(galaxyStore)
		_, err := noHistory.Invoke(ctx, string(ToolGalaxyHistory), map[string]any{"system_name": historySystem})
		require.ErrorContains(t, err, "historical data not available")
	})

	t.Run("describe_tool", func(t *testing.T) {
		result, err := exec.Invoke(ctx, string(ToolDescribeTool), map[string]any{"tool_name": string(ToolGalaxySystem)})
		require.NoError(t, err)
		require.NotNil(t, result)
	})
}

type galaxySmokeSamples struct {
	system        string
	station       string
	carrier       string
	bodySystem    string
	signalSystem  string
	surfaceSystem string
	power         string
	faction       string
	marketID      int64
	powerSystem   string
	controlSystem string
	controlPower  string
}

func loadGalaxySmokeSamples(t *testing.T, ctx context.Context, galaxy *galaxystore.Store) galaxySmokeSamples {
	t.Helper()
	return galaxySmokeSamples{
		system:        requireSampleString(t, ctx, galaxy, `SELECT c.name FROM galaxy.system_catalog c JOIN galaxy.system s ON s.id64=c.id64 LIMIT 1`),
		station:       requireSampleString(t, ctx, galaxy, `SELECT name FROM galaxy.station ORDER BY market_id LIMIT 1`),
		carrier:       requireSampleString(t, ctx, galaxy, `SELECT carrier_id FROM galaxy.fleet_carrier ORDER BY carrier_id LIMIT 1`),
		bodySystem:    requireSampleString(t, ctx, galaxy, `SELECT c.name FROM galaxy.system_catalog c JOIN galaxy.body b ON b.system_id64=c.id64 LIMIT 1`),
		signalSystem:  requireSampleString(t, ctx, galaxy, `SELECT c.name FROM galaxy.system_catalog c JOIN galaxy.signal s ON s.system_id64=c.id64 LIMIT 1`),
		surfaceSystem: requireSampleString(t, ctx, galaxy, `SELECT c.name FROM galaxy.system_catalog c JOIN galaxy.surface_site s ON s.system_id64=c.id64 LIMIT 1`),
		power:         requireSampleString(t, ctx, galaxy, `SELECT name FROM galaxy.power ORDER BY name LIMIT 1`),
		faction:       requireSampleString(t, ctx, galaxy, `SELECT name FROM galaxy.faction ORDER BY name LIMIT 1`),
		marketID: requireSampleInt64(t, ctx, galaxy, `
SELECT market_id
FROM galaxy.market_commodity
ORDER BY market_id
LIMIT 1`),
		powerSystem: requireSampleString(t, ctx, galaxy, `
SELECT c.name
FROM galaxy.system_power sp
JOIN galaxy.system_catalog c ON c.id64=sp.system_id64
WHERE sp.power_name IS NOT NULL
LIMIT 1`),
		controlSystem: requireSampleString(t, ctx, galaxy, `
SELECT c.name
FROM galaxy.system_power sp
JOIN galaxy.system_catalog c ON c.id64=sp.system_id64
WHERE sp.power_name IS NOT NULL
  AND sp.powerplay_state IN ('Fortified','Stronghold')
ORDER BY c.id64
LIMIT 1`),
		controlPower: requireSampleString(t, ctx, galaxy, `
SELECT sp.power_name
FROM galaxy.system_power sp
WHERE sp.power_name IS NOT NULL
  AND sp.powerplay_state IN ('Fortified','Stronghold')
ORDER BY sp.system_id64
LIMIT 1`),
	}
}

func requireSampleString(t *testing.T, ctx context.Context, source interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, sql string) string {
	t.Helper()
	var out string
	require.NoError(t, source.QueryRow(ctx, sql).Scan(&out))
	require.NotEmpty(t, out)
	return out
}

func requireSampleInt64(t *testing.T, ctx context.Context, source interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, sql string) int64 {
	t.Helper()
	var out int64
	require.NoError(t, source.QueryRow(ctx, sql).Scan(&out))
	require.Positive(t, out)
	return out
}

func invokeMap(t *testing.T, exec *Executor, ctx context.Context, name ToolName, args map[string]any) map[string]any {
	t.Helper()
	result, err := exec.Invoke(ctx, string(name), args)
	require.NoError(t, err)
	out, ok := result.(map[string]any)
	require.True(t, ok, "tool %s returned %T", name, result)
	return out
}

func requireIntegrationDSN(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	require.NotEmpty(t, value, "%s is required for the MR7 integration smoke", name)
	return value
}

func newStoreClientFromDSN(t *testing.T, dsn, schema string) *store.Client {
	t.Helper()
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	password, _ := parsed.User.Password()
	port := 5432
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		require.NoError(t, err)
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	client, err := store.New(context.Background(), store.Config{
		Enabled:  true,
		Host:     host,
		Port:     port,
		User:     parsed.User.Username(),
		Password: password,
		Database: parsed.Path[1:],
		Schema:   schema,
		PoolSize: 2,
	})
	require.NoError(t, err)
	return client
}
