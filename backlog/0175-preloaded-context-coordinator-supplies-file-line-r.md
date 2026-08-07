---
id: "0175"
title: 'Preloaded context: coordinator supplies file+line-range tuples injected as synthetic Read tool calls in the implementer''s seed history'
status: done
priority: 3
created: "2026-07-07"
updated: "2026-08-07"
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

## Plan

Add a structured `preload_files` param to spawn_implementer: the coordinator supplies {path, offset?, limit?} tuples, and the orchestrator constructs the implementer's seed history with a synthetic tool exchange (real Read outputs executed at spawn time), seed prompt last.

## Design

**History shape** (implementer loop, installed via `loop.SetHistory` on the fresh loop, then `loop.Seed(implementerPrompt(...))` so the real seed prompt is the LAST message):
1. user: fixed nudge constant (e.g. "Orient yourself: read these coordinator-preselected files before starting.")
2. assistant: Content "" + one Read ToolCall per tuple, ids `preload_1..N`, args JSON `{"file_path":..., "offset":..., "limit":...}` (omit offset/limit when unset)
3. tool: one result per call — produced by dispatching the REAL Read tool through the implementer's registry (`reg.Dispatch` with a constructed gollama.ToolCall), so formatting/read-policy/error behavior are byte-identical. Stale paths yield honest error tool-results, never spawn failure.
4. user: implementerPrompt seed (via loop.Seed)

Seed-prompt-last means the synthetic assistant tool_use turn is never in Anthropic's constrained final position, so no signed thinking block is required. Codex buildInput handles this history generically (assistant with tool_calls but no recorded items → plain function_call items; tool role → function_call_output).

**Bounds** (new file internal/orchestrator/preload.go):
- maxPreloadFiles = 16 tuples; extra tuples are dropped and noted in the nudge/last result.
- maxPreloadBytes = 64*1024 total result content. Once the budget is exhausted, the current result is truncated with a visible marker ("…[preload truncated: total preload budget exceeded; Read the rest yourself]") and remaining tuples get a "(preload skipped: budget exhausted; Read this file yourself)" result instead of a real read.
- Media results (Images/Documents from Read of a png/pdf): strip the attachments, keep/replace text with a note telling the model to Read the file itself — synthetic seed messages must stay text-only for cross-backend validity.

**Event-log fidelity**: after bounding, emit with actor "implementer" (d.Emitter.With("implementer")): one minimal model_turn {text:"", tool_calls:N, model_name/backend/model_id, synthetic:true}, then per tuple tool_call {name:"Read", args, id, synthetic:true} and tool_result {name, result (the FINAL content that entered history), error, id, duration_ms, synthetic:true}. Do NOT emit user_input (ReplayHistory treats all user_input events as coordinator conversation regardless of actor — that would corrupt coordinator replay; the implementer's real seed prompt is likewise unlogged today). Coordinator ReplayHistory ignores actor-"implementer" events by its existing actor filter — add a regression test. (Implementer loops are not reconstructed on session reopen by existing design — Deps.impl starts nil — so event-log recording is the replay surface here; the transcript renders the synthetic exchange like normal implementer tool calls.)

**Orchestrator wiring** (internal/orchestrator/orchestrator.go, spawnImplementer):
- New schema param `preload_files`: array of objects {path:string (required), offset:int?, limit:int?}; parse via tools.GetMapSlice + a new exported tools.GetInt wrapper (getInt already exists unexported in internal/tools).
- Build the synthetic exchange after `loop := d.newLoop(...)` / before `loop.Seed`; works identically for foreground and background spawns (reads execute synchronously at spawn time in the tool call, before the fork).
- Work-log breadcrumb: "preloaded N file(s) (~X KB) into implementer context" (best-effort, like context hints).
- Keep string context_hints fully working alongside; preload is additive. Reviewers/propose_plan are out of scope.
- Tool description + the CONTEXT HINTS paragraph in coordinatorSystem (prompts.go) get a short mention of preload_files (structured tuples → files pre-read into the implementer's context; use for files the implementer will certainly need).

**Empirical verification**:
- Anthropic: live test (skipped without ANTHROPIC_API_KEY) that builds exactly this seed history (nudge, synthetic Read exchange with a real Read output, seed prompt last), Thinking "adaptive", registry including Read, runs one Loop turn against api.anthropic.com and asserts no API error (model pattern: internal/engine/stream_live_test.go). ANTHROPIC_API_KEY is set on this box — run it for real.
- OpenAI: an equivalent live test gated on OPENAI_API_KEY (skips here; no key), plus unit coverage that codex buildInput converts the synthetic history into a valid Responses item sequence (message/function_call/function_call_output in order, call_ids preserved).

**Unit tests** (internal/orchestrator/preload_test.go + small additions):
- history construction: exact message order/roles, ids, args JSON, genuine Read output content, seed prompt last (spawn integration via the existing `scripted` fake pattern asserting the first request's messages).
- bounding: total-budget truncation marker, skipped-tuple results, tuple-count cap.
- stale path → error tool result, spawn still succeeds.
- media read → attachments stripped with note.
- ReplayHistory ignores the synthetic implementer-actor events (coordinator history unchanged).
- tools.GetInt wrapper.

Run gofmt, go vet, `go test ./internal/orchestrator ./internal/tools ./internal/engine ./internal/codex`, plus the live Anthropic test.

### Starting points
- internal/orchestrator/orchestrator.go: spawnImplementer (~line 402), runImplementer, Deps
- internal/orchestrator/prompts.go: implementerPrompt, contextHintsBlock, coordinatorSystem CONTEXT HINTS paragraph
- internal/tools/tools.go: Registry.Dispatch, exported Get* helpers (add GetInt); worker.go readFile (Read tool, defaultReadLines=2000, maxReadBytes=128KB)
- internal/engine/loop.go: SetHistory/Seed/Post; event emission shapes for model_turn/tool_call/tool_result (Run, ~lines 867-994)
- internal/engine/replay.go: ReplayHistory filters ev.Actor != "coordinator" for model_turn/tool_call/tool_result but takes ALL user_input events — never emit user_input for the synthetic nudge
- internal/engine/stream_live_test.go: live-test pattern gated on ANTHROPIC_API_KEY (key IS set on this box)
- internal/codex/codex.go: buildInput/buildAssistantItems — synthetic assistant tool_calls fall through to plain function_call items (no recorded-items block needed)
- internal/orchestrator/hints_test.go + revise_test.go: `scripted` fake Turner pattern for spawn integration tests

## Work log
- 2026-08-07 plan: Add a structured `preload_files` param to spawn_implementer: the coordinator supplies {path, offset?, limit?} tuples, and the orchestrator constructs the implementer's seed history with a synthetic to
…[truncated]
- 2026-08-07 context hints: 8 recorded with plan
- 2026-08-07 context hints: internal/orchestrator/orchestrator.go: spawnImplementer (~line 402), runImplementer, Deps; internal/orchestrator/prompts.go: implementerPrompt, contextHintsBlock, coordinatorSystem CONTEXT HINTS parag
…[truncated]
- 2026-08-07 implementer report: Implemented task 0175.  Changes: - Added `spawn_implementer.preload_files` schema/parsing for `{path, offset?, limit?}` tuples while preserving `context_hints`. - Added `internal/orchestrator/preload.
…[truncated]
- 2026-08-07 review tier: single-opus — reviewers: sol
- 2026-08-07 review (sol): accept — The change correctly adds structured `preload_files` support, executes the real scoped Read handler before the implementer’s first turn, installs the synthetic user/assistant/tool exchange with the 
…[truncated]
- 2026-08-07 verification: live checks passed — Anthropic (claude-opus-4-8, adaptive thinking) via TestSyntheticPreloadLiveAnthropic, and OpenAI/codex (gpt-5.6-sol via ChatGPT subscription OAuth) via a one-off scratch run: both accepted the synthetic seed history and the codex model quoted the preloaded content back. OPENAI_API_KEY-gated live test remains for platform-API runs. Note: implementer loops are not reconstructed on session reopen (existing design), so replay fidelity is at the event-log level: synthetic events record the exact seeded content and coordinator ReplayHistory is provably unaffected.
- 2026-08-07 decision: accept — commit: spawn_implementer: preload_files seeds real Read outputs as a synthetic tool exchange in the implementer's initial history (task 0175)
- 2026-08-07 usage: 3,427,641 tok (in 698,588, out 62,045, cache_r 5,702,758, cache_w 338,606) · cost n/a (unpriced)
  implementer: 2,801,582 tok (in 497,282, out 23,340, cache_r 2,280,960, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 599,740 tok (in 201,238, out 12,454, cache_r 386,048, cache_w 0) · cost n/a (unpriced)
  coordinator: 26,319 tok (in 68, out 26,251, cache_r 3,035,750, cache_w 338,606) · cost n/a (unpriced)
