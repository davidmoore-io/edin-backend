//go:build e2e

// Phase 15.2 of edin-bot plan. Posts → edits → strikes → unstrikes a real
// Discord message in a designated test channel via discordclient.RealClient.
//
// Gated by both:
//   - the //go:build e2e build tag (so it never compiles under default tests)
//   - EDIN_E2E=1 env var
//   - EDIN_BOT_TOKEN + EDIN_E2E_TEST_CHANNEL_ID env vars (else SKIP)
//
// Run: make test-edin-bot-e2e
//
// On failure, the test message stays in Discord — manual cleanup needed.
package edinbot_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/edinbot/discordclient"
	"github.com/stretchr/testify/require"
)

func TestE2E_PostEditStrikeUnstrikeAgainstRealDiscord(t *testing.T) {
	if os.Getenv("EDIN_E2E") != "1" {
		t.Skip("set EDIN_E2E=1 to run end-to-end tests against real Discord")
	}
	token := os.Getenv("EDIN_BOT_TOKEN")
	channelID := os.Getenv("EDIN_E2E_TEST_CHANNEL_ID")
	require.NotEmpty(t, token, "EDIN_BOT_TOKEN required")
	require.NotEmpty(t, channelID, "EDIN_E2E_TEST_CHANNEL_ID required")

	dc, err := discordclient.NewRealClient(token)
	require.NoError(t, err)
	defer dc.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	embed := &discordgo.MessageEmbed{
		Title:       "EDIN E2E test — POST",
		Description: "This message will be edited several times by an automated test.",
		Color:       0x9CA3AF,
	}
	msgID, err := dc.PostMessage(ctx, channelID, embed)
	require.NoError(t, err)
	require.NotEmpty(t, msgID)

	t.Cleanup(func() {
		// best-effort cleanup; do not fail the test on cleanup error.
		sess, err := discordgo.New("Bot " + token)
		if err == nil {
			_ = sess.ChannelMessageDelete(channelID, msgID)
			_ = sess.Close()
		}
	})

	embed.Title = "EDIN E2E test — EDIT"
	require.NoError(t, dc.EditMessage(ctx, channelID, msgID, embed))

	embed.Title = "~~EDIN E2E test — STRIKE~~"
	embed.Footer = &discordgo.MessageEmbedFooter{Text: "no longer present at " + time.Now().UTC().Format("15:04 UTC")}
	require.NoError(t, dc.EditMessage(ctx, channelID, msgID, embed))

	embed.Title = "EDIN E2E test — UNSTRIKE"
	embed.Footer = &discordgo.MessageEmbedFooter{Text: "returned at " + time.Now().UTC().Format("15:04 UTC")}
	require.NoError(t, dc.EditMessage(ctx, channelID, msgID, embed))
}
