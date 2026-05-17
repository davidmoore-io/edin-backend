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
	cfg, err := bindings.Load(strings.NewReader(yaml))
	require.NoError(t, err)
	require.Len(t, cfg.Bindings, 2)
	require.Empty(t, cfg.SlashGuilds)

	require.Equal(t, "kaine-poll", cfg.Bindings[0].ID)
	require.Equal(t, "1334858214533103646", cfg.Bindings[0].GuildID)
	require.Equal(t, 15*time.Minute, cfg.Bindings[0].PollInterval)
	require.True(t, cfg.Bindings[0].IsPoll)

	require.Equal(t, "edin-event", cfg.Bindings[1].ID)
	require.True(t, cfg.Bindings[1].IsEvent)
	require.Equal(t, time.Duration(0), cfg.Bindings[1].PollInterval)
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

func TestLoader_SlashGuilds_HappyPath(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "poll-fake"
    poll_interval: 1m
slash_guilds:
  - guild_id: "1334858214533103646"
    watch_channel_id: "1498813935057637597"
  - guild_id: "1289051766456848546"
    watch_channel_id: "1503701700320432239"
    allowed_role_ids:
      - "1289051766582677507"
      - "1353722595043971112"
      - "1329039235063480360"
`
	cfg, err := bindings.Load(strings.NewReader(yaml))
	require.NoError(t, err)
	require.Len(t, cfg.Bindings, 1)
	require.Len(t, cfg.SlashGuilds, 2)

	// Admin-only guild: no roles
	require.Equal(t, "1334858214533103646", cfg.SlashGuilds[0].GuildID)
	require.Equal(t, "1498813935057637597", cfg.SlashGuilds[0].WatchChannelID)
	require.Empty(t, cfg.SlashGuilds[0].AllowedRoleIDs)

	// Role-restricted guild
	require.Equal(t, "1289051766456848546", cfg.SlashGuilds[1].GuildID)
	require.Equal(t, "1503701700320432239", cfg.SlashGuilds[1].WatchChannelID)
	require.Equal(t, []string{"1289051766582677507", "1353722595043971112", "1329039235063480360"}, cfg.SlashGuilds[1].AllowedRoleIDs)
}

func TestLoader_SlashGuilds_NoSlashGuildsIsValid(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings:
  - id: "x"
    guild_id: "1"
    channel_id: "2"
    feature: "poll-fake"
    poll_interval: 1m
`
	cfg, err := bindings.Load(strings.NewReader(yaml))
	require.NoError(t, err)
	require.Empty(t, cfg.SlashGuilds)
}

func TestLoader_SlashGuilds_DuplicateGuildFails(t *testing.T) {
	setupRegistryForTests(t)
	yaml := `
bindings: []
slash_guilds:
  - guild_id: "1234567890"
    watch_channel_id: "9876543210"
  - guild_id: "1234567890"
    watch_channel_id: "1111111111"
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate")
}

func TestLoader_SlashGuilds_BadGuildSnowflakeFails(t *testing.T) {
	yaml := `
bindings: []
slash_guilds:
  - guild_id: "not-a-snowflake"
    watch_channel_id: "9876543210"
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "guild_id")
}

func TestLoader_SlashGuilds_BadChannelSnowflakeFails(t *testing.T) {
	yaml := `
bindings: []
slash_guilds:
  - guild_id: "1234567890"
    watch_channel_id: "not-a-snowflake"
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "watch_channel_id")
}

func TestLoader_SlashGuilds_BadRoleSnowflakeFails(t *testing.T) {
	yaml := `
bindings: []
slash_guilds:
  - guild_id: "1234567890"
    watch_channel_id: "9876543210"
    allowed_role_ids:
      - "valid-enough"
`
	_, err := bindings.Load(strings.NewReader(yaml))
	require.Error(t, err)
	require.Contains(t, err.Error(), "allowed_role_ids")
}
