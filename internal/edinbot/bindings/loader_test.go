package bindings_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/bindings"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/stretchr/testify/require"
)

// fakePollFeature satisfies PollFeature for loader tests so we can populate
// features.Registry without depending on the concrete platinum/ltd impls.
type fakePollFeature struct {
	name       string
	defaults   features.Config
	validateFn func(features.Config) error
}

func (f *fakePollFeature) Name() string                                                              { return f.name }
func (f *fakePollFeature) DefaultConfig() features.Config                                            { return f.defaults }
func (f *fakePollFeature) Validate(cfg features.Config) error                                        { return f.validateFn(cfg) }
func (f *fakePollFeature) Poll(ctx context.Context, cfg features.Config) (features.Snapshot, error) { return features.Snapshot{}, nil }

type fakeEventFeature struct {
	name       string
	defaults   features.Config
	validateFn func(features.Config) error
}

func (f *fakeEventFeature) Name() string                                                                          { return f.name }
func (f *fakeEventFeature) DefaultConfig() features.Config                                                        { return f.defaults }
func (f *fakeEventFeature) Validate(cfg features.Config) error                                                    { return f.validateFn(cfg) }
func (f *fakeEventFeature) Subscribe(ctx context.Context, cfg features.Config) (<-chan features.Snapshot, error) {
	ch := make(chan features.Snapshot)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}

func setupRegistryForTests(t *testing.T) {
	t.Helper()
	original := features.Registry
	features.Registry = map[string]features.Feature{
		"poll-fake":  &fakePollFeature{name: "poll-fake", defaults: features.Config{}, validateFn: func(c features.Config) error { return nil }},
		"event-fake": &fakeEventFeature{name: "event-fake", defaults: features.Config{}, validateFn: func(c features.Config) error { return nil }},
	}
	t.Cleanup(func() { features.Registry = original })
}

func TestLoader_HappyPath(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "kaine-poll"
    guild_id: "1334858214533103646"
    channel_id: "1487248197582852321"
    feature: "poll-fake"
    poll_interval: 15m
    config: {}
  - id: "edin-event"
    guild_id: "1497743490744975534"
    channel_id: "1497743648488554607"
    feature: "event-fake"
    config: {}
`
	bs, err := bindings.Load(strings.NewReader(yaml))
	require.NoError(t, err)
	require.Len(t, bs, 2)

	require.Equal(t, "kaine-poll", bs[0].ID)
	require.Equal(t, "1334858214533103646", bs[0].GuildID)
	require.Equal(t, 15*time.Minute, bs[0].PollInterval)
	require.True(t, bs[0].IsPoll)

	require.Equal(t, "edin-event", bs[1].ID)
	require.True(t, bs[1].IsEvent)
	require.Equal(t, time.Duration(0), bs[1].PollInterval)
}

func TestLoader_DuplicateIDFails(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "poll-fake"
    poll_interval: 1m
  - id: "x"
    guild_id: "3"
    channel_id: "4"
    feature: "poll-fake"
    poll_interval: 1m
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate")
}

func TestLoader_BadIDFormatFails(t *testing.T) {
	setupRegistryForTests(t)
	for _, badID := range []string{"UPPER", "with space", "with/slash", "", "!"} {
		t.Run(badID, func(t *testing.T) {
			yaml := `
bindings:
  - id: "` + badID + `"
    guild_id: "1"
    channel_id: "2"
    feature: "poll-fake"
    poll_interval: 1m
`
			_, err := bindings.Load(strings.NewReader(yaml))
			require.Error(t, err, "id %q must be rejected", badID)
		})
	}
}

func TestLoader_NonNumericGuildIDFails(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "not-a-snowflake"
    channel_id: "2"
    feature: "poll-fake"
    poll_interval: 1m
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "guild_id")
}

func TestLoader_UnknownFeatureFails(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "does-not-exist"
    poll_interval: 1m
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown feature")
}

func TestLoader_PollIntervalRequiredForPollFeature(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "poll-fake"
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "poll_interval required")
}

func TestLoader_PollIntervalForbiddenOnEventFeature(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "event-fake"
    poll_interval: 1m
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "poll_interval forbidden")
}

func TestLoader_PollIntervalSubMinimumFails(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "poll-fake"
    poll_interval: 5s
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "minimum 30s")
}

func TestLoader_FeatureValidateFailureSurfaces(t *testing.T) {
	original := features.Registry
	features.Registry = map[string]features.Feature{
		"strict-poll": &fakePollFeature{
			name:     "strict-poll",
			defaults: features.Config{},
			validateFn: func(c features.Config) error {
				if _, ok := c["required_key"]; !ok {
					return errors.New("required_key missing")
				}
				return nil
			},
		},
	}
	t.Cleanup(func() { features.Registry = original })

	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "strict-poll"
    poll_interval: 1m
    config: {}
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "required_key missing")
}
