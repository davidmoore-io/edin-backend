package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/edin-space/edin-backend/internal/edinbot/controlclient"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/edin-space/edin-backend/internal/edinbot/slash"
	"github.com/edin-space/edin-backend/internal/edinbot/store"
	"github.com/edin-space/edin-backend/internal/galaxy"
)

// knownNotFoundErrors enumerates the discordclient sentinels that all map
// to "the resource we wanted is already gone". Used by isDiscordNotFound;
// kept as a slice rather than a switch so a future client addition is a
// one-line append.
var knownNotFoundErrors = []error{
	discordclient.ErrChannelGone,
	discordclient.ErrMessageNotFound,
}

// HandlerDeps bundles every collaborator the /watch and /unwatch handlers
// need. Fields are interfaces so handler tests can drop in fakes; the
// production wire-up in cmd/edin-bot/main.go passes the concrete
// PostgresStore, controlclient.Client, and discordclient.RealClient.
type HandlerDeps struct {
	Store   Store
	Snap    Snapshotter
	Discord Discord
	Cfg     Config
	GuildID string // Kaine guild — used to set guild_id on the persisted row
	NowFunc func() time.Time
	LogFunc func(format string, args ...any) // optional; defaults to log.Printf
}

// now returns the configured clock or wallclock if not configured. Tests
// inject a deterministic NowFunc; production leaves it nil.
func (d *HandlerDeps) now() time.Time {
	if d.NowFunc != nil {
		return d.NowFunc()
	}
	return time.Now().UTC()
}

func (d *HandlerDeps) logf(format string, args ...any) {
	if d.LogFunc != nil {
		d.LogFunc(format, args...)
		return
	}
	log.Printf(format, args...)
}

// systemOption fishes the "system" string option out of an interaction's
// options slice. Discord transports them as a slice rather than a map.
func systemOption(ic *discordgo.InteractionCreate) string {
	for _, o := range ic.ApplicationCommandData().Options {
		if o.Name == "system" {
			return o.StringValue()
		}
	}
	return ""
}

// deferEphemeral acknowledges the interaction with a "thinking..."
// placeholder, ephemeral. Discord allows 3 seconds between receiving the
// interaction and the bot's first response; deferring buys an additional
// 15-minute window in which we can edit the deferred response with the
// real reply. Without deferral a slow Memgraph or Discord post (>3s)
// would cause Discord to drop the interaction and the operator would
// see "interaction failed" with no recourse.
func deferEphemeral(resp slash.Responder, ic *discordgo.InteractionCreate) error {
	return resp.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
		},
	})
}

// reply edits the deferred ephemeral response with the real content. Used
// for every /watch and /unwatch result branch — even success — so the
// channel shows only the actual watched-system message, not chatter.
//
// Must be called after deferEphemeral. If the defer was skipped (e.g.
// validation rejected the interaction before any I/O), use replyImmediate
// instead.
func reply(resp slash.Responder, ic *discordgo.InteractionCreate, content string) error {
	_, err := resp.InteractionResponseEdit(ic.Interaction, &discordgo.WebhookEdit{
		Content: &content,
	})
	return err
}

// messageLink builds a Discord deep-link to a channel message. Used in
// the "already watched" reply so commanders can jump straight to the
// existing watch message rather than scrolling.
func messageLink(guildID, channelID, messageID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, messageID)
}

// Watch returns a slash.Handler bound to the supplied deps. Lives outside
// the Watcher type because the handler doesn't need to share state with
// the polling goroutine — the goroutine reads from the same store on its
// own ticker and picks up new watches naturally on the next pass.
//
// Behaviour branches (each ending in an ephemeral reply):
//   - Empty input              → "system name required"
//   - Memgraph 404              → "I can't find a system named X"
//   - Cap reached               → "channel already has the maximum N watches"
//   - Already watched           → "X is already being watched in this channel"
//   - Discord post failed       → "I couldn't post the watch message"
//   - Happy path                → "Now watching X" with a link
func Watch(deps HandlerDeps) slash.Handler {
	deps.Cfg = deps.Cfg.Defaults()
	return func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		// Acknowledge the interaction within Discord's 3-second window
		// before any I/O. Every reply path below is reached via reply()
		// which uses InteractionResponseEdit on the deferred reply.
		if err := deferEphemeral(resp, ic); err != nil {
			deps.logf("[ERROR] watch: defer ack failed: %v", err)
			return err
		}

		raw := strings.TrimSpace(systemOption(ic))
		if raw == "" {
			return reply(resp, ic, "Please provide a system name: `/watch <system>`.")
		}
		slug := galaxy.Slugify(raw)
		if slug == "" {
			return reply(resp, ic, "I couldn't form a valid system slug from that input.")
		}

		// 1. Existence check — galaxy data must know about the system
		// before we promise to watch it.
		snap, err := deps.Snap.GetSystemWatchSnapshot(ctx, slug)
		if err != nil {
			if errors.Is(err, controlclient.ErrSystemNotFound) {
				return reply(resp, ic,
					fmt.Sprintf("I can't find a system named **%s** in our galaxy data. Check the spelling?", raw))
			}
			deps.logf("[ERROR] watch: snapshot fetch failed for %q: %v", slug, err)
			return reply(resp, ic,
				"Something went wrong looking up that system. Please try again in a moment.")
		}

		// 2. Already-watched check before the cap so the polite reply
		// doesn't tell someone "the channel is full" when their dupe
		// would have collided anyway.
		if existing, err := deps.Store.GetWatch(ctx, ic.ChannelID, slug); err == nil && existing != nil {
			return reply(resp, ic, fmt.Sprintf(
				"**%s** is already being watched in this channel — see %s",
				existing.SystemName, messageLink(existing.GuildID, existing.ChannelID, existing.MessageID)))
		}

		// 3. Capacity gate.
		if count, err := deps.Store.CountWatchesInChannel(ctx, ic.ChannelID); err == nil && count >= deps.Cfg.MaxWatchesPerChannel {
			return reply(resp, ic, fmt.Sprintf(
				"This channel already has the maximum %d watches; `/unwatch` one before adding another.",
				deps.Cfg.MaxWatchesPerChannel))
		}

		// 4. Render + post.
		now := deps.now()
		userID := ""
		if ic.Member != nil && ic.Member.User != nil {
			userID = ic.Member.User.ID
		}
		embed := Render(snap, now.Unix(), userID)
		msgID, err := deps.Discord.PostMessage(ctx, ic.ChannelID, embed)
		if err != nil {
			deps.logf("[ERROR] watch: post message for %q in %s failed: %v", slug, ic.ChannelID, err)
			return reply(resp, ic, "I couldn't post the watch message. Check that I have Send Messages permission in this channel.")
		}

		// 5. Persist. AddWatch can race with another concurrent /watch
		// for the same (channel, slug); a unique-violation here is the
		// dedup hitting at the same moment as the GetWatch above. We
		// roll back the just-posted Discord message to keep the table
		// in sync with reality.
		raw_render, _ := json.Marshal(embed)
		err = deps.Store.AddWatch(ctx, store.WatchedSystem{
			GuildID:       deps.GuildID,
			ChannelID:     ic.ChannelID,
			SystemSlug:    slug,
			SystemName:    snap.Name,
			MessageID:     msgID,
			CreatedBy:     userID,
			WatchedAt:     now,
			LastUpdatedAt: now,
			LastStateHash: stateHash(snap),
			LastRender:    raw_render,
		})
		if err != nil {
			if errors.Is(err, store.ErrAlreadyWatched) {
				// Race: someone else's /watch landed between our
				// GetWatch and AddWatch. Roll back our message — the
				// other watcher already posted theirs.
				_ = deps.Discord.DeleteMessage(ctx, ic.ChannelID, msgID)
				return reply(resp, ic, fmt.Sprintf(
					"**%s** is already being watched in this channel.", snap.Name))
			}
			// Genuine persistence error — also roll back the message
			// so we don't leak orphan posts.
			_ = deps.Discord.DeleteMessage(ctx, ic.ChannelID, msgID)
			deps.logf("[ERROR] watch: persist for %q in %s failed: %v", slug, ic.ChannelID, err)
			return reply(resp, ic, "I posted the message but failed to record the watch. Please /unwatch and try again.")
		}

		return reply(resp, ic, fmt.Sprintf(
			"Now watching **%s** — %s",
			snap.Name, messageLink(deps.GuildID, ic.ChannelID, msgID)))
	}
}

// Unwatch returns a slash.Handler that removes a watch. Idempotent on the
// Discord side — a 404 on DeleteMessage is treated as success (someone
// may have deleted the message manually, in which case we still want to
// drop the row).
func Unwatch(deps HandlerDeps) slash.Handler {
	deps.Cfg = deps.Cfg.Defaults()
	return func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		if err := deferEphemeral(resp, ic); err != nil {
			deps.logf("[ERROR] unwatch: defer ack failed: %v", err)
			return err
		}

		raw := strings.TrimSpace(systemOption(ic))
		if raw == "" {
			return reply(resp, ic, "Please provide a system name: `/unwatch <system>`.")
		}
		slug := galaxy.Slugify(raw)
		if slug == "" {
			return reply(resp, ic, "I couldn't form a valid system slug from that input.")
		}

		existing, err := deps.Store.GetWatch(ctx, ic.ChannelID, slug)
		if err != nil {
			deps.logf("[ERROR] unwatch: lookup for %q in %s failed: %v", slug, ic.ChannelID, err)
			return reply(resp, ic, "Something went wrong looking up that watch. Please try again in a moment.")
		}
		if existing == nil {
			return reply(resp, ic, fmt.Sprintf(
				"**%s** is not currently being watched in this channel.", raw))
		}

		// Discord delete first — if it fails for any non-404 reason
		// we keep the row (better stale-but-true than ghost-row).
		if err := deps.Discord.DeleteMessage(ctx, ic.ChannelID, existing.MessageID); err != nil {
			// 404 is fine — message was manually deleted; we still
			// want to clean up the row. Any other error: bail.
			if !isDiscordNotFound(err) {
				deps.logf("[ERROR] unwatch: discord delete for %q in %s failed: %v", slug, ic.ChannelID, err)
				return reply(resp, ic, "I couldn't delete the watch message. Try again or remove it manually.")
			}
		}

		if _, err := deps.Store.RemoveWatch(ctx, ic.ChannelID, slug); err != nil {
			deps.logf("[ERROR] unwatch: store remove for %q in %s failed: %v", slug, ic.ChannelID, err)
			return reply(resp, ic, "I deleted the message but failed to record the un-watch. Bot will retry on next poll.")
		}

		return reply(resp, ic, fmt.Sprintf("Stopped watching **%s**.", existing.SystemName))
	}
}

// isDiscordNotFound returns true when the error is a Discord 404 — typed
// errors are checked separately by callers when they need to distinguish
// channel-gone from message-not-found, but for /unwatch we treat both as
// "the message we wanted to delete is already gone" → success.
func isDiscordNotFound(err error) bool {
	if err == nil {
		return false
	}
	// Both ErrChannelGone and ErrMessageNotFound from the discordclient
	// package wrap the same 404-class outcome.
	for _, sentinel := range knownNotFoundErrors {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
