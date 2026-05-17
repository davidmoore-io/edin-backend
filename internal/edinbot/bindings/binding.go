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

// SlashGuild holds slash-command registration config for one Discord guild.
// AllowedRoleIDs drives which Discord permission model is used:
//
//   - Empty: DefaultMemberPermissions="8" (admin-only). Discord hides the
//     command from non-admins across all channels. Admins see it everywhere
//     but the runtime channel gate enforces the watch channel restriction.
//
//   - Non-empty: DefaultMemberPermissions="0" (hidden for all by default),
//     then the Application Command Permissions API grants the listed roles
//     and restricts visibility to WatchChannelID only.
type SlashGuild struct {
	GuildID        string   // Discord snowflake
	WatchChannelID string   // Channel where /watch and /unwatch are honoured
	AllowedRoleIDs []string // Discord role snowflakes; empty = admin-only
	// AllowedUserIDs is an optional runtime allowlist. When non-empty, only
	// these user snowflakes may use the slash commands in this guild; anyone
	// else receives an ephemeral "no permission" reply. Intended as a stopgap
	// until Discord-side role/channel restrictions can be configured via the
	// Integrations UI (requires Administrator in the target guild).
	AllowedUserIDs []string
}

// Config is the parsed, validated result of loading bindings.yml.
type Config struct {
	Bindings    []Binding
	SlashGuilds []SlashGuild
}
