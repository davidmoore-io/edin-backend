package galaxystore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestSystemRowToSystemData(t *testing.T) {
	x, y, z := 1.25, -2.5, 3.75
	allegiance := "Alliance"
	power := "Nakato Kaine"
	state := "Stronghold"
	faction := "Test Faction"
	factionState := "Boom"
	when := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)

	row := systemRow{
		id64:                    42,
		name:                    "Test System",
		x:                       &x,
		y:                       &y,
		z:                       &z,
		population:              1000,
		allegiance:              &allegiance,
		controllingFaction:      &faction,
		controllingFactionState: &factionState,
		powerName:               &power,
		powerplayState:          &state,
		powers:                  []string{power},
		reinforcement:           12,
		undermining:             34,
		controlProgress:         56.5,
		conflictProgress:        []byte(`[{"power":"Aisling Duval","progress":10}]`),
		lastEDDNUpdate:          &when,
	}

	got := row.toSystemData()
	require.Equal(t, "Test System", got.Name)
	require.Equal(t, int64(42), got.ID64)
	require.Equal(t, &Coords{X: x, Y: y, Z: z}, got.Coordinates)
	require.Equal(t, "Nakato Kaine", got.ControllingPower)
	require.Equal(t, "Stronghold", got.PowerplayState)
	require.Equal(t, int64(12), got.Reinforcement)
	require.Equal(t, int64(34), got.Undermining)
	require.NotNil(t, got.ControlProgress)
	require.Equal(t, 56.5, *got.ControlProgress)
	require.Equal(t, "Test Faction", got.ControllingFaction)
	require.Equal(t, "Boom", got.ControllingFactionState)
	require.Len(t, got.PowerplayConflictProgress, 1)
	require.Equal(t, when, got.LastEDDNUpdate)
}

func TestSystemRowWithoutPowerplayOmitsControlProgress(t *testing.T) {
	row := systemRow{name: "Unpowered"}
	got := row.toSystemData()
	require.Nil(t, got.ControlProgress)
	require.Empty(t, got.PowerplayState)
}

func TestStoreSystemLookupIntegration(t *testing.T) {
	dsn := os.Getenv("GALAXY_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("TSDB_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("set GALAXY_TEST_DSN or TSDB_TEST_DSN for relational galaxy integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx) //nolint:errcheck

	store := newWithQuerier(tx)
	eventTime := time.Date(2026, 7, 7, 12, 30, 0, 0, time.UTC)
	factionID := -700001

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.system_catalog (id64, x, y, z, first_seen, name)
VALUES ($1, 1.5, 2.5, 3.5, $2, 'W5 Test System')`, int64(922337203685400001), eventTime)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.faction (faction_id, name, allegiance, government)
OVERRIDING SYSTEM VALUE
VALUES ($1, 'W5 Test Faction', 'Independent', 'Democracy')`, factionID)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.system (
	id64, population, last_event_time, last_faction_update, controlling_faction_id,
	name, allegiance, government, economy, security
) VALUES ($1, 12345, $2, $2, $3, 'W5 Test System', 'Independent', 'Democracy', 'Industrial', 'High')`, int64(922337203685400001), eventTime, factionID)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.system_power (
	system_id64, reinforcement, undermining, last_event_time, control_progress,
	power_name, powerplay_state, powers_present
) VALUES ($1, 7, 9, $2, 42.5, 'Nakato Kaine', 'Stronghold', ARRAY['Nakato Kaine'])`, int64(922337203685400001), eventTime)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.system_faction (
	system_id64, faction_id, influence, last_event_time, state, happiness, active_states, pending_states
) VALUES ($1, $3, 0.75, $2, 'Boom', 'Happy', ARRAY['Boom'], ARRAY['Expansion'])`, int64(922337203685400001), eventTime, factionID)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.station (
	market_id, system_id64, last_event_time, dist_from_star_ls, controlling_faction_id,
	large_pads, medium_pads, small_pads, name, station_type, kind, services
) VALUES (922337203685400002, $1, $2, 12.5, $3, 1, 0, 0, 'W5 Port', 'Coriolis', 'station', ARRAY['Market'])`, int64(922337203685400001), eventTime, factionID)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.market (market_id, last_event_time, station_name, system_name)
VALUES (922337203685400002, $1, 'W5 Port', 'W5 Test System')`, eventTime)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
INSERT INTO galaxy.fleet_carrier (
	carrier_id, current_system_id64, first_seen, last_event_time, jump_count, name
) VALUES ('W5T-001', $1, $2, $2, 2, 'W5 Carrier')`, int64(922337203685400001), eventTime)
	require.NoError(t, err)

	full, err := store.GetSystemFull(ctx, "w5 test system")
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "W5 Test System", full.System.Name)
	require.Equal(t, "Nakato Kaine", full.System.ControllingPower)
	require.Equal(t, "Boom", full.System.ControllingFactionState)
	require.Len(t, full.Factions, 1)
	require.Len(t, full.Stations, 1)
	require.Len(t, full.FleetCarriers, 1)

	bySlug, err := store.GetSystemFullBySlug(ctx, "W5TestSystem")
	require.NoError(t, err)
	require.Equal(t, full.System.ID64, bySlug.System.ID64)

	watch, err := store.GetSystemWatchSnapshot(ctx, "W5TestSystem")
	require.NoError(t, err)
	require.Equal(t, "W5TestSystem", watch.Slug)
	require.Equal(t, "Boom", watch.ControllingWatchFaction)
	require.Len(t, watch.Factions, 1)
}
