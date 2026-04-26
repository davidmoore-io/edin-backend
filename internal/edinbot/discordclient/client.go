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

// Client is the surface area the publisher and scheduler depend on. Production
// implementation is *RealClient (uses discordgo); tests use FakeDiscordClient.
type Client interface {
	PostMessage(ctx context.Context, channelID string, embed *discordgo.MessageEmbed) (messageID string, err error)
	EditMessage(ctx context.Context, channelID, messageID string, embed *discordgo.MessageEmbed) error
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
	_, err := c.sess.ChannelMessageEditEmbed(channelID, messageID, embed, discordgo.WithContext(ctx))
	if err != nil && isChannelGone(err) {
		return ErrChannelGone
	}
	return err
}

// Close shuts down the discordgo session cleanly. Production main.go calls this
// during graceful shutdown.
func (c *RealClient) Close() error { return c.sess.Close() }

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
