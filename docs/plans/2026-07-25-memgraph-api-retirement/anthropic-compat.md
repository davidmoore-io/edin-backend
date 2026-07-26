# Anthropic Compatibility Evidence

Date: 2026-07-25

## Configuration

- current configured model: `claude-sonnet-5`
- original failure/probe model: `claude-opus-4-6`
- configured maximum output: 16,384 tokens
- Go SDK: `github.com/anthropics/anthropic-sdk-go v1.22.1`
- non-streaming path: `internal/assistant/runner.go`
- streaming path: `internal/assistant/runner_streaming.go`

No API key or conversation content is recorded in this file.

## Observed Application Failure

An authenticated local Kaine web-chat request returned HTTP 400:

```text
invalid_request_error:
context_management.edits.0: Input tag 'compact_20260112' found using 'type'
does not match any of the expected tags: 'clear_thinking_20251015',
'clear_tool_uses_20250919'
```

The user-facing error rendered an application request URL of
`http://127.0.0.1:8787/v1/messages?beta=true`. Inspection after restart
confirmed that quick-dev had inherited
`ANTHROPIC_BASE_URL=http://127.0.0.1:8787` from its parent environment. The
successful probes did not use that override and reached Anthropic directly.
Development configuration now explicitly pins
`ANTHROPIC_BASE_URL=https://api.anthropic.com`.

## Documentation Comparison

The supplied current documentation,
`supporting-docs/anthropic/compaction-api-readme.md`, states:

- `claude-opus-4-6` supports server-side compaction;
- compaction requires beta `compact-2026-01-12`;
- the edit type is `compact_20260112`;
- an input-token trigger must be at least 50,000;
- response content, including the typed compaction block, must be appended to
  subsequent requests;
- streaming emits compaction block events that clients must handle;
- usage for compaction is reported in `usage.iterations`.

The configured 100,000-token compaction trigger is valid.

## Redacted Live Matrix

Three minimal one-output-token requests were sent to the live API using the
then-configured model, `claude-opus-4-6`. Only status, returned model, and stop
reason were retained.

| Request shape | Result |
|---|---|
| compaction beta + compaction edit | accepted; model `claude-opus-4-6` |
| context-management beta + clear-tool edit | accepted; model `claude-opus-4-6` |
| both betas + both edits | accepted; model `claude-opus-4-6` |

The same compaction-only and combined requests also succeeded against the
SDK-style `/v1/messages?beta=true` URL. Repeated beta header fields in either
order also succeeded. Model support, combining the edits, repeated headers,
and the beta query parameter are therefore not independently sufficient to
reproduce the application failure.

## Sonnet 5 Model Switch

David selected `claude-sonnet-5` as the runtime model on 2026-07-25. The local
and development environment configuration and both code fallbacks now use that
model.

A minimal live request using the runner's combined beta/edit shape returned
HTTP 200 from `claude-sonnet-5`:

| Request shape | Result |
|---|---|
| both beta headers + both edits | accepted; model `claude-sonnet-5`; stop reason `max_tokens` |

After the quick-dev restart, host-side process inspection recorded:

- backend health: HTTP 200;
- effective model: `claude-sonnet-5`;
- effective base URL: `https://api.anthropic.com`;
- no Anthropic call failure in the new backend log.

This proves model-level acceptance only. The rebuilt application smoke gate
and typed compaction persistence work below remain required.

## Confirmed Code Gaps

1. The non-streaming runner converts a typed compaction block to ordinary text.
2. The streaming runner does not process compaction block events or stop
   reason.
3. Persisted `llm.Message` history stores only role and text, so it cannot
   round-trip a typed compaction block across user turns.
4. Usage accounting does not consume `usage.iterations`.
5. The current custom compaction instruction does not explicitly prohibit tool
   calls during summarization.

These are contract defects regardless of whether the original 400 was
transient. MR7A remains blocked until the rebuilt application passes the
request-recorder, typed persistence, streaming, and authenticated live gates.
