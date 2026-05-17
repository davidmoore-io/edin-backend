// Package watcher implements the /watch /unwatch system-watch feature.
//
// Architecture (Phase 4 of docs/plans/system-watch-feature.md):
//
//	slash.Router → handler.Watch / handler.Unwatch → store + control-API + Discord
//	                                                ↓
//	                                          watched_systems row
//	                                                ↓
//	                    Watcher loop (120s ticker) → control-API → diff against last_state_hash
//	                                                               ↓
//	                                                    Discord EditMessage on change
//
// The watcher is *not* a publisher.PollFeature — it has a fundamentally
// different lifecycle (user-driven creation, per-row polling, no
// strikethrough/spoiler) and warrants its own home rather than fitting
// the alerts model awkwardly. Cross-package dependencies are kept narrow
// via small interfaces (Store, Snapshotter, Discord) so handler tests
// can drop in real-data fakes without monkey-patching.
package watcher

import (
	"context"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// Store is the watcher's narrow view of the bot's persistence. Defined here
// rather than reusing store.Store directly so handler tests can implement a
// 6-method fake, not the 18-method full surface. *PostgresStore satisfies
// both interfaces.
type Store interface {
	AddWatch(ctx context.Context, w store.WatchedSystem) error
	RemoveWatch(ctx context.Context, channelID, systemSlug string) (bool, error)
	GetWatch(ctx context.Context, channelID, systemSlug string) (*store.WatchedSystem, error)
	ListAllWatches(ctx context.Context) ([]store.WatchedSystem, error)
	CountWatchesInChannel(ctx context.Context, channelID string) (int, error)
	UpdateWatchState(ctx context.Context, channelID, systemSlug, hash string, render []byte, updatedAt time.Time) error
}

// Snapshotter is the watcher's view of the control-API. Defined as an
// interface so handler tests can drop in a fixture-driven fake rather than
// stand up an HTTP server.
type Snapshotter interface {
	GetSystemWatchSnapshot(ctx context.Context, slug string) (*controlclient.SystemWatchSnapshot, error)
}

// Discord is the watcher's view of the Discord I/O surface — three methods
// from discordclient.Client, the rest of which is alert-shaped and not
// relevant here. *discordclient.RealClient satisfies both interfaces.
type Discord interface {
	PostMessage(ctx context.Context, channelID string, embed *discordgo.MessageEmbed) (string, error)
	EditMessage(ctx context.Context, channelID, messageID string, embed *discordgo.MessageEmbed) error
	DeleteMessage(ctx context.Context, channelID, messageID string) error
}

// Config gates the watcher's external behaviour.
type Config struct {
	// MaxWatchesPerChannel caps how many concurrent watches a channel
	// holds. Default 50; protects against accidental flooding by a
	// curious commander running /watch on every system in the bubble.
	MaxWatchesPerChannel int

	// PollInterval — how often the watch loop rebuilds. Default 120s;
	// shorter wastes API calls (kaine state moves on a slower cadence
	// than that), longer feels stale to operators staring at the
	// channel.
	PollInterval time.Duration

	// PerWatchStagger — delay between consecutive watch fetches inside
	// one tick. Default 1s; protects Memgraph + control-API from a
	// thundering-herd of N parallel queries when the cap is full.
	PerWatchStagger time.Duration
}

// Defaults returns a copy of the Config with zero-valued fields filled
// in from the documented defaults. Both the handler factory and the
// watcher loop call this at construction time, so callers can pass
// Config{} and rely on the defaults landing.
func (c Config) Defaults() Config { return c.defaults() }

// defaults applies the documented defaults when the embedding caller
// leaves zero values. Centralised so the tests can construct a Config{}
// literal and still get sensible behaviour.
func (c Config) defaults() Config {
	if c.MaxWatchesPerChannel == 0 {
		c.MaxWatchesPerChannel = 50
	}
	if c.PollInterval == 0 {
		c.PollInterval = 120 * time.Second
	}
	if c.PerWatchStagger == 0 {
		c.PerWatchStagger = 1 * time.Second
	}
	return c
}
