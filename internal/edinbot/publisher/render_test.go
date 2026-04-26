package publisher_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/edinbot/publisher"
	"github.com/stretchr/testify/require"
)

func TestAnnotateTimestamps_OnlyPostedWhenEqual(t *testing.T) {
	at := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	in := &discordgo.MessageEmbed{Description: "**Platinum buyers** — 3 stations"}

	out := publisher.AnnotateTimestamps(in, at, at)
	require.Contains(t, out.Description, "**Platinum buyers**")
	require.Contains(t, out.Description, fmt.Sprintf("Posted <t:%d:R>", at.Unix()))
	require.NotContains(t, out.Description, "Updated", "no Updated line on initial post")
	require.Equal(t, "**Platinum buyers** — 3 stations", in.Description, "input not mutated")
}

func TestAnnotateTimestamps_BothWhenEdited(t *testing.T) {
	posted := time.Date(2026, 4, 26, 14, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 4, 26, 15, 0, 0, 0, time.UTC)
	in := &discordgo.MessageEmbed{Description: "Body"}

	out := publisher.AnnotateTimestamps(in, posted, updated)
	require.Contains(t, out.Description, fmt.Sprintf("Posted <t:%d:R>", posted.Unix()))
	require.Contains(t, out.Description, fmt.Sprintf("Updated <t:%d:R>", updated.Unix()))
}

func TestAnnotateTimestamps_EmptyDescription(t *testing.T) {
	at := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	in := &discordgo.MessageEmbed{}

	out := publisher.AnnotateTimestamps(in, at, at)
	require.Equal(t, fmt.Sprintf("Posted <t:%d:R>", at.Unix()), out.Description)
}

func TestStrikeThrough_WrapsAllTextFields(t *testing.T) {
	at := time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	in := &discordgo.MessageEmbed{
		Title:       "Sol",
		Description: "Boom faction here",
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Demand", Value: "1500t"},
		},
	}

	out := publisher.RenderStruckThrough(in, at)
	require.True(t, strings.HasPrefix(out.Title, "~~"))
	require.True(t, strings.HasSuffix(out.Title, "~~"))
	require.Contains(t, out.Description, "~~")
	require.NotNil(t, out.Footer)
	require.Contains(t, out.Footer.Text, "no longer present at 14:23 UTC")
	require.True(t, strings.HasPrefix(out.Fields[0].Name, "~~"))
	require.True(t, strings.HasPrefix(out.Fields[0].Value, "~~"))
}

func TestUnstrikeThrough_RemovesStrikesAddsReturnedFooter(t *testing.T) {
	at := time.Date(2026, 4, 26, 16, 0, 0, 0, time.UTC)
	last := &discordgo.MessageEmbed{
		Title:       "~~Sol~~",
		Description: "~~Boom~~",
		Footer:      &discordgo.MessageEmbedFooter{Text: "no longer present at 14:23 UTC"},
	}

	out := publisher.RenderUnstruckThrough(last, at)
	require.Equal(t, "Sol", out.Title)
	require.Equal(t, "Boom", out.Description)
	require.NotNil(t, out.Footer)
	require.Contains(t, out.Footer.Text, "returned at 16:00 UTC")
}

func TestUnstrikeThrough_PreservesNonStruckText(t *testing.T) {
	at := time.Date(2026, 4, 26, 16, 0, 0, 0, time.UTC)
	last := &discordgo.MessageEmbed{
		Title: "Sol",
	}
	out := publisher.RenderUnstruckThrough(last, at)
	require.Equal(t, "Sol", out.Title, "non-struck text passes through unchanged")
}

func TestItemAction_StringIsStable(t *testing.T) {
	require.Equal(t, "post", string(publisher.ActionPost))
	require.Equal(t, "edit", string(publisher.ActionEdit))
	require.Equal(t, "strike", string(publisher.ActionStrike))
	require.Equal(t, "unstrike", string(publisher.ActionUnstrike))
	require.Equal(t, "noop", string(publisher.ActionNoop))
}

func TestResult_Tally_CountsByAction(t *testing.T) {
	r := publisher.Result{
		Items: []publisher.ItemResult{
			{Action: publisher.ActionPost},
			{Action: publisher.ActionPost, Err: errors.New("network")},
			{Action: publisher.ActionEdit},
			{Action: publisher.ActionNoop},
		},
	}
	r.Tally()
	require.Equal(t, 1, r.Posted, "successful posts only")
	require.Equal(t, 1, r.Edited)
	require.Equal(t, 1, r.Noop)
	require.Equal(t, 1, r.Failed)
}
