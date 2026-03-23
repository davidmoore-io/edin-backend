package discord

import (
	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/config"
)

const (
	commandStatus      = "status"
	commandRestart     = "restart"
	commandLogs        = "logs"
	commandOpsAssist   = "ops-assist"
	commandCG          = "cg"
	commandDayzEconomy = "dayz-ssg-economy"
)

func buildCommandDefinitions(cfg *config.Config) []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{
		{
			Name:        commandStatus,
			Description: "Fetch the current state of a managed service",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "service",
					Description:  "Service to query",
					Required:     true,
					Autocomplete: true,
				},
			},
		},
		{
			Name:        commandRestart,
			Description: "Restart a managed service",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "service",
					Description:  "Service to restart",
					Required:     true,
					Autocomplete: true,
				},
			},
		},
		{
			Name:        commandLogs,
			Description: "Retrieve recent logs for a service",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "service",
					Description:  "Service to inspect",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "tail",
					Description: "Number of log lines to return (default 200)",
					Required:    false,
					MinValue:    float64Ptr(1),
				},
			},
		},
		{
			Name:        commandOpsAssist,
			Description: "Private LLM assistant for operations",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "prompt",
					Description: "Question or instruction",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "session_id",
					Description: "Existing session ID to continue a conversation",
					Required:    false,
				},
			},
		},
		{
			Name:        commandCG,
			Description: "Query powerplay status for Col 359 sector systems",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "prompt",
					Description: "Optional: ask a question about the CG data (e.g., 'who is winning?')",
					Required:    false,
				},
			},
		},
		{
			Name:        commandDayzEconomy,
			Description: "Show DayZ server loot economy statistics or search for specific items",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "item",
					Description: "Search for a specific item to see its spawn config (e.g., 'key', 'truck', 'ak')",
					Required:    false,
				},
			},
		},
	}
}

func float64Ptr(v float64) *float64 {
	return &v
}
