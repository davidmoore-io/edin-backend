package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// commanderFIDKey is the context key used to pass the authenticated commander's FID
// into tool invocations. Set by WithCommanderFID; read by commanderFIDFromContext.
type commanderFIDKey struct{}

// WithCommanderFID stores the commander's FID (Frontier ID) in ctx.
// Called by the copilot WebSocket handler before invoking the runner.
func WithCommanderFID(ctx context.Context, fid string) context.Context {
	return context.WithValue(ctx, commanderFIDKey{}, fid)
}

// commanderFIDFromContext retrieves the FID stored by WithCommanderFID.
func commanderFIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(commanderFIDKey{}).(string)
	return v
}

// CommanderEventsToolDefinition returns the MCP tool definition for commander_events.
func CommanderEventsToolDefinition() mcp.Tool {
	return mcp.NewTool(string(ToolCommanderEvents),
		mcp.WithDescription("Query the commander's synced journal events from their Elite Dangerous game session. Returns recent events, optionally filtered by type and time range."),
		mcp.WithString("event_types", mcp.Description("Comma-separated event types to filter (e.g. 'FSDJump,Docked'). Omit for all types.")),
		mcp.WithString("since", mcp.Description("Start time in RFC3339 format. Omit for no lower bound.")),
		mcp.WithString("until", mcp.Description("End time in RFC3339 format. Omit for now.")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of events to return (default 20, max 100)")),
	)
}

// CommanderLocationToolDefinition returns the MCP tool definition for commander_location.
func CommanderLocationToolDefinition() mcp.Tool {
	return mcp.NewTool(string(ToolCommanderLocation),
		mcp.WithDescription("Get the commander's last known location from their synced Elite Dangerous journal events."),
	)
}

// commanderEvents queries journal events for the authenticated commander.
func (e *Executor) commanderEvents(ctx context.Context, args map[string]any) (any, error) {
	if e.commanderRepo == nil {
		return nil, fmt.Errorf("commander repository not available")
	}

	fid := commanderFIDFromContext(ctx)
	if fid == "" {
		return nil, fmt.Errorf("no commander identity in context")
	}

	// Parse limit using executor-configured bounds (defaults: 20 / 100).
	defaultLimit := e.commanderEventsDefaultLimit
	if defaultLimit <= 0 {
		defaultLimit = 20
	}
	maxLimit := e.commanderEventsMaxLimit
	if maxLimit <= 0 {
		maxLimit = 100
	}
	limit := defaultLimit
	if v, ok := args["limit"]; ok {
		switch n := v.(type) {
		case float64:
			limit = int(n)
		case int:
			limit = n
		}
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	// Parse time range. We must NOT default `until` here — the branch below uses
	// `!until.IsZero()` to decide between EventsByType and RecentEvents, and an
	// auto-populated `until` would force every call into the EventsByType path.
	// The default is applied inside the EventsByType branch only (see below).
	var since, until time.Time
	if s, ok := args["since"].(string); ok && s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("invalid 'since' timestamp: %w", err)
		}
		since = t
	}
	if u, ok := args["until"].(string); ok && u != "" {
		t, err := time.Parse(time.RFC3339, u)
		if err != nil {
			return nil, fmt.Errorf("invalid 'until' timestamp: %w", err)
		}
		until = t
	}

	// Parse event_types filter
	var eventTypes []string
	if et, ok := args["event_types"].(string); ok && et != "" {
		for _, t := range strings.Split(et, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				eventTypes = append(eventTypes, trimmed)
			}
		}
	}

	var events []struct {
		Timestamp time.Time
		EventType string
	}

	if len(eventTypes) > 0 || !since.IsZero() || !until.IsZero() {
		// EventsByType's SQL uses `timestamp <= until`, so an unset `until` must
		// fall forward to "now" — Go's zero time is year 1 AD, and every real
		// event fails `timestamp <= year-1 AD`. Regression guarded by
		// TestCommanderEvents_DefaultsUntilToNow.
		if until.IsZero() {
			until = time.Now().UTC()
		}
		rows, err := e.commanderRepo.EventsByType(ctx, fid, eventTypes, since, until)
		if err != nil {
			return nil, fmt.Errorf("querying commander events: %w", err)
		}
		for _, r := range rows {
			events = append(events, struct {
				Timestamp time.Time
				EventType string
			}{r.Timestamp, r.EventType})
		}
		if len(events) > limit {
			events = events[:limit]
		}
	} else {
		rows, err := e.commanderRepo.RecentEvents(ctx, fid, limit)
		if err != nil {
			return nil, fmt.Errorf("querying commander events: %w", err)
		}
		for _, r := range rows {
			events = append(events, struct {
				Timestamp time.Time
				EventType string
			}{r.Timestamp, r.EventType})
		}
	}

	if len(events) == 0 {
		return "No events found for this commander.", nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Recent events for commander (last %d):\n", len(events))
	for _, ev := range events {
		fmt.Fprintf(&sb, "- %s | %s\n", ev.Timestamp.UTC().Format("2006-01-02 15:04:05"), ev.EventType)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// commanderLocation returns the commander's last known location.
func (e *Executor) commanderLocation(ctx context.Context) (any, error) {
	if e.commanderRepo == nil {
		return nil, fmt.Errorf("commander repository not available")
	}

	fid := commanderFIDFromContext(ctx)
	if fid == "" {
		return nil, fmt.Errorf("no commander identity in context")
	}

	loc, err := e.commanderRepo.CurrentLocation(ctx, fid)
	if err != nil {
		return nil, fmt.Errorf("querying commander location: %w", err)
	}
	if loc == nil {
		return "No location data found for this commander.", nil
	}

	return fmt.Sprintf("Commander's last known location:\nSystem: %s\nLast seen: %s UTC",
		loc.SystemName,
		loc.UpdatedAt.UTC().Format("2006-01-02 15:04:05"),
	), nil
}
