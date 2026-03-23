package config

import (
	_ "embed"
	"strings"
)

//go:embed prompts/system_prompt.md
var embeddedSystemPrompt string

//go:embed prompts/kaine_system_prompt.md
var embeddedKaineSystemPrompt string

func defaultSystemPrompt() string {
	return strings.TrimSpace(embeddedSystemPrompt)
}

func defaultKaineSystemPrompt() string {
	return strings.TrimSpace(embeddedKaineSystemPrompt)
}
