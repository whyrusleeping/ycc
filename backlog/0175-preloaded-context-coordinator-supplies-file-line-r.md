---
id: "0175"
title: 'Preloaded context: coordinator supplies file+line-range tuples injected as synthetic Read tool calls in the implementer''s seed history'
status: todo
priority: 3
created: "2026-07-07"
updated: "2026-07-08"
depends_on: []
spec_refs: []
---

## Description
## Idea

Today `context_hints` are advisory strings; the implementer still spends turns (and thinking tokens) making its own Read calls. Extend the mechanism so the coordinator can pass structured preload tuples — `{path, offset?, limit?}` — and the orchestrator constructs the implementer's initial history with a **synthetic tool exchange** that makes it look like the agent already read those files:

```
user:      "Orient yourself: read these files first." (synthetic nudge)
assistant: [Read tool_calls for each tuple]           (synthetic)
tool:      [REAL Read tool outputs, executed at spawn time]
user:      <implementerPrompt seed>                   (last message)
```

Savings: N API round trips and the per-turn output/thinking tokens the model would spend deciding to read (input tokens are paid either way; caching applies in both cases).

## Design points

- **Execute the real Read tool handler at spawn time** to generate result text — byte-identical formatting, read-policy enforcement, honest error results for stale hints.
- **Ordering** puts the real seed prompt last so the synthetic assistant tool_use turn is not in Anthropic's constrained final position (extended thinking requires a signed thinking block on the final assistant tool_use turn; we cannot forge signatures). Verify empirically against both Anthropic and OpenAI backends.
- **Event-log fidelity**: record the synthetic exchange in events.jsonl (tool_call/tool_result + a minimal model_turn, flagged `synthetic:true` or similar) so ReplayHistory reconstructs the same history on session reopen. TUI may render them normally or badge them as preloaded.
- **Bounds**: cap total preloaded content (~64 KB suggested) and per-tuple line limits; refuse or truncate beyond it with a visible marker.
- Keep string `context_hints` working alongside (symbols/snippets still have value); tuples are a new optional param (e.g. `preload_files`) on `spawn_implementer` (and possibly `propose_plan` for the plan artifact).
- Consider the same mechanism for reviewers later (out of scope initially).

## Acceptance criteria

- `spawn_implementer` accepts structured preload tuples; the implementer's first real API request contains the synthetic exchange with genuine Read outputs, seed prompt last.
- Works (empirically verified) with extended thinking enabled on Anthropic and with the OpenAI backend.
- Session reopen replays a byte-identical history including the synthetic exchange.
- Total preload size is bounded; stale paths produce error tool-results rather than spawn failure.
- Unit tests cover history construction, bounding, and replay round-trip.

## Work log
