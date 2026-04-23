package tools

import (
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/edin-space/edin-backend/internal/authz"
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

// MCPToAnthropicAll converts a slice of MCP tool definitions to Anthropic SDK
// format, filtered by the caller's scope set.
//
// A tool is included when toolVisible(callerScopes, ToolName(tool.Name)) is
// true — i.e. when the tool is registered in toolScopes AND either the required
// scope is empty or the caller holds the required scope. Tools not present in
// toolScopes are silently dropped (fail-closed); the same check is enforced at
// dispatch time in Executor.Invoke.
func MCPToAnthropicAll(tools []mcp.Tool, callerScopes []authz.Scope) []sdk.ToolUnionParam {
	result := make([]sdk.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		if !toolVisible(callerScopes, ToolName(tool.Name)) {
			continue
		}
		result = append(result, MCPToAnthropic(tool))
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

// MCPToBetaAll converts a slice of MCP tool definitions to Anthropic Beta SDK
// format, filtered by the caller's scope set. See MCPToAnthropicAll for the
// filter semantics.
func MCPToBetaAll(tools []mcp.Tool, callerScopes []authz.Scope) []sdk.BetaToolUnionParam {
	result := make([]sdk.BetaToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		if !toolVisible(callerScopes, ToolName(tool.Name)) {
			continue
		}
		result = append(result, MCPToBeta(tool))
	}
	return result
}

// toolVisible returns true when a tool with the given name should be listed to
// (and invokable by) a caller holding callerScopes.
//
// Rules, in order:
//  1. Tools not present in toolScopes are refused (fail-closed). A missing
//     entry is a coding mistake caught by scopes_test.go's guardrail, not an
//     implicit-public tool.
//  2. Tools registered with an empty required scope are available to any
//     caller that has already passed the product's coarse endpoint gate.
//  3. Otherwise the caller must hold the required scope.
//
// Keep this helper in sync with the identical check in Executor.Invoke — both
// must stay fail-closed on missing registry entries.
func toolVisible(callerScopes []authz.Scope, name ToolName) bool {
	required, registered := toolScopes[name]
	if !registered {
		return false
	}
	if required == "" {
		return true
	}
	return authz.Allow(callerScopes, required)
}
