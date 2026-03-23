package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/edin-space/edin-backend/internal/config"
	"github.com/edin-space/edin-backend/internal/observability"
)

// ErrUnknownService indicates the requested service is not registered.
var ErrUnknownService = errors.New("unknown service")

// Manager orchestrates Docker and Ansible operations.
type Manager struct {
	cfg    config.OperationsConfig
	logger *observability.Logger
}

// ServiceInfo describes a managed service for tooling discovery.
type ServiceInfo struct {
	Name      string `json:"name"`
	Container string `json:"container"`
	Label     string `json:"label,omitempty"`
}

// NewManager constructs a Manager instance from configuration.
func NewManager(cfg config.OperationsConfig, logger *observability.Logger) (*Manager, error) {
	if len(cfg.Services) == 0 {
		return nil, errors.New("no managed services configured")
	}
	if logger == nil {
		logger = observability.NewLogger("ops")
	}
	return &Manager{cfg: cfg, logger: logger}, nil
}

func (m *Manager) containerForService(service string) (config.DockerService, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return config.DockerService{}, fmt.Errorf("service name cannot be empty")
	}
	def, ok := m.cfg.Services[service]
	if !ok {
		return config.DockerService{}, fmt.Errorf("%w: %s", ErrUnknownService, service)
	}
	return def, nil
}

func (m *Manager) runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %w (output: %s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// LogTailDefault returns the configured default log tail size.
func (m *Manager) LogTailDefault() int {
	if m.cfg.LogTailDefault <= 0 {
		return 200
	}
	return m.cfg.LogTailDefault
}

// VirtualLogServices are log-only services that don't have a backing container.
// They provide filtered views of other services' logs.
var VirtualLogServices = map[string]string{
	"dayz-player": "DayZ (Players Only)",
}

// ServiceNames returns the allow-listed service identifiers.
func (m *Manager) ServiceNames() []string {
	names := make([]string, 0, len(m.cfg.Services)+len(VirtualLogServices))
	for name := range m.cfg.Services {
		names = append(names, name)
	}
	// Include virtual log services
	for name := range VirtualLogServices {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Services returns metadata for each managed service.
func (m *Manager) Services() []ServiceInfo {
	names := m.ServiceNames()
	defs := make([]ServiceInfo, 0, len(names))
	for _, name := range names {
		// Check if it's a virtual service first
		if label, ok := VirtualLogServices[name]; ok {
			defs = append(defs, ServiceInfo{
				Name:  name,
				Label: label,
			})
			continue
		}

		def := m.cfg.Services[name]
		info := ServiceInfo{
			Name:      name,
			Container: def.Container,
		}
		if label, ok := m.cfg.ServiceLabels[name]; ok && strings.TrimSpace(label) != "" {
			info.Label = label
		}
		defs = append(defs, info)
	}
	return defs
}

// PlaybookNames returns the curated playbook identifiers.
func (m *Manager) PlaybookNames() []string {
	names := make([]string, 0, len(m.cfg.Playbooks))
	for name := range m.cfg.Playbooks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parseAnsibleVars(vars map[string]string) (string, error) {
	if len(vars) == 0 {
		return "", nil
	}
	payload := make(map[string]string, len(vars))
	for k, v := range vars {
		if strings.TrimSpace(k) == "" {
			return "", fmt.Errorf("extra vars contain empty key")
		}
		payload[k] = v
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}
