---
id: "0246"
title: 'iOS question sheet: auto-advance, bottom submit, and clear the answered state everywhere'
status: in_review
priority: 2
created: "2026-08-05"
updated: "2026-08-05"
depends_on: []
spec_refs: []
---

## Description

Field feedback on the iOS `ask_user` sheet:

1. Choosing an answer left the user to scroll to the next question themselves.
2. Submitting a batch was only possible from the navigation bar, far from where the thumb already is after answering the last question.
3. After submitting, the rest of the app kept prompting for an answer for a noticeable period.

Cause of (3) was staleness in two independent places:

- **The session view's own banner.** `pendingQuestion` only cleared when the `question_answered` event completed its round trip through the stream — so the "A question is waiting / Answer" banner kept nagging until then, and indefinitely if the stream happened to be mid-reconnect.
- **The inbox and drawer badges.** `SessionListModel` only learns `waitingInput` from a `ListSessionHistory` load, which happens on appear, on foreground, or on pull-to-refresh — none of which fire when returning from a session. The needs-answer section, the row bell, and the drawer badges all stayed lit.

## Acceptance criteria

- [x] Picking an option in a batch scrolls to the next *unanswered* question, or to the submit button after the last.
- [x] A batch sheet has a prominent submit button at the bottom of the form, in addition to the navigation-bar one.
- [x] Answering clears the session's pending-question gate as soon as the daemon accepts it, without waiting for the event.
- [x] A rejected answer leaves the gate open.
- [x] The inbox rows and drawer badges stop showing "needs answer" for a session answered from this client, without a refetch.
- [x] Returning to the inbox refreshes it, since the agent has usually moved on.
- [x] Covered by headless tests.
- [ ] Verified on device with a multi-question `ask_user` batch.

## Work log
- `QuestionSheet`: wrapped the form in a `ScrollViewReader`, gave each question and the submit section scroll anchors, and added `advance(from:proxy:)` which targets the next *unanswered* question (so revisiting an earlier answer doesn't bounce the user back down) or the submit anchor. Added a full-width tinted submit section with an "answer every question" footer; `allAnswered` now derives from a shared `isAnswered(_:)`.
- `SessionProjection.clearPendingQuestion()` + `SessionViewModel.clearAnsweredGate()`: the gate closes on a successful answer, while the durable event stays authoritative for folding the answer into the transcript row. Four tests, including the failure case that must keep the gate open.
- `SessionListModel.markAnswered(sessionID:)` clears a cached row's `waitingInput`; `activityByProject` became computed (from `allSessions`) rather than cached at load, so badges reflect local corrections immediately. Tests for the correction, the no-op cases, and empty projects still reporting zero activity.
- `AppModel.answeredQuestionSessions` is the seam between the session view and the separately-owned list model; `LandingView` drains it, and also refreshes whenever the navigation path returns to the root.
- Answer callbacks are now `@MainActor` function types, since they touch the app/session view models.
- NOT YET VERIFIED on device.
