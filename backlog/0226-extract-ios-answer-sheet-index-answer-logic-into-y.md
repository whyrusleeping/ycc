---
id: "0226"
title: Extract iOS answer-sheet index/answer logic into YccKit for test coverage
status: done
priority: 3
created: "2026-07-30"
updated: "2026-11-22"
depends_on: []
spec_refs: []
---

## Description

Current state: todo, not started, nothing blocking. Verification runs on the configured Mac (see the 2026-11-10 work-log entry).

The `QuestionSheet` batch/single answer logic lives entirely in the App layer
(`clients/ios/App/SessionView.swift`) — per-question `texts`/`selected` state,
`allAnswered`, and `submitBatch()` positional-answer assembly. App-layer SwiftUI
views have no unit tests (only YccKit + a keychain app test), so a crash bug shipped
here: `@State` arrays seeded from `pending.questions.count` in `init` are not
re-seeded when the pending gate changes shape under an open sheet, and
`ForEach(pending.questions)` then trapped on an out-of-range `texts[index]` when
reopening a session that had awaited an `ask_user`. Fixed by `.id(pending.rowID)`
plus bounds-safe accessors, but with no regression test.

Extract the pure answer-collection logic (array sizing keyed to a gate id,
per-question selection/text, `allAnswered`, positional batch assembly) into a small
`Sendable` model in YccKit so it can be unit-tested headlessly, mirroring
`SessionProjection`. The SwiftUI view becomes a thin renderer over it.

## Acceptance criteria

- [ ] Answer-collection state + `allAnswered` + batch assembly live in a YccKit type
- [ ] Unit tests cover: single vs batch, option-vs-text precedence, and a gate
      changing shape (fewer→more questions) without any out-of-range access
- [ ] `QuestionSheet` renders over the extracted model; behavior unchanged
- [ ] `swift test` in YccKit passes

## Outcome

QuestionAnswers (YccKit) holds the per-question option/text drafts keyed to the gate rowID. It handles sync/resize, bounds-safe access, override rules, nextUnanswered, batch assembly, and isComplete(for:), which guards batch Submit against a gate that has grown. QuestionSheet now renders over it. Six unit tests cover single and batch questions, override precedence, and gate growth/shrink. YccKit swift test passed (665 tests) and the app built on the Mac. Live sim checks of the batch and single paths passed.

Commit: iOS: extract answer-sheet drafts into YccKit QuestionAnswers with tests
