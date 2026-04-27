// Package discordclient is the bot's Discord I/O layer. It wraps discordgo
// with a per-channel token bucket (Discord's per-channel write limit is 5 in
// 5s for most channels) and exposes a small Client interface so tests can
// substitute FakeDiscordClient.
package discordclient

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// ErrChannelGone is returned when Discord reports the channel does not exist
// or the bot has been kicked from the guild. The publisher uses this signal to
// disable the binding (per spec §3 "Channel deleted / bot kicked from guild").
var ErrChannelGone = errors.New("discord: channel unreachable (403/404)")

// ErrMessageNotFound is returned when Discord reports the specific message ID
// no longer exists (404 Unknown Message). Distinct from ErrChannelGone because
// the channel itself is still reachable — typically the message was deleted
// by hand. The /admin/clear handler treats this as success and proceeds to
// purge the corresponding posted_messages row.
var ErrMessageNotFound = errors.New("discord: message not found (404)")

// Client is the surface area the publisher and scheduler depend on. Production
// implementation is *RealClient (uses discordgo); tests use FakeDiscordClient.
type Client interface {
	PostMessage(ctx context.Context, channelID string, embed *discordgo.MessageEmbed) (messageID string, err error)
	EditMessage(ctx context.Context, channelID, messageID string, embed *discordgo.MessageEmbed) error
	// ReplaceWithText edits an existing bot message to drop its embed entirely
	// and replace it with plain text content. Used by the strike/completion
	// path: instead of leaving a greyed-out embed in the channel, the bot
	// collapses the message to a one-line spoiler ("||🏁 COMPLETED · …||").
	// Reverse path (un-strike) goes through PostMessage / EditMessage with a
	// fresh embed; the publisher must clear content explicitly when it does.
	ReplaceWithText(ctx context.Context, channelID, messageID, content string) error
	DeleteMessage(ctx context.Context, channelID, messageID string) error
}

type RealClient struct {
	sess    *discordgo.Session
	limiter *PerChannelLimiter
}

func NewRealClient(token string) (*RealClient, error) {
	sess, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	if err := sess.Open(); err != nil {
		return nil, err
	}
	return &RealClient{
		sess:    sess,
		limiter: NewPerChannelLimiter(5, 5*time.Second),
	}, nil
}

func (c *RealClient) PostMessage(ctx context.Context, channelID string, embed *discordgo.MessageEmbed) (string, error) {
	if err := c.limiter.Wait(ctx, channelID); err != nil {
		return "", err
	}
	msg, err := c.sess.ChannelMessageSendEmbed(channelID, embed, discordgo.WithContext(ctx))
	if err != nil {
		if isChannelGone(err) {
			return "", ErrChannelGone
		}
		return "", err
	}
	return msg.ID, nil
}

func (c *RealClient) EditMessage(ctx context.Context, channelID, messageID string, embed *discordgo.MessageEmbed) error {
	if err := c.limiter.Wait(ctx, channelID); err != nil {
		return err
	}
	// Use EditComplex with an explicit empty Content so any prior text content
	// (e.g. a strike-spoiler the message previously held) is cleared when we
	// swap a fresh embed back in. The simpler ChannelMessageEditEmbed would
	// leave leftover content stacked above the new embed.
	empty := ""
	embeds := []*discordgo.MessageEmbed{embed}
	_, err := c.sess.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:      messageID,
		Channel: channelID,
		Content: &empty,
		Embeds:  &embeds,
	}, discordgo.WithContext(ctx))
	if err != nil && isChannelGone(err) {
		return ErrChannelGone
	}
	return err
}

// ReplaceWithText edits a message to drop the embed and set plain content.
// Implemented via ChannelMessageEditComplex so we can explicitly null the
// embeds field — the simpler ChannelMessageEdit would leave the existing
// embed alongside the new content.
func (c *RealClient) ReplaceWithText(ctx context.Context, channelID, messageID, content string) error {
	if err := c.limiter.Wait(ctx, channelID); err != nil {
		return err
	}
	noEmbeds := []*discordgo.MessageEmbed{}
	_, err := c.sess.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID:      messageID,
		Channel: channelID,
		Content: &content,
		Embeds:  &noEmbeds,
	}, discordgo.WithContext(ctx))
	if err != nil && isChannelGone(err) {
		return ErrChannelGone
	}
	return err
}

// DeleteMessage deletes a single message the bot posted earlier. Used by the
// /admin/clear endpoint to wipe Discord state for a binding.
//   - 404 (Unknown Message) → ErrMessageNotFound (caller treats as success)
//   - 403 (Missing Access / kicked) → ErrChannelGone
//   - other → raw error
//
// A bot can always delete its own messages without Manage Messages permission,
// so 403 here genuinely means the bot has lost access to the channel.
func (c *RealClient) DeleteMessage(ctx context.Context, channelID, messageID string) error {
	if err := c.limiter.Wait(ctx, channelID); err != nil {
		return err
	}
	err := c.sess.ChannelMessageDelete(channelID, messageID, discordgo.WithContext(ctx))
	if err == nil {
		return nil
	}
	if isMessageNotFound(err) {
		return ErrMessageNotFound
	}
	if isChannelGone(err) {
		return ErrChannelGone
	}
	return err
}

// Close shuts down the discordgo session cleanly. Production main.go calls this
// during graceful shutdown.
func (c *RealClient) Close() error { return c.sess.Close() }

// isMessageNotFound matches "404 Unknown Message" specifically. We need this
// distinct from isChannelGone because the channel itself is still reachable;
// only the targeted message is gone.
func isMessageNotFound(err error) bool {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == 404 {
		// Discord error code 10008 = Unknown Message. 10003 = Unknown Channel.
		if rest.Message != nil && rest.Message.Code == 10008 {
			return true
		}
	}
	return strings.Contains(err.Error(), "Unknown Message")
}

// isChannelGone identifies the Discord HTTP error codes that mean "the bot
// can no longer reach this channel": 403 (Missing Access / Missing Permissions)
// and 404 (Unknown Channel / Unknown Message).
func isChannelGone(err error) bool {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Response != nil {
		switch rest.Response.StatusCode {
		case 403, 404:
			return true
		}
	}
	// Fallback string match for cases where the typed error is not exposed.
	msg := err.Error()
	return strings.Contains(msg, "Unknown Channel") ||
		strings.Contains(msg, "Unknown Message") ||
		strings.Contains(msg, "Missing Access")
}
