package ops

import (
	"context"
	"fmt"
	"strings"
)

const satisfactoryVersionScript = `
log_dir=/config/gamefiles/FactoryGame/Saved/Logs
if [ ! -d "$log_dir" ]; then exit 0; fi
file=$(ls -1t "$log_dir"/FactoryGame*.log 2>/dev/null | head -n 1)
if [ -z "$file" ]; then exit 0; fi
line=$(grep 'LogInit: Build:' "$file" | tail -n 1)
printf "%s" "$line"
`

func (m *Manager) satisfactoryVersion(ctx context.Context, container string, running bool) (string, error) {
	if !running {
		return "", nil
	}
	out, err := m.runCommand(ctx, m.cfg.DockerBinary, "exec", container, "sh", "-c", satisfactoryVersionScript)
	if err != nil {
		return "", err
	}
	return parseSatisfactoryVersion(out), nil
}

func parseSatisfactoryVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "LogInit: ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "LogInit: "))
		}
		if strings.HasPrefix(line, "Build:") {
			build := strings.TrimSpace(strings.TrimPrefix(line, "Build:"))
			return normaliseSatisfactoryBuild(build)
		}
		return line
	}
	return ""
}

func normaliseSatisfactoryBuild(build string) string {
	build = strings.TrimSpace(build)
	if build == "" {
		return ""
	}
	const prefix = "++FactoryGame+"
	build = strings.TrimPrefix(build, prefix)
	if idx := strings.Index(build, "-CL-"); idx >= 0 {
		clIdx := idx + len("-CL-")
		if clIdx < len(build) {
			release := strings.TrimSuffix(build[:idx], "-")
			if release == "" {
				release = build[:idx]
			}
			cl := build[clIdx:]
			return fmt.Sprintf("%s (CL-%s)", release, cl)
		}
	}
	return build
}
