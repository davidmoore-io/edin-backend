// Package bindings parses and validates the deploy-time bindings YAML.
// One Binding = one (guild, channel, feature) mapping with optional config.
//
// The canonical Binding.ID is the YAML 'id' field — the same string used as
// binding_id throughout discord.* tables in postgres. See spec §4 + §6.
package bindings

import "time"

// Binding is one row in bindings.yml after parse + validate.
type Binding struct {
	ID           string         // canonical id, used everywhere as binding_id
	GuildID      string         // Discord snowflake (numeric string)
	ChannelID    string         // Discord snowflake
	FeatureName  string         // Registry key
	PollInterval time.Duration  // 0 for EventDrivenFeature
	Config       map[string]any
	IsPoll       bool // true if the registered feature is a PollFeature
	IsEvent      bool // true if the registered feature is an EventDrivenFeature
}
