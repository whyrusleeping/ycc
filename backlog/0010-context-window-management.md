---
id: "0010"
title: Context-window management for long sessions
status: done
priority: 3
created: "2026-06-26"
updated: "2026-06-29"
depends_on:
    - "0002"
spec_refs:
    - Agent engine
---

## Description
The agent loop keeps unbounded history (every assistant turn + every tool result, which
include file reads up to 128 KiB and bash output up to 64 KiB). A long work session or a
chatty implementer will eventually exceed the model's context window; today `Client.Turn`
then returns a provider error and the loop aborts mid-task with no graceful degradation.
Reused subagent loops (revise rounds) compound this. (Review 2026-06-26, MINOR #9.)

Note: per an earlier decision we did NOT add automatic compaction (the prior "prompt too
long" incident was a single oversized tool result — a binary matched by grep — now fixed
by Bash/rg + output caps, not accumulation). This task is to decide the approach for
genuine long-session growth. Discuss with the user before implementing.

## Acceptance criteria
- [ ] decide approach: graceful failure message vs. eliding oldest tool results vs.
      summarization (needs user input — they pushed back on compaction before)
- [ ] at minimum: detect a context-length error from the backend and fail with a clear,
      actionable message instead of an opaque provider error
- [ ] surface approximate context size somewhere (event/telemetry) so growth is visible

## Work log
- 2026-06-29 plan: User decided: minimum approach — detect context-length errors and fail with a clear, actionable message + emit context-size telemetry. No compaction/eliding/summarization.  1. New file internal/engi
…[truncated]
- 2026-06-29 implementer report: Implemented task 0010 (context-window management — minimum approach: detect & fail clearly + telemetry; no compaction/eliding/summarization, per the user's decision).  Changes: 1. New file internal/
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change implements the agreed minimum approach for task 0010 cleanly and completely. It adds IsContextLengthError (matching real Anthropic/OpenAI context-window signatures while excluding output-tr
…[truncated]
- 2026-06-29 decision: accept — commit: engine: detect context-window-exceeded errors and surface context-size telemetry [0010]  Add IsContextLengthError + approxContextTokens; the agent loop now fails with a clear, actionable message on co
…[truncated]
- 2026-06-29 usage: 16,659 tok (in 84, out 16,575, cache_r 777,646, cache_w 101,422) · cost n/a (unpriced)
