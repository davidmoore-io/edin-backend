package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/galaxystore"
	"github.com/stretchr/testify/require"
)

func TestModalWireShapeMatchesMR0Fixture(t *testing.T) {
	when := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	payload := map[string]any{
		"system_name": "MR0 Contract",
		"factions": modalFactions([]galaxystore.FactionPresence{{
			FactionName:   "Alpha Faction",
			SystemName:    "MR0 Contract",
			Influence:     0.75,
			State:         "Boom",
			ActiveStates:  []string{"Boom"},
			PendingStates: []string{"Expansion"},
			Happiness:     "Happy",
			LastEventTime: when,
		}}),
		"stations": modalStations([]galaxystore.StationData{
			{
				ID64:           0,
				Name:           "Contract Stub",
				Type:           "MegaShip",
				SystemName:     "MR0 Contract",
				LastEDDNUpdate: when.Add(-time.Hour),
			},
			{
				ID64:               700001,
				Name:               "Contract Orbital",
				Type:               "Coriolis",
				SystemID64:         999999, // Must never reach the modal wire contract.
				SystemName:         "MR0 Contract",
				DistanceLS:         42,
				MaxPad:             "L",
				Services:           []string{"Market", "Shipyard"},
				ControllingFaction: "Alpha Faction",
				HasMarket:          true,
				HasShipyard:        true,
				LastEDDNUpdate:     when,
			},
			{
				ID64:       700002,
				Name:       "Transient Carrier",
				Type:       "FLEETCARRIER",
				SystemName: "MR0 Contract",
			},
		}),
	}

	got, err := json.Marshal(payload)
	require.NoError(t, err)
	want, err := os.ReadFile("../../docs/plans/2026-07-25-memgraph-api-retirement/contract-fixtures/modal-shape-populated.json")
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}

func TestModalBranchesRequireGalaxyStore(t *testing.T) {
	server := &Server{cfg: &config.Config{}}
	for _, endpoint := range []string{"factions", "stations"} {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/edin/systems/Sol/"+endpoint, nil)
			rr := httptest.NewRecorder()
			server.handleEDINSystemHistory(rr, req)
			require.Equal(t, http.StatusServiceUnavailable, rr.Code)
			require.JSONEq(t, `{"error":"Galaxy database not configured"}`, rr.Body.String())
		})
	}
}
