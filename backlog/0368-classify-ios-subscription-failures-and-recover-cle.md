---
id: "0368"
title: Classify iOS subscription failures and recover cleanly after auth changes or restart
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §18.2 Settings
    - §18.4 Reasoning and streaming
    - §18.6 Session history and reopen
---

## Description
SessionViewModel.startLiveLoop retries every stream error forever. Unauthorized should route to authentication, and not-found after daemon restart should offer persisted-session recovery rather than endless reconnecting. Backoff also never resets after healthy activity. A cancelled transcript fetch can clear a replacement streamTask handle. Evidence: clients/ios/YccKit/Sources/YccKit/SessionViewModel.swift:183,189–245.

## Acceptance criteria
- Distinguish transient disconnects from unauthorized, not-found, cancellation, and terminal failures.
- Unauthorized clears authenticated state through the shared app path while preserving the saved endpoint/profile.
- Missing live sessions transition to a truthful persisted/reopenable state when history exists; do not silently restart execution.
- Reset reconnect backoff after a healthy connection/event and resume from the last durable seq without duplicate rows or stale tails.
- Task identity/generation protects replacement streams from stale cleanup; stop reliably cancels the active subscription.
- Stub-driven tests cover rotated credentials, daemon restart, repeated transient flaps, backoff reset, cancellation/reopen races, and missing persisted history.

## Outcome

Implemented classified transcript/subscription transport errors, shared authentication reset, persisted-history recovery without implicit execution, activity-reset reconnect backoff, and generation-guarded cancellation/cleanup. Terminal exits clear transient presentation. Added stub regressions for auth, restart/history, flaps, durable replay, backoff, cancellation/reopen races, and production Connect-code mapping.

Independent review accepted. `git diff --check` passes; Swift tests and the iOS build were not run because Swift/Xcode is unavailable in this environment. Unrelated working-tree changes preserved with a selective commit.

Commit subject: `fix(ios): classify subscription failures and recover persisted sessions`
