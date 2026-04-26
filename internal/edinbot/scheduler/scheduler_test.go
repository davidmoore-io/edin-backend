package scheduler_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/edinbot/bindings"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/edin-space/edin-backend/internal/edinbot/publisher"
	"github.com/edin-space/edin-backend/internal/edinbot/scheduler"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
	"github.com/stretchr/testify/require"
)

// memStore copy (separate package from publisher_test).
type memStore struct {
	mu       sync.Mutex
	posted   map[string]map[string]store.PostedMessage
	cycles   []store.PollCycle
	disabled map[string]time.Time
}

func newMemStore() *memStore {
	return &memStore{
		posted:   map[string]map[string]store.PostedMessage{},
		disabled: map[string]time.Time{},
	}
}

func (m *memStore) GetPosted(ctx context.Context, bid string) (map[string]store.PostedMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]store.PostedMessage{}
	for k, v := range m.posted[bid] {
		out[k] = v
	}
	return out, nil
}
func (m *memStore) UpsertPosted(ctx context.Context, p store.PostedMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.posted[p.BindingID]; !ok {
		m.posted[p.BindingID] = map[string]store.PostedMessage{}
	}
	m.posted[p.BindingID][p.Identity] = p
	return nil
}
func (m *memStore) MarkStruck(ctx context.Context, bid, id string, at time.Time) error    { return nil }
func (m *memStore) MarkUnstruck(ctx context.Context, bid, id string, at time.Time) error  { return nil }
func (m *memStore) UpdateLastSeen(ctx context.Context, bid string, ids []string, at time.Time) error {
	return nil
}
func (m *memStore) DisableBinding(ctx context.Context, bid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disabled[bid] = at
	return nil
}
func (m *memStore) IsBindingDisabled(ctx context.Context, bid string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.disabled[bid]
	return ok, nil
}
func (m *memStore) RecordPollCycle(ctx context.Context, c store.PollCycle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cycles = append(m.cycles, c)
	return nil
}
func (m *memStore) RecordDiagnoseReport(ctx context.Context, r store.DiagnoseReport) error {
	return nil
}

func (m *memStore) LatestSuccessAt(ctx context.Context, bid string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest time.Time
	for _, c := range m.cycles {
		if c.BindingID == bid && (c.Status == "success" || c.Status == "event") && c.TickedAt.After(latest) {
			latest = c.TickedAt
		}
	}
	return latest, nil
}

// stub PollFeature.
type tickPollFeature struct {
	calls atomic.Int64
}

func (f *tickPollFeature) Name() string                     { return "tick" }
func (f *tickPollFeature) DefaultConfig() features.Config   { return features.Config{} }
func (f *tickPollFeature) Validate(c features.Config) error { return nil }
func (f *tickPollFeature) Poll(ctx context.Context, c features.Config) (features.Snapshot, error) {
	f.calls.Add(1)
	return features.Snapshot{
		Items:       []features.Item{&schedItem{id: "system:Sol", hash: "h1"}},
		Healthy:     true,
		GeneratedAt: time.Now().UTC(),
	}, nil
}

type schedItem struct {
	id   string
	hash string
}

func (s *schedItem) Identity() string { return s.id }
func (s *schedItem) StateHash() string {
	x := sha256.Sum256([]byte(s.hash))
	return hex.EncodeToString(x[:])
}
func (s *schedItem) Render() *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{Title: "Sol-" + s.hash}
}

func TestScheduler_PollFeature_FiresOnCadence(t *testing.T) {
	feat := &tickPollFeature{}
	registry := features.Registry
	defer func() { features.Registry = registry }()
	features.Registry = map[string]features.Feature{"tick": feat}

	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	pub := publisher.New(st, dc)
	bus := scheduler.NewOpsBus()

	bnd := bindings.Binding{
		ID: "tick-binding", GuildID: "g", ChannelID: "c",
		FeatureName: "tick", PollInterval: 100 * time.Millisecond, IsPoll: true,
	}

	s := scheduler.New(scheduler.Config{
		Bindings:  []bindings.Binding{bnd},
		Publisher: pub,
		Store:     st,
		Bus:       bus,
		StaggerMs: 0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	require.NoError(t, s.Start(ctx))
	<-ctx.Done()
	require.NoError(t, s.Stop(time.Second))

	require.GreaterOrEqual(t, feat.calls.Load(), int64(3))
}

func TestScheduler_RecordsPollCycleAuditRow(t *testing.T) {
	feat := &tickPollFeature{}
	registry := features.Registry
	defer func() { features.Registry = registry }()
	features.Registry = map[string]features.Feature{"tick": feat}

	st := newMemStore()
	pub := publisher.New(st, discordclient.NewFakeDiscordClient())
	bus := scheduler.NewOpsBus()

	bnd := bindings.Binding{
		ID: "tick-binding", GuildID: "g", ChannelID: "c",
		FeatureName: "tick", PollInterval: 50 * time.Millisecond, IsPoll: true,
	}
	s := scheduler.New(scheduler.Config{
		Bindings:  []bindings.Binding{bnd},
		Publisher: pub,
		Store:     st,
		Bus:       bus,
		StaggerMs: 0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, s.Start(ctx))
	<-ctx.Done()
	require.NoError(t, s.Stop(time.Second))

	st.mu.Lock()
	count := len(st.cycles)
	st.mu.Unlock()
	require.GreaterOrEqual(t, count, 2, "scheduler must record poll_cycles for every tick")

	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.cycles {
		require.Equal(t, "tick-binding", c.BindingID)
		require.Equal(t, "success", c.Status)
	}
}

func TestScheduler_GracefulShutdownWithinDeadline(t *testing.T) {
	feat := &tickPollFeature{}
	registry := features.Registry
	defer func() { features.Registry = registry }()
	features.Registry = map[string]features.Feature{"tick": feat}

	bnd := bindings.Binding{
		ID: "x", GuildID: "g", ChannelID: "c",
		FeatureName: "tick", PollInterval: 100 * time.Millisecond, IsPoll: true,
	}
	s := scheduler.New(scheduler.Config{
		Bindings:  []bindings.Binding{bnd},
		Publisher: publisher.New(newMemStore(), discordclient.NewFakeDiscordClient()),
		Store:     newMemStore(),
		Bus:       scheduler.NewOpsBus(),
		StaggerMs: 0,
	})

	require.NoError(t, s.Start(context.Background()))
	start := time.Now()
	require.NoError(t, s.Stop(2*time.Second))
	require.Less(t, time.Since(start), 2*time.Second)
}

// --- EventDrivenFeature path ---

type tickEventFeature struct {
	ch chan features.Snapshot
}

func (f *tickEventFeature) Name() string                     { return "event" }
func (f *tickEventFeature) DefaultConfig() features.Config   { return features.Config{} }
func (f *tickEventFeature) Validate(c features.Config) error { return nil }
func (f *tickEventFeature) Subscribe(ctx context.Context, c features.Config) (<-chan features.Snapshot, error) {
	return f.ch, nil
}

func TestScheduler_EventDrivenFeature_DeliversSnapshots(t *testing.T) {
	ch := make(chan features.Snapshot, 1)
	feat := &tickEventFeature{ch: ch}
	registry := features.Registry
	defer func() { features.Registry = registry }()
	features.Registry = map[string]features.Feature{"event": feat}

	st := newMemStore()
	dc := discordclient.NewFakeDiscordClient()
	pub := publisher.New(st, dc)
	bus := scheduler.NewOpsBus()

	bnd := bindings.Binding{
		ID: "event-binding", GuildID: "g", ChannelID: "c",
		FeatureName: "event", IsEvent: true,
	}
	s := scheduler.New(scheduler.Config{
		Bindings:  []bindings.Binding{bnd},
		Publisher: pub,
		Store:     st,
		Bus:       bus,
		StaggerMs: 0,
	})

	require.NoError(t, s.Start(context.Background()))

	ch <- features.Snapshot{
		Items:       []features.Item{&schedItem{id: "outage:x", hash: "v1"}},
		Healthy:     true,
		GeneratedAt: time.Now().UTC(),
	}

	require.Eventually(t, func() bool {
		return len(dc.PostCalls()) > 0
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, s.Stop(time.Second))

	st.mu.Lock()
	defer st.mu.Unlock()
	require.GreaterOrEqual(t, len(st.cycles), 1)
	require.Equal(t, "event", st.cycles[0].Status)
}

func TestScheduler_DisabledBinding_SkipsPoll(t *testing.T) {
	feat := &tickPollFeature{}
	registry := features.Registry
	defer func() { features.Registry = registry }()
	features.Registry = map[string]features.Feature{"tick": feat}

	st := newMemStore()
	require.NoError(t, st.DisableBinding(context.Background(), "x", time.Now()))

	bnd := bindings.Binding{
		ID: "x", GuildID: "g", ChannelID: "c",
		FeatureName: "tick", PollInterval: 50 * time.Millisecond, IsPoll: true,
	}
	s := scheduler.New(scheduler.Config{
		Bindings:  []bindings.Binding{bnd},
		Publisher: publisher.New(st, discordclient.NewFakeDiscordClient()),
		Store:     st,
		Bus:       scheduler.NewOpsBus(),
		StaggerMs: 0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, s.Start(ctx))
	<-ctx.Done()
	require.NoError(t, s.Stop(time.Second))

	require.EqualValues(t, 0, feat.calls.Load(), "disabled binding must NEVER call Poll()")
}
