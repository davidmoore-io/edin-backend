package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
	betaCompact       = "compact-2026-01-12"
	betaContextManage = "context-management-2025-06-27"
	compactionTrigger = 100_000          // input tokens
	clearToolsTrigger = 50_000           // input tokens
	clearToolsKeep    = 5                // keep last N tool uses
	toolExecTimeout   = 60 * time.Second // per-tool execution timeout
)

// CompactionInstructions tells the compaction model what to preserve.
const CompactionInstructions = "Do not call tools while producing this summary. Preserve Elite Dangerous system names, powerplay states, commander context, and any mining intel discussed. Summarize tool results but keep specific station and system recommendations."

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
	ToolID   string // stable tool_use id from the model; pairs start/complete for one call
	Message  string
	Error    bool
}

// ProgressCallback is called when progress events occur during execution.
type ProgressCallback func(event ProgressEvent)

// Runner orchestrates Anthropic conversations with MCP-backed tools.
type Runner struct {
	client       *anthropic.Client
	executor     *tools.Executor
	mu           sync.RWMutex // protects systemPrompt for concurrent hot-reload
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

// SetSystemPrompt replaces the system prompt used for new conversations.
// Safe for concurrent use — the write is protected by a mutex so in-flight
// RunWithProgress calls are not affected; they snapshotted the prompt at start.
func (r *Runner) SetSystemPrompt(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.systemPrompt = strings.TrimSpace(content)
}

// betaToolDefsForContext returns beta tool definitions filtered by the
// caller's scope set from context. Ops and admin receive full (non-slim)
// definitions so operators see complete parameter schemas; everyone else gets
// slim definitions that nudge the model to call describe_tool first.
//
// The "slim for chat users, full for ops" split is a UX decision, not an
// authorisation one — the per-tool authz filter is identical in both branches
// and flows through toolScopes via tools.SlimBetaToolDefinitionsForScopes /
// tools.BetaToolDefinitionsForScopes.
func (r *Runner) betaToolDefsForContext(ctx context.Context) []sdk.BetaToolUnionParam {
	scopes := authz.ScopesFromContext(ctx)
	if len(scopes) == 0 {
		return nil
	}

	for _, s := range scopes {
		if s == authz.ScopeLlmOperator || s == authz.ScopeAdmin {
			return tools.BetaToolDefinitionsForScopes(scopes)
		}
	}

	return tools.SlimBetaToolDefinitionsForScopes(scopes)
}

// Run executes a single conversational turn given prior session history and the new user message.
func (r *Runner) Run(ctx context.Context, history []llm.Message, userMessage string) (string, error) {
	return r.RunWithProgress(ctx, history, userMessage, nil)
}

// WithSystemPrompt returns a new Runner with the provided system prompt, sharing the
// same client and executor. Use this to create per-session runners with personalised
// prompts (e.g. copilot chat where the prompt includes the commander name).
func (r *Runner) WithSystemPrompt(systemPrompt string) *Runner {
	return &Runner{
		client:       r.client,
		executor:     r.executor,
		systemPrompt: strings.TrimSpace(systemPrompt),
		maxIter:      r.maxIter,
		logger:       r.logger,
	}
}

// RunWithProgress executes a conversational turn with optional progress callbacks.
// Uses the Beta Messages API with context management (compaction + clear_tool_uses).
func (r *Runner) RunWithProgress(ctx context.Context, history []llm.Message, userMessage string, onProgress ProgressCallback) (string, error) {
	result, err := r.RunWithProgressContext(ctx, history, llm.ProviderContext{}, userMessage, onProgress)
	return result.Text, err
}

// RunWithProgressContext executes a conversational turn and returns the exact
// Anthropic context required for the next turn.
func (r *Runner) RunWithProgressContext(
	ctx context.Context,
	history []llm.Message,
	providerContext llm.ProviderContext,
	userMessage string,
	onProgress ProgressCallback,
) (TurnResult, error) {
	if r.client == nil {
		return TurnResult{}, fmt.Errorf("anthropic client unavailable")
	}

	sessionID := SessionIDFromContext(ctx)
	userID := UserIDFromContext(ctx)
	start := time.Now()
	r.logger.Info(fmt.Sprintf("run_start session=%s user=%s history=%d message=\"%s\"", sessionID, userID, len(history), observability.Sanitize(userMessage, 160)))

	messageParams, err := r.buildTurnMessages(history, providerContext, userMessage, nil)
	if err != nil {
		return TurnResult{}, err
	}

	var lastAssistant string
	var usage TurnUsage
	exhausted := true
	for iter := 0; iter < r.maxIter; iter++ {
		r.logger.Info(fmt.Sprintf("iteration_start session=%s user=%s iter=%d messages=%d", sessionID, userID, iter+1, len(messageParams)))

		if onProgress != nil {
			onProgress(ProgressEvent{Type: ProgressThinking, Message: "Thinking..."})
		}

		contextTools := r.betaToolDefsForContext(ctx)

		req := r.buildBetaRequest(contextTools, messageParams)

		resp, err := r.client.CreateBetaMessage(ctx, req)
		if err != nil {
			r.logger.Error(fmt.Sprintf("anthropic_call_failed session=%s user=%s iter=%d", sessionID, userID, iter+1), err)
			return TurnResult{}, err
		}
		usage.add(resp.Usage)

		var compaction compactionState
		messageParams, compaction, err = appendBetaResponse(messageParams, resp)
		if err != nil {
			return TurnResult{}, err
		}
		switch compaction {
		case compactionValid:
			r.logger.Info(fmt.Sprintf("compaction_checkpoint session=%s user=%s iter=%d", sessionID, userID, iter+1))
		case compactionNull:
			r.logger.Warn(fmt.Sprintf("compaction_failed_noop session=%s user=%s iter=%d", sessionID, userID, iter+1))
		}

		assistantText, toolBlocks := r.extractBetaContent(resp)
		r.logger.Info(fmt.Sprintf("iteration_response session=%s user=%s iter=%d tools=%d assistant=\"%s\"", sessionID, userID, iter+1, len(toolBlocks), observability.Sanitize(assistantText, 200)))

		if len(toolBlocks) == 0 {
			lastAssistant = assistantText
			exhausted = false
			r.logger.Info(fmt.Sprintf("iteration_complete session=%s user=%s iter=%d no_tool_calls", sessionID, userID, iter+1))
			break
		}

		toolResults, err := r.invokeBetaToolsWithProgress(ctx, toolBlocks, onProgress)
		if err != nil {
			r.logger.Error(fmt.Sprintf("tool_invocation_failed session=%s user=%s iter=%d", sessionID, userID, iter+1), err)
			return TurnResult{}, err
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

	encodedContext, err := encodeProviderContext(messageParams)
	if err != nil {
		return TurnResult{}, err
	}
	return TurnResult{
		Text:            lastAssistant,
		ProviderContext: encodedContext,
		Usage:           usage,
	}, nil
}

// ImageInput is an image attached to the current user turn. Base64 is the raw
// base64 payload (no data-URI prefix); MediaType is an HTTP image media type
// such as "image/png". It is sent only on the turn it is attached — images are
// not persisted into conversation history (send-per-turn).
type ImageInput struct {
	Base64    string
	MediaType string
}

// imageMediaType maps an HTTP media-type string to the SDK's base64 image
// media-type constant, defaulting to PNG (the client encodes screenshots as PNG).
func imageMediaType(s string) sdk.BetaBase64ImageSourceMediaType {
	switch s {
	case "image/jpeg":
		return sdk.BetaBase64ImageSourceMediaTypeImageJPEG
	case "image/gif":
		return sdk.BetaBase64ImageSourceMediaTypeImageGIF
	case "image/webp":
		return sdk.BetaBase64ImageSourceMediaTypeImageWebP
	default:
		return sdk.BetaBase64ImageSourceMediaTypeImagePNG
	}
}

func (r *Runner) buildBetaMessageParams(history []llm.Message, userMessage string, image *ImageInput) []sdk.BetaMessageParam {
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

	// Final (current) user turn. When an image is attached, send it as an image
	// content block alongside the text (image first, then any text). An
	// image-only turn (empty userMessage) is valid — the Messages API accepts a
	// user message containing only an image block.
	if image != nil && image.Base64 != "" {
		blocks := []sdk.BetaContentBlockParamUnion{
			{OfImage: &sdk.BetaImageBlockParam{
				Source: sdk.BetaImageBlockParamSourceUnion{
					OfBase64: &sdk.BetaBase64ImageSourceParam{
						Data:      image.Base64,
						MediaType: imageMediaType(image.MediaType),
					},
				},
			}},
		}
		if userMessage != "" {
			blocks = append(blocks, sdk.NewBetaTextBlock(userMessage))
		}
		params = append(params, sdk.NewBetaUserMessage(blocks...))
	} else {
		params = append(params, sdk.NewBetaUserMessage(sdk.NewBetaTextBlock(userMessage)))
	}
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

// betaToolUse is an intermediate struct for tool use blocks from the beta response.
type betaToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
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
				ToolID:   block.ID,
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
				ToolID:   block.ID,
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
