package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

const ToolDescribeTool ToolName = "describe_tool"

// DescribeTool returns the full parameter schema and usage guidance for a tool.
func DescribeTool(toolName string) (map[string]any, error) {
	name := ToolName(toolName)

	// Find the MCP definition for this tool
	var found *mcp.Tool
	for _, t := range MCPToolDefinitions() {
		if t.Name == toolName {
			found = &t
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("unknown tool %q — use one of the available tools listed in your instructions", toolName)
	}

	result := map[string]any{
		"tool_name":   found.Name,
		"description": found.Description,
		"parameters":  found.InputSchema.Properties,
		"required":    found.InputSchema.Required,
	}

	// Add guidance if available
	if guidance, ok := ToolGuidance[name]; ok {
		result["usage_guidance"] = strings.TrimSpace(guidance)
	}

	return result, nil
}

// DescribeToolMCPDefinition returns the MCP tool definition for describe_tool itself.
func DescribeToolMCPDefinition() mcp.Tool {
	return mcp.NewTool(string(ToolDescribeTool),
		mcp.WithDescription("Get detailed parameter schema and usage guidance for any available tool. Call this before using a complex tool for the first time."),
		mcp.WithString("tool_name", mcp.Required(), mcp.Description("Name of the tool to describe (e.g. 'galaxy_market', 'galaxy_query')")),
	)
}

// DescribeToolJSON returns guidance as a JSON string for tool results.
func DescribeToolJSON(toolName string) (string, error) {
	result, err := DescribeTool(toolName)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal describe_tool result: %w", err)
	}
	return string(data), nil
}
