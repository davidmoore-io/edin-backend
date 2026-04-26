package publisher

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// AnnotateTimestamps appends a "Posted <relative> · Updated <relative>" line
// to the embed description. Uses Discord's <t:UNIX:R> formatting so each
// viewer's client renders a live, locale-appropriate "5 minutes ago" — the
// timestamps stay accurate without ever editing the message. Returns a copy
// (does not mutate the input). If postedAt and updatedAt are equal (initial
// post), only the "Posted" line is rendered.
func AnnotateTimestamps(in *discordgo.MessageEmbed, postedAt, updatedAt time.Time) *discordgo.MessageEmbed {
	out := *in
	stamp := fmt.Sprintf("Posted <t:%d:R>", postedAt.Unix())
	if !updatedAt.Equal(postedAt) {
		stamp += fmt.Sprintf(" · Updated <t:%d:R>", updatedAt.Unix())
	}
	if out.Description == "" {
		out.Description = stamp
	} else {
		out.Description = out.Description + "\n" + stamp
	}
	return &out
}

// RenderStruckThrough returns a copy of the embed with all text wrapped in
// Discord strikethrough markdown (~~ ~~) and a footer noting when the item
// became absent. Uses the canonical Snapshot.GeneratedAt timestamp passed in
// as 'at' — never time.Now().
func RenderStruckThrough(in *discordgo.MessageEmbed, at time.Time) *discordgo.MessageEmbed {
	out := *in // shallow copy
	out.Title = wrapStrike(in.Title)
	out.Description = wrapStrike(in.Description)

	if len(in.Fields) > 0 {
		out.Fields = make([]*discordgo.MessageEmbedField, len(in.Fields))
		for i, f := range in.Fields {
			cp := *f
			cp.Name = wrapStrike(f.Name)
			cp.Value = wrapStrike(f.Value)
			out.Fields[i] = &cp
		}
	}

	out.Footer = &discordgo.MessageEmbedFooter{
		Text: "no longer present at " + at.UTC().Format("15:04 UTC"),
	}
	return &out
}

// RenderUnstruckThrough returns a copy of the embed with strikethrough markdown
// removed and a footer noting return time. Used when a previously-struck item
// reappears.
func RenderUnstruckThrough(in *discordgo.MessageEmbed, at time.Time) *discordgo.MessageEmbed {
	out := *in
	out.Title = unwrapStrike(in.Title)
	out.Description = unwrapStrike(in.Description)

	if len(in.Fields) > 0 {
		out.Fields = make([]*discordgo.MessageEmbedField, len(in.Fields))
		for i, f := range in.Fields {
			cp := *f
			cp.Name = unwrapStrike(f.Name)
			cp.Value = unwrapStrike(f.Value)
			out.Fields[i] = &cp
		}
	}

	out.Footer = &discordgo.MessageEmbedFooter{
		Text: "returned at " + at.UTC().Format("15:04 UTC"),
	}
	return &out
}

func wrapStrike(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "~~") && strings.HasSuffix(s, "~~") {
		return s
	}
	return "~~" + s + "~~"
}

func unwrapStrike(s string) string {
	for strings.HasPrefix(s, "~~") && strings.HasSuffix(s, "~~") {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "~~"), "~~")
	}
	return s
}
