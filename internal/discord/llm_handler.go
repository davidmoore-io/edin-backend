package discord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/edin-space/edin-backend/internal/llm"
)

// handleOpsAssist routes privileged LLM interactions for Discord administrators.
func (b *Bot) handleOpsAssist(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate) {
	if !b.userHasRoles(i.Member, b.cfg.Discord.LLMOperatorRoleIDs) {
		_ = respondEphemeral(s, i, "LLM access is restricted to designated operators.")
		return
	}

	prompt := optionValue(i, "prompt")
	sessionID := optionValue(i, "session_id")
	if strings.TrimSpace(prompt) == "" {
		_ = respondEphemeral(s, i, "Prompt cannot be empty.")
		return
	}

	if err := deferResponse(s, i, false); err != nil {
		b.logger.Error("failed to defer ops-assist response", err)
		return
	}

	// Show thinking indicator
	_ = editResponse(s, i, "🧠 Thinking...")

	// Run LLM call with progress indicator
	userID := "discord:" + i.Member.User.ID

	// Channel to receive result
	type llmResult struct {
		session *llm.Session
		reply   string
		err     error
	}
	resultCh := make(chan llmResult, 1)

	// Start LLM call in background
	start := time.Now()
	go func() {
		session, reply, err := b.control.CreateLLMSession(ctx, sessionID, userID, prompt)
		resultCh <- llmResult{session, reply, err}
	}()

	// Update message periodically while waiting
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	updateCount := 0
	for {
		select {
		case result := <-resultCh:
			// Got result, send final message
			if result.err != nil {
				b.logger.Error("ops-assist command failed", result.err)
				_ = editResponse(s, i, fmt.Sprintf("⚠️ %v", result.err))
				return
			}
			message := fmt.Sprintf("🤖 %s\n\n_Session:_ `%s`", result.reply, result.session.ID)
			if err := editResponse(s, i, message); err != nil {
				b.logger.Error("failed to send ops-assist reply", err)
			}
			return

		case <-ticker.C:
			// Update progress indicator
			elapsed := time.Since(start).Round(time.Second)
			updateCount++
			var statusMsg string
			if elapsed < 30*time.Second {
				statusMsg = fmt.Sprintf("🧠 Thinking... (%s)", elapsed)
			} else {
				// Likely running a long operation like carrier route
				statusMsg = fmt.Sprintf("⏳ Still working... (%s)\n_Long operations like carrier routes can take up to 2 minutes_", elapsed)
			}
			_ = editResponse(s, i, statusMsg)

		case <-ctx.Done():
			_ = editResponse(s, i, "⚠️ Request cancelled")
			return
		}
	}
}
