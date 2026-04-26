//go:build integration

// Package edinbot_test contains the cross-package integration test for the
// edin-bot framework. It wires scheduler + publisher + a real PostgresStore
// (testcontainer-backed) and exercises the full lifecycle (post → edit →
// noop → strike → unstrike) against a scriptable fake control-API and a
// FakeDiscordClient.
//
// Run via: make test-edin-bot-integration
package edinbot_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/authclient"
	"github.com/edin-space/edin-backend/internal/edinbot/bindings"
	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/edin-space/edin-backend/internal/edinbot/publisher"
	"github.com/edin-space/edin-backend/internal/edinbot/scheduler"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
	"github.com/edin-space/edin-backend/internal/testutil"
	"github.com/stretchr/testify/require"
)

// TestMain disables testcontainers' ryuk reaper. Ryuk requires
// /var/run/docker.sock which is not available in rootless podman; disabling
// it is safe because each test starts its own container via t.Cleanup.
func TestMain(m *testing.M) {
	os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true") //nolint:errcheck
	os.Exit(m.Run())
}

// startFakeOAuth returns 1h tokens at /application/o/token/.
func startFakeOAuth(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-1","token_type":"bearer","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/application/o/token/"
}

// fakeKaine is a scriptable httptest server that toggles its plasmium-buyers
// response between phases via atomic.Int32. Phases:
//
//	0: Sol with one buyer @ 280k
//	1: Sol with one buyer @ 290k (state hash changes → publisher edits)
//	2: same as phase 1 (no change → publisher noop)
//	3: empty buyers (Sol disappears → publisher strikes)
//	4: Sol returns @ 295k (publisher unstrikes)
type fakeKaine struct {
	srv   *httptest.Server
	phase atomic.Int32
}

func startFakeKaine(t *testing.T) *fakeKaine {
	t.Helper()
	f := &fakeKaine{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/kaine/mining/plasmium-buyers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch f.phase.Load() {
		case 0:
			_, _ = w.Write([]byte(`{"maps":[{"system_name":"M","buyers":[{"system_name":"Sol","station_name":"Galileo","faction":"x","faction_state":"Boom","platinum_demand":1500,"platinum_price":280000,"score":175}]}],"generated_at":"2026-04-26T14:23:00Z","total_maps":1,"total_buyers":1}`))
		case 1:
			_, _ = w.Write([]byte(`{"maps":[{"system_name":"M","buyers":[{"system_name":"Sol","station_name":"Galileo","faction":"x","faction_state":"Boom","platinum_demand":1500,"platinum_price":290000,"score":180}]}],"generated_at":"2026-04-26T14:38:00Z","total_maps":1,"total_buyers":1}`))
		case 2:
			_, _ = w.Write([]byte(`{"maps":[{"system_name":"M","buyers":[{"system_name":"Sol","station_name":"Galileo","faction":"x","faction_state":"Boom","platinum_demand":1500,"platinum_price":290000,"score":180}]}],"generated_at":"2026-04-26T14:53:00Z","total_maps":1,"total_buyers":1}`))
		case 3:
			_, _ = w.Write([]byte(`{"maps":[{"system_name":"M","buyers":[]}],"generated_at":"2026-04-26T15:08:00Z","total_maps":1,"total_buyers":0}`))
		case 4:
			_, _ = w.Write([]byte(`{"maps":[{"system_name":"M","buyers":[{"system_name":"Sol","station_name":"Galileo","faction":"x","faction_state":"Boom","platinum_demand":1700,"platinum_price":295000,"score":190}]}],"generated_at":"2026-04-26T15:23:00Z","total_maps":1,"total_buyers":1}`))
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newPgStore(t *testing.T) (store.Store, context.Context) {
	t.Helper()
	pool, _ := testutil.StartTestDB(t)
	ctx := context.Background()
	require.NoError(t, store.MigrateDiscordSchema(ctx, pool))
	return store.NewPostgresStore(pool), ctx
}

// TestEdinBot_FullLifecycle exercises the whole framework against a
// testcontainer postgres, scriptable control-API, and FakeDiscordClient.
// Asserts each lifecycle phase produces the right Discord action and the
// right persisted state.
func TestEdinBot_FullLifecycle_PostEditNoopStrikeUnstrike(t *testing.T) {
	st, ctx := newPgStore(t)
	dc := discordclient.NewFakeDiscordClient()
	tokenURL := startFakeOAuth(t)
	kaine := startFakeKaine(t)

	auth := authclient.New(authclient.Config{
		TokenURL: tokenURL, ClientID: "cid", ClientSecret: "csec",
	})
	control := controlclient.New(kaine.srv.URL, auth)

	platinum := features.NewPlatinumBoomAlerts(control)
	platinum.SetRetryIntervals([]time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond})

	originalRegistry := features.Registry
	features.Registry = map[string]features.Feature{"platinum-boom-alerts": platinum}
	t.Cleanup(func() { features.Registry = originalRegistry })

	bnd := bindings.Binding{
		ID:           "kaine-platinum-boom",
		GuildID:      "g",
		ChannelID:    "c",
		FeatureName:  "platinum-boom-alerts",
		PollInterval: 100 * time.Millisecond,
		IsPoll:       true,
	}
	bus := scheduler.NewOpsBus()
	pub := publisher.New(st, dc)
	sch := scheduler.New(scheduler.Config{
		Bindings:  []bindings.Binding{bnd},
		Publisher: pub,
		Store:     st,
		Bus:       bus,
		StaggerMs: 0,
	})
	require.NoError(t, sch.Start(ctx))
	defer sch.Stop(2 * time.Second)

	// Phase 0: post.
	require.Eventually(t, func() bool {
		return len(dc.PostCalls()) >= 1
	}, 5*time.Second, 20*time.Millisecond, "phase 0: must POST initial message")
	dc.Reset()

	// Phase 1: edit (price changed → hash changes).
	kaine.phase.Store(1)
	require.Eventually(t, func() bool {
		return len(dc.EditCalls()) >= 1
	}, 5*time.Second, 20*time.Millisecond, "phase 1: must EDIT on hash change")
	dc.Reset()

	// Phase 2: noop (same hash). Wait a couple of tick cycles.
	kaine.phase.Store(2)
	time.Sleep(350 * time.Millisecond)
	require.Empty(t, dc.PostCalls(), "phase 2: noop must NOT POST")
	require.Empty(t, dc.EditCalls(), "phase 2: noop must NOT EDIT")
	dc.Reset()

	// Phase 3: strike (Sol disappears).
	kaine.phase.Store(3)
	require.Eventually(t, func() bool {
		return len(dc.EditCalls()) >= 1
	}, 5*time.Second, 20*time.Millisecond, "phase 3: must EDIT (strikethrough) on disappearance")

	posted, err := st.GetPosted(ctx, "kaine-platinum-boom")
	require.NoError(t, err)
	require.NotNil(t, posted["system:Sol"].StruckAt, "struck_at must be set after strike")
	dc.Reset()

	// Phase 4: unstrike (Sol returns).
	kaine.phase.Store(4)
	require.Eventually(t, func() bool {
		return len(dc.EditCalls()) >= 1
	}, 5*time.Second, 20*time.Millisecond, "phase 4: must EDIT (unstrike) on reappearance")

	posted, err = st.GetPosted(ctx, "kaine-platinum-boom")
	require.NoError(t, err)
	require.Nil(t, posted["system:Sol"].StruckAt, "struck_at must clear after unstrike")
	require.NotNil(t, posted["system:Sol"].UnstruckAt)
}

// Restart safety: bring up scheduler #2 against the same DB and confirm no
// double-posts. Spec §3 'Restart safety' / acceptance criterion 8.
func TestEdinBot_RestartSafety_NoDoublePosts(t *testing.T) {
	st, ctx := newPgStore(t)
	dc := discordclient.NewFakeDiscordClient()
	tokenURL := startFakeOAuth(t)
	kaine := startFakeKaine(t)

	auth := authclient.New(authclient.Config{TokenURL: tokenURL, ClientID: "c", ClientSecret: "s"})
	control := controlclient.New(kaine.srv.URL, auth)
	feat := features.NewPlatinumBoomAlerts(control)
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})

	originalRegistry := features.Registry
	features.Registry = map[string]features.Feature{"platinum-boom-alerts": feat}
	t.Cleanup(func() { features.Registry = originalRegistry })

	bnd := bindings.Binding{
		ID:           "k",
		GuildID:      "g",
		ChannelID:    "c",
		FeatureName:  "platinum-boom-alerts",
		PollInterval: 100 * time.Millisecond,
		IsPoll:       true,
	}

	// First "process lifetime" — first post lands.
	{
		s1 := scheduler.New(scheduler.Config{
			Bindings: []bindings.Binding{bnd}, Publisher: publisher.New(st, dc),
			Store: st, Bus: scheduler.NewOpsBus(), StaggerMs: 0,
		})
		require.NoError(t, s1.Start(ctx))
		require.Eventually(t, func() bool { return len(dc.PostCalls()) >= 1 }, 5*time.Second, 20*time.Millisecond)
		require.NoError(t, s1.Stop(time.Second))
	}
	require.Equal(t, 1, len(dc.PostCalls()), "first lifetime: exactly one post")

	// Second "process lifetime" — same DB, same kaine response.
	{
		dc.Reset() // forget post calls but DB still has the row
		s2 := scheduler.New(scheduler.Config{
			Bindings: []bindings.Binding{bnd}, Publisher: publisher.New(st, dc),
			Store: st, Bus: scheduler.NewOpsBus(), StaggerMs: 0,
		})
		require.NoError(t, s2.Start(ctx))
		// Wait long enough for several poll cycles.
		time.Sleep(500 * time.Millisecond)
		require.NoError(t, s2.Stop(time.Second))
	}
	require.Empty(t, dc.PostCalls(), "second lifetime: NO new posts (existing row → noop)")
}

// Channel-deleted: publisher disables binding, scheduler stops calling.
func TestEdinBot_ChannelDeleted_DisablesBinding(t *testing.T) {
	st, ctx := newPgStore(t)
	dc := discordclient.NewFakeDiscordClient()
	dc.PostErr = discordclient.ErrChannelGone

	tokenURL := startFakeOAuth(t)
	kaine := startFakeKaine(t)
	auth := authclient.New(authclient.Config{TokenURL: tokenURL, ClientID: "c", ClientSecret: "s"})
	control := controlclient.New(kaine.srv.URL, auth)
	feat := features.NewPlatinumBoomAlerts(control)
	feat.SetRetryIntervals([]time.Duration{10 * time.Millisecond})

	originalRegistry := features.Registry
	features.Registry = map[string]features.Feature{"platinum-boom-alerts": feat}
	t.Cleanup(func() { features.Registry = originalRegistry })

	bnd := bindings.Binding{
		ID: "kbad", GuildID: "g", ChannelID: "c",
		FeatureName: "platinum-boom-alerts", PollInterval: 100 * time.Millisecond, IsPoll: true,
	}
	s := scheduler.New(scheduler.Config{
		Bindings: []bindings.Binding{bnd}, Publisher: publisher.New(st, dc),
		Store: st, Bus: scheduler.NewOpsBus(), StaggerMs: 0,
	})
	require.NoError(t, s.Start(ctx))
	defer s.Stop(2 * time.Second)

	require.Eventually(t, func() bool {
		d, _ := st.IsBindingDisabled(ctx, "kbad")
		return d
	}, 5*time.Second, 20*time.Millisecond, "binding must be disabled after ErrChannelGone")
}
