---
id: "0055"
title: Record timing in model-output and tool-call logs
status: todo
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
