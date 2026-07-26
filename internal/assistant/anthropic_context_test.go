package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/edin-space/edin-backend/internal/anthropic"
	"github.com/edin-space/edin-backend/internal/llm"
)

func TestBuildBetaRequestPinsCompactionContract(t *testing.T) {
	client, err := anthropic.New("test-key", "claude-sonnet-5", 16384)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	runner := NewRunner(client, nil, "system prompt", 5)

	req := runner.buildBetaRequest(nil, []sdk.BetaMessageParam{
		sdk.NewBetaUserMessage(sdk.NewBetaTextBlock("hello")),
	})
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if len(req.Betas) != 2 ||
		req.Betas[0] != betaCompact ||
		req.Betas[1] != betaContextManage {
		t.Fatalf("beta headers = %#v, want compaction and context management", req.Betas)
	}
	body := string(raw)
	for _, want := range []string{
		`"type":"compact_20260112"`,
		`"type":"clear_tool_uses_20250919"`,
		`"Do not call tools`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("request missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "pause_after_compaction") {
		t.Fatalf("normal chat must not pause after compaction:\n%s", body)
	}
}

func TestAppendBetaResponsePreservesAndPrunesAtValidCompaction(t *testing.T) {
	response := decodeBetaMessage(t, `{
		"id":"msg_compact",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-5",
		"content":[
			{"type":"compaction","content":"durable summary"},
			{"type":"text","text":"final answer"}
		],
		"stop_reason":"end_turn",
		"stop_sequence":null,
		"usage":{"input_tokens":10,"output_tokens":5}
	}`)
	prior := []sdk.BetaMessageParam{
		sdk.NewBetaUserMessage(sdk.NewBetaTextBlock("old question")),
		{
			Role:    sdk.BetaMessageParamRoleAssistant,
			Content: []sdk.BetaContentBlockParamUnion{sdk.NewBetaTextBlock("old answer")},
		},
	}

	got, state, err := appendBetaResponse(prior, response)
	if err != nil {
		t.Fatalf("append response: %v", err)
	}
	if state != compactionValid {
		t.Fatalf("state = %v, want valid compaction", state)
	}
	if len(got) != 1 {
		t.Fatalf("persisted messages = %d, want only compaction checkpoint", len(got))
	}

	context, err := encodeProviderContext(got)
	if err != nil {
		t.Fatalf("encode context: %v", err)
	}
	if !strings.Contains(string(context.Messages[0]), `"type":"compaction"`) ||
		!strings.Contains(string(context.Messages[0]), `"content":"durable summary"`) {
		t.Fatalf("typed compaction block was not preserved: %s", context.Messages[0])
	}

	runner := NewRunner(nil, nil, "", 5)
	next, err := runner.buildTurnMessages(nil, context, "next question", nil)
	if err != nil {
		t.Fatalf("decode next-turn context: %v", err)
	}
	if len(next) != 2 || next[0].Role != sdk.BetaMessageParamRoleAssistant ||
		next[1].Role != sdk.BetaMessageParamRoleUser {
		t.Fatalf("next-turn roles/context are invalid: %#v", next)
	}
}

func TestAppendBetaResponseTreatsNullCompactionAsNoOp(t *testing.T) {
	response := decodeBetaMessage(t, `{
		"id":"msg_null",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-5",
		"content":[{"type":"compaction","content":null}],
		"stop_reason":"end_turn",
		"stop_sequence":null,
		"usage":{"input_tokens":10,"output_tokens":1}
	}`)
	prior := []sdk.BetaMessageParam{
		sdk.NewBetaUserMessage(sdk.NewBetaTextBlock("keep me")),
	}

	got, state, err := appendBetaResponse(prior, response)
	if err != nil {
		t.Fatalf("append response: %v", err)
	}
	if state != compactionNull {
		t.Fatalf("state = %v, want null compaction", state)
	}
	if len(got) != 2 {
		t.Fatalf("null compaction pruned history: got %d messages", len(got))
	}
	context, err := encodeProviderContext(got)
	if err != nil {
		t.Fatalf("encode context: %v", err)
	}
	if !strings.Contains(string(context.Messages[1]), `"content":null`) {
		t.Fatalf("null compaction did not round-trip as null: %s", context.Messages[1])
	}
}

func TestStreamingAccumulatorPreservesCompactionBlock(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","model":"claude-sonnet-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"compaction","content":null}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"streamed summary"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5,"iterations":[{"type":"compaction","input_tokens":10,"output_tokens":2},{"type":"message","input_tokens":3,"output_tokens":5}]}}`,
		`{"type":"message_stop"}`,
	}
	var response sdk.BetaMessage
	for _, raw := range events {
		var event sdk.BetaRawMessageStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatalf("decode stream event: %v", err)
		}
		if err := response.Accumulate(event); err != nil {
			t.Fatalf("accumulate stream event %s: %v", raw, err)
		}
	}

	got, state, err := appendBetaResponse(
		[]sdk.BetaMessageParam{sdk.NewBetaUserMessage(sdk.NewBetaTextBlock("question"))},
		&response,
	)
	if err != nil {
		t.Fatalf("append streamed response: %v", err)
	}
	if state != compactionValid || len(got) != 1 {
		raw, _ := json.Marshal(response)
		t.Fatalf("streamed compaction state/messages = %v/%d response=%s block=%s",
			state, len(got), raw, response.Content[0].RawJSON())
	}
	context, err := encodeProviderContext(got)
	if err != nil {
		t.Fatalf("encode streamed context: %v", err)
	}
	wire := string(context.Messages[0])
	if !strings.Contains(wire, `"type":"compaction"`) ||
		!strings.Contains(wire, `"content":"streamed summary"`) ||
		!strings.Contains(wire, `"text":"answer"`) {
		t.Fatalf("streamed response lost typed content: %s", wire)
	}
}

func TestTurnUsageSumsIterationsInsteadOfTopLevel(t *testing.T) {
	var usage sdk.BetaUsage
	if err := json.Unmarshal([]byte(`{
		"input_tokens":23,
		"output_tokens":7,
		"iterations":[
			{"type":"compaction","input_tokens":180,"output_tokens":35},
			{"type":"message","input_tokens":23,"output_tokens":7}
		]
	}`), &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	var got TurnUsage
	got.add(usage)
	if got.InputTokens != 203 || got.OutputTokens != 42 {
		t.Fatalf("usage totals = %d/%d, want 203/42", got.InputTokens, got.OutputTokens)
	}
	if got.CompactionIterations != 1 || got.MessageIterations != 1 {
		t.Fatalf("iteration counts = compaction:%d message:%d", got.CompactionIterations, got.MessageIterations)
	}
}

func decodeBetaMessage(t *testing.T, raw string) *sdk.BetaMessage {
	t.Helper()
	var message sdk.BetaMessage
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		t.Fatalf("decode beta message: %v", err)
	}
	return &message
}

func TestBuildTurnMessagesSeedsLegacyDisplayHistoryOnce(t *testing.T) {
	runner := NewRunner(nil, nil, "", 5)
	history := []llm.Message{
		{Role: "user", Content: "legacy question"},
		{Role: "assistant", Content: "legacy answer"},
	}
	got, err := runner.buildTurnMessages(history, llm.ProviderContext{}, "new question", nil)
	if err != nil {
		t.Fatalf("build messages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("messages = %d, want legacy pair plus current user", len(got))
	}
}
