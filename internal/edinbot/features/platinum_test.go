package features_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/stretchr/testify/require"
)

type fakeTokenSource struct{}

func (f *fakeTokenSource) Token(ctx context.Context) (string, error) { return "test-token", nil }

func TestPlatinumBoomAlerts_HappyPath_BuildsItemsFromBuyers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/kaine/mining/plasmium-buyers", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"maps": [{
				"system_name": "MapSystem",
				"buyers": [
					{"system_name":"Sol","station_name":"Galileo","faction":"FedX","faction_state":"Boom","platinum_demand":1500,"platinum_price":280000,"score":175,"market_updated_at":"2026-04-26T13:00:00Z"},
					{"system_name":"Sol","station_name":"Daedalus","faction":"FedX","faction_state":"Boom","platinum_demand":800,"platinum_price":250000,"score":110,"market_updated_at":"2026-04-26T13:00:00Z"}
				]
			}],
			"generated_at": "2026-04-26T14:23:01Z",
			"total_maps": 1,
			"total_buyers": 2
		}`))
	}))
	defer srv.Close()

	feat := features.NewPlatinumBoomAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})
	snap, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.NoError(t, err)
	require.True(t, snap.Healthy)

	// One Item per buyer SYSTEM (not per station).
	require.Len(t, snap.Items, 1)
	require.Equal(t, "system:Sol", snap.Items[0].Identity())

	embed := snap.Items[0].Render()
	bodyText := embed.Description
	require.Contains(t, bodyText, "Sol", "sell system appears in Sell at line")
	require.Contains(t, bodyText, "Galileo")
	require.Contains(t, bodyText, "Daedalus")
	require.Contains(t, bodyText, "MapSystem", "mine system appears in Mine at line")
	require.Contains(t, bodyText, "280,000c/t", "platinum price in price line")
	require.Contains(t, bodyText, "Rapid acquisition mining alert")
	require.Contains(t, bodyText, "`Platinum:`", "label as backtick token")
}

func TestPlatinumBoomAlerts_DedupExcludeOCSAndSortByScore(t *testing.T) {
	// HIP 61332 sits inside several mining bubbles → same buyer arrives via
	// multiple maps. Plus an "Orbital Construction Site" entry that must be
	// dropped (cargo can't dock there). Two real stations remain, and they
	// should be ordered by descending score.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"maps": [
				{"system_name":"Bubble1","buyers":[
					{"system_name":"HIP 61332","station_name":"Leeuwenhoek","faction_state":"Boom","platinum_demand":554,"platinum_price":193000,"score":2,"market_updated_at":"2026-04-26T13:00:00Z"}
				]},
				{"system_name":"Bubble2","buyers":[
					{"system_name":"HIP 61332","station_name":"Leeuwenhoek","faction_state":"Boom","platinum_demand":554,"platinum_price":193000,"score":2,"market_updated_at":"2026-04-26T13:00:00Z"},
					{"system_name":"HIP 61332","station_name":"Orbital Construction Site: Galeen City","faction_state":"Boom","platinum_demand":0,"score":40,"market_updated_at":"2026-04-26T13:00:00Z"}
				]},
				{"system_name":"Bubble3","buyers":[
					{"system_name":"HIP 61332","station_name":"Top Tier","faction_state":"Boom","platinum_demand":1000,"platinum_price":250000,"score":100,"market_updated_at":"2026-04-26T13:00:00Z"}
				]}
			],
			"generated_at": "2026-04-26T14:23:01Z","total_maps":3,"total_buyers":4
		}`))
	}))
	defer srv.Close()

	feat := features.NewPlatinumBoomAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})
	snap, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.NoError(t, err)
	require.Len(t, snap.Items, 1)

	embed := snap.Items[0].Render()
	body := embed.Description
	require.Contains(t, body, "Top Tier")
	require.Contains(t, body, "Leeuwenhoek")
	require.NotContains(t, body, "Orbital Construction Site",
		"OCS must be excluded from the rendered description")

	// Highest score first: "Top Tier" line must appear before "Leeuwenhoek".
	require.Less(t, indexOf(body, "Top Tier"), indexOf(body, "Leeuwenhoek"),
		"highest-score station must appear first")
}

// indexOf is strings.Index re-exposed inline so tests don't import strings
// solely for this single assertion.
func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestPlatinumBoomAlerts_FreshnessFilter(t *testing.T) {
	// MaxFreshness=24h. Three buyers: one fresh, one stale, one with no
	// market timestamp at all. Only the fresh one should survive.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"maps": [{
				"system_name": "M",
				"buyers": [
					{"system_name":"Sol","station_name":"Galileo","faction_state":"Boom","platinum_demand":1500,"platinum_price":280000,"score":40,"market_updated_at":"2026-04-26T13:00:00Z"},
					{"system_name":"Sol","station_name":"AncientPort","faction_state":"Boom","platinum_demand":1000,"platinum_price":280000,"score":40,"market_updated_at":"2026-04-20T14:00:00Z"},
					{"system_name":"Sol","station_name":"Bayliss","faction_state":"Boom","platinum_demand":500,"platinum_price":0,"score":40,"bgs_updated_at":"2026-04-26T13:00:00Z"}
				]
			}],
			"generated_at": "2026-04-26T14:23:01Z",
			"total_maps": 1, "total_buyers": 3
		}`))
	}))
	defer srv.Close()

	feat := features.NewPlatinumBoomAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})
	snap, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.NoError(t, err)
	require.Len(t, snap.Items, 1, "Sol survives because Galileo is fresh")

	body := snap.Items[0].Render().Description
	require.Contains(t, body, "Galileo")
	require.NotContains(t, body, "AncientPort", "stale (>24h) buyer must be dropped")
	require.NotContains(t, body, "Bayliss", "buyer with no MarketUpdatedAt must be dropped")
}

func TestPlatinumBoomAlerts_StructurallyInvalidResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	feat := features.NewPlatinumBoomAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})
	_, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.Error(t, err, "structurally invalid envelope must fail the cycle")
}

func TestPlatinumBoomAlerts_HTTPError_RetriesThenErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	feat := features.NewPlatinumBoomAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond})
	_, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.Error(t, err)
}

func TestPlatinumBoomAlerts_EmptyResponseIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"maps":[],"generated_at":"2026-04-26T14:23:01Z","total_maps":0,"total_buyers":0}`))
	}))
	defer srv.Close()

	feat := features.NewPlatinumBoomAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})
	snap, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.NoError(t, err, "empty results IS valid (quiet day, per spec §6)")
	require.True(t, snap.Healthy)
	require.Empty(t, snap.Items)
}

func TestPlatinumBoomAlerts_StateHashChangesOnPriceChange(t *testing.T) {
	a := features.BuildPlatinumItemForTest("Sol", []controlclient.Buyer{
		{StationName: "Galileo", PlatinumPrice: 280000, PlatinumDemand: 1500, Score: 175},
	})
	b := features.BuildPlatinumItemForTest("Sol", []controlclient.Buyer{
		{StationName: "Galileo", PlatinumPrice: 290000, PlatinumDemand: 1500, Score: 175},
	})
	require.NotEqual(t, a.StateHash(), b.StateHash(), "price change must change the hash")
}

func TestPlatinumBoomAlerts_RetryAndBackoffOnTransientError(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			http.Error(w, "transient", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"maps":[],"generated_at":"2026-04-26T14:23:01Z","total_maps":0,"total_buyers":0}`))
	}))
	defer srv.Close()

	feat := features.NewPlatinumBoomAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := feat.Poll(ctx, feat.DefaultConfig())
	require.NoError(t, err, "retries must succeed within the deadline")
	require.GreaterOrEqual(t, hits, 3)
}

// LTD parallels.

func TestLTDAlerts_HappyPath_BuildsItemsFromBuyers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/kaine/mining/ltd-buyers", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"maps": [{
				"system_name": "MapSystem",
				"buyers": [{"system_name":"Sol","station_name":"Galileo","faction":"FedX","faction_state":"Expansion","ltd_demand":1300,"ltd_price":950000,"score":165,"market_updated_at":"2026-04-26T13:00:00Z"}]
			}],
			"generated_at": "2026-04-26T14:23:01Z",
			"total_maps": 1,
			"total_buyers": 1
		}`))
	}))
	defer srv.Close()

	feat := features.NewLTDAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})
	snap, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.NoError(t, err)
	require.Len(t, snap.Items, 1)
	require.Equal(t, "system:Sol", snap.Items[0].Identity())

	embed := snap.Items[0].Render()
	bodyText := embed.Description
	require.Contains(t, bodyText, "Galileo")
	require.Contains(t, bodyText, "<t:", "freshness rendered as live <t:N:R>")
	require.Contains(t, bodyText, "Rapid acquisition mining alert")
}

func TestLTDAlerts_EmptyResponseIsHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"maps":[],"generated_at":"2026-04-26T14:23:01Z","total_maps":0,"total_buyers":0}`))
	}))
	defer srv.Close()

	feat := features.NewLTDAlerts(controlclient.New(srv.URL, &fakeTokenSource{}))
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})
	snap, err := feat.Poll(context.Background(), feat.DefaultConfig())
	require.NoError(t, err)
	require.True(t, snap.Healthy)
	require.Empty(t, snap.Items)
}

func TestLTDAlerts_StateHashChangesOnPriceChange(t *testing.T) {
	a := features.BuildLTDItemForTest("Sol", []controlclient.Buyer{
		{StationName: "Galileo", LTDPrice: 900000, LTDDemand: 1300, Score: 165},
	})
	b := features.BuildLTDItemForTest("Sol", []controlclient.Buyer{
		{StationName: "Galileo", LTDPrice: 920000, LTDDemand: 1300, Score: 165},
	})
	require.NotEqual(t, a.StateHash(), b.StateHash())
}

func TestLTDAlerts_ValidateRejectsUnknownKeys(t *testing.T) {
	feat := features.NewLTDAlerts(nil)
	require.NoError(t, feat.Validate(features.Config{}))
	require.Error(t, feat.Validate(features.Config{"bogus": 1}))
}
