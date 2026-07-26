package assistant

import (
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/edin-space/edin-backend/internal/llm"
)

const (
	anthropicProvider       = "anthropic"
	anthropicContextVersion = 1
)

// TurnResult contains display text plus the exact provider context required for
// the next turn. ProviderContext is stored server-side and never sent to clients.
type TurnResult struct {
	Text            string
	ProviderContext llm.ProviderContext
	Usage           TurnUsage
}

// TurnUsage includes every sampling iteration. Anthropic's top-level token
// counts omit internal compaction work when iteration usage is present.
type TurnUsage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	MessageIterations        int
	CompactionIterations     int
}

func (u *TurnUsage) add(usage sdk.BetaUsage) {
	if len(usage.Iterations) == 0 {
		u.InputTokens += usage.InputTokens
		u.OutputTokens += usage.OutputTokens
		u.CacheCreationInputTokens += usage.CacheCreationInputTokens
		u.CacheReadInputTokens += usage.CacheReadInputTokens
		return
	}
	for _, iteration := range usage.Iterations {
		u.InputTokens += iteration.InputTokens
		u.OutputTokens += iteration.OutputTokens
		u.CacheCreationInputTokens += iteration.CacheCreationInputTokens
		u.CacheReadInputTokens += iteration.CacheReadInputTokens
		switch iteration.Type {
		case "compaction":
			u.CompactionIterations++
		default:
			u.MessageIterations++
		}
	}
}

func (r *Runner) buildBetaRequest(ctxTools []sdk.BetaToolUnionParam, messages []sdk.BetaMessageParam) sdk.BetaMessageNewParams {
	req := sdk.BetaMessageNewParams{
		Model:     r.client.Model(),
		MaxTokens: r.client.MaxTokens(),
		Messages:  messages,
		Tools:     ctxTools,
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
	return req
}

func (r *Runner) buildTurnMessages(
	history []llm.Message,
	providerContext llm.ProviderContext,
	userMessage string,
	image *ImageInput,
) ([]sdk.BetaMessageParam, error) {
	var messages []sdk.BetaMessageParam
	if providerContext.Provider == anthropicProvider &&
		providerContext.Version == anthropicContextVersion &&
		len(providerContext.Messages) > 0 {
		messages = make([]sdk.BetaMessageParam, 0, len(providerContext.Messages)+1)
		for i, raw := range providerContext.Messages {
			var message sdk.BetaMessageParam
			if err := json.Unmarshal(raw, &message); err != nil {
				return nil, fmt.Errorf("decode Anthropic context message %d: %w", i, err)
			}
			messages = append(messages, message)
		}
	} else {
		messages = r.buildBetaMessageParams(history, "", nil)
		if len(messages) > 0 {
			messages = messages[:len(messages)-1]
		}
	}
	return append(messages, r.buildCurrentUserMessage(userMessage, image)), nil
}

func (r *Runner) buildCurrentUserMessage(userMessage string, image *ImageInput) sdk.BetaMessageParam {
	if image == nil || image.Base64 == "" {
		return sdk.NewBetaUserMessage(sdk.NewBetaTextBlock(userMessage))
	}
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
	return sdk.NewBetaUserMessage(blocks...)
}

func appendBetaResponse(messages []sdk.BetaMessageParam, response *sdk.BetaMessage) ([]sdk.BetaMessageParam, compactionState, error) {
	if response == nil {
		return messages, compactionNone, nil
	}
	state := betaCompactionState(response)
	responseParam := response.ToParam()
	for i, block := range response.Content {
		if block.Type != "compaction" {
			continue
		}
		content, valid := betaCompactionContent(block)
		compaction := sdk.BetaCompactionBlockParam{}
		if valid {
			compaction.Content = param.NewOpt(content)
		} else {
			compaction.Content = param.Null[string]()
		}
		responseParam.Content[i] = sdk.BetaContentBlockParamUnion{OfCompaction: &compaction}
	}
	messages = append(messages, responseParam)
	if state == compactionValid {
		// Anthropic ignores everything before the latest valid compaction block.
		// Dropping it here bounds persisted context without changing semantics.
		messages = append([]sdk.BetaMessageParam(nil), messages[len(messages)-1])
	}
	return messages, state, nil
}

type compactionState int

const (
	compactionNone compactionState = iota
	compactionValid
	compactionNull
)

func betaCompactionState(response *sdk.BetaMessage) compactionState {
	state := compactionNone
	for _, block := range response.Content {
		if block.Type != "compaction" {
			continue
		}
		if _, valid := betaCompactionContent(block); valid {
			return compactionValid
		}
		state = compactionNull
	}
	return state
}

func betaCompactionContent(block sdk.BetaContentBlockUnion) (string, bool) {
	raw := block.RawJSON()
	if raw != "" {
		var wire struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal([]byte(raw), &wire); err == nil && len(wire.Content) > 0 {
			var content string
			if err := json.Unmarshal(wire.Content, &content); err == nil && content != "" {
				return content, true
			}
		}
	}
	// The SDK's streaming accumulator stores compaction_delta content in this
	// union field; its synthesized RawJSON is not the original wire shape.
	if block.Content.OfString != "" {
		return block.Content.OfString, true
	}
	return "", false
}

func encodeProviderContext(messages []sdk.BetaMessageParam) (llm.ProviderContext, error) {
	context := llm.ProviderContext{
		Provider: anthropicProvider,
		Version:  anthropicContextVersion,
		Messages: make([]json.RawMessage, 0, len(messages)),
	}
	for i, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return llm.ProviderContext{}, fmt.Errorf("encode Anthropic context message %d: %w", i, err)
		}
		context.Messages = append(context.Messages, raw)
	}
	return context, nil
}
