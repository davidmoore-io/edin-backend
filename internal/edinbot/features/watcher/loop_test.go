package watcher_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features/watcher"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// scriptedSnapshotter returns successive responses from a queue keyed by
// slug. Lets a single test simulate "snapshot 1, then snapshot 2" without
// monkey-patching time. nil queue position → reuse the last value.
type scriptedSnapshotter struct {
	mu    sync.Mutex
	next  map[string][]*controlclient.SystemWatchSnapshot
	errs  map[string]error
	calls int32
}

func newScripted() *scriptedSnapshotter {
	return &scriptedSnapshotter{
		next: map[string][]*controlclient.SystemWatchSnapshot{},
		errs: map[string]error{},
	}
}
func (s *scriptedSnapshotter) Push(slug string, v *controlclient.SystemWatchSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next[slug] = append(s.next[slug], v)
}
func (s *scriptedSnapshotter) SetError(slug string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs[slug] = err
}
func (s *scriptedSnapshotter) GetSystemWatchSnapshot(ctx context.Context, slug string) (*controlclient.SystemWatchSnapshot, error) {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.errs[slug]; ok && err != nil {
		return nil, err
	}
	q := s.next[slug]
	if len(q) == 0 {
		return nil, controlclient.ErrSystemNotFound
	}
	v := q[0]
	if len(q) > 1 {
		s.next[slug] = q[1:]
	}
	return v, nil
}

// runOnce drives a Watcher for exactly one cycle by passing a context
// that cancels just after the boot-recovery cycle completes. We keep the
// PollInterval long (1h) so the ticker never fires during the test —
// boot recovery is the only cycle observed.
func runOnce(t *testing.T, st *fakeStore, snap watcher.Snapshotter, dc *fakeDiscord, cfg watcher.Config) {
	t.Helper()
	deps := watcher.LoopDeps{
		Store: st, Snap: snap, Discord: dc,
		Cfg:     cfg.Defaults(),
		LogFunc: func(format string, args ...any) {},
		NowFunc: func() time.Time { return time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC) },
	}
	deps.Cfg.PollInterval = 1 * time.Hour           // ticker won't fire
	deps.Cfg.PerWatchStagger = 1 * time.Microsecond // boot cycle finishes promptly

	ctx, cancel := context.WithCancel(context.Background())
	w := watcher.NewWatcher(deps)
	w.Start(ctx)

	// Wait for the boot cycle to drain. The cycle calls
	// ListAllWatches → for each row, fetch + maybe edit. Once the
	// snapshotter has been called once per persisted row, we can
	// safely cancel.
	expected := int32(rowCount(st))
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&snap.(*scriptedSnapshotter).calls) < expected && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	<-w.Done()
}

func rowCount(st *fakeStore) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.rows)
}

// Set up a single watch row pointing at HIP 61332 with a state-hash that
// matches the snapshot below — used by tests that want to start from a
// "no edit needed" baseline.
func seedHIP(t *testing.T, st *fakeStore, snap *controlclient.SystemWatchSnapshot) {
	t.Helper()
	require.NoError(t, st.AddWatch(context.Background(), store.WatchedSystem{
		GuildID: "kaine-guild", ChannelID: "watch-channel",
		SystemSlug: "HIP61332", SystemName: "HIP 61332",
		MessageID: "m-1", CreatedBy: "u-1",
		WatchedAt:     time.Now(),
		LastUpdatedAt: time.Now(),
		LastStateHash: watcher.StateHashForTest(snap),
		LastRender:    []byte(`{}`),
	}))
}

func TestWatcher_NoEditWhenStateUnchanged(t *testing.T) {
	st := newFakeStore()
	snap := mkSnapshot()
	seedHIP(t, st, snap)

	scripted := newScripted()
	scripted.Push("HIP61332", snap)
	dc := &fakeDiscord{}

	runOnce(t, st, scripted, dc, watcher.Config{})

	require.Equal(t, 0, dc.edits, "no edit when state-hash matches existing row")
}

func TestWatcher_EditsWhenStateChanged(t *testing.T) {
	st := newFakeStore()
	original := mkSnapshot() // ControllingPower = Felicia Winters
	seedHIP(t, st, original)

	updated := mkSnapshot()
	updated.ControllingPower = "Aisling Duval" // forces hash to move

	scripted := newScripted()
	scripted.Push("HIP61332", updated)
	dc := &fakeDiscord{}

	runOnce(t, st, scripted, dc, watcher.Config{})

	require.Equal(t, 1, dc.edits, "edit must fire when state-hash moves")

	// Persisted state-hash + last_updated_at must reflect the new snapshot.
	got, _ := st.GetWatch(context.Background(), "watch-channel", "HIP61332")
	require.NotNil(t, got)
	require.Equal(t, watcher.StateHashForTest(updated), got.LastStateHash)
}

func TestWatcher_DropsRowOnSystemNotFound(t *testing.T) {
	st := newFakeStore()
	seedHIP(t, st, mkSnapshot())

	scripted := newScripted()
	scripted.SetError("HIP61332", controlclient.ErrSystemNotFound)
	dc := &fakeDiscord{}

	runOnce(t, st, scripted, dc, watcher.Config{})

	require.Len(t, dc.deletes, 1, "Discord delete fired when system left galaxy data")
	got, _ := st.GetWatch(context.Background(), "watch-channel", "HIP61332")
	require.Nil(t, got, "row removed defensively when system no longer in graph")
}

func TestWatcher_DropsRowWhenDiscordMessageGone(t *testing.T) {
	st := newFakeStore()
	original := mkSnapshot()
	seedHIP(t, st, original)

	updated := mkSnapshot()
	updated.ControllingPower = "Jerome Archer"
	scripted := newScripted()
	scripted.Push("HIP61332", updated)

	// EditMessage will return ErrMessageNotFound — operator manually
	// deleted the watched message. Watcher must reap the stale row.
	dc := &fakeDiscord{editErr: discordclient.ErrMessageNotFound}

	runOnce(t, st, scripted, dc, watcher.Config{})

	got, _ := st.GetWatch(context.Background(), "watch-channel", "HIP61332")
	require.Nil(t, got, "row reaped when EditMessage reports message missing")
}

func TestWatcher_SwallowsTransientErrorsAndContinues(t *testing.T) {
	// Two watches; one snapshot fetch fails (transient 5xx), the other
	// succeeds. The cycle must still process the second watch — one bad
	// system mustn't poison the rest.
	st := newFakeStore()
	require.NoError(t, st.AddWatch(context.Background(), store.WatchedSystem{
		GuildID: "g", ChannelID: "watch-channel",
		SystemSlug: "BrokenSlug", SystemName: "Broken",
		MessageID: "m-broken", CreatedBy: "u",
		WatchedAt: time.Now(), LastUpdatedAt: time.Now(),
		LastStateHash: "h-broken", LastRender: []byte(`{}`),
	}))
	updated := mkSnapshot()
	updated.ControllingPower = "Pranav Antal"
	seedHIP(t, st, mkSnapshot())

	scripted := newScripted()
	scripted.SetError("BrokenSlug", &transientErr{msg: "boom"})
	scripted.Push("HIP61332", updated)
	dc := &fakeDiscord{}

	runOnce(t, st, scripted, dc, watcher.Config{})

	require.Equal(t, 1, dc.edits, "successful watch processed despite transient error on the other")
	// Row for the broken slug stays — we don't delete on transient errors.
	got, _ := st.GetWatch(context.Background(), "watch-channel", "BrokenSlug")
	require.NotNil(t, got)
}

// transientErr is a non-sentinel error used to simulate "something
// recoverable went wrong" — distinct from controlclient.ErrSystemNotFound
// so the watcher doesn't reap on it.
type transientErr struct{ msg string }

func (e *transientErr) Error() string { return e.msg }

// Quiet the discordgo unused import warning when go vet runs on this
// file in isolation — the package's other tests do use the symbol but
// loop_test.go doesn't reference it directly.
var _ = discordgo.PermissionAdministrator
