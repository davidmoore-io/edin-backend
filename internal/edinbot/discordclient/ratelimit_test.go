package discordclient_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/stretchr/testify/require"
)

func TestPerChannelLimiter_AllowsBurstUpToCapacity(t *testing.T) {
	l := discordclient.NewPerChannelLimiter(5, 5*time.Second)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, l.Wait(ctx, "channel-A"), "burst %d should not block", i)
	}
}

func TestPerChannelLimiter_ChannelsAreIndependent(t *testing.T) {
	l := discordclient.NewPerChannelLimiter(5, 5*time.Second)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, l.Wait(ctx, "ch-A"))
	}
	for i := 0; i < 5; i++ {
		require.NoError(t, l.Wait(ctx, "ch-B"), "channel B's bucket is independent of A")
	}
}

func TestPerChannelLimiter_BlocksWhenExhausted(t *testing.T) {
	l := discordclient.NewPerChannelLimiter(2, 1*time.Second)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		require.NoError(t, l.Wait(ctx, "ch"))
	}

	start := time.Now()
	require.NoError(t, l.Wait(ctx, "ch"))
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 400*time.Millisecond, "third call must block while bucket refills")
}

func TestPerChannelLimiter_RespectsContextCancellation(t *testing.T) {
	l := discordclient.NewPerChannelLimiter(1, 1*time.Hour) // very slow refill
	ctx := context.Background()
	require.NoError(t, l.Wait(ctx, "ch"))

	ctx2, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := l.Wait(ctx2, "ch")
	require.Error(t, err)
}

func TestPerChannelLimiter_ConcurrentCallsSerialise(t *testing.T) {
	l := discordclient.NewPerChannelLimiter(5, 5*time.Second)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, l.Wait(ctx, "shared"))
		}()
	}
	wg.Wait()
}
