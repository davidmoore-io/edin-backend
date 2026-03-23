package discord

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/controlclient"
	"github.com/edin-space/edin-backend/internal/observability"
)

// Bot coordinates Discord interactions with the control API.
type Bot struct {
	cfg         *config.Config
	session     *discordgo.Session
	control     *controlclient.Client
	mcp         *controlclient.MCPClient
	logger      *observability.Logger
	commands    []commandRegistration
	commandDefs []*discordgo.ApplicationCommand
}

type commandRegistration struct {
	GuildID   string
	CommandID string
}

// Run connects to Discord gateway and processes events.
func Run(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	controlClient, err := controlclient.New(cfg.ControlAPIBaseURL, cfg.HTTP.InternalKey)
	if err != nil {
		return fmt.Errorf("create control client: %w", err)
	}

	mcpClient, err := controlclient.NewMCPClient(cfg.MCPBaseURL, cfg.HTTP.InternalKey)
	if err != nil {
		return fmt.Errorf("create mcp client: %w", err)
	}

	session, err := discordgo.New("Bot " + cfg.Discord.BotToken)
	if err != nil {
		return fmt.Errorf("create discord session: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages
	session.State.TrackVoice = false

	bot := &Bot{
		cfg:     cfg,
		session: session,
		control: controlClient,
		mcp:     mcpClient,
		logger:  observability.NewLogger("discord-bot"),
	}
	bot.commandDefs = buildCommandDefinitions(cfg)

	bot.registerInteractionHandler()
	bot.registerLifecycleHandlers()

	if err := bot.session.Open(); err != nil {
		return fmt.Errorf("open discord session: %w", err)
	}
	defer bot.session.Close()

	if err := bot.registerCommands(); err != nil {
		return err
	}
	defer bot.deleteCommands()

	bot.logger.Info("Discord bot connected and ready.")

	<-ctx.Done()
	return nil
}

func (b *Bot) registerCommands() error {
	targetGuilds := b.cfg.Discord.GuildIDs
	if len(targetGuilds) == 0 {
		targetGuilds = []string{""} // Global
	}

	b.commands = b.commands[:0]

	for _, guildID := range targetGuilds {
		created, err := b.session.ApplicationCommandBulkOverwrite(b.cfg.Discord.AppID, guildID, b.commandDefs)
		if err != nil {
			return fmt.Errorf("register commands for guild %q: %w", guildID, err)
		}
		for _, cmd := range created {
			b.commands = append(b.commands, commandRegistration{
				GuildID:   guildID,
				CommandID: cmd.ID,
			})
		}
	}
	return nil
}

func (b *Bot) deleteCommands() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for _, registration := range b.commands {
		select {
		case <-ctx.Done():
			return
		default:
			if err := b.session.ApplicationCommandDelete(b.cfg.Discord.AppID, registration.GuildID, registration.CommandID); err != nil {
				b.logger.Error("delete command", err)
			}
		}
	}
}

func (b *Bot) registerLifecycleHandlers() {
	b.session.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		b.logger.Info(fmt.Sprintf("Logged in as %s#%s", r.User.Username, r.User.Discriminator))
	})
	b.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		b.logger.Warn("Discord gateway disconnected")
	})
	b.session.AddHandler(func(_ *discordgo.Session, e *discordgo.Resumed) {
		b.logger.Info("Connection resumed")
	})
}

func (b *Bot) validateGuildCommand(i *discordgo.InteractionCreate) error {
	if i.GuildID == "" {
		return errors.New("commands must be run from within a guild")
	}
	return nil
}
