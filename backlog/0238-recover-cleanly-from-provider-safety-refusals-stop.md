---
id: "0238"
title: Recover cleanly from provider safety refusals (stop_reason "refusal")
status: done
priority: 2
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs: []
---

## Description
Anthropic streaming-classifier refusals (`stop_reason: "refusal"`) are sticky: per Anthropic docs, continuing the conversation without resetting context results in continued refusals, and retrying the same model usually refuses again (recommended recovery: switch model). Today ycc appends the synthesized "(the model declined…)" turn to history and lets the user keep sending messages, so the session degrades into an endless string of empty turns (seen live: oldgrowth s_4ec404b0ee028c8d).

Acceptance criteria:
- Engine: a no-tool-call turn with stop_reason "refusal" returns `Result.Refused`; the refused turn is kept OUT of live history (still recorded as a model_turn event for the transcript) so the pending user/tool turn can be cleanly re-run.
- Replay mirrors this: `ReplayHistory` skips refusal turns, so reopening a refused session re-runs the pending turn instead of replaying the poisoned placeholder.
- Session: a refusal parks the session in error state with a `session_error` (kind "refusal") explaining the recovery; SendInput is rejected while refused; Resume retries the turn as-is; a coordinator model change via SetRoleConfig clears the gate and retries automatically.
- Tests at engine (loop + replay) and session levels.


## Acceptance criteria

## Work log
