---
id: "0197"
title: 'Codex: preserve and replay stateless Responses reasoning items'
status: todo
priority: 1
created: "2026-07-15"
updated: "2026-07-15"
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

## Work log
