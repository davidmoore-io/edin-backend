// Package slash routes Discord slash-command interactions to handlers and
// applies channel/permission gates before dispatch.
//
// Why a dedicated package: the existing edinbot features ride scheduled
// pollers (PollFeature) or pubsub (EventDrivenFeature). Slash commands are
// an entirely different lifecycle — they're user-initiated request/response
// interactions with a strict 3-second initial-reply window. Mixing the two
// in the scheduler package would muddle responsibilities, so the slash
// router lives on its own.
//
// Surface area is deliberately small:
//
//	r := slash.NewRouter(slash.Config{
//	    AllowedChannelIDs:    []string{"1498813935057637597"},
//	    RequirePermissions:   discordgo.PermissionAdministrator,
//	    Logger:               log.Default(),
//	})
//	r.Handle("watch",   watchHandler.Run)
//	r.Handle("unwatch", unwatchHandler.Run)
//
//	sess.AddHandler(r.Dispatch)  // wire into the discordgo session
package slash

import (
	"context"
	"fmt"
	"log"

	"github.com/bwmarrin/discordgo"
)

// Responder is the small surface area handlers use to reply to interactions
// — abstracted from *discordgo.Session so handler tests can substitute a
// recorder without a network stack. Production wiring is *discordgo.Session
// (it implements all four methods natively).
type Responder interface {
	InteractionRespond(ic *discordgo.Interaction, resp *discordgo.InteractionResponse, opts ...discordgo.RequestOption) error
	InteractionResponseEdit(ic *discordgo.Interaction, edit *discordgo.WebhookEdit, opts ...discordgo.RequestOption) (*discordgo.Message, error)
	FollowupMessageCreate(ic *discordgo.Interaction, wait bool, params *discordgo.WebhookParams, opts ...discordgo.RequestOption) (*discordgo.Message, error)
}

// Handler runs one slash command. Implementations are responsible for
// calling the appropriate Interaction-respond / -follow-up method via the
// Responder — the router only dispatches; it doesn't reply.
type Handler func(ctx context.Context, resp Responder, ic *discordgo.InteractionCreate) error

// Config governs gate behaviour for the whole router. All slash commands
// registered with the router share the same gate; a future revision can
// add per-command overrides.
type Config struct {
	// AllowedChannelIDs are the channels in which slash commands are
	// honoured. An interaction in any other channel gets an ephemeral
	// "this command is not enabled in this channel" reply. nil/empty
	// means "any channel" (use sparingly).
	AllowedChannelIDs []string

	// RequirePermissions is a Discord permissions bitmask the calling
	// member must hold. Defaults to PermissionAdministrator. The slice
	// shape isn't necessary today — admin OR another role would be
	// expressed by adding more bits to this single bitmask.
	RequirePermissions int64

	// Logger receives gate decisions and dispatch errors. nil → log.Default.
	Logger *log.Logger
}

// Router stores command-name → handler mappings and applies the gate.
type Router struct {
	cfg      Config
	handlers map[string]Handler
}

// NewRouter constructs a router with the given gate config. Commands are
// registered separately via Handle.
func NewRouter(cfg Config) *Router {
	if cfg.RequirePermissions == 0 {
		cfg.RequirePermissions = discordgo.PermissionAdministrator
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Router{cfg: cfg, handlers: map[string]Handler{}}
}

// Handle registers a handler for a top-level command name (e.g. "watch").
// Subcommands aren't supported today — the only commands the bot exposes
// are flat. Re-registering an existing name is a programmer error and
// panics on bot startup so the duplicate is impossible to miss.
func (r *Router) Handle(name string, h Handler) {
	if _, exists := r.handlers[name]; exists {
		panic(fmt.Sprintf("slash: handler for %q already registered", name))
	}
	r.handlers[name] = h
}

// Dispatch is the discordgo InteractionCreate listener. Wire it via
// session.AddHandler(router.Dispatch). Non-ApplicationCommand interactions
// (button clicks, modal submits) are ignored here so future bot features
// can register their own handlers without conflicting.
//
// Discord's session struct satisfies the Responder interface, so tests can
// substitute a recorder while production passes the live session.
func (r *Router) Dispatch(sess *discordgo.Session, ic *discordgo.InteractionCreate) {
	r.dispatch(sess, ic)
}

// dispatch is the testable core. Takes a Responder rather than a concrete
// session so unit tests can verify ephemeral content without a network
// stack. The exported Dispatch above adapts the discordgo signature.
func (r *Router) dispatch(resp Responder, ic *discordgo.InteractionCreate) {
	if ic.Type != discordgo.InteractionApplicationCommand {
		return
	}
	cmdName := ic.ApplicationCommandData().Name
	h, ok := r.handlers[cmdName]
	if !ok {
		// Unknown command — most likely a stale registration on Discord's
		// side. Log it but don't reply (Discord would show a generic
		// "this command is no longer available" anyway).
		r.cfg.Logger.Printf("slash: no handler for command %q (interaction id=%s)", cmdName, ic.ID)
		return
	}

	// Channel gate.
	if !r.channelAllowed(ic.ChannelID) {
		r.replyEphemeral(resp, ic, "This command is not enabled in this channel.")
		return
	}

	// Permission gate. ic.Member is nil in DM contexts — the channel gate
	// above blocks those today (DMs aren't in any allowed channel list)
	// so a nil Member here would mean Discord didn't populate it, which
	// is itself a permission failure.
	if ic.Member == nil {
		r.replyEphemeral(resp, ic, "This command requires guild membership.")
		return
	}
	if ic.Member.Permissions&r.cfg.RequirePermissions == 0 {
		r.replyEphemeral(resp, ic,
			"This command requires the Administrator permission.")
		return
	}

	// Dispatch. Handlers are responsible for their own response timing
	// (defer-then-follow-up if the work might take >3s).
	if err := h(context.Background(), resp, ic); err != nil {
		r.cfg.Logger.Printf("slash: handler %q failed: %v", cmdName, err)
	}
}

// DispatchForTest is the unit-test entrypoint — same logic as Dispatch but
// takes a Responder fake instead of a live discordgo session.
func (r *Router) DispatchForTest(resp Responder, ic *discordgo.InteractionCreate) {
	r.dispatch(resp, ic)
}

func (r *Router) channelAllowed(channelID string) bool {
	if len(r.cfg.AllowedChannelIDs) == 0 {
		return true
	}
	for _, id := range r.cfg.AllowedChannelIDs {
		if id == channelID {
			return true
		}
	}
	return false
}

// replyEphemeral sends a one-liner only the caller sees. Discord requires
// an interaction reply within 3s; this lands well inside that window.
func (r *Router) replyEphemeral(resp Responder, ic *discordgo.InteractionCreate, text string) {
	err := resp.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: text,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		r.cfg.Logger.Printf("slash: ephemeral reply failed: %v", err)
	}
}
