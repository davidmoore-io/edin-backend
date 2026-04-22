package tools

import (
	"context"
	"encoding/json"
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

// perEventDataBytesCap bounds how much of an individual event's event_data
// JSON we forward to the LLM. Most journal events are under 1KB; a handful
// (StoredModules, StoredShips, Cargo, MaterialsCollected) can balloon into
// tens or hundreds of KB. Truncating per-event keeps a page of 20 events
// well within the context window without blinding the AI to the common case.
const perEventDataBytesCap = 2048

// CommanderEventsToolDefinition returns the MCP tool definition for commander_events.
func CommanderEventsToolDefinition() mcp.Tool {
	return mcp.NewTool(string(ToolCommanderEvents),
		mcp.WithDescription(
			"Query the commander's synced Elite Dangerous journal events. Returns each event's timestamp, event_type, and the full event_data JSON payload. "+
				"Use this to answer questions about where the commander has been, what they've done, what station they last docked at, what they bought or sold, mission state, etc. — "+
				"each journal event carries its own named fields inside event_data (e.g. Docked events have StationName/StarSystem, FSDJump events have StarSystem/JumpDist, MissionAccepted has Faction/Reward). "+
				"When a filter is helpful, set event_types to narrow the page.",
		),
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

// commanderEventSummary is the per-event shape returned to the LLM.
// EventData is the raw journal payload so the model can read any field it
// needs (station names, system coords, mission state, ...). Oversized
// payloads are replaced with a short note rather than dropped entirely, so
// the model still sees that the event occurred even if the details are
// beyond the context budget.
type commanderEventSummary struct {
	Timestamp string          `json:"timestamp"`
	EventType string          `json:"event_type"`
	EventData json.RawMessage `json:"event_data,omitempty"`
	Note      string          `json:"note,omitempty"`
}

// commanderEventsResult is the structured response for commander_events.
type commanderEventsResult struct {
	FID    string                  `json:"fid"`
	Count  int                     `json:"count"`
	Events []commanderEventSummary `json:"events"`
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

	summaries := make([]commanderEventSummary, 0, limit)

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
			if len(summaries) >= limit {
				break
			}
			summaries = append(summaries, toEventSummary(r.Timestamp, r.EventType, r.EventData))
		}
	} else {
		rows, err := e.commanderRepo.RecentEvents(ctx, fid, limit)
		if err != nil {
			return nil, fmt.Errorf("querying commander events: %w", err)
		}
		for _, r := range rows {
			summaries = append(summaries, toEventSummary(r.Timestamp, r.EventType, r.EventData))
		}
	}

	return commanderEventsResult{
		FID:    fid,
		Count:  len(summaries),
		Events: summaries,
	}, nil
}

// toEventSummary builds the per-event response entry, caring for size budget
// and for event_data that somehow didn't round-trip as valid JSON (treat it
// as an opaque string rather than silently dropping it).
func toEventSummary(ts time.Time, eventType string, data json.RawMessage) commanderEventSummary {
	out := commanderEventSummary{
		Timestamp: ts.UTC().Format(time.RFC3339),
		EventType: eventType,
	}

	if len(data) == 0 {
		return out
	}
	if len(data) > perEventDataBytesCap {
		out.Note = fmt.Sprintf(
			"event_data omitted (%d bytes exceeds %d-byte per-event cap; query with a narrower event_types filter to see the full payload for this type)",
			len(data), perEventDataBytesCap,
		)
		return out
	}
	// Validate the payload parses as JSON. pg rows should always carry valid
	// JSON here (event_data is jsonb), but surface any surprise cleanly rather
	// than emitting invalid JSON to the tool-result stream.
	if !json.Valid(data) {
		out.Note = "event_data was not valid JSON and has been omitted"
		return out
	}
	out.EventData = data
	return out
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
