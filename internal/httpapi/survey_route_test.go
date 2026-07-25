package httpapi

import (
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/stretchr/testify/require"
)

func TestSurveySystemsFromProjection(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-2 * time.Hour)
	got := surveySystemsFromProjection(&galaxystore.SurveyProjection{
		Candidates: []galaxystore.SurveyCandidate{
			{
				Name:       "Updated",
				X:          1,
				Y:          2,
				Z:          3,
				LastUpdate: &updated,
				Stations: []galaxystore.SurveyStation{
					{Name: "Near", Type: "Coriolis", DistanceLS: 10},
				},
			},
			{Name: "Never", X: 4, Y: 5, Z: 6},
		},
	}, now)

	require.Len(t, got, 2)
	require.Equal(t, 2.0, got[0].Staleness)
	require.Equal(t, "Near", got[0].Stations[0].Name)
	require.Equal(t, 999999.0, got[1].Staleness)
	require.Empty(t, got[1].Stations)
}

func TestNearestNeighbourRouteFromExplicitStart(t *testing.T) {
	start := &surveySystem{Name: "Start", X: 0, Y: 0, Z: 0}
	got := nearestNeighbourRoute([]surveySystem{
		{Name: "Far", X: 20, Y: 0, Z: 0},
		{Name: "Near", X: 3, Y: 4, Z: 0},
	}, start)

	require.Equal(t, []string{"Near", "Far"}, []string{got[0].Name, got[1].Name})
	require.Equal(t, 1, got[0].Order)
	require.Equal(t, 5.0, got[0].DistFromPreviousLy)
	require.Equal(t, 2, got[1].Order)
	require.Equal(t, 17.5, got[1].DistFromPreviousLy)
}
