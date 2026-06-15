package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/edin-space/edin-backend/internal/llm"
	"github.com/edin-space/edin-backend/internal/voice"
)

// StreamingRunnerCallbacks are fired incrementally as the streaming response arrives.
type StreamingRunnerCallbacks struct {
	OnTextDelta  func(string)
	OnSpeakChunk func(string)
	OnDataChunk  func(string)
	OnProgress   ProgressCallback
}

// streamingToolUse accumulates a tool_use block during streaming.
type streamingToolUse struct {
	id          string
	name        string
	inputBuffer strings.Builder
}

// RunWithStreaming executes a conversational turn using Anthropic token streaming.
// ADDITIVE: RunWithProgress is untouched. Same helpers are reused.
func (r *Runner) RunWithStreaming(ctx context.Context, history []llm.Message, userMessage string, cb StreamingRunnerCallbacks) (string, error) {
	if r.client == nil {
		return "", fmt.Errorf("anthropic client unavailable")
	}
	if cb.OnTextDelta == nil {
		cb.OnTextDelta = func(string) {}
	}
	if cb.OnSpeakChunk == nil {
		cb.OnSpeakChunk = func(string) {}
	}
	if cb.OnDataChunk == nil {
		cb.OnDataChunk = func(string) {}
	}

	tagParser := voice.NewStreamingTagParser()
	tagParser.OnSpeakChunk(cb.OnSpeakChunk)
	tagParser.OnDataChunk(cb.OnDataChunk)

	sessionID := SessionIDFromContext(ctx)
	userID := UserIDFromContext(ctx)
	start := time.Now()
	r.logger.Info(fmt.Sprintf("stream_start session=%s user=%s history=%d", sessionID, userID, len(history)))

	messageParams := r.buildBetaMessageParams(history, userMessage)
	var lastAssistant string
	exhausted := true

	for iter := 0; iter < r.maxIter; iter++ {
		if cb.OnProgress != nil {
			cb.OnProgress(ProgressEvent{Type: ProgressThinking, Message: "Thinking..."})
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
					{OfCompact20260112: &sdk.BetaCompact20260112EditParam{
						Instructions: param.NewOpt(CompactionInstructions),
						Trigger:      sdk.BetaInputTokensTriggerParam{Value: compactionTrigger},
					}},
					{OfClearToolUses20250919: &sdk.BetaClearToolUses20250919EditParam{
						ClearToolInputs: sdk.BetaClearToolUses20250919EditClearToolInputsUnionParam{OfBool: sdk.Bool(true)},
						Keep:            sdk.BetaToolUsesKeepParam{Value: clearToolsKeep},
						Trigger: sdk.BetaClearToolUses20250919EditTriggerUnionParam{
							OfInputTokens: &sdk.BetaInputTokensTriggerParam{Value: clearToolsTrigger},
						},
					}},
				},
			},
			ToolChoice: sdk.BetaToolChoiceUnionParam{OfAuto: &sdk.BetaToolChoiceAutoParam{}},
		}

		r.mu.RLock()
		systemPrompt := r.systemPrompt
		r.mu.RUnlock()
		if systemPrompt != "" {
			req.System = []sdk.BetaTextBlockParam{{
				Text:         systemPrompt,
				CacheControl: sdk.BetaCacheControlEphemeralParam{TTL: sdk.BetaCacheControlEphemeralTTLTTL5m},
			}}
		}

		stream := r.client.RawClient().Beta.Messages.NewStreaming(ctx, req)
		var textBuilder strings.Builder
		toolsByIndex := map[int64]*streamingToolUse{}
		var toolOrder []int64

		for stream.Next() {
			event := stream.Current()
			switch e := event.AsAny().(type) {
			case sdk.BetaRawContentBlockStartEvent:
				if e.ContentBlock.Type == "tool_use" {
					toolsByIndex[e.Index] = &streamingToolUse{
						id:   e.ContentBlock.ID,
						name: e.ContentBlock.Name,
					}
					toolOrder = append(toolOrder, e.Index)
					if cb.OnProgress != nil {
						cb.OnProgress(ProgressEvent{
							Type:     ProgressToolStart,
							ToolName: e.ContentBlock.Name,
							ToolID:   e.ContentBlock.ID,
							Message:  fmt.Sprintf("Running %s...", e.ContentBlock.Name),
						})
					}
				}
			case sdk.BetaRawContentBlockDeltaEvent:
				switch e.Delta.Type {
				case "text_delta":
					if e.Delta.Text != "" {
						textBuilder.WriteString(e.Delta.Text)
						cb.OnTextDelta(e.Delta.Text)
						tagParser.Feed(e.Delta.Text)
					}
				case "input_json_delta":
					if tu, ok := toolsByIndex[e.Index]; ok {
						tu.inputBuffer.WriteString(e.Delta.PartialJSON)
					}
				}
			}
		}
		if err := stream.Err(); err != nil {
			r.logger.Error(fmt.Sprintf("stream_failed session=%s iter=%d", sessionID, iter+1), err)
			return "", err
		}

		tagParser.Flush()

		var toolBlocks []betaToolUse
		for _, idx := range toolOrder {
			tu := toolsByIndex[idx]
			raw := json.RawMessage(tu.inputBuffer.String())
			if len(raw) == 0 {
				raw = json.RawMessage("{}")
			}
			toolBlocks = append(toolBlocks, betaToolUse{ID: tu.id, Name: tu.name, Input: raw})
		}

		assistantText := strings.TrimSpace(textBuilder.String())
		r.logger.Info(fmt.Sprintf("stream_iteration session=%s iter=%d tools=%d text_len=%d", sessionID, iter+1, len(toolBlocks), len(assistantText)))

		if len(toolBlocks) == 0 {
			lastAssistant = assistantText
			exhausted = false
			break
		}

		assistantParam := r.buildStreamingAssistantParam(assistantText, toolBlocks, sessionID, userID)
		messageParams = append(messageParams, assistantParam)

		toolResults, err := r.invokeBetaToolsWithProgress(ctx, toolBlocks, cb.OnProgress)
		if err != nil {
			return "", err
		}
		messageParams = append(messageParams, sdk.NewBetaUserMessage(toolResults...))
		lastAssistant = assistantText

		// Reset tag parser for next iteration
		tagParser = voice.NewStreamingTagParser()
		tagParser.OnSpeakChunk(cb.OnSpeakChunk)
		tagParser.OnDataChunk(cb.OnDataChunk)
	}

	elapsed := time.Since(start)
	r.logger.Info(fmt.Sprintf("stream_complete session=%s exhausted=%v duration=%s", sessionID, exhausted, elapsed))

	if lastAssistant == "" && exhausted {
		lastAssistant = "I ran out of steps trying to answer that. Could you try rephrasing, or ask something more specific?"
	}
	return strings.TrimSpace(lastAssistant), nil
}

// buildStreamingAssistantParam constructs an assistant message param from streamed content.
func (r *Runner) buildStreamingAssistantParam(text string, toolBlocks []betaToolUse, sessionID, userID string) sdk.BetaMessageParam {
	blocks := make([]sdk.BetaContentBlockParamUnion, 0, 1+len(toolBlocks))
	if text != "" {
		blocks = append(blocks, sdk.NewBetaTextBlock(text))
	}
	for _, block := range toolBlocks {
		var input any
		if len(block.Input) > 0 {
			if err := json.Unmarshal(block.Input, &input); err != nil {
				input = json.RawMessage(block.Input)
			}
		}
		blocks = append(blocks, sdk.NewBetaToolUseBlock(block.ID, input, block.Name))
	}
	if len(blocks) == 0 {
		blocks = append(blocks, sdk.NewBetaTextBlock(""))
	}
	return sdk.BetaMessageParam{Role: sdk.BetaMessageParamRoleAssistant, Content: blocks}
}
