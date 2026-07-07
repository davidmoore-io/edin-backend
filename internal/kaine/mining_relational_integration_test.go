package kaine

import (
	"context"
	"os"
	"testing"

	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestRelationalMiningSmoke(t *testing.T) {
	edinDSN := os.Getenv("EDIN_TEST_DSN")
	galaxyDSN := os.Getenv("GALAXY_TEST_DSN")
	if edinDSN == "" || galaxyDSN == "" {
		t.Skip("set EDIN_TEST_DSN and GALAXY_TEST_DSN to run relational mining smoke")
	}

	ctx := context.Background()
	edinPool, err := pgxpool.New(ctx, edinDSN)
	require.NoError(t, err)
	defer edinPool.Close()

	galaxyPool, err := pgxpool.New(ctx, galaxyDSN)
	require.NoError(t, err)
	defer galaxyPool.Close()

	store := NewStore(edinPool)
	galaxy := galaxystore.New(galaxyPool)

	plasmium, err := store.FindPlasmiumBuyers(ctx, galaxy, nil)
	require.NoError(t, err)
	require.NotNil(t, plasmium)

	ltd, err := store.FindLTDBuyers(ctx, galaxy, nil)
	require.NoError(t, err)
	require.NotNil(t, ltd)

	targets, err := store.FindExpansionTargets(ctx, galaxy, nil)
	require.NoError(t, err)
	require.NotNil(t, targets)

	survey, err := store.SurveyExport(ctx, galaxy, nil)
	require.NoError(t, err)
	require.NotNil(t, survey)
}
