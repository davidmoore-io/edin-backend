package discord

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

const discordMaxMessageLength = 2000
const discordChunkSize = 1800

func splitForDiscord(content string) []string {
	runes := []rune(content)
	if len(runes) <= discordChunkSize {
		return []string{content}
	}
	var parts []string
	for len(runes) > 0 {
		end := discordChunkSize
		if len(runes) < discordChunkSize {
			end = len(runes)
		}
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
	return parts
}

func trimMessage(content string) string {
	parts := splitForDiscord(content)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func deferResponse(session *discordgo.Session, interaction *discordgo.InteractionCreate, ephemeral bool) error {
	flags := discordgo.MessageFlags(0)
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}
	return session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: flags},
	})
}

func editResponse(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string) error {
	parts := splitForDiscord(content)
	if len(parts) == 0 {
		empty := ""
		_, err := session.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{Content: &empty})
		return err
	}

	first := parts[0]
	_, err := session.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{Content: &first})
	if err != nil {
		return err
	}

	for _, extra := range parts[1:] {
		if err := followup(session, interaction, extra, false); err != nil {
			return err
		}
	}
	return nil
}

func respondEphemeral(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string) error {
	parts := splitForDiscord(content)
	if len(parts) == 0 {
		return session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
		})
	}

	first := parts[0]
	if err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: first,
		},
	}); err != nil {
		return err
	}

	for _, extra := range parts[1:] {
		if err := followup(session, interaction, extra, true); err != nil {
			return err
		}
	}
	return nil
}

func respond(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string) error {
	parts := splitForDiscord(content)
	if len(parts) == 0 {
		return session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{},
		})
	}

	first := parts[0]
	if err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: first,
		},
	}); err != nil {
		return err
	}

	for _, extra := range parts[1:] {
		if err := followup(session, interaction, extra, false); err != nil {
			return err
		}
	}
	return nil
}

func respondError(session *discordgo.Session, interaction *discordgo.InteractionCreate, err error) error {
	return respondEphemeral(session, interaction, fmt.Sprintf("⚠️ %v", err))
}

func followup(session *discordgo.Session, interaction *discordgo.InteractionCreate, content string, ephemeral bool) error {
	flags := discordgo.MessageFlags(0)
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}
	parts := splitForDiscord(content)
	if len(parts) == 0 {
		return nil
	}
	for _, part := range parts {
		if _, err := session.FollowupMessageCreate(interaction.Interaction, true, &discordgo.WebhookParams{
			Content: part,
			Flags:   flags,
		}); err != nil {
			return err
		}
	}
	return nil
}
