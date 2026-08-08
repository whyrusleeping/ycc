---
id: "0305"
title: 'iOS: render work-loop waiting state (resume time + wait kind)'
status: todo
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on:
    - "0295"
spec_refs: []
---

## Description
Task 0295 taught the daemon work loop to enter a live `waiting` state when a loop session dies on a retryable provider failure (subscription/usage limit, overload, network), with escalating retries up to 8h patience and auto-resume. `WorkLoopInfo` gained `resume_at` (RFC3339) and `wait_kind`; the state string can now be `waiting`. Swift protos are already regenerated.

The iOS work-loop screen should:
- Treat `waiting` as a live state (same as running/stopping) for polling/observation and the start/stop button state.
- Render the waiting status clearly: e.g. "Waiting for provider (rate_limit) — resumes 03:15" using `resume_at`/`wait_kind`.

Acceptance:
- YccKit work-loop model treats state `waiting` as live, exposes resume time + kind; headless unit tests cover it.
- Work-loop view renders the waiting row; stop works during waiting.
- Builds/tests on-device per iOS convention (ends in_review awaiting on-device use).

## Acceptance criteria

## Work log
