package copilot_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/edin-space/edin-backend/internal/copilot"
)

const testTemplateDir = "../../../edin-personality/system-prompts"

func TestPromptAssembler_LoadsTemplates(t *testing.T) {
	a, err := copilot.NewPromptAssembler(testTemplateDir)
	require.NoError(t, err)
	assert.NotNil(t, a)
}

func TestPromptAssembler_AssemblesTheMindCompanion(t *testing.T) {
	a, err := copilot.NewPromptAssembler(testTemplateDir)
	require.NoError(t, err)
	prompt := a.Assemble("the_mind", "companion", "Pattern State")
	assert.Contains(t, prompt, "Pattern State")
	assert.Contains(t, prompt, "<speak>")
	assert.Contains(t, prompt, "<data>")
}

func TestPromptAssembler_DualChannelProtocolInAllPrompts(t *testing.T) {
	a, err := copilot.NewPromptAssembler(testTemplateDir)
	require.NoError(t, err)
	for _, persona := range []string{"the_mind", "the_analyst", "bob_uk", "the_veteran"} {
		for _, mode := range []string{"tactical", "standard", "companion"} {
			prompt := a.Assemble(persona, mode, "CMDR")
			assert.Contains(t, prompt, "<speak>", "persona=%s mode=%s missing speak tag", persona, mode)
		}
	}
}

func TestPromptAssembler_FallsBackToDefault(t *testing.T) {
	a, err := copilot.NewPromptAssembler(testTemplateDir)
	require.NoError(t, err)
	prompt := a.Assemble("unknown_persona", "unknown_mode", "CMDR")
	assert.NotEmpty(t, prompt)
	assert.True(t, strings.Contains(prompt, "<speak>"))
}

func TestPromptAssembler_InjectsCommanderName(t *testing.T) {
	a, err := copilot.NewPromptAssembler(testTemplateDir)
	require.NoError(t, err)
	prompt := a.Assemble("the_mind", "standard", "Nakato Kaine")
	assert.Contains(t, prompt, "Nakato Kaine")
}

func TestNewDefaultAssembler_ReturnsNonEmpty(t *testing.T) {
	a := copilot.NewDefaultAssembler()
	prompt := a.Assemble("the_mind", "standard", "Commander")
	assert.NotEmpty(t, prompt)
	assert.Contains(t, prompt, "<speak>")
}
