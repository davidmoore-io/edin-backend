package tools

import "github.com/mark3labs/mcp-go/mcp"

// CommanderEventsToolDefinition returns the MCP tool definition for commander_events.
// Full implementation in Story 5.2. This stub exists so the copilot scope filter works.
func CommanderEventsToolDefinition() mcp.Tool {
	return mcp.NewTool(string(ToolCommanderEvents),
		mcp.WithDescription("Query the commander's synced journal events from their Elite Dangerous game session."),
		mcp.WithString("event_types", mcp.Required(), mcp.Description("Comma-separated event types to filter (e.g. 'FSDJump,Docked')")),
		mcp.WithString("since", mcp.Description("Start time in RFC3339 format")),
		mcp.WithString("until", mcp.Description("End time in RFC3339 format")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of events to return (default 50)")),
	)
}

// CommanderLocationToolDefinition returns the MCP tool definition for commander_location.
// Full implementation in Story 5.3. This stub exists so the copilot scope filter works.
func CommanderLocationToolDefinition() mcp.Tool {
	return mcp.NewTool(string(ToolCommanderLocation),
		mcp.WithDescription("Get the commander's last known location from their synced journal events."),
	)
}
