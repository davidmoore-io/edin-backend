package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// status retrieves the status of a managed service.
func (e *Executor) status(ctx context.Context, args map[string]any) (any, error) {
	service := strings.TrimSpace(getString(args, "service"))
	if service == "" {
		return nil, errors.New("service parameter is required")
	}
	status, err := e.ops.ServiceStatus(ctx, service)
	if err != nil {
		return nil, err
	}
	return status, nil
}

// restart restarts a managed service.
func (e *Executor) restart(ctx context.Context, args map[string]any) (any, error) {
	service := strings.TrimSpace(getString(args, "service"))
	if service == "" {
		return nil, errors.New("service parameter is required")
	}
	result, err := e.ops.RestartService(ctx, service)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// tailLogs retrieves recent log entries from a managed service.
func (e *Executor) tailLogs(ctx context.Context, args map[string]any) (any, error) {
	service := strings.TrimSpace(getString(args, "service"))
	if service == "" {
		return nil, errors.New("service parameter is required")
	}
	limit := getInt(args, "limit", e.ops.LogTailDefault())
	if limit <= 0 {
		limit = e.ops.LogTailDefault()
	}
	entries, err := e.ops.TailLogs(ctx, service, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"service": service,
		"limit":   limit,
		"entries": entries,
	}, nil
}

// runAnsible executes an allow-listed Ansible playbook.
func (e *Executor) runAnsible(ctx context.Context, args map[string]any) (any, error) {
	playbook := strings.TrimSpace(getString(args, "playbook"))
	if playbook == "" {
		return nil, errors.New("playbook parameter is required")
	}
	extraVars := map[string]string{}
	if raw, ok := args["extra_vars"]; ok {
		switch typed := raw.(type) {
		case map[string]any:
			for k, v := range typed {
				if str, ok := v.(string); ok {
					extraVars[k] = str
				}
			}
		case map[string]string:
			extraVars = typed
		case json.RawMessage:
			_ = json.Unmarshal(typed, &extraVars)
		}
	}
	job, err := e.ops.RunPlaybook(ctx, playbook, extraVars)
	if err != nil {
		return nil, err
	}
	return job, nil
}

// listServices returns all managed services.
func (e *Executor) listServices(ctx context.Context) (any, error) {
	if e.ops == nil {
		return nil, errors.New("operations manager unavailable")
	}
	services := e.ops.Services()
	payload := make([]map[string]any, 0, len(services))
	for _, svc := range services {
		entry := map[string]any{
			"name":      svc.Name,
			"container": svc.Container,
		}
		if svc.Label != "" {
			entry["label"] = svc.Label
		}
		payload = append(payload, entry)
	}
	return payload, nil
}
