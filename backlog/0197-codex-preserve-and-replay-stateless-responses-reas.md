---
id: "0197"
title: 'Codex: preserve and replay stateless Responses reasoning items'
status: done
priority: 1
created: "2026-07-15"
updated: "2026-08-07"
depends_on: []
spec_refs:
    - Agent engine
    - Backends & model registry
    - Client UI (TUI)#Session history browser & reopen
---

## Description
The ChatGPT/Codex transport uses the Responses API with `store:false`, but currently reduces output to visible assistant messages and function calls. It drops opaque reasoning response items (including encrypted content) and therefore cannot replay the complete provider output between tool calls or after session reopen. OpenAI's stateless Responses contract requires preserving all relevant output items, especially reasoning items around function calls.

Design and implement a lossless provider-state path through `internal/codex`, engine history, durable `model_turn` events, and session replay. Avoid a Codex-only in-memory workaround that would make live behavior differ from reopen behavior.

## Acceptance criteria
- [ ] Codex parses and retains opaque reasoning response items returned by the Responses stream, including encrypted content and any identifiers/fields required for replay.
- [ ] Subsequent stateless requests replay all relevant response items in the original order together with function calls and function outputs.
- [ ] The event log durably records enough provider state for `ResumeSession` to reconstruct equivalent Codex history.
- [ ] Provider-private opaque state is not rendered as user-visible chain-of-thought or leaked into ordinary transcript text.
- [ ] Tests cover at least two consecutive function/tool-call turns and prove that reasoning items survive both live continuation and event-log reopen.
- [ ] Existing Anthropic thinking-block replay and non-Codex backends remain unchanged.
- [ ] `go test ./...` passes.

## Plan

## Goal

Make the Codex (`store:false` Responses) transport lossless: capture the provider's reasoning
response items (with `encrypted_content` and ids), carry them through engine history, persist them
on the `model_turn` event, and replay them — in original item order, together with function calls
and function outputs — both live and after `ResumeSession`/reopen.

Verified provider contract (openai/codex `codex-rs`, OpenAI Responses schema):
- request must carry `include: ["reasoning.encrypted_content"]` (codex-rs `client.rs`: `let include = vec!["reasoning.encrypted_content"]` with `store:false`).
- a reasoning input item is `{"type":"reasoning","id":"rs_…","summary":[{"type":"summary_text","text":…}],"encrypted_content":"…"}`; `summary` is REQUIRED (may be `[]`), `id` should be echoed.
- a reasoning item must be followed by "its required following item" (the message / function_call it produced) — a dangling reasoning item is a 400.

## Design

**1. Item capture (internal/codex).** In `parseStream`, on `response.output_item.done` record the
turn's output items in stream order into a slice of a new type (new file `internal/codex/items.go`):

```go
type ResponseItem struct {
    Type             string        `json:"type"`              // reasoning | message | function_call
    ID               string        `json:"id,omitempty"`      // provider item id (rs_/msg_/fc_)
    EncryptedContent string        `json:"encrypted_content,omitempty"`
    Summary          []SummaryPart `json:"summary,omitempty"` // {type:"summary_text", text}
    CallID           string        `json:"call_id,omitempty"` // function_call
}
```
Extend `sseEvent.Item` with `encrypted_content`. Record reasoning items fully; record
`message`/`function_call` items only for their `id` + `call_id` (their text/args stay owned by the
engine's canonical history — see replay rules below). Keep the existing summary-text/thinking
handling exactly as-is: `msg.Thinking` remains the human-visible summary; the item payload is
independent of it.

**2. Carrier through the engine (no new gollama field available).** `gollama.Message.ThinkingBlocks`
is the existing "opaque provider state replayed verbatim" channel and is already persisted on
`model_turn` as `thinking_blocks` and rebuilt by `ReplayHistory` — reuse it, explicitly and
marked, rather than inventing a Codex-only side channel. Emit ONE block per codex assistant turn:
`gollama.ThinkingBlock{Redacted: "codex-response-items-v1:" + <compact JSON>}` where the JSON is
`{"model":"<request model id>","items":[…]}` (model recorded because encrypted content is
model/org-bound). Thinking/Signature stay empty on that block. Exported helpers in
`internal/codex/items.go`: `EncodeItemsBlock`, `DecodeItemsBlock`, `IsItemsBlock`. Document clearly
why the block rides in ThinkingBlocks.

This gives durable persistence + reopen for free: `loop.go` already writes
`toEventThinking(msg.ThinkingBlocks)` to `model_turn.thinking_blocks`, and `replay.go`
`parseThinkingBlocks` rebuilds them (map shape from JSONL included). `sanitizeThinkingBlocks` keeps
blocks with non-empty `Redacted`, so no engine change is needed for survival. Verify replay's
truncated-turn path (drops blocks) still behaves — that is correct here too.

**3. Replay into the request (`buildInput`).** For an assistant message, decode its items block
(ignore unmarked/foreign blocks, and drop items whose recorded `model` != the current request
model — stale encrypted content from before a model switch). Then emit, in recorded order:
- `reasoning` → verbatim (`type,id,encrypted_content,summary`); `summary` must marshal as `[]`
  when empty (required field), and the block must never leak into transcript text.
- `message` → assistant message item using the ENGINE's `m.Content` (canonical) plus the recorded
  provider `id`; skip when content is empty.
- `function_call` → consume the next unconsumed `m.ToolCalls[i]` and emit call_id/name/arguments
  from the ENGINE's tool call (canonical after id canonicalization in replay and arg repair) with
  the recorded provider `id`; skip the item if no tool call remains.
Afterwards append anything the recorded items did not cover: a message item if `m.Content` is
non-empty and unconsumed, and synthesized `function_call` items for remaining tool calls (this is
today's behavior, preserved for turns with no items — legacy logs, other backends).
SAFETY RULE: if a turn's reasoning items would end up with no following message/function_call item
in the same turn, drop the reasoning items (avoids the documented "provided without its required
following item" 400). Tool results (role "tool") keep becoming `function_call_output` keyed by the
same canonical call_id, unchanged.

**4. Request change.** Add `Include []string \`json:"include,omitempty"\`` to `request` and always
set `["reasoning.encrypted_content"]` in `buildRequest`.

**5. Cross-backend safety (small, defensive).** A codex items block sent to Anthropic would become
an invalid `redacted_thinking` block (400). In `engine.Loop.Run`, when building `opts.Messages`,
strip codex items blocks when the current backend is not `openai` (allocate a copy only when such a
block is actually present; leave history itself untouched). `internal/engine` may import
`internal/codex` for `codex.IsItemsBlock` (no cycle: codex imports only gollama). Anthropic
thinking blocks continue to flow untouched, and codex ignores them (as today).

## Tests (all in-repo, no network)

- `internal/codex`: stream fixture with `reasoning` (id + encrypted_content + summary) →
  `function_call` → assert the returned message carries exactly one marked items block, that
  `msg.Thinking` is unchanged (summary text only) and contains no encrypted content.
- `internal/codex`: TWO consecutive tool-call turns fed back as history (assistant+items+toolcall,
  tool result, assistant+items+toolcall, tool result, …) → marshal `buildRequest` and assert the
  `input` array order is exactly: user, reasoning#1, function_call#1, function_call_output#1,
  reasoning#2, function_call#2, function_call_output#2 …, with encrypted content and provider ids
  present, `summary` present, and `include` set on the request.
- `internal/codex`: items whose recorded model differs from the request model are dropped; a
  reasoning item with no following item is dropped; unmarked (Anthropic) thinking blocks are ignored.
- Reopen path: build an event log (model_turn with `thinking_blocks` carrying the marked payload as
  it lands in JSONL — go through the same map-decoding path `parseThinkingBlocks` handles) →
  `engine.ReplayHistory` → feed the reconstructed history into `codex.buildRequest` → assert the
  input array is byte-identical to the live-continuation case (proves reopen == live).
- `internal/engine`: codex items blocks are stripped for a non-openai backend and preserved for
  openai; existing Anthropic thinking-block replay tests must keep passing unchanged.
- `go test ./...` must pass.

## Docs

Update `spec.md` §13 (openai/ChatGPT bullet) and the reasoning paragraph in §7.4 with 2–4 sentences:
codex requests include `reasoning.encrypted_content`; the turn's reasoning response items are
preserved verbatim as an opaque provider-state block recorded on `model_turn.thinking_blocks`, and
replayed in original order ahead of the turn's message/function_call items so stateless
continuation and reopen are identical; the opaque state is never rendered as transcript text.

## Work log
- 2026-08-07 plan: ## Goal  Make the Codex (`store:false` Responses) transport lossless: capture the provider's reasoning response items (with `encrypted_content` and ids), carry them through engine history, persist the
…[truncated]
- 2026-08-07 implementer report: Implemented Task 0197 end to end.  Changes: - Added `internal/codex/items.go` with marked `codex-response-items-v1:` opaque provider-state blocks, response/summary item types, and encode/decode/detect
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — Task 0197 is implemented end to end. Codex now requests and captures encrypted reasoning items, carries them in a marked opaque ThinkingBlock, durably persists and reconstructs them through model_turn
…[truncated]
- 2026-08-07 revision: Addressed the reviewer’s cleanup request.  Changes: - Moved tool-argument repair to immediately after thinking-block sanitization, before any `model_turn` emission or assistant-history append. - All
…[truncated]
- 2026-08-07 review (sol): accept — The revision remains correct and improves consistency by canonicalizing repaired tool-call arguments before the assistant turn enters history, while preserving the per-call `repaired` forensic metadat
…[truncated]
- 2026-08-07 decision: accept — commit: codex: preserve and replay stateless Responses reasoning items (task 0197)  Codex requests now send include=["reasoning.encrypted_content"], and each turn's reasoning output items (id, encrypted conte
…[truncated]
