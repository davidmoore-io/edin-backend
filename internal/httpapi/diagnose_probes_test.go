package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeGalaxyReaderProber struct {
	queryFn func(ctx context.Context) error
}

func (f *fakeGalaxyReaderProber) ProbeReader(ctx context.Context) error {
	return f.queryFn(ctx)
}

type fakePgProber struct {
	pingFn func(ctx context.Context) error
}

func (f *fakePgProber) Ping(ctx context.Context) error { return f.pingFn(ctx) }

type fakeListenerLagProber struct {
	lagFn func(ctx context.Context) (time.Duration, error)
}

func (f *fakeListenerLagProber) Lag(ctx context.Context) (time.Duration, error) {
	return f.lagFn(ctx)
}

func TestProbeGalaxyReader_OK(t *testing.T) {
	pp := &fakeGalaxyReaderProber{queryFn: func(ctx context.Context) error { return nil }}
	r := probeGalaxyReader(context.Background(), pp)
	require.True(t, r.OK)
	require.Empty(t, r.Error)
	require.GreaterOrEqual(t, r.LatencyMs, 0)
}

func TestProbeGalaxyReader_FailureIsRecorded(t *testing.T) {
	pp := &fakeGalaxyReaderProber{queryFn: func(ctx context.Context) error { return errors.New("wrong database role") }}
	r := probeGalaxyReader(context.Background(), pp)
	require.False(t, r.OK)
	require.Contains(t, r.Error, "wrong database role")
}

func TestProbePostgres_OK(t *testing.T) {
	pp := &fakePgProber{pingFn: func(ctx context.Context) error { return nil }}
	r := probePostgres(context.Background(), pp)
	require.True(t, r.OK)
}

func TestProbePostgres_FailureIsRecorded(t *testing.T) {
	pp := &fakePgProber{pingFn: func(ctx context.Context) error { return errors.New("server closed") }}
	r := probePostgres(context.Background(), pp)
	require.False(t, r.OK)
	require.Contains(t, r.Error, "server closed")
}

func TestProbeEDDNListener_FreshIsHealthy(t *testing.T) {
	lp := &fakeListenerLagProber{lagFn: func(ctx context.Context) (time.Duration, error) {
		return 30 * time.Second, nil
	}}
	r := probeEDDNListener(context.Background(), lp)
	require.True(t, r.OK)
	require.Empty(t, r.Error)
}

func TestProbeEDDNListener_StaleIsDegraded(t *testing.T) {
	lp := &fakeListenerLagProber{lagFn: func(ctx context.Context) (time.Duration, error) {
		return 10 * time.Minute, nil // > 5 min stale threshold
	}}
	r := probeEDDNListener(context.Background(), lp)
	require.False(t, r.OK)
	require.Contains(t, r.Error, "stale")
}

func TestProbeEDDNListener_QueryErrorIsDegraded(t *testing.T) {
	lp := &fakeListenerLagProber{lagFn: func(ctx context.Context) (time.Duration, error) {
		return 0, errors.New("query failed")
	}}
	r := probeEDDNListener(context.Background(), lp)
	require.False(t, r.OK)
	require.Contains(t, r.Error, "query failed")
}
