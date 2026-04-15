package httpapi

import (
	"strings"
	"testing"
)

func TestCopilotPrompt_ContainsCommanderName(t *testing.T) {
	prompt := CopilotSystemPrompt("Pattern State")
	if !strings.Contains(prompt, "Pattern State") {
		t.Errorf("expected prompt to contain commander name %q, but it did not; prompt=%s", "Pattern State", prompt)
	}
}

func TestCopilotPrompt_MentionsGalaxyAndGameState(t *testing.T) {
	prompt := CopilotSystemPrompt("Pattern State")
	keywords := []string{"galaxy", "Galaxy", "Elite Dangerous", "game state"}
	for _, kw := range keywords {
		if strings.Contains(prompt, kw) {
			return
		}
	}
	t.Errorf("expected prompt to contain at least one of %v, but none were found; prompt=%s", keywords, prompt)
}

func TestCopilotPrompt_DoesNotRevealToolNamesInSystemPromptText(t *testing.T) {
	prompt := CopilotSystemPrompt("Pattern State")
	forbiddenTools := []string{
		"commander_events",
		"commander_location",
		"galaxy_system",
		"galaxy_station",
		"galaxy_power",
		"galaxy_market",
		"galaxy_query",
		"galaxy_stats",
		"spansh_query",
	}
	for _, tool := range forbiddenTools {
		if strings.Contains(prompt, tool) {
			t.Errorf("expected prompt NOT to contain tool name %q, but it did; prompt=%s", tool, prompt)
		}
	}
}

func TestCopilotPrompt_ContainsUntrustedDataWarning(t *testing.T) {
	prompt := CopilotSystemPrompt("Pattern State")
	keywords := []string{"untrusted", "user-provided", "unverified", "may contain", "injections", "UNTRUSTED"}
	for _, kw := range keywords {
		if strings.Contains(prompt, kw) {
			return
		}
	}
	t.Errorf("expected prompt to contain an untrusted data warning (one of %v), but none were found; prompt=%s", keywords, prompt)
}

func TestCopilotPrompt_DifferentCommanderNames_AreIncluded(t *testing.T) {
	names := []string{"Jameson", "Sidewinder McDeath"}
	for _, name := range names {
		prompt := CopilotSystemPrompt(name)
		if !strings.Contains(prompt, name) {
			t.Errorf("expected prompt to contain commander name %q, but it did not; prompt=%s", name, prompt)
		}
	}
}

func TestCopilotPrompt_EmptyCommanderName_DoesNotPanic(t *testing.T) {
	var prompt string
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CopilotSystemPrompt(\"\") panicked: %v", r)
		}
	}()
	prompt = CopilotSystemPrompt("")
	if prompt == "" {
		t.Error("expected CopilotSystemPrompt(\"\") to return a non-empty string")
	}
}
