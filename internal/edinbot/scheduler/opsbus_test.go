package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/scheduler"
	"github.com/stretchr/testify/require"
)

func TestOpsBus_PublishDeliversToSubscribers(t *testing.T) {
	bus := scheduler.NewOpsBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := bus.Subscribe(ctx)

	go bus.Publish(scheduler.OpsEvent{BindingID: "x", Reason: "poll_exhausted"})

	select {
	case got := <-sub:
		require.Equal(t, "x", got.BindingID)
		require.Equal(t, "poll_exhausted", got.Reason)
	case <-time.After(time.Second):
		t.Fatal("did not receive event")
	}
}

func TestOpsBus_MultipleSubscribers_AllReceive(t *testing.T) {
	bus := scheduler.NewOpsBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub1 := bus.Subscribe(ctx)
	sub2 := bus.Subscribe(ctx)

	go bus.Publish(scheduler.OpsEvent{BindingID: "x", Reason: "r"})

	for i, sub := range []<-chan scheduler.OpsEvent{sub1, sub2} {
		select {
		case got := <-sub:
			require.Equal(t, "x", got.BindingID, "sub %d", i)
		case <-time.After(time.Second):
			t.Fatalf("sub %d did not receive event", i)
		}
	}
}

func TestOpsBus_ContextCancel_Unsubscribes(t *testing.T) {
	bus := scheduler.NewOpsBus()
	ctx, cancel := context.WithCancel(context.Background())
	sub := bus.Subscribe(ctx)
	cancel()

	bus.Publish(scheduler.OpsEvent{})
	bus.Publish(scheduler.OpsEvent{})

	for {
		select {
		case _, ok := <-sub:
			if !ok {
				return
			}
		case <-time.After(100 * time.Millisecond):
			return
		}
	}
}

func TestOpsBus_SlowSubscriberDoesNotBlockPublish(t *testing.T) {
	bus := scheduler.NewOpsBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = bus.Subscribe(ctx) // never read

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			bus.Publish(scheduler.OpsEvent{BindingID: "x"})
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on slow subscriber — must drop events instead")
	}
}
