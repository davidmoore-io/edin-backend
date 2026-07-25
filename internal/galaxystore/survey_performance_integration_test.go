package galaxystore

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/kaine"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestSurveyProjectionPerformanceIntegration(t *testing.T) {
	galaxyDSN := os.Getenv("GALAXY_TEST_DSN")
	edinDSN := os.Getenv("EDIN_TEST_DSN")
	if galaxyDSN == "" || edinDSN == "" {
		t.Skip("set GALAXY_TEST_DSN and EDIN_TEST_DSN for the survey performance gate")
	}

	ctx := context.Background()
	galaxyPool, err := pgxpool.New(ctx, galaxyDSN)
	require.NoError(t, err)
	defer galaxyPool.Close()
	edinPool, err := pgxpool.New(ctx, edinDSN)
	require.NoError(t, err)
	defer edinPool.Close()

	mapSystems, err := kaine.NewStore(edinPool).GetMiningMapSystems(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, mapSystems)

	store := New(galaxyPool)
	_, err = store.GetSurveyProjection(ctx, mapSystems, "")
	require.NoError(t, err)

	durations := make([]time.Duration, 20)
	for i := range durations {
		start := time.Now()
		_, err := store.GetSurveyProjection(ctx, mapSystems, "")
		require.NoError(t, err)
		durations[i] = time.Since(start)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[18]
	t.Logf("anchors_requested=%d runs=%d p95=%s", len(mapSystems), len(durations), p95)
	require.LessOrEqual(t, p95, 2*time.Second)

	rows, err := galaxyPool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS) "+surveyCandidatesSQL, mapSystems)
	require.NoError(t, err)
	defer rows.Close()
	var planLines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		planLines = append(planLines, line)
	}
	require.NoError(t, rows.Err())
	plan := strings.Join(planLines, "\n")
	t.Log("SURVEY EXPLAIN\n" + plan)
	require.Contains(t, plan, "idx_catalog_loc")
}
