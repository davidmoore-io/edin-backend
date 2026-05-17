package slash_test

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/require"

	"github.com/edin-space/edin-backend/internal/edinbot/slash"
)

// recordingResponder captures the calls slash.Router makes so tests can
// observe gate decisions without a live Discord session. Implements every
// method of slash.Responder; production wiring is *discordgo.Session.
type recordingResponder struct {
	mu      sync.Mutex
	replies []string
	follows []string
	edits   int
}

func (r *recordingResponder) InteractionRespond(ic *discordgo.Interaction, resp *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if resp.Data != nil {
		r.replies = append(r.replies, resp.Data.Content)
	}
	return nil
}

func (r *recordingResponder) InteractionResponseEdit(ic *discordgo.Interaction, edit *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edits++
	return &discordgo.Message{}, nil
}

func (r *recordingResponder) FollowupMessageCreate(ic *discordgo.Interaction, _ bool, params *discordgo.WebhookParams, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.follows = append(r.follows, params.Content)
	return &discordgo.Message{}, nil
}

func (r *recordingResponder) Replies() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.replies))
	copy(out, r.replies)
	return out
}

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func mkInteraction(channelID, command string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			ChannelID: channelID,
			Member:    &discordgo.Member{},
			Data: discordgo.ApplicationCommandInteractionData{
				Name: command,
			},
		},
	}
}

func TestRouter_DispatchesToHandlerOnHappyPath(t *testing.T) {
	var (
		mu     sync.Mutex
		called int
	)
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs: []string{"watch-channel"},
		Logger:            quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		mu.Lock()
		called++
		mu.Unlock()
		return nil
	})

	resp := &recordingResponder{}
	r.DispatchForTest(resp, mkInteraction("watch-channel", "watch"))

	require.Equal(t, 1, called, "handler must be invoked exactly once on happy path")
	require.Empty(t, resp.Replies(), "router must not reply on happy path — handler owns the reply")
}

func TestRouter_RejectsWrongChannel(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs: []string{"watch-channel"},
		Logger:            quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	resp := &recordingResponder{}
	r.DispatchForTest(resp, mkInteraction("some-other-channel", "watch"))

	require.Equal(t, 0, called, "handler must not be called when channel is wrong")
	require.Len(t, resp.Replies(), 1)
	require.Contains(t, resp.Replies()[0], "not enabled in this channel")
}

func TestRouter_RejectsDM(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs: []string{"watch-channel"},
		Logger:            quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	ic := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			ChannelID: "watch-channel",
			Member:    nil,
			Data:      discordgo.ApplicationCommandInteractionData{Name: "watch"},
		},
	}
	resp := &recordingResponder{}
	r.DispatchForTest(resp, ic)

	require.Equal(t, 0, called)
	require.Len(t, resp.Replies(), 1)
	require.Contains(t, resp.Replies()[0], "guild membership")
}

func TestRouter_RejectsUnauthorisedUser(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs: []string{"watch-channel"},
		AllowedUsersByGuild: map[string]map[string]bool{
			"guild-1": {"allowed-user": true},
		},
		Logger: quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	resp := &recordingResponder{}
	ic := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			GuildID:   "guild-1",
			ChannelID: "watch-channel",
			Member:    &discordgo.Member{User: &discordgo.User{ID: "unauthorised-user"}},
			Data:      discordgo.ApplicationCommandInteractionData{Name: "watch"},
		},
	}
	r.DispatchForTest(resp, ic)

	require.Equal(t, 0, called, "handler must not fire for unlisted user")
	require.Len(t, resp.Replies(), 1)
	require.Contains(t, resp.Replies()[0], "don't have permission")
}

func TestRouter_AllowsAuthorisedUser(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs: []string{"watch-channel"},
		AllowedUsersByGuild: map[string]map[string]bool{
			"guild-1": {"allowed-user": true},
		},
		Logger: quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	resp := &recordingResponder{}
	ic := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			GuildID:   "guild-1",
			ChannelID: "watch-channel",
			Member:    &discordgo.Member{User: &discordgo.User{ID: "allowed-user"}},
			Data:      discordgo.ApplicationCommandInteractionData{Name: "watch"},
		},
	}
	r.DispatchForTest(resp, ic)

	require.Equal(t, 1, called, "handler must fire for listed user")
	require.Empty(t, resp.Replies())
}

func TestRouter_NoUserGateForUnlistedGuild(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs: []string{"watch-channel"},
		AllowedUsersByGuild: map[string]map[string]bool{
			"guild-1": {"allowed-user": true},
		},
		Logger: quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	resp := &recordingResponder{}
	// guild-2 has no user allowlist — any user passes
	ic := &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type:      discordgo.InteractionApplicationCommand,
			GuildID:   "guild-2",
			ChannelID: "watch-channel",
			Member:    &discordgo.Member{User: &discordgo.User{ID: "random-user"}},
			Data:      discordgo.ApplicationCommandInteractionData{Name: "watch"},
		},
	}
	r.DispatchForTest(resp, ic)

	require.Equal(t, 1, called, "handler must fire when guild has no user allowlist")
	require.Empty(t, resp.Replies())
}

func TestRouter_DoubleRegisterPanics(t *testing.T) {
	r := slash.NewRouter(slash.Config{Logger: quietLogger()})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		return nil
	})

	defer func() {
		if recover() == nil {
			t.Fatal("re-registering a handler must panic")
		}
	}()
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		return errors.New("should never be called")
	})
}

func TestRouter_IgnoresNonApplicationCommandInteractions(t *testing.T) {
	called := 0
	r := slash.NewRouter(slash.Config{
		AllowedChannelIDs: []string{"watch-channel"},
		Logger:            quietLogger(),
	})
	r.Handle("watch", func(ctx context.Context, resp slash.Responder, ic *discordgo.InteractionCreate) error {
		called++
		return nil
	})

	resp := &recordingResponder{}
	r.DispatchForTest(resp, &discordgo.InteractionCreate{
		Interaction: &discordgo.Interaction{
			Type: discordgo.InteractionMessageComponent,
		},
	})

	require.Equal(t, 0, called, "non-slash interactions must be ignored")
	require.Empty(t, resp.Replies())
}
