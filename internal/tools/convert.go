package tools

import (
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPToAnthropic converts a single MCP tool definition to an Anthropic SDK ToolUnionParam.
func MCPToAnthropic(tool mcp.Tool) sdk.ToolUnionParam {
	var desc param.Opt[string]
	if trimmed := strings.TrimSpace(tool.Description); trimmed != "" {
		desc = param.NewOpt(trimmed)
	}

	return sdk.ToolUnionParam{
		OfTool: &sdk.ToolParam{
			Name:        tool.Name,
			Description: desc,
			Type:        sdk.ToolTypeCustom,
			InputSchema: sdk.ToolInputSchemaParam{
				Properties: tool.InputSchema.Properties,
				Required:   tool.InputSchema.Required,
			},
		},
	}
}

// MCPToAnthropicAll converts a slice of MCP tool definitions to Anthropic SDK format.
func MCPToAnthropicAll(tools []mcp.Tool) []sdk.ToolUnionParam {
	result := make([]sdk.ToolUnionParam, len(tools))
	for i, tool := range tools {
		result[i] = MCPToAnthropic(tool)
	}
	return result
}

// MCPToBeta converts a single MCP tool definition to an Anthropic Beta SDK BetaToolUnionParam.
func MCPToBeta(tool mcp.Tool) sdk.BetaToolUnionParam {
	var desc param.Opt[string]
	if trimmed := strings.TrimSpace(tool.Description); trimmed != "" {
		desc = param.NewOpt(trimmed)
	}

	return sdk.BetaToolUnionParam{
		OfTool: &sdk.BetaToolParam{
			Name:        tool.Name,
			Description: desc,
			Type:        sdk.BetaToolTypeCustom,
			InputSchema: sdk.BetaToolInputSchemaParam{
				Properties: tool.InputSchema.Properties,
				Required:   tool.InputSchema.Required,
			},
		},
	}
}

// MCPToBetaAll converts a slice of MCP tool definitions to Anthropic Beta SDK format.
func MCPToBetaAll(tools []mcp.Tool) []sdk.BetaToolUnionParam {
	result := make([]sdk.BetaToolUnionParam, len(tools))
	for i, tool := range tools {
		result[i] = MCPToBeta(tool)
	}
	return result
}
