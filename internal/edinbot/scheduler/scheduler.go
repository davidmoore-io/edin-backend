package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/bindings"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/edin-space/edin-backend/internal/edinbot/publisher"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

type Config struct {
	Bindings  []bindings.Binding
	Publisher *publisher.Publisher
	Store     store.Store
	Bus       *OpsBus

	// StaggerMs is the per-binding startup delay (binding[i] waits i*StaggerMs
	// before its first tick) to avoid thundering-herd on cold start. Default
	// 500ms; tests can set 0.
	StaggerMs int
}

type Scheduler struct {
	cfg     Config
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	running atomic.Bool
}

func New(cfg Config) *Scheduler {
	if cfg.StaggerMs == 0 {
		cfg.StaggerMs = 500
	}
	return &Scheduler{cfg: cfg}
}

func (s *Scheduler) Start(ctx context.Context) error {
	if s.running.Swap(true) {
		return errors.New("scheduler already started")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	for i, b := range s.cfg.Bindings {
		s.wg.Add(1)
		go s.runBinding(ctx, i, b)
	}
	return nil
}

func (s *Scheduler) Stop(deadline time.Duration) error {
	if !s.running.Load() {
		return nil
	}
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(deadline):
		return errors.New("scheduler stop deadline exceeded")
	}
}

func (s *Scheduler) runBinding(ctx context.Context, idx int, b bindings.Binding) {
	defer s.wg.Done()

	feat, ok := features.Registry[b.FeatureName]
	if !ok {
		// Already validated by bindings.Load; defensive.
		return
	}

	// Stagger.
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(idx*s.cfg.StaggerMs) * time.Millisecond):
	}

	if pf, ok := feat.(features.PollFeature); ok {
		s.runPoll(ctx, b, pf)
		return
	}
	if ef, ok := feat.(features.EventDrivenFeature); ok {
		s.runEventDriven(ctx, b, ef)
		return
	}
}

func (s *Scheduler) runPoll(ctx context.Context, b bindings.Binding, pf features.PollFeature) {
	t := time.NewTicker(b.PollInterval)
	defer t.Stop()

	var inFlight atomic.Bool

	tick := func() {
		if !inFlight.CompareAndSwap(false, true) {
			// Previous Poll still running — skip this tick rather than queue.
			return
		}
		defer inFlight.Store(false)

		// Disabled binding (channel-gone history) — skip Poll() entirely.
		disabled, _ := s.cfg.Store.IsBindingDisabled(ctx, b.ID)
		if disabled {
			return
		}

		start := time.Now()
		snap, err := pf.Poll(ctx, b.Config)
		dur := time.Since(start)

		cycle := store.PollCycle{
			TickedAt:   start.UTC(),
			BindingID:  b.ID,
			Attempts:   1,
			DurationMs: int(dur.Milliseconds()),
		}

		if err != nil || !snap.Healthy {
			cycle.Status = "failed"
			cycle.ItemCount = 0
			if err != nil {
				m := err.Error()
				cycle.LastError = &m
			}
			_ = s.cfg.Store.RecordPollCycle(ctx, cycle)
			return
		}

		cycle.Status = "success"
		cycle.ItemCount = len(snap.Items)
		_ = s.cfg.Store.RecordPollCycle(ctx, cycle)

		_, applyErr := s.cfg.Publisher.Apply(ctx, b, snap)
		if applyErr != nil {
			fmt.Printf("[ERROR] publisher apply failed for %s: %v\n", b.ID, applyErr)
		}
	}

	tick() // first tick immediately after stagger
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

func (s *Scheduler) runEventDriven(ctx context.Context, b bindings.Binding, ef features.EventDrivenFeature) {
	ch, err := ef.Subscribe(ctx, b.Config)
	if err != nil {
		fmt.Printf("[ERROR] subscribe failed for %s: %v\n", b.ID, err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case snap, ok := <-ch:
			if !ok {
				return
			}
			cycle := store.PollCycle{
				TickedAt:   snap.GeneratedAt,
				BindingID:  b.ID,
				Status:     "event",
				Attempts:   1,
				ItemCount:  len(snap.Items),
				DurationMs: 0,
			}
			_ = s.cfg.Store.RecordPollCycle(ctx, cycle)

			if !snap.Healthy {
				continue
			}
			_, applyErr := s.cfg.Publisher.Apply(ctx, b, snap)
			if applyErr != nil {
				fmt.Printf("[ERROR] publisher apply failed for %s: %v\n", b.ID, applyErr)
			}
		}
	}
}
