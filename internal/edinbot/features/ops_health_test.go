package features_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/stretchr/testify/require"
)

type fakeBus struct {
	ch chan features.OpsBusEvent
}

func newFakeBus() *fakeBus { return &fakeBus{ch: make(chan features.OpsBusEvent, 16)} }

func (f *fakeBus) Subscribe() <-chan features.OpsBusEvent { return f.ch }

func TestOpsHealthAlerts_FirstEvent_ProducesSnapshotWithOneItem(t *testing.T) {
	bus := newFakeBus()
	feat := features.NewOpsHealthAlerts(bus.Subscribe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := feat.Subscribe(ctx, feat.DefaultConfig())
	require.NoError(t, err)

	at := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	bus.ch <- features.OpsBusEvent{
		BindingID:  "kaine-platinum-boom",
		Reason:     "poll_exhausted",
		Attempts:   4,
		Error:      errors.New("memgraph timeout"),
		OccurredAt: at,
	}

	select {
	case snap := <-ch:
		require.True(t, snap.Healthy)
		require.Len(t, snap.Items, 1)
		identity := snap.Items[0].Identity()
		require.Contains(t, identity, "kaine-platinum-boom")
		require.Contains(t, identity, "poll_exhausted")
		require.Contains(t, identity, "2026-04-26T14:23")
	case <-time.After(time.Second):
		t.Fatal("did not receive snapshot")
	}
}

func TestOpsHealthAlerts_SecondEventSameOutage_FoldsIntoSameItem(t *testing.T) {
	bus := newFakeBus()
	feat := features.NewOpsHealthAlerts(bus.Subscribe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := feat.Subscribe(ctx, feat.DefaultConfig())

	at1 := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	at2 := time.Date(2026, 4, 26, 14, 38, 0, 0, time.UTC)

	bus.ch <- features.OpsBusEvent{BindingID: "x", Reason: "poll_exhausted", Attempts: 4, OccurredAt: at1}
	bus.ch <- features.OpsBusEvent{BindingID: "x", Reason: "poll_exhausted", Attempts: 8, OccurredAt: at2}

	var firstID, secondID string
	select {
	case s := <-ch:
		firstID = s.Items[0].Identity()
	case <-time.After(time.Second):
		t.Fatal("no first snapshot")
	}
	select {
	case s := <-ch:
		require.Len(t, s.Items, 1)
		secondID = s.Items[0].Identity()
	case <-time.After(time.Second):
		t.Fatal("no second snapshot")
	}

	require.Equal(t, firstID, secondID, "both events must share identity (same outage_started_at)")
}

func TestOpsHealthAlerts_RecoveryEvent_SnapshotMarkedResolved(t *testing.T) {
	bus := newFakeBus()
	feat := features.NewOpsHealthAlerts(bus.Subscribe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := feat.Subscribe(ctx, feat.DefaultConfig())

	at := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	rec := time.Date(2026, 4, 26, 14, 50, 0, 0, time.UTC)

	bus.ch <- features.OpsBusEvent{BindingID: "x", Reason: "poll_exhausted", Attempts: 4, OccurredAt: at}
	<-ch // drain initial snapshot

	bus.ch <- features.OpsBusEvent{BindingID: "x", Reason: "poll_recovered", OccurredAt: rec}
	select {
	case s := <-ch:
		require.Len(t, s.Items, 1)
		embed := s.Items[0].Render()
		require.Contains(t, embed.Description, "RESOLVED",
			"recovery snapshot must mark the outage resolved in the embed body")
	case <-time.After(time.Second):
		t.Fatal("no recovery snapshot")
	}
}

func TestOpsHealthAlerts_NewOutageAfterRecovery_NewIdentity(t *testing.T) {
	bus := newFakeBus()
	feat := features.NewOpsHealthAlerts(bus.Subscribe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := feat.Subscribe(ctx, feat.DefaultConfig())

	t1 := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 26, 14, 50, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 26, 17, 12, 0, 0, time.UTC)

	bus.ch <- features.OpsBusEvent{BindingID: "x", Reason: "poll_exhausted", Attempts: 4, OccurredAt: t1}
	id1 := (<-ch).Items[0].Identity()

	bus.ch <- features.OpsBusEvent{BindingID: "x", Reason: "poll_recovered", OccurredAt: t2}
	<-ch // drain recovery

	bus.ch <- features.OpsBusEvent{BindingID: "x", Reason: "poll_exhausted", Attempts: 4, OccurredAt: t3}
	id2 := (<-ch).Items[0].Identity()

	require.NotEqual(t, id1, id2, "new outage after recovery must have a fresh identity")
	require.Contains(t, id2, "17:12")
}

func TestOpsHealthAlerts_WatchBindingsConfigFiltersEvents(t *testing.T) {
	bus := newFakeBus()
	feat := features.NewOpsHealthAlerts(bus.Subscribe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := feat.Subscribe(ctx, features.Config{
		"watch_bindings": []any{"only-this-one"},
	})

	at := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	bus.ch <- features.OpsBusEvent{BindingID: "other", Reason: "poll_exhausted", OccurredAt: at}
	bus.ch <- features.OpsBusEvent{BindingID: "only-this-one", Reason: "poll_exhausted", OccurredAt: at}

	var got string
	select {
	case s := <-ch:
		got = s.Items[0].Identity()
	case <-time.After(time.Second):
		t.Fatal("no snapshot received")
	}
	require.Contains(t, got, "only-this-one")
	require.False(t, strings.Contains(got, "other"))
}

func TestOpsHealthAlerts_Validate_RejectsUnknownKeys(t *testing.T) {
	feat := features.NewOpsHealthAlerts(nil)
	require.NoError(t, feat.Validate(features.Config{}))
	require.NoError(t, feat.Validate(features.Config{
		"watch_bindings":      []any{"x"},
		"diagnose_on_failure": true,
		"outage_dedup":        true,
	}))
	require.Error(t, feat.Validate(features.Config{"bogus": 1}))
}

// Phase 13.2: Boot recovery test — ensures LoadPriorOutages seeds the live map.
func TestOpsHealthAlerts_BootRecovery_RebuildsOutageMapFromStore(t *testing.T) {
	bus := newFakeBus()
	feat := features.NewOpsHealthAlerts(bus.Subscribe)

	startedAt := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	prior := []features.PriorOutage{
		{
			Identity:  "outage:kaine-platinum-boom:poll_exhausted:2026-04-26T14:23:00Z",
			BindingID: "kaine-platinum-boom",
			Reason:    "poll_exhausted",
			StartedAt: startedAt,
		},
	}
	require.NoError(t, feat.LoadPriorOutages(prior))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := feat.Subscribe(ctx, feat.DefaultConfig())

	rec := time.Date(2026, 4, 26, 14, 50, 0, 0, time.UTC)
	bus.ch <- features.OpsBusEvent{BindingID: "kaine-platinum-boom", Reason: "poll_recovered", OccurredAt: rec}

	select {
	case s := <-ch:
		require.Len(t, s.Items, 1)
		require.Contains(t, s.Items[0].Identity(), "2026-04-26T14:23:00Z",
			"recovery must use the persisted started_at, NOT now")
	case <-time.After(time.Second):
		t.Fatal("recovery snapshot lost — boot recovery is broken")
	}
}
