package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// LoopDeps mirrors HandlerDeps for the polling goroutine. Same Store,
// Snap, Discord, Cfg fields — the loop and handlers share collaborators
// in production but live in different files so the responsibilities are
// readable separately.
type LoopDeps struct {
	Store   Store
	Snap    Snapshotter
	Discord Discord
	Cfg     Config
	NowFunc func() time.Time
	LogFunc func(format string, args ...any)
}

// Watcher is the long-running goroutine that polls every watched system
// every Cfg.PollInterval and edits the Discord message when the snapshot
// hash changes. Construct with NewWatcher; start with Start(ctx); the
// returned Watcher's Done() channel signals graceful shutdown.
type Watcher struct {
	deps     LoopDeps
	inFlight atomic.Bool // skip-tick guard if a previous cycle is still running
	stopped  chan struct{}
	stopOnce sync.Once
}

// NewWatcher constructs a Watcher. Defaults are applied to deps.Cfg so
// the embedding caller can pass Config{} and still get sensible behaviour.
func NewWatcher(deps LoopDeps) *Watcher {
	deps.Cfg = deps.Cfg.defaults()
	return &Watcher{
		deps:    deps,
		stopped: make(chan struct{}),
	}
}

// Start kicks the goroutine. Behaviour:
//
//   1. Boot recovery: immediately run one full cycle for every persisted
//      watch, so we don't have a 2-minute blank window after a restart.
//   2. Then tick every Cfg.PollInterval until ctx is cancelled.
//
// Each cycle: list all watches, sort (already done by the store), iterate
// with PerWatchStagger between fetches. Per-watch:
//   - Fetch the snapshot from control-API.
//   - Compute state-hash. If equal to last_state_hash, skip (no edit).
//   - Otherwise render + EditMessage + UpdateWatchState.
//
// Per-watch failures are logged and skipped — one bad system doesn't
// poison the rest of the channel.
func (w *Watcher) Start(ctx context.Context) {
	go func() {
		defer close(w.stopped)

		// Boot recovery first. If there are 50 persisted watches, this
		// cycle takes ~50s with the default 1s stagger; the next ticker
		// tick lands at 120s, so they don't overlap.
		w.runCycle(ctx)

		t := time.NewTicker(w.deps.Cfg.PollInterval)
		defer t.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				w.runCycle(ctx)
			}
		}
	}()
}

// Done returns a channel closed when the goroutine has exited cleanly.
// Used by main.go's graceful-shutdown path to wait for in-flight Discord
// edits to finish before the process exits.
func (w *Watcher) Done() <-chan struct{} { return w.stopped }

// runCycle is one full pass over all watches. The skip-tick guard makes
// this safe to call from the boot path AND the ticker path concurrently
// — though in normal operation a long previous cycle should be rare.
func (w *Watcher) runCycle(ctx context.Context) {
	if !w.inFlight.CompareAndSwap(false, true) {
		w.logf("[INFO] watcher: previous cycle still running; skipping this tick")
		return
	}
	defer w.inFlight.Store(false)

	watches, err := w.deps.Store.ListAllWatches(ctx)
	if err != nil {
		w.logf("[ERROR] watcher: list watches failed: %v", err)
		return
	}
	if len(watches) == 0 {
		return
	}

	for i, watch := range watches {
		// Honour ctx cancellation between iterations so a Stop signal
		// drops out promptly even mid-cycle.
		if ctx.Err() != nil {
			return
		}
		if i > 0 {
			// Stagger between consecutive fetches. ctx-aware so a
			// shutdown signal doesn't have to wait the full stagger.
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.deps.Cfg.PerWatchStagger):
			}
		}
		w.processOne(ctx, watch)
	}
}

// processOne handles a single watch. Errors are logged and swallowed —
// the cycle continues with the remaining watches. If the message has
// been deleted manually (Discord 404), we drop the row defensively so
// the next user's /watch on this system can post fresh.
func (w *Watcher) processOne(ctx context.Context, row store.WatchedSystem) {
	snap, err := w.deps.Snap.GetSystemWatchSnapshot(ctx, row.SystemSlug)
	if err != nil {
		if errors.Is(err, controlclient.ErrSystemNotFound) {
			// System dropped from Memgraph — vanishingly rare for
			// real-galaxy data, but if it happens we don't want a
			// dead watch in the channel. Drop it.
			w.logf("[WARN] watcher: system %q no longer in galaxy data; removing watch", row.SystemSlug)
			_ = w.deps.Discord.DeleteMessage(ctx, row.ChannelID, row.MessageID)
			_, _ = w.deps.Store.RemoveWatch(ctx, row.ChannelID, row.SystemSlug)
			return
		}
		w.logf("[ERROR] watcher: snapshot fetch %q: %v", row.SystemSlug, err)
		return
	}

	hash := stateHash(snap)
	if hash == row.LastStateHash {
		// No content change since the last edit — skip.
		return
	}

	now := w.now()
	embed := Render(snap, row.WatchedAt.Unix())
	if err := w.deps.Discord.EditMessage(ctx, row.ChannelID, row.MessageID, embed); err != nil {
		if errors.Is(err, discordclient.ErrMessageNotFound) || errors.Is(err, discordclient.ErrChannelGone) {
			// Someone manually deleted the message, or the bot lost
			// access to the channel. Either way, the row no longer
			// matches reality — drop it and move on.
			w.logf("[WARN] watcher: message gone for watch %q; removing row", row.SystemSlug)
			_, _ = w.deps.Store.RemoveWatch(ctx, row.ChannelID, row.SystemSlug)
			return
		}
		w.logf("[ERROR] watcher: edit message for %q: %v", row.SystemSlug, err)
		return
	}

	raw, _ := json.Marshal(embed)
	if err := w.deps.Store.UpdateWatchState(ctx, row.ChannelID, row.SystemSlug, hash, raw, now); err != nil {
		w.logf("[ERROR] watcher: persist state for %q: %v", row.SystemSlug, err)
	}
}

func (w *Watcher) now() time.Time {
	if w.deps.NowFunc != nil {
		return w.deps.NowFunc()
	}
	return time.Now().UTC()
}

func (w *Watcher) logf(format string, args ...any) {
	if w.deps.LogFunc != nil {
		w.deps.LogFunc(format, args...)
		return
	}
	log.Printf(format, args...)
}
