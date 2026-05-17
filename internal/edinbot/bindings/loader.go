package bindings

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"gopkg.in/yaml.v3"
)

const minPollInterval = 30 * time.Second

var (
	idPattern        = regexp.MustCompile(`^[a-z0-9-]+$`)
	snowflakePattern = regexp.MustCompile(`^[0-9]+$`)
)

type rawSlashGuild struct {
	GuildID        string   `yaml:"guild_id"`
	WatchChannelID string   `yaml:"watch_channel_id"`
	AllowedRoleIDs []string `yaml:"allowed_role_ids,omitempty"`
}

type fileShape struct {
	Bindings    []rawBinding    `yaml:"bindings"`
	SlashGuilds []rawSlashGuild `yaml:"slash_guilds,omitempty"`
}

type rawBinding struct {
	ID           string         `yaml:"id"`
	GuildID      string         `yaml:"guild_id"`
	ChannelID    string         `yaml:"channel_id"`
	Feature      string         `yaml:"feature"`
	PollInterval string         `yaml:"poll_interval,omitempty"`
	Config       map[string]any `yaml:"config,omitempty"`
}

// Load parses YAML from r, validates every binding and slash guild against
// their respective constraints, and returns the resolved Config. Any
// validation failure is fatal — partial results are never returned. Per spec §6.
func Load(r io.Reader) (Config, error) {
	var f fileShape
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("parse bindings yaml: %w", err)
	}

	bindings := make([]Binding, 0, len(f.Bindings))
	seenID := map[string]bool{}
	for i, raw := range f.Bindings {
		b, err := validate(raw)
		if err != nil {
			return Config{}, fmt.Errorf("binding[%d] (%q): %w", i, raw.ID, err)
		}
		if seenID[b.ID] {
			return Config{}, fmt.Errorf("binding[%d]: duplicate id %q", i, b.ID)
		}
		seenID[b.ID] = true
		bindings = append(bindings, b)
	}

	slashGuilds := make([]SlashGuild, 0, len(f.SlashGuilds))
	seenGuildID := map[string]bool{}
	for i, raw := range f.SlashGuilds {
		sg, err := validateSlashGuild(raw)
		if err != nil {
			return Config{}, fmt.Errorf("slash_guild[%d]: %w", i, err)
		}
		if seenGuildID[sg.GuildID] {
			return Config{}, fmt.Errorf("slash_guild[%d]: duplicate guild_id %q", i, sg.GuildID)
		}
		seenGuildID[sg.GuildID] = true
		slashGuilds = append(slashGuilds, sg)
	}

	return Config{Bindings: bindings, SlashGuilds: slashGuilds}, nil
}

func validate(raw rawBinding) (Binding, error) {
	if !idPattern.MatchString(raw.ID) {
		return Binding{}, fmt.Errorf("invalid id %q (must match %s)", raw.ID, idPattern.String())
	}
	if !snowflakePattern.MatchString(raw.GuildID) {
		return Binding{}, errors.New("guild_id must be a numeric snowflake string")
	}
	if !snowflakePattern.MatchString(raw.ChannelID) {
		return Binding{}, errors.New("channel_id must be a numeric snowflake string")
	}

	feat, ok := features.Registry[raw.Feature]
	if !ok {
		return Binding{}, fmt.Errorf("unknown feature %q", raw.Feature)
	}

	_, isPoll := feat.(features.PollFeature)
	_, isEvent := feat.(features.EventDrivenFeature)

	b := Binding{
		ID:          raw.ID,
		GuildID:     raw.GuildID,
		ChannelID:   raw.ChannelID,
		FeatureName: raw.Feature,
		Config:      raw.Config,
		IsPoll:      isPoll,
		IsEvent:     isEvent,
	}

	switch {
	case isPoll:
		if raw.PollInterval == "" {
			return Binding{}, errors.New("poll_interval required for PollFeature")
		}
		d, err := time.ParseDuration(raw.PollInterval)
		if err != nil {
			return Binding{}, fmt.Errorf("invalid poll_interval %q: %w", raw.PollInterval, err)
		}
		if d < minPollInterval {
			return Binding{}, fmt.Errorf("poll_interval %s below minimum 30s", d)
		}
		b.PollInterval = d
	case isEvent:
		if raw.PollInterval != "" {
			return Binding{}, errors.New("poll_interval forbidden for EventDrivenFeature")
		}
	default:
		return Binding{}, fmt.Errorf("feature %q implements neither PollFeature nor EventDrivenFeature", raw.Feature)
	}

	if err := feat.Validate(b.Config); err != nil {
		return Binding{}, fmt.Errorf("feature %q config validation: %w", raw.Feature, err)
	}
	return b, nil
}

func validateSlashGuild(raw rawSlashGuild) (SlashGuild, error) {
	if !snowflakePattern.MatchString(raw.GuildID) {
		return SlashGuild{}, errors.New("guild_id must be a non-empty numeric snowflake string")
	}
	if !snowflakePattern.MatchString(raw.WatchChannelID) {
		return SlashGuild{}, errors.New("watch_channel_id must be a non-empty numeric snowflake string")
	}
	for i, roleID := range raw.AllowedRoleIDs {
		if !snowflakePattern.MatchString(roleID) {
			return SlashGuild{}, fmt.Errorf("allowed_role_ids[%d] %q must be a numeric snowflake string", i, roleID)
		}
	}
	return SlashGuild{
		GuildID:        raw.GuildID,
		WatchChannelID: raw.WatchChannelID,
		AllowedRoleIDs: raw.AllowedRoleIDs,
	}, nil
}
