package copilot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PromptAssembler loads persona×mode templates and assembles system prompts.
type PromptAssembler struct {
	templates map[string]string
	protocol  string
	mu        sync.RWMutex
}

func NewPromptAssembler(templateDir string) (*PromptAssembler, error) {
	protocol, err := os.ReadFile(filepath.Join(templateDir, "_dual-channel-protocol.md"))
	if err != nil {
		return nil, fmt.Errorf("load dual-channel protocol: %w", err)
	}

	a := &PromptAssembler{
		templates: make(map[string]string),
		protocol:  string(protocol),
	}

	personas := []string{"the_mind", "the_analyst", "bob_uk", "the_veteran"}
	modes := []string{"tactical", "standard", "companion"}

	for _, persona := range personas {
		for _, mode := range modes {
			filename := fmt.Sprintf("%s-%s.md", strings.ReplaceAll(persona, "_", "-"), mode)
			content, err := os.ReadFile(filepath.Join(templateDir, filename))
			if err != nil {
				continue
			}
			a.templates[persona+"_"+mode] = string(content)
		}
	}

	return a, nil
}

// NewDefaultAssembler returns an assembler with minimal hardcoded templates.
func NewDefaultAssembler() *PromptAssembler {
	protocol := "All output must use <speak> and <data> tags. Example: <speak>Found it.</speak><data>results here</data>"
	return &PromptAssembler{
		templates: map[string]string{
			"the_mind_standard": "You are EDIN, an intelligence network helping Commander {{COMMANDER_NAME}} in Elite Dangerous. Be helpful, precise, and use the dual-channel output protocol. {{DUAL_CHANNEL_PROTOCOL}}",
		},
		protocol: protocol,
	}
}

func (a *PromptAssembler) Assemble(persona, mode, commanderName string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	key := persona + "_" + mode
	template, ok := a.templates[key]
	if !ok {
		template = a.templates["the_mind_standard"]
	}
	if template == "" {
		template = "You are EDIN, an intelligence network. {{DUAL_CHANNEL_PROTOCOL}}"
	}

	result := strings.ReplaceAll(template, "{{COMMANDER_NAME}}", commanderName)
	result = strings.ReplaceAll(result, "{{DUAL_CHANNEL_PROTOCOL}}", a.protocol)
	return strings.TrimSpace(result)
}

func (a *PromptAssembler) Default() string {
	return a.Assemble("the_mind", "standard", "Commander")
}
