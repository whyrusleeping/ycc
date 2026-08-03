---
id: "0226"
title: Extract iOS answer-sheet index/answer logic into YccKit for test coverage
status: proposed
priority: 3
created: "2026-07-30"
updated: "2026-07-30"
depends_on: []
spec_refs: []
---

## Description
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

## Work log
