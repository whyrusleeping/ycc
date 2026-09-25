---
id: "0370"
title: Show pause-request acknowledgment and phase-appropriate session controls
status: done
priority: 2
created: "2026-09-08"
updated: "2026-09-25"
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

## Outcome

Interrupt emits durable `pause_requested` immediately (duplicates/while-paused are no-ops); Resume before the checkpoint emits `pause_cancelled`; `interrupted` remains authoritative and checkpoint-only. Pending clears on interrupted/resumed/pause_cancelled/stop/reopen in sessionview (new SessionViewState.pause_requested, Go+Swift regen), TUI, web and iOS, each showing a distinct "pausing at next checkpoint" state and phase-gated Interrupt/Resume (Cancel pause) controls. Hard Stop unchanged. Tests: session lifecycle cases, sessionview reconciliation, web fold/replay, YccKit projection (60/60 in Docker). SwiftUI App not compiled here. Claude review accepted; sol reviewer unavailable (401).

Commit: session+clients: durably acknowledge pause requests (pause_requested/pause_cancelled) with phase-gated Interrupt/Resume across TUI, web, iOS (0370)
