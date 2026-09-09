---
id: "0367"
title: Preserve iOS paused state and show queued versus delivered steering
status: done
priority: 1
created: "2026-09-08"
updated: "2026-09-09"
depends_on: []
spec_refs:
    - §18.1 Session input
    - §18.7 Interrupt and steer
---

## Description
SessionProjection.foldPhase treats user_input and generic activity as running. A queued steer while paused can hide the Resume banner even though execution has not resumed. The iOS projection also ignores user_input.queued and user_input_delivered, presenting accepted input as already delivered. Evidence: clients/ios/YccKit/Sources/YccKit/SessionProjection.swift:257–266,537–540,777–778; SessionView paused banner; daemon input/checkpoint events.

## Acceptance criteria
- Queued input does not clear paused state; only authoritative resume/lifecycle evidence changes it, with careful handling of concurrent subagent activity.
- Transcript input rows show accepted/queued versus delivered state and resolve by the daemon's referenced input sequence.
- Resume remains available after steering while paused; delivery state is consistent after replay and another client's actions.
- Tests cover interrupt → paused → steer echo → resume → delivery, mid-run queued input, multiple queued messages, subagent events during pause, and duplicate replay.
- Add YccKit projection/action regressions; on-device UI verification remains explicit and requires the user's Mac.

## Outcome
- Paused phase now survives queued steering and concurrent subagent activity until authoritative lifecycle evidence. Informational reopen events do not resume execution.
- Input bubbles show Queued/Delivered; delivery resolves the referenced durable input sequence, including replay and other-client actions.
- Added focused YccKit projection and action regressions; independent review accepted.
- Verification: `git diff --check` and focused daemon steer/checkpoint tests passed. No Swift/Xcode toolchain here: run YccKit `swift test`, the iOS build, and on-device pause → steer → Resume → delivery verification on the user's Mac.
- Commit subject: `fix(ios): preserve paused state and track steering delivery`
