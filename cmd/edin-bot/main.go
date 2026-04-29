// edin-bot — the EDIN multi-server, multi-channel Discord bot. See
// docs/superpowers/specs/2026-04-26-edin-discord-bot-design.md for
// the design.
//
// All wiring decisions live HERE. No business logic. Concrete features are
// constructed and assigned to features.Registry directly; nothing imports a
// global "register all" helper.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/authclient"
	"github.com/edin-space/edin-backend/internal/edinbot/bindings"
	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/features"
	"github.com/edin-space/edin-backend/internal/edinbot/features/watcher"
	"github.com/edin-space/edin-backend/internal/edinbot/httpserver"
	"github.com/edin-space/edin-backend/internal/edinbot/publisher"
	"github.com/edin-space/edin-backend/internal/edinbot/scheduler"
	"github.com/edin-space/edin-backend/internal/edinbot/slash"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
)

// version is set via -ldflags at build time (see Dockerfile + Makefile).
var version = "dev"

func main() {
	if err := run(); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
}

func run() error {
	cfg, err := loadEnv()
	if err != nil {
		return fmt.Errorf("load env: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 1. postgres pool + schema migration
	pool, err := pgxpool.New(ctx, cfg.PostgresURL)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()
	if err := store.MigrateDiscordSchema(ctx, pool); err != nil {
		return fmt.Errorf("migrate discord schema: %w", err)
	}
	st := store.NewPostgresStore(pool)

	// 2. auth + control-API client
	auth := authclient.New(authclient.Config{
		TokenURL:        cfg.OAuthTokenURL,
		ClientID:        cfg.OAuthClientID,
		ClientSecret:    cfg.OAuthClientSecret,
		Scope:           cfg.OAuthScope,
		RefreshLeadTime: 30 * time.Second,
	})
	control := controlclient.New(cfg.ControlAPIURL, auth)

	// 3. Discord client
	dc, err := discordclient.NewRealClient(cfg.DiscordBotToken)
	if err != nil {
		return fmt.Errorf("discord client: %w", err)
	}
	defer dc.Close()

	// 4. ops bus and feature registration
	bus := scheduler.NewOpsBus()
	registerFeatures(control, bus, ctx)

	// 5. boot recovery for ops-health-alerts
	if err := loadPriorOutages(ctx, st); err != nil {
		log.Printf("[WARN] load prior outages: %v (continuing — recoveries for prior outages may be lost)", err)
	}

	// 6. bindings YAML
	bs, err := loadBindings(cfg.BindingsPath)
	if err != nil {
		return fmt.Errorf("load bindings: %w", err)
	}

	// 7. publisher + scheduler
	pub := publisher.New(st, dc)
	sch := scheduler.New(scheduler.Config{
		Bindings:  bs,
		Publisher: pub,
		Store:     st,
		Bus:       bus,
		StaggerMs: 500,
	})

	// 8. http server (separate goroutine)
	srv := httpserver.New(httpserver.Config{
		Addr:       ":8080",
		Health:     newHealthOracle(st, bs),
		Trigger:    sch,
		Cleaner:    pub,
		AdminToken: cfg.AdminToken,
		Version:    version,
	})
	go func() {
		if err := srv.Start(ctx); err != nil {
			log.Printf("[ERROR] http server: %v", err)
		}
	}()

	if err := sch.Start(ctx); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}

	// 9. Slash-command router + /watch /unwatch handlers + watcher loop.
	// Wired only when WATCH_CHANNEL_ID and SLASH_GUILD_ID are set;
	// otherwise the bot keeps the alerts-only behaviour and the slash
	// surface is dormant. Both ID env vars carry the same Kaine guild
	// today; kept separate so a second guild could host /watch later
	// without forking the alerts deploy.
	if cfg.WatchChannelID != "" && cfg.SlashGuildID != "" {
		if err := startSlashAndWatcher(ctx, cfg, st, control, dc); err != nil {
			log.Printf("[WARN] slash/watcher startup failed: %v (continuing without /watch feature)", err)
		} else {
			log.Printf("[INFO] /watch /unwatch enabled in channel %s (guild %s)", cfg.WatchChannelID, cfg.SlashGuildID)
		}
	} else {
		log.Printf("[INFO] /watch /unwatch disabled — set WATCH_CHANNEL_ID and SLASH_GUILD_ID to enable")
	}

	log.Printf("[INFO] edin-bot %s started; %d binding(s)", version, len(bs))
	<-ctx.Done()
	log.Printf("[INFO] shutting down")
	return sch.Stop(10 * time.Second)
}

// startSlashAndWatcher registers slash commands with Discord, attaches the
// router as an InteractionCreate listener, and kicks off the polling
// goroutine. Failures here are non-fatal — the alerts side of the bot
// keeps running even if the watch feature can't initialise (e.g. Discord
// rejected the command spec). All wiring of concrete deps lives here.
func startSlashAndWatcher(ctx context.Context, cfg envConfig, st *store.PostgresStore, control *controlclient.Client, dc *discordclient.RealClient) error {
	deps := watcher.HandlerDeps{
		Store:   st,
		Snap:    control,
		Discord: dc,
		Cfg: watcher.Config{
			AllowedChannelID: cfg.WatchChannelID,
			// MaxWatchesPerChannel / PollInterval / PerWatchStagger
			// fall back to defaults via Config.Defaults().
		},
		GuildID: cfg.SlashGuildID,
	}

	router := slash.NewRouter(slash.Config{
		AllowedChannelIDs:  []string{cfg.WatchChannelID},
		RequirePermissions: discordgo.PermissionAdministrator,
	})
	router.Handle("watch", watcher.Watch(deps))
	router.Handle("unwatch", watcher.Unwatch(deps))
	dc.Session().AddHandler(router.Dispatch)

	// Register guild-local slash commands. Re-registering with the same
	// name + signature is a no-op on Discord's side; a deploy that
	// changes the option list propagates instantly because guild
	// commands skip the global cache.
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "watch",
			Description: "Watch a system for powerplay/faction changes",
			Options: []*discordgo.ApplicationCommandOption{{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "system",
				Description: "System name (e.g. HIP 61332)",
				Required:    true,
			}},
		},
		{
			Name:        "unwatch",
			Description: "Stop watching a system",
			Options: []*discordgo.ApplicationCommandOption{{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "system",
				Description: "System name to stop watching",
				Required:    true,
			}},
		},
	}
	sess := dc.Session()
	for _, cmd := range commands {
		if _, err := sess.ApplicationCommandCreate(sess.State.User.ID, cfg.SlashGuildID, cmd); err != nil {
			return fmt.Errorf("register %s: %w", cmd.Name, err)
		}
	}

	// Kick the polling goroutine. NewWatcher applies Config defaults
	// internally; the deps above carry only the channel-ID gate which
	// the loop doesn't consume (it polls every persisted row regardless
	// of channel — handlers gate intake at /watch time).
	w := watcher.NewWatcher(watcher.LoopDeps{
		Store:   st,
		Snap:    control,
		Discord: dc,
		Cfg:     watcher.Config{},
	})
	w.Start(ctx)

	return nil
}

// envConfig is read from the environment (which docker-compose populates from
// the rendered edin-bot.env file).
type envConfig struct {
	DiscordBotToken   string
	OAuthTokenURL     string
	OAuthClientID     string
	OAuthClientSecret string
	OAuthScope        string
	ControlAPIURL     string
	PostgresURL       string
	BindingsPath      string
	AdminToken        string
	WatchChannelID    string
	SlashGuildID      string
}

func loadEnv() (envConfig, error) {
	c := envConfig{
		DiscordBotToken:   os.Getenv("DISCORD_BOT_TOKEN"),
		OAuthTokenURL:     os.Getenv("OAUTH_TOKEN_URL"),
		OAuthClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		OAuthClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
		OAuthScope:        os.Getenv("OAUTH_SCOPE"),
		ControlAPIURL:     os.Getenv("CONTROL_API_URL"),
		PostgresURL:       os.Getenv("POSTGRES_URL"),
		BindingsPath:      os.Getenv("BINDINGS_PATH"),
		AdminToken:        os.Getenv("ADMIN_TOKEN"),
		WatchChannelID:    os.Getenv("WATCH_CHANNEL_ID"),
		SlashGuildID:      os.Getenv("SLASH_GUILD_ID"),
	}
	if c.BindingsPath == "" {
		c.BindingsPath = "/etc/edin-bot/bindings.yml"
	}
	if c.OAuthScope == "" {
		// Authentik rejects "openid" on client_credentials grants (no user
		// session → no OIDC). Just the custom group-mapping scope is enough
		// to attach the bot:edin group claim to the JWT.
		c.OAuthScope = "edin-bot-groups"
	}
	missing := []string{}
	if c.DiscordBotToken == "" {
		missing = append(missing, "DISCORD_BOT_TOKEN")
	}
	if c.OAuthTokenURL == "" {
		missing = append(missing, "OAUTH_TOKEN_URL")
	}
	if c.OAuthClientID == "" {
		missing = append(missing, "OAUTH_CLIENT_ID")
	}
	if c.OAuthClientSecret == "" {
		missing = append(missing, "OAUTH_CLIENT_SECRET")
	}
	if c.ControlAPIURL == "" {
		missing = append(missing, "CONTROL_API_URL")
	}
	if c.PostgresURL == "" {
		missing = append(missing, "POSTGRES_URL")
	}
	if len(missing) > 0 {
		return envConfig{}, fmt.Errorf("missing env vars: %v", missing)
	}
	return c, nil
}

// registerFeatures explicitly populates features.Registry with fully-wired
// concrete features. Per the Phase 12 escalation: registration happens here,
// in main.go, NOT in init() functions in the features package.
func registerFeatures(control *controlclient.Client, bus *scheduler.OpsBus, ctx context.Context) {
	features.Registry["platinum-boom-alerts"] = features.NewPlatinumBoomAlerts(control)
	features.Registry["ltd-alerts"] = features.NewLTDAlerts(control)
	features.Registry["ops-health-alerts"] = features.NewOpsHealthAlerts(func() <-chan features.OpsBusEvent {
		// Bridge scheduler.OpsBus → features.OpsBusEvent. The two are separate
		// types to avoid an import cycle. Subscriber lifetime = process
		// lifetime (the goroutine exits when ctx is cancelled).
		src := bus.Subscribe(ctx)
		dst := make(chan features.OpsBusEvent, 16)
		go func() {
			defer close(dst)
			for ev := range src {
				dst <- features.OpsBusEvent{
					BindingID:  ev.BindingID,
					Reason:     ev.Reason,
					Attempts:   ev.Attempts,
					Error:      ev.Error,
					Report:     ev.Report,
					OccurredAt: ev.OccurredAt,
				}
			}
		}()
		return dst
	})
}

// loadPriorOutages queries non-struck 'outage:*' rows from
// discord.posted_messages and seeds the ops-health-alerts feature's outage
// map. The ops binding ID is hard-coded to "edin-ops" (matches the canonical
// bindings.yml). If a future bindings.yml uses a different id, this function
// must be updated.
func loadPriorOutages(ctx context.Context, st store.Store) error {
	feat, ok := features.Registry["ops-health-alerts"].(*features.OpsHealthAlerts)
	if !ok {
		return fmt.Errorf("ops-health-alerts not registered or wrong type")
	}

	const opsBindingID = "edin-ops"
	posted, err := st.GetPosted(ctx, opsBindingID)
	if err != nil {
		return err
	}

	prior := []features.PriorOutage{}
	for identity, m := range posted {
		if m.StruckAt != nil {
			continue
		}
		// identity = "outage:<binding>:<reason>:<RFC3339>"
		parts := strings.SplitN(identity, ":", 4)
		if len(parts) != 4 || parts[0] != "outage" {
			continue
		}
		startedAt, err := time.Parse(time.RFC3339, parts[3])
		if err != nil {
			continue
		}
		prior = append(prior, features.PriorOutage{
			Identity:  identity,
			BindingID: parts[1],
			Reason:    parts[2],
			StartedAt: startedAt,
		})
	}
	return feat.LoadPriorOutages(prior)
}

func loadBindings(path string) ([]bindings.Binding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return bindings.Load(f)
}

// healthOracle implements httpserver.HealthOracle by querying
// discord.poll_cycles for recent successful cycles per binding.
//
// A binding is "healthy" when its most-recent ticked_at where status IN
// ('success', 'event') is within:
//   - 2 × PollInterval for PollFeature bindings
//   - eventHealthWindow (default 6h) for EventDrivenFeature bindings
//
// The startupGrace covers the first few minutes of bot lifetime where no
// poll has yet completed: any binding younger than startupGrace at process
// boot reports healthy unconditionally.
type healthOracle struct {
	st           store.Store
	bindings     []bindings.Binding
	startedAt    time.Time
	startupGrace time.Duration
	eventWindow  time.Duration
}

func newHealthOracle(st store.Store, bs []bindings.Binding) *healthOracle {
	return &healthOracle{
		st:           st,
		bindings:     bs,
		startedAt:    time.Now(),
		startupGrace: 2 * time.Minute,
		eventWindow:  6 * time.Hour,
	}
}

func (h *healthOracle) AllBindingsHealthy() bool {
	for _, v := range h.PerBindingHealth() {
		if !v {
			return false
		}
	}
	return true
}

func (h *healthOracle) PerBindingHealth() map[string]bool {
	out := map[string]bool{}
	now := time.Now()
	stillStarting := now.Sub(h.startedAt) < h.startupGrace

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for _, b := range h.bindings {
		if stillStarting {
			out[b.ID] = true
			continue
		}
		latest, err := h.st.LatestSuccessAt(ctx, b.ID)
		if err != nil {
			// Treat query errors as unhealthy — Discord operators should
			// know the bot can't even ask the DB.
			out[b.ID] = false
			continue
		}
		if latest.IsZero() {
			// Never had a successful cycle past the startup grace window —
			// definitely unhealthy.
			out[b.ID] = false
			continue
		}
		var window time.Duration
		if b.IsPoll {
			window = 2 * b.PollInterval
		} else {
			window = h.eventWindow
		}
		out[b.ID] = now.Sub(latest) <= window
	}
	return out
}
