package discordclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/stretchr/testify/require"
)

func TestFakeDiscordClient_PostRecordsAndReturnsID(t *testing.T) {
	f := discordclient.NewFakeDiscordClient()
	embed := &discordgo.MessageEmbed{Title: "test"}

	msgID, err := f.PostMessage(context.Background(), "channel-1", embed)
	require.NoError(t, err)
	require.NotEmpty(t, msgID)

	calls := f.PostCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "channel-1", calls[0].ChannelID)
	require.Equal(t, "test", calls[0].Embed.Title)
}

func TestFakeDiscordClient_EditRecords(t *testing.T) {
	f := discordclient.NewFakeDiscordClient()
	require.NoError(t, f.EditMessage(context.Background(), "ch", "msg-1", &discordgo.MessageEmbed{Title: "edited"}))

	calls := f.EditCalls()
	require.Len(t, calls, 1)
	require.Equal(t, "msg-1", calls[0].MessageID)
}

func TestFakeDiscordClient_PostError_PropagatesAndDoesNotRecord(t *testing.T) {
	f := discordclient.NewFakeDiscordClient()
	f.PostErr = errors.New("simulated 500")

	_, err := f.PostMessage(context.Background(), "ch", &discordgo.MessageEmbed{})
	require.ErrorContains(t, err, "simulated 500")
	require.Empty(t, f.PostCalls(), "failed posts MUST NOT be recorded as successful")
}

func TestFakeDiscordClient_ChannelGoneErrorIsTyped(t *testing.T) {
	require.ErrorIs(t, discordclient.ErrChannelGone, discordclient.ErrChannelGone)
	wrapped := errors.Join(errors.New("ctx"), discordclient.ErrChannelGone)
	require.ErrorIs(t, wrapped, discordclient.ErrChannelGone)
}

func TestFakeDiscordClient_Reset_ClearsState(t *testing.T) {
	f := discordclient.NewFakeDiscordClient()
	_, _ = f.PostMessage(context.Background(), "ch", &discordgo.MessageEmbed{})
	require.Len(t, f.PostCalls(), 1)

	f.PostErr = errors.New("set")
	f.Reset()
	require.Empty(t, f.PostCalls())
	require.Nil(t, f.PostErr)
}
