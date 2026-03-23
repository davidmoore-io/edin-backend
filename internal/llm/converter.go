package llm

import "github.com/edin-space/edin-backend/internal/anthropic"

// ToAnthropicMessages transforms internal messages into Anthropic payloads.
func ToAnthropicMessages(msgs []Message) []anthropic.Message {
	out := make([]anthropic.Message, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, anthropic.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return out
}
