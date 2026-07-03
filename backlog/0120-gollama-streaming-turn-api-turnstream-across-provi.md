---
id: "0120"
title: 'gollama: streaming turn API (TurnStream) across providers'
status: blocked
priority: 3
created: "2026-07-02"
updated: "2026-07-03"
depends_on: []
spec_refs:
    - 7. Agent engine
    - 18.4 Reasoning (thinking) in the event stream
---

## Description
## Description
Prerequisite for ycc task 0114 (incremental model-output streaming in the session view).
Work happens in the separate gollama repo (/home/why/code/gollama); this task tracks it
from the ycc backlog, like earlier gollama thinking-levels work.

gollama's `Turn` is hard-wired non-streaming (`turn.go`: `opts.Stream = false`; no
SSE/delta handling in any provider path). Add a streaming variant — e.g.
`TurnStream(opts RequestOptions, onDelta func(Delta)) (*ResponseMessageGenerate, error)` —
that:
- streams partial assistant text (and ideally thinking deltas) via the callback/channel
  while the turn runs, then returns the same normalized final `ResponseMessageGenerate`
  as `Turn` (so callers can treat the final result identically);
- implements provider SSE/streaming for **Anthropic** first (the primary ycc backend),
  then OpenAI-compatible and Ollama as follow-ups;
- **falls back gracefully**: for providers without streaming support, performs a blocking
  turn and delivers the whole text as one delta — callers never need to branch;
- keeps tool-call turns correct (tool_use blocks assembled from the stream match the
  non-streaming shape; thinking block signatures round-trip intact).

## Acceptance criteria
- [ ] TurnStream exists with the fallback behavior; `Turn` semantics unchanged
- [ ] Anthropic SSE streaming implemented and covered by offline tests (recorded stream fixtures) + a live smoke test
- [ ] final message from a streamed turn is byte-equivalent (content/tool calls/thinking blocks) to the non-streaming shape for the same response
- [ ] ycc can adopt it by swapping the `engine.Turner` call site (no other API churn)


## Acceptance criteria

## Work log
- 2026-07-05 blocked (coordinator): the work lives in the separate gollama repo at
  /home/why/code/gollama, which does not exist in this environment (gollama is only
  present as a read-only module-cache dependency). Cloning/forking gollama into the ycc
  workspace would be a hard-to-reverse structural decision the task explicitly avoids.
  Unblock when the gollama working repo is available alongside ycc.
