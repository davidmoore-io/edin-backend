package ops

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AnsibleJob encapsulates details about a launched playbook run.
type AnsibleJob struct {
	Playbook    string            `json:"playbook"`
	Path        string            `json:"path"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt time.Time         `json:"completed_at"`
	Success     bool              `json:"success"`
	Output      string            `json:"output"`
	ExtraVars   map[string]string `json:"extra_vars,omitempty"`
}

// RunPlaybook executes a curated playbook with validated parameters.
func (m *Manager) RunPlaybook(ctx context.Context, playbook string, extraVars map[string]string) (*AnsibleJob, error) {
	playbook = strings.TrimSpace(playbook)
	if playbook == "" {
		return nil, fmt.Errorf("playbook name cannot be empty")
	}

	path, ok := m.cfg.Playbooks[playbook]
	if !ok {
		return nil, fmt.Errorf("playbook %s is not allowed", playbook)
	}

	var args []string
	if inv := strings.TrimSpace(m.cfg.AnsibleInventory); inv != "" {
		args = append(args, "-i", inv)
	}
	args = append(args, path)

	varArg, err := parseAnsibleVars(extraVars)
	if err != nil {
		return nil, err
	}
	if varArg != "" {
		args = append(args, "--extra-vars", varArg)
	}

	start := time.Now().UTC()
	output, err := m.runCommand(ctx, m.cfg.AnsibleBinary, args...)
	success := err == nil
	if err != nil {
		// include partial output in error for context
		output = fmt.Sprintf("%s\n%s", output, err.Error())
	}

	job := &AnsibleJob{
		Playbook:    playbook,
		Path:        path,
		StartedAt:   start,
		CompletedAt: time.Now().UTC(),
		Success:     success,
		Output:      strings.TrimSpace(output),
		ExtraVars:   extraVars,
	}

	if !success {
		return job, fmt.Errorf("ansible playbook %s failed", playbook)
	}
	return job, nil
}
