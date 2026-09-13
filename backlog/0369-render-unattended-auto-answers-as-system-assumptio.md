---
id: "0369"
title: Render unattended auto-answers as system assumptions in iOS
status: blocked
priority: 2
created: "2026-09-08"
updated: "2026-09-10"
depends_on: []
spec_refs:
    - §11 Questions, unattended work, and confirmation
    - §18.3 Structured questions
---

## Description
The daemon stamps auto:true on unattended question/answer events, but iOS ignores it: an automatic question can appear as a pending human gate and its canned answer can be attributed to the user. Evidence: internal/session/interaction.go auto-answer emission; clients/ios/YccKit/Sources/YccKit/SessionProjection.swift applyQuestionAsked/foldAnswer. TUI/web already distinguish automatic handling.

## Acceptance criteria
- Auto-answered questions never open an interactive answer sheet or imply a required user action.
- Transcript presentation clearly labels unattended/system assumption and preserves the original question and automatic response as evidence.
- Real human answers remain distinguishable, including batched questions and answers from another client.
- Tests cover live auto question+answer arrival, transcript catch-up, duplicate replay, mixed human/automatic exchanges, and legacy events without the flag.
- Preserve question coalescing and authoritative durable-answer behavior.

## Work log

- 2026-09-10: Selected as the highest-priority ready todo task. Preflight found unrelated staged changes already in `clients/ios/YccKit/Sources/YccKit/SessionProjection.swift`, `clients/ios/App/SessionView.swift`, and `clients/ios/YccKit/Tests/YccKitTests/SessionProjectionTests.swift`; inspected diffs contain coordinator-rollover behavior and composer Return changes, not this task's automatic-answer fix. No implementation was attempted and existing code/index state was preserved.
- Safe review/finalization requires a clean isolated task-owned worktree/session: the current `internal/git/changeset.go:263–306` rejects modifications to any path dirty at the captured baseline, even disjoint hunks. Task 0366's latest work log independently records that exact review refusal. Available delegation/review tools cannot select a different workspace or reset the session baseline. Unblock by launching this task in an isolated clean worktree, or by resolving the existing owners' changes before starting a new session; do not discard or absorb their work. Then implement the original acceptance criteria, with focused projection replay tests. Swift is not installed on this host, so Mac execution remains necessary and must not be claimed here.
