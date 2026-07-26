package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
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

// RunWithStreaming executes a conversational turn using Anthropic token streaming.
// ADDITIVE: RunWithProgress is untouched. Same helpers are reused.
func (r *Runner) RunWithStreaming(ctx context.Context, history []llm.Message, userMessage string, image *ImageInput, cb StreamingRunnerCallbacks) (string, error) {
	result, err := r.RunWithStreamingContext(ctx, history, llm.ProviderContext{}, userMessage, image, cb)
	return result.Text, err
}

// RunWithStreamingContext streams display events while retaining the complete
// typed Anthropic response for subsequent turns.
func (r *Runner) RunWithStreamingContext(
	ctx context.Context,
	history []llm.Message,
	providerContext llm.ProviderContext,
	userMessage string,
	image *ImageInput,
	cb StreamingRunnerCallbacks,
) (TurnResult, error) {
	if r.client == nil {
		return TurnResult{}, fmt.Errorf("anthropic client unavailable")
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

	messageParams, err := r.buildTurnMessages(history, providerContext, userMessage, image)
	if err != nil {
		return TurnResult{}, err
	}
	var lastAssistant string
	var usage TurnUsage
	exhausted := true

	for iter := 0; iter < r.maxIter; iter++ {
		if cb.OnProgress != nil {
			cb.OnProgress(ProgressEvent{Type: ProgressThinking, Message: "Thinking..."})
		}

		contextTools := r.betaToolDefsForContext(ctx)
		req := r.buildBetaRequest(contextTools, messageParams)

		stream := r.client.RawClient().Beta.Messages.NewStreaming(ctx, req)
		var response sdk.BetaMessage
		var textBuilder strings.Builder

		for stream.Next() {
			event := stream.Current()
			if err := response.Accumulate(event); err != nil {
				return TurnResult{}, fmt.Errorf("accumulate Anthropic stream: %w", err)
			}
			switch e := event.AsAny().(type) {
			case sdk.BetaRawContentBlockStartEvent:
				if e.ContentBlock.Type == "tool_use" {
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
				}
			}
		}
		if err := stream.Err(); err != nil {
			r.logger.Error(fmt.Sprintf("stream_failed session=%s iter=%d", sessionID, iter+1), err)
			return TurnResult{}, err
		}
		usage.add(response.Usage)

		tagParser.Flush()

		assistantText, toolBlocks := r.extractBetaContent(&response)
		if streamedText := strings.TrimSpace(textBuilder.String()); assistantText == "" {
			assistantText = streamedText
		}

		var compaction compactionState
		messageParams, compaction, err = appendBetaResponse(messageParams, &response)
		if err != nil {
			return TurnResult{}, err
		}
		switch compaction {
		case compactionValid:
			r.logger.Info(fmt.Sprintf("stream_compaction_checkpoint session=%s user=%s iter=%d", sessionID, userID, iter+1))
		case compactionNull:
			r.logger.Warn(fmt.Sprintf("stream_compaction_failed_noop session=%s user=%s iter=%d", sessionID, userID, iter+1))
		}
		r.logger.Info(fmt.Sprintf("stream_iteration session=%s iter=%d tools=%d text_len=%d", sessionID, iter+1, len(toolBlocks), len(assistantText)))

		if len(toolBlocks) == 0 {
			lastAssistant = assistantText
			exhausted = false
			break
		}

		toolResults, err := r.invokeBetaToolsWithProgress(ctx, toolBlocks, cb.OnProgress)
		if err != nil {
			return TurnResult{}, err
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
	encodedContext, err := encodeProviderContext(messageParams)
	if err != nil {
		return TurnResult{}, err
	}
	return TurnResult{
		Text:            strings.TrimSpace(lastAssistant),
		ProviderContext: encodedContext,
		Usage:           usage,
	}, nil
}
