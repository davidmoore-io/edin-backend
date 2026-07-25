package galaxystore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestMemgraphRetirementProjectionIntegration(t *testing.T) {
	dsn := os.Getenv("GALAXY_TEST_DSN")
	if dsn == "" {
		t.Skip("set GALAXY_TEST_DSN for Memgraph retirement projection test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const (
		systemID       = int64(922337203685410001)
		nearbyID       = int64(922337203685410002)
		factionID      = -710001
		faction2ID     = -710002
		marketID       = int64(922337203685410101)
		depotID        = int64(922337203685410102)
		nullDistanceID = int64(922337203685410103)
	)
	when := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)

	_, err = pool.Exec(ctx, `
INSERT INTO galaxy.system_catalog (id64,x,y,z,first_seen,name) VALUES
	($1,0,0,0,$6,'MR0 Contract'),
	($2,5,0,0,$6,'MR0 % Literal')
ON CONFLICT (id64) DO UPDATE SET x=EXCLUDED.x,y=EXCLUDED.y,z=EXCLUDED.z,name=EXCLUDED.name;
INSERT INTO galaxy.system (id64,population,last_event_time,name) VALUES
	($1,1000,$6,'MR0 Contract'),
	($2,1000,$6,'MR0 % Literal')
ON CONFLICT (id64) DO UPDATE SET population=EXCLUDED.population,last_event_time=EXCLUDED.last_event_time,name=EXCLUDED.name;
INSERT INTO galaxy.system_power (system_id64,last_event_time,power_name,powerplay_state)
VALUES ($1,$6,'Nakato Kaine','Stronghold')
ON CONFLICT (system_id64) DO UPDATE SET last_event_time=EXCLUDED.last_event_time,power_name=EXCLUDED.power_name,powerplay_state=EXCLUDED.powerplay_state;
INSERT INTO galaxy.faction (faction_id,last_event_time,name,allegiance,government)
OVERRIDING SYSTEM VALUE VALUES
	($3,$8,'Alpha Faction','Independent','Democracy'),
	($7,$8,'Beta Faction','Independent','Democracy')
ON CONFLICT (faction_id) DO UPDATE SET last_event_time=EXCLUDED.last_event_time,name=EXCLUDED.name;
INSERT INTO galaxy.system_faction
	(system_id64,faction_id,influence,last_event_time,state,happiness,active_states,pending_states)
VALUES
	($1,$3,0.75,$8,'Boom','Happy',ARRAY['Boom'],ARRAY['Expansion']),
	($1,$7,0.25,'0001-01-01T00:00:00Z','',NULL,'{}','{}')
ON CONFLICT (system_id64,faction_id) DO UPDATE SET
	influence=EXCLUDED.influence,last_event_time=EXCLUDED.last_event_time,state=EXCLUDED.state,
	happiness=EXCLUDED.happiness,active_states=EXCLUDED.active_states,pending_states=EXCLUDED.pending_states;
INSERT INTO galaxy.station
	(market_id,system_id64,last_event_time,dist_from_star_ls,controlling_faction_id,
	 large_pads,name,station_type,kind,services)
VALUES
	($4,$1,$8,42,$3,1,'Contract Orbital','Coriolis','station',ARRAY['Market','Shipyard']),
	($5,$1,$8,1,$3,1,'Contract Depot','Coriolis','space_depot',ARRAY['Market']),
	($9,$1,$8,NULL,$3,1,'A Null Distance','Orbis','station','{}')
ON CONFLICT (market_id) DO UPDATE SET name=EXCLUDED.name,kind=EXCLUDED.kind,last_event_time=EXCLUDED.last_event_time;
INSERT INTO galaxy.station_stub (system_id64,last_event_time,name,type) VALUES
	($1,$8::timestamptz - interval '1 hour','Contract Stub','MegaShip'),
	($1,$8::timestamptz - interval '1 hour','Contract Orbital','Outpost')
ON CONFLICT (system_id64,name) DO UPDATE SET last_event_time=EXCLUDED.last_event_time,type=EXCLUDED.type;
INSERT INTO galaxy.market (market_id,last_event_time,station_name,system_name)
VALUES ($4,$8,'Contract Orbital','MR0 Contract')
ON CONFLICT (market_id) DO UPDATE SET last_event_time=EXCLUDED.last_event_time;
INSERT INTO galaxy.shipyard (market_id,last_event_time)
VALUES ($4,$8)
ON CONFLICT (market_id) DO UPDATE SET last_event_time=EXCLUDED.last_event_time;
`, systemID, nearbyID, factionID, marketID, depotID, when, faction2ID, when, nullDistanceID)
	require.NoError(t, err)

	store := New(pool)

	factions, err := store.GetFactionsInSystem(ctx, "MR0 Contract")
	require.NoError(t, err)
	require.Len(t, factions, 2)
	require.Equal(t, "Alpha Faction", factions[0].FactionName)
	require.Equal(t, 0.75, factions[0].Influence)
	require.Equal(t, "Beta Faction", factions[1].FactionName)
	require.True(t, factions[1].LastEventTime.Equal(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)))

	factions, err = store.GetFactionsInSystem(ctx, "mr0 contract")
	require.NoError(t, err)
	require.Empty(t, factions)

	stations, err := store.GetStationsInSystem(ctx, "MR0 Contract")
	require.NoError(t, err)
	require.Len(t, stations, 3)
	require.Equal(t, "A Null Distance", stations[0].Name)
	require.Equal(t, float64(0), stations[0].DistanceLS)
	require.Equal(t, "Contract Stub", stations[1].Name)
	require.Equal(t, int64(0), stations[1].ID64)
	require.Equal(t, "Contract Orbital", stations[2].Name)
	require.True(t, stations[2].HasMarket)
	require.True(t, stations[2].HasShipyard)

	stations, err = store.GetStationsInSystem(ctx, "mr0 contract")
	require.NoError(t, err)
	require.Empty(t, stations)

	factions, err = store.GetFactionsInSystem(ctx, "MR0 % Literal")
	require.NoError(t, err)
	require.Empty(t, factions)

	search, err := store.SearchSystems(ctx, "MR0 %", 10)
	require.NoError(t, err)
	require.Len(t, search, 1)
	require.Equal(t, "MR0 % Literal", search[0].Name)

	projection, err := store.GetSurveyProjection(ctx, []string{"MR0 Contract", "MR0 Contract"}, "MR0 % Literal")
	require.NoError(t, err)
	require.Equal(t, 2, projection.AnchorsUsed)
	require.NotNil(t, projection.Start)
	require.Len(t, projection.Candidates, 1)
	require.Equal(t, "MR0 Contract", projection.Candidates[0].Name)
	require.Len(t, projection.Candidates[0].Stations, 2)
	require.Equal(t, "A Null Distance", projection.Candidates[0].Stations[0].Name)
	require.Equal(t, "Contract Orbital", projection.Candidates[0].Stations[1].Name)

	projection, err = store.GetSurveyProjection(ctx, []string{"Unknown Anchor"}, "")
	require.NoError(t, err)
	require.Equal(t, 0, projection.AnchorsUsed)
	require.Empty(t, projection.Candidates)

	_, err = store.GetSurveyProjection(ctx, []string{"MR0 Contract"}, "unknown start")
	require.ErrorIs(t, err, ErrSystemNotFound)

	tx, err := store.BeginRepeatableReadOnly(ctx)
	require.NoError(t, err)
	var isolation, readOnly string
	require.NoError(t, tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&isolation))
	require.NoError(t, tx.QueryRow(ctx, "SHOW transaction_read_only").Scan(&readOnly))
	require.Equal(t, "repeatable read", isolation)
	require.Equal(t, "on", readOnly)
	require.NoError(t, tx.Rollback(ctx))

	require.ErrorContains(t, store.ProbeReader(ctx), "want galaxy_reader")
	readerTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer readerTx.Rollback(ctx) //nolint:errcheck
	_, err = readerTx.Exec(ctx, "SET LOCAL ROLE galaxy_reader")
	require.NoError(t, err)
	require.NoError(t, newWithQuerier(readerTx).ProbeReader(ctx))

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.GetFactionsInSystem(cancelled, "MR0 Contract")
	require.Error(t, err)
}
