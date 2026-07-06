---
id: "0120"
title: 'gollama: streaming turn API (TurnStream) across providers'
status: todo
priority: 3
created: "2026-07-02"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 7. Agent engine
    - 18.4 Reasoning (thinking) in the event stream
---

## Description
Delivers incremental model-output streaming in the session view end to end. Originally
the gollama-only prerequisite for ycc task 0114; per pm grooming 2026-07-08, 0114's small
remainder (the ycc adapter + live verification) was **folded into this task** and 0114
closed as merged. gollama work happens in the separate repo (/home/why/code/gollama);
this task tracks it from the ycc backlog, like earlier gollama thinking-levels work.

ycc-side groundwork is already done (tasks 0128/0129): the transient never-persisted
`turn_delta` broadcast path, the engine `StreamTurner` seam (throttled, snapshot
semantics), and the TUI live tail row. The engine loop type-asserts `StreamTurner`, so
once the gollama client implements TurnStream, adoption is a small adapter.

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
- [ ] (folded from 0114) ycc adapter: gollama client implements the engine `StreamTurner` seam, wiring TurnStream deltas to the existing transient `turn_delta` path
- [ ] (folded from 0114) live end-to-end verification: model text streams incrementally in the TUI session view; final persisted `model_turn` unchanged; events.jsonl/replay contain no deltas

## Work log
- 2026-07-08 pm grooming (with user): unblocked — the gollama working repo now exists at
  /home/why/code/gollama (HEAD d8e738f, still no TurnStream). Scope kept Anthropic-first.
  Folded in the remainder of 0114 (ycc StreamTurner adapter + live end-to-end
  verification) so one cross-repo session lands streaming completely; 0114 closed as
  merged into this task.
- 2026-07-05 blocked (coordinator): the work lives in the separate gollama repo at
  /home/why/code/gollama, which does not exist in this environment (gollama is only
  present as a read-only module-cache dependency). Cloning/forking gollama into the ycc
  workspace would be a hard-to-reverse structural decision the task explicitly avoids.
  Unblock when the gollama working repo is available alongside ycc.
