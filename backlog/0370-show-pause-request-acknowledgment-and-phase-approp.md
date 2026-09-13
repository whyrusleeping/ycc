---
id: "0370"
title: Show pause-request acknowledgment and phase-appropriate session controls
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §5.3 Transient events
    - §18.7 Interrupt and steer
---

## Description
Interrupt sets pauseReq but emits no immediate event; interrupted arrives only at a later checkpoint. Clients can show no response for a long model/tool operation, and iOS offers controls without sufficient phase gating. Evidence: internal/session/session.go Interrupt and CheckpointMessages; clients/ios/App/SessionView.swift session controls.

## Acceptance criteria
- Immediately acknowledge an accepted pause request with a cross-client observable 'pausing at next checkpoint' state, distinct from actual paused state.
- Select transient versus durable representation deliberately and provide truthful reconnect/status reconciliation; a dropped hint cannot leave a permanent false phase.
- interrupted remains authoritative for actual pause, and resumed/stopped/failure clears pending presentation appropriately.
- TUI/web/iOS show phase-appropriate Interrupt/Resume controls; put common supervision actions in discoverable locations near live work/composer where appropriate.
- Preserve safe-checkpoint interruption and confirmed destructive Hard Stop; do not cancel mid-write to make pause look faster.
- Tests cover long turns/tools, duplicate pause requests, request then hard stop, reconnect, and another client's resume.

## Work log

- Preflight: blocked in this workspace by pre-existing changes in `internal/session/session.go`, `internal/tui/session.go`, `clients/ios/App/SessionView.swift`, and web projection code. Verified `internal/git/changeset.go` refuses edits to baseline-dirty paths; no implementation attempted. Requires an isolated clean task session or resolution of the existing owners' changes before a new baseline. Preserve the existing work.
