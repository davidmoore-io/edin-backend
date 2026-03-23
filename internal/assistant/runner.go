package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/edin-space/edin-backend/internal/anthropic"
	"github.com/edin-space/edin-backend/internal/authz"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/observability"
	"github.com/edin-space/edin-backend/internal/tools"
)

// Beta headers required for context management.
const (
	betaCompact        = "compact-2026-01-12"
	betaContextManage  = "context-management-2025-06-27"
	compactionTrigger  = 100_000 // input tokens
	clearToolsTrigger  = 50_000  // input tokens
	clearToolsKeep     = 5       // keep last N tool uses
	toolExecTimeout    = 60 * time.Second // per-tool execution timeout
)

// CompactionInstructions tells the compaction model what to preserve.
const CompactionInstructions = "Preserve Elite Dangerous system names, powerplay states, and any mining intel discussed. Summarize tool results but keep specific station/system recommendations."

// ProgressEventType identifies the kind of progress event.
type ProgressEventType string

const (
	ProgressToolStart    ProgressEventType = "tool_start"
	ProgressToolComplete ProgressEventType = "tool_complete"
	ProgressThinking     ProgressEventType = "thinking"
)

// ProgressEvent represents a progress update during LLM execution.
type ProgressEvent struct {
	Type     ProgressEventType
	ToolName string
	Message  string
	Error    bool
}

// ProgressCallback is called when progress events occur during execution.
type ProgressCallback func(event ProgressEvent)

// Runner orchestrates Anthropic conversations with MCP-backed tools.
type Runner struct {
	client       *anthropic.Client
	executor     *tools.Executor
	systemPrompt string
	maxIter      int
	logger       *observability.Logger
}

// NewRunner builds a runner with sensible defaults.
func NewRunner(client *anthropic.Client, executor *tools.Executor, systemPrompt string, maxIterations int) *Runner {
	if maxIterations <= 0 {
		maxIterations = 5
	}
	return &Runner{
		client:       client,
		executor:     executor,
		systemPrompt: strings.TrimSpace(systemPrompt),
		maxIter:      maxIterations,
		logger:       observability.NewLogger("assistant.runner"),
	}
}

// betaToolDefsForContext returns beta tool definitions filtered by authorization scope.
// Kaine scope gets slim definitions (forces describe_tool usage); ops/admin gets full.
func (r *Runner) betaToolDefsForContext(ctx context.Context) []sdk.BetaToolUnionParam {
	scopes := authz.ScopesFromContext(ctx)

	for _, s := range scopes {
		if s == authz.ScopeLlmOperator || s == authz.ScopeAdmin {
			return tools.BetaToolDefinitions()
		}
	}

	for _, s := range scopes {
		if s == authz.ScopeKaineChat {
			return tools.SlimBetaToolDefinitionsForScope(authz.ScopeKaineChat)
		}
	}

	return nil
}

// Run executes a single conversational turn given prior session history and the new user message.
func (r *Runner) Run(ctx context.Context, history []llm.Message, userMessage string) (string, error) {
	return r.RunWithProgress(ctx, history, userMessage, nil)
}

// RunWithProgress executes a conversational turn with optional progress callbacks.
// Uses the Beta Messages API with context management (compaction + clear_tool_uses).
func (r *Runner) RunWithProgress(ctx context.Context, history []llm.Message, userMessage string, onProgress ProgressCallback) (string, error) {
	if r.client == nil {
		return "", fmt.Errorf("anthropic client unavailable")
	}

	sessionID := SessionIDFromContext(ctx)
	userID := UserIDFromContext(ctx)
	start := time.Now()
	r.logger.Info(fmt.Sprintf("run_start session=%s user=%s history=%d message=\"%s\"", sessionID, userID, len(history), observability.Sanitize(userMessage, 160)))

	messageParams := r.buildBetaMessageParams(history, userMessage)

	var lastAssistant string
	exhausted := true
	for iter := 0; iter < r.maxIter; iter++ {
		r.logger.Info(fmt.Sprintf("iteration_start session=%s user=%s iter=%d messages=%d", sessionID, userID, iter+1, len(messageParams)))

		if onProgress != nil {
			onProgress(ProgressEvent{Type: ProgressThinking, Message: "Thinking..."})
		}

		contextTools := r.betaToolDefsForContext(ctx)

		req := sdk.BetaMessageNewParams{
			Model:     r.client.Model(),
			MaxTokens: r.client.MaxTokens(),
			Messages:  messageParams,
			Tools:     contextTools,
			Betas:     []sdk.AnthropicBeta{betaCompact, betaContextManage},
			ContextManagement: sdk.BetaContextManagementConfigParam{
				Edits: []sdk.BetaContextManagementConfigEditUnionParam{
					{
						OfCompact20260112: &sdk.BetaCompact20260112EditParam{
							Instructions: param.NewOpt(CompactionInstructions),
							Trigger: sdk.BetaInputTokensTriggerParam{
								Value: compactionTrigger,
							},
						},
					},
					{
						OfClearToolUses20250919: &sdk.BetaClearToolUses20250919EditParam{
							ClearToolInputs: sdk.BetaClearToolUses20250919EditClearToolInputsUnionParam{
								OfBool: sdk.Bool(true),
							},
							Keep: sdk.BetaToolUsesKeepParam{
								Value: clearToolsKeep,
							},
							Trigger: sdk.BetaClearToolUses20250919EditTriggerUnionParam{
								OfInputTokens: &sdk.BetaInputTokensTriggerParam{
									Value: clearToolsTrigger,
								},
							},
						},
					},
				},
			},
			ToolChoice: sdk.BetaToolChoiceUnionParam{
				OfAuto: &sdk.BetaToolChoiceAutoParam{},
			},
		}

		// System prompt with cache control breakpoint for compaction preservation
		if r.systemPrompt != "" {
			req.System = []sdk.BetaTextBlockParam{
				{
					Text: r.systemPrompt,
					CacheControl: sdk.BetaCacheControlEphemeralParam{
						TTL: sdk.BetaCacheControlEphemeralTTLTTL5m,
					},
				},
			}
		}

		resp, err := r.client.CreateBetaMessage(ctx, req)
		if err != nil {
			r.logger.Error(fmt.Sprintf("anthropic_call_failed session=%s user=%s iter=%d", sessionID, userID, iter+1), err)
			return "", err
		}

		// Handle compaction: append the compaction block and continue
		if resp.StopReason == sdk.BetaStopReasonCompaction {
			r.logger.Info(fmt.Sprintf("compaction_triggered session=%s user=%s iter=%d", sessionID, userID, iter+1))
			compactionBlocks := r.extractBetaCompactionBlocks(resp)
			if len(compactionBlocks) > 0 {
				messageParams = append(messageParams, sdk.BetaMessageParam{
					Role:    sdk.BetaMessageParamRoleAssistant,
					Content: compactionBlocks,
				})
			}
			continue
		}

		assistantText, toolBlocks := r.extractBetaContent(resp)
		r.logger.Info(fmt.Sprintf("iteration_response session=%s user=%s iter=%d tools=%d assistant=\"%s\"", sessionID, userID, iter+1, len(toolBlocks), observability.Sanitize(assistantText, 200)))

		if len(toolBlocks) == 0 {
			lastAssistant = assistantText
			exhausted = false
			r.logger.Info(fmt.Sprintf("iteration_complete session=%s user=%s iter=%d no_tool_calls", sessionID, userID, iter+1))
			break
		}

		assistantParam := r.sanitizeBetaAssistantMessage(resp, sessionID, userID)
		messageParams = append(messageParams, assistantParam)

		toolResults, err := r.invokeBetaToolsWithProgress(ctx, toolBlocks, onProgress)
		if err != nil {
			r.logger.Error(fmt.Sprintf("tool_invocation_failed session=%s user=%s iter=%d", sessionID, userID, iter+1), err)
			return "", err
		}
		messageParams = append(messageParams, sdk.NewBetaUserMessage(toolResults...))
		lastAssistant = assistantText
	}

	elapsed := time.Since(start)
	if exhausted {
		r.logger.Warn(fmt.Sprintf("run_exhausted session=%s user=%s iterations=%d duration=%s", sessionID, userID, r.maxIter, elapsed))
	}

	lastAssistant = strings.TrimSpace(lastAssistant)
	if lastAssistant == "" && exhausted {
		lastAssistant = "I ran out of steps trying to answer that. Could you try rephrasing, or ask something more specific?"
		r.logger.Warn(fmt.Sprintf("run_completed session=%s user=%s exhausted_fallback duration=%s", sessionID, userID, elapsed))
	} else if lastAssistant == "" {
		r.logger.Warn(fmt.Sprintf("run_completed session=%s user=%s empty_reply duration=%s", sessionID, userID, elapsed))
	} else {
		r.logger.Info(fmt.Sprintf("run_completed session=%s user=%s reply=\"%s\" duration=%s", sessionID, userID, observability.Sanitize(lastAssistant, 200), elapsed))
	}

	return lastAssistant, nil
}

func (r *Runner) buildBetaMessageParams(history []llm.Message, userMessage string) []sdk.BetaMessageParam {
	params := make([]sdk.BetaMessageParam, 0, len(history)+1)
	for _, msg := range history {
		content := sdk.NewBetaTextBlock(msg.Content)
		switch strings.ToLower(msg.Role) {
		case "assistant":
			params = append(params, sdk.BetaMessageParam{
				Role:    sdk.BetaMessageParamRoleAssistant,
				Content: []sdk.BetaContentBlockParamUnion{content},
			})
		case "system":
			continue
		default:
			params = append(params, sdk.NewBetaUserMessage(content))
		}
	}
	params = append(params, sdk.NewBetaUserMessage(sdk.NewBetaTextBlock(userMessage)))
	return params
}

func (r *Runner) extractBetaContent(resp *sdk.BetaMessage) (string, []betaToolUse) {
	if resp == nil {
		return "", nil
	}

	var builder strings.Builder
	var toolBlocks []betaToolUse

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(text)
			}
		case "tool_use":
			toolBlocks = append(toolBlocks, betaToolUse{
				ID:    block.ID,
				Name:  block.Name,
				Input: block.Input,
			})
		case "web_search_tool_result":
			if summary := renderBetaWebSearchResult(block); summary != "" {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(summary)
			}
		}
	}
	return builder.String(), toolBlocks
}

func (r *Runner) extractBetaCompactionBlocks(resp *sdk.BetaMessage) []sdk.BetaContentBlockParamUnion {
	var blocks []sdk.BetaContentBlockParamUnion
	for _, block := range resp.Content {
		if block.Type == "compaction" {
			// Re-emit as text for the conversation to continue
			blocks = append(blocks, sdk.NewBetaTextBlock(block.Text))
		}
	}
	return blocks
}

// betaToolUse is an intermediate struct for tool use blocks from the beta response.
type betaToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
}

func (r *Runner) sanitizeBetaAssistantMessage(resp *sdk.BetaMessage, sessionID, userID string) sdk.BetaMessageParam {
	if resp == nil {
		return sdk.BetaMessageParam{
			Role:    sdk.BetaMessageParamRoleAssistant,
			Content: []sdk.BetaContentBlockParamUnion{sdk.NewBetaTextBlock("")},
		}
	}

	blocks := make([]sdk.BetaContentBlockParamUnion, 0, len(resp.Content))
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			blocks = append(blocks, sdk.NewBetaTextBlock(block.Text))
		case "tool_use":
			var input any
			if len(block.Input) > 0 {
				if err := json.Unmarshal(block.Input, &input); err != nil {
					input = json.RawMessage(block.Input)
				}
			}
			blocks = append(blocks, sdk.NewBetaToolUseBlock(block.ID, input, block.Name))
		case "compaction":
			r.logger.Info(fmt.Sprintf("compaction_block_preserved session=%s user=%s", sessionID, userID))
			blocks = append(blocks, sdk.NewBetaTextBlock(block.Text))
		case "server_tool_use":
			r.logger.Warn(fmt.Sprintf("server_tool_use_ignored session=%s user=%s id=%s name=%s", sessionID, userID, block.ID, block.Name))
		case "web_search_tool_result":
			r.logger.Warn(fmt.Sprintf("web_search_tool_result_ignored session=%s user=%s", sessionID, userID))
		default:
			r.logger.Warn(fmt.Sprintf("content_block_ignored session=%s user=%s type=%s", sessionID, userID, block.Type))
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, sdk.NewBetaTextBlock(""))
	}

	return sdk.BetaMessageParam{
		Role:    sdk.BetaMessageParamRoleAssistant,
		Content: blocks,
	}
}

func (r *Runner) invokeBetaToolsWithProgress(ctx context.Context, blocks []betaToolUse, onProgress ProgressCallback) ([]sdk.BetaContentBlockParamUnion, error) {
	results := make([]sdk.BetaContentBlockParamUnion, 0, len(blocks))
	sessionID := SessionIDFromContext(ctx)
	userID := UserIDFromContext(ctx)

	for _, block := range blocks {
		input := map[string]any{}
		if len(block.Input) > 0 {
			_ = json.Unmarshal(block.Input, &input)
		}

		var (
			payload []byte
			err     error
		)

		inputPreview := observability.Sanitize(string(block.Input), 160)
		toolStart := time.Now()
		r.logger.Info(fmt.Sprintf("tool_call_start session=%s user=%s tool=%s id=%s input=\"%s\"", sessionID, userID, block.Name, block.ID, inputPreview))

		if onProgress != nil {
			onProgress(ProgressEvent{
				Type:     ProgressToolStart,
				ToolName: block.Name,
				Message:  fmt.Sprintf("Running %s...", block.Name),
			})
		}

		toolCtx, toolCancel := context.WithTimeout(ctx, toolExecTimeout)
		result, execErr := r.executor.Invoke(toolCtx, block.Name, input)
		toolCancel()
		if execErr != nil {
			err = execErr
			errMsg := execErr.Error()
			if toolCtx.Err() == context.DeadlineExceeded {
				errMsg = fmt.Sprintf("tool execution timed out after %s — try a simpler query", toolExecTimeout)
			}
			r.logger.Error(fmt.Sprintf("tool_call_error session=%s user=%s tool=%s id=%s", sessionID, userID, block.Name, block.ID), execErr)
			payload, _ = json.Marshal(map[string]any{
				"error": errMsg,
			})
		} else {
			payload, err = json.MarshalIndent(result, "", "  ")
			if err != nil {
				payload = []byte(fmt.Sprintf(`{"error":"failed to encode result: %v"}`, err))
			}
		}

		isError := err != nil
		elapsed := time.Since(toolStart)
		r.logger.Info(fmt.Sprintf("tool_call_complete session=%s user=%s tool=%s id=%s error=%t duration=%s output=\"%s\"", sessionID, userID, block.Name, block.ID, isError, elapsed, observability.Sanitize(string(payload), 200)))

		if onProgress != nil {
			onProgress(ProgressEvent{
				Type:     ProgressToolComplete,
				ToolName: block.Name,
				Message:  fmt.Sprintf("%s completed in %s", block.Name, elapsed.Round(time.Millisecond)),
				Error:    isError,
			})
		}

		toolResult := sdk.NewBetaToolResultBlock(block.ID)
		toolResult.OfToolResult.Content = []sdk.BetaToolResultBlockParamContentUnion{
			{OfText: &sdk.BetaTextBlockParam{Text: string(payload)}},
		}
		toolResult.OfToolResult.IsError = param.NewOpt(isError)

		results = append(results, toolResult)
	}
	return results, nil
}

func renderBetaWebSearchResult(block sdk.BetaContentBlockUnion) string {
	if block.Type != "web_search_tool_result" {
		return ""
	}
	// Beta web search results: extract text from the block if available
	if block.Text != "" {
		return fmt.Sprintf("web_search: %s", observability.Sanitize(block.Text, 500))
	}
	return "web_search: results available"
}
