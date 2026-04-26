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

type fileShape struct {
	Bindings []rawBinding `yaml:"bindings"`
}

type rawBinding struct {
	ID           string         `yaml:"id"`
	GuildID      string         `yaml:"guild_id"`
	ChannelID    string         `yaml:"channel_id"`
	Feature      string         `yaml:"feature"`
	PollInterval string         `yaml:"poll_interval,omitempty"`
	Config       map[string]any `yaml:"config,omitempty"`
}

// Load parses YAML from r, validates every binding against the registered
// feature's expectations, and returns the resolved []Binding. Any validation
// failure is fatal — partial results are never returned. Per spec §6.
func Load(r io.Reader) ([]Binding, error) {
	var f fileShape
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse bindings yaml: %w", err)
	}

	out := make([]Binding, 0, len(f.Bindings))
	seenID := map[string]bool{}

	for i, raw := range f.Bindings {
		b, err := validate(raw)
		if err != nil {
			return nil, fmt.Errorf("binding[%d] (%q): %w", i, raw.ID, err)
		}
		if seenID[b.ID] {
			return nil, fmt.Errorf("binding[%d]: duplicate id %q", i, b.ID)
		}
		seenID[b.ID] = true
		out = append(out, b)
	}
	return out, nil
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
