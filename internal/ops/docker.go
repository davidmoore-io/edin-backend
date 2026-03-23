package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ServiceStatus represents a summarised view of a managed service.
type ServiceStatus struct {
	Service    string    `json:"service"`
	Container  string    `json:"container"`
	State      string    `json:"state"`
	Health     string    `json:"health,omitempty"`
	Running    bool      `json:"running"`
	Restarting bool      `json:"restarting"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	ExitCode   int       `json:"exit_code,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// RestartResult captures the outcome of a restart action.
type RestartResult struct {
	Service     string    `json:"service"`
	Container   string    `json:"container"`
	RestartedAt time.Time `json:"restarted_at"`
	Output      string    `json:"output"`
}

// ServiceStatus retrieves status information for an allow-listed container.
func (m *Manager) ServiceStatus(ctx context.Context, service string) (*ServiceStatus, error) {
	def, err := m.containerForService(service)
	if err != nil {
		return nil, err
	}

	out, err := m.runCommand(ctx, m.cfg.DockerBinary, "inspect", "--format", "{{json .State}}", def.Container)
	if err != nil {
		return nil, err
	}

	var state dockerState
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &state); err != nil {
		return nil, fmt.Errorf("inspect parse: %w", err)
	}

	status := &ServiceStatus{
		Service:    service,
		Container:  def.Container,
		State:      state.Status,
		Health:     state.HealthStatus(),
		Running:    state.Running,
		Restarting: state.Restarting,
		ExitCode:   state.ExitCode,
		Detail:     strings.TrimSpace(state.Error),
	}

	if t, err := state.ParseStartedAt(); err == nil {
		status.StartedAt = t
	}
	if t, err := state.ParseFinishedAt(); err == nil {
		status.FinishedAt = t
	}

	return status, nil
}

// RestartService restarts the underlying Docker container for the specified service.
func (m *Manager) RestartService(ctx context.Context, service string) (*RestartResult, error) {
	def, err := m.containerForService(service)
	if err != nil {
		return nil, err
	}
	output, err := m.runCommand(ctx, m.cfg.DockerBinary, "restart", def.Container)
	if err != nil {
		return nil, err
	}
	return &RestartResult{
		Service:     service,
		Container:   def.Container,
		RestartedAt: time.Now().UTC(),
		Output:      strings.TrimSpace(output),
	}, nil
}

type dockerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Restarting bool   `json:"Restarting"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
	ExitCode   int    `json:"ExitCode"`
	Error      string `json:"Error"`
	Health     *struct {
		Status string `json:"Status"`
	} `json:"Health"`
}

func (s dockerState) HealthStatus() string {
	if s.Health == nil {
		return ""
	}
	return s.Health.Status
}

func (s dockerState) ParseStartedAt() (time.Time, error) {
	return parseDockerTime(s.StartedAt)
}

func (s dockerState) ParseFinishedAt() (time.Time, error) {
	return parseDockerTime(s.FinishedAt)
}

func parseDockerTime(val string) (time.Time, error) {
	val = strings.TrimSpace(val)
	if val == "" || val == "0001-01-01T00:00:00Z" {
		return time.Time{}, fmt.Errorf("zero time")
	}
	if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, val)
}
