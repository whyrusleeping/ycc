---
id: "0055"
title: Record timing in model-output and tool-call logs
status: in_progress
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on: []
spec_refs: []
---

## Description
## Description
Add timing/duration information to each model output and tool-call log entry so we can see
which parts of a session are slow. Currently the logs capture the model outputs and tool
calls/responses but not how long each took, making it hard to identify performance bottlenecks
(e.g. slow model generations vs. slow tool executions).

Capture and record:
- Duration of each model generation/output (latency from request to completed response;
  optionally time-to-first-token if available).
- Duration of each tool call (from invocation to response).

The timing should be persisted in the log entries and surfaced wherever those logs are
viewed/exported.

## Acceptance criteria
- [ ] Each model-output log entry records its elapsed duration
- [ ] Each tool-call log entry records its elapsed duration
- [ ] Timing is persisted with the on-disk/session logs (not just in-memory)
- [ ] Timing is visible when inspecting the logs (TUI chat-log row and/or exported log)
- [ ] No meaningful overhead or regression to existing logging behavior

## Acceptance criteria

## Work log
- 2026-06-29 plan: Add elapsed-duration timing to model-output and tool-call log entries.  1. internal/engine/loop.go:    - Time the model generation: capture start before `client.Turn(opts)`, compute `time.Since(start)
…[truncated]
- 2026-06-29 implementer report: Added elapsed-duration timing to model-output and tool-call log entries (task 0055).  Changes: - internal/engine/loop.go: import `time`. In Run(), capture `start` before `client.Turn(opts)` and record
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change adds elapsed-duration timing (duration_ms) to model generation (model_turn), tool calls (tool_result), and session_error events in internal/engine/loop.go. Timing is measured tightly around
…[truncated]
