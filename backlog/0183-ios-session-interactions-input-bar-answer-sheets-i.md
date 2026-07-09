---
id: "0183"
title: 'iOS: session interactions — input bar, answer sheets, interrupt/resume/stop + smoke runbook'
status: done
priority: 2
created: "2026-07-08"
updated: "2026-07-08"
depends_on:
    - "0182"
spec_refs:
    - "18.3"
    - "18.7"
    - docs/design/ios-client.md#6. Screens & feature phases
---

## Description
Interactive controls on the session view per `docs/design/ios-client.md` §6 phase 1 step 4 — completes the "observe, answer, control" milestone.

## Description
- Sticky bottom input bar → `SendInput` (prod running/idle sessions; steer-by-default queues it).
- Question sheet on `question_asked`: suggested options as buttons + free-text field; single → `AnswerQuestion`, batched ask_user → `AnswerQuestions` (positional answers[i]); `optionIndex >= 0` selects an option, `-1` sends text; sheet dismissed by `question_answered` (incl. when answered from another client). Handle `failed_precondition` ("no pending question") gracefully.
- Toolbar/overflow actions: `Interrupt` (graceful pause-to-steer), `Resume`, `StopSession` (confirmation dialog — hard terminate).
- Reflect session state in chrome (paused banner after `interrupted`, idle/error banners).
- Add `plans/ios-client-smoke.md`: runbook mirroring plans/remote-access-smoke.md — daemon on a tailnet addr + token, connect, watch live, answer an ask_user, kill network mid-stream and verify replay-from-seq reconnect, interrupt/steer/resume, stop.

## Acceptance criteria
- Answering options and free text both work against a live daemon; batch questions answered positionally.
- Interrupt/Resume/Stop round-trip and the feed reflects the resulting events.
- Error toasts for not_found/failed_precondition; no crash on races (question answered elsewhere).
- Smoke runbook committed; YccKit interaction view-model logic under `swift test`.

## Plan

Add interactive controls to the iOS session view (docs/design/ios-client.md §6 phase 1 step 4): input bar, question sheets, interrupt/resume/stop, state banners, plus runbook extension. Keep all logic headless-testable in YccKit; App/ is a thin renderer.

1. YccKit — YccClient RPC wrappers (YccClient.swift):
   - `sendInput(sessionId:text:)` → SendInput.
   - `answerQuestion(sessionId:text:optionIndex:)` → AnswerQuestion (optionIndex >= 0 selects an option; -1 = free text).
   - `answerQuestions(sessionId:answers:[ (text, optionIndex) ])` → AnswerQuestions (positional answers[i]).
   - `interrupt(sessionId:)`, `resume(sessionId:)`, `stopSession(sessionId:)`.
   - Error mapping: extend `YccError` (YccError.swift) with distinguishable cases for `not_found` and `failed_precondition` (keep `.unauthorized` and `.rpc` behavior for everything else) so the UI can toast them specifically. Update `map(_:)` accordingly.

2. YccKit — SessionProjection (SessionProjection.swift):
   - Extend `PendingQuestion` to carry the FULL batch: a `questions: [Question]` array (`Question { prompt, options }`) parsed from both single-shaped (`question`/`options`) and batch-shaped (`questions: [{question, options}]`) `question_asked` payloads (see internal/session/interaction.go askData/askManyData for the daemon shapes). Keep the existing summary prompt/options for the transcript row; `isBatch` derivable from `questions.count > 1`. Keep `question_answered` clearing `pendingQuestion` (this is what dismisses the sheet, including when answered from another client).
   - Add a derived `phase` (e.g. enum: running/paused/idle/error/stopped) folded from lifecycle events: `interrupted` → paused, `resumed`/`user_input`/`model_turn` etc. → running, `session_idle` → idle, `session_error` → error(message), `session_stopped`/`session_ended` → stopped. Ensure `interrupted`/`resumed` also render sensible system rows (add cases in `systemSummary` if the generic fallback is poor).

3. YccKit — SessionViewModel (SessionViewModel.swift):
   - Add an injectable actions source (extend the existing `SessionTranscriptSource` protocol or add a `SessionActionSource` protocol implemented by YccClient) with the six methods above so the view-model is testable with fakes.
   - Async action methods on the view model: `send(text:)`, `answer(optionIndex:)/answer(text:)` for single, `answerBatch(_ answers:)` for batch (positional), `interrupt()`, `resumeSession()`, `stopSession()`. Each sets a short-lived `actionError: String?` (toast) on failure; `failed_precondition` on answer ("no pending question", e.g. answered elsewhere) must NOT crash — surface a mild toast and rely on projection state to have already dismissed the sheet. Expose `phase` and `pendingQuestion` passthroughs.
   - No optimistic mutation of projection state: the event stream is the source of truth (question_answered dismisses the sheet, interrupted shows the banner).

4. App — SessionView.swift (+ small new views as needed):
   - Sticky bottom input bar (live sessions only): TextField + send button → `SendInput`; disable while empty; keep the transcript's bottom-anchor behavior working with the keyboard (safeAreaInset(edge: .bottom) is the natural fit).
   - Question sheet: presented while `pendingQuestion != nil` (e.g. `.sheet(isPresented:)` bound to it). Single question: prompt, option buttons (tap → AnswerQuestion optionIndex=i), free-text field + send (optionIndex=-1). Batch: one section per question, each with option buttons/text field; a Submit sends AnswerQuestions with answers[i] positional (option picked → optionIndex, else text with -1). Sheet auto-dismisses when pendingQuestion clears (answered here or elsewhere). Don't allow swipe-dismiss to lose typed state silently — plain interactive dismiss is fine, question row still shows pending.
   - Toolbar overflow menu (live): Interrupt, Resume, Stop… (Stop behind a `confirmationDialog` — destructive, hard terminate).
   - Chrome banners driven by `phase`: paused ("Paused — send a steer or Resume"), idle, error banners above the input bar; error toast presentation for actionError (e.g. a transient overlay or `.alert`).
5. Tests (YccKit, `swift test`):
   - Projection: batch question_asked parses all questions+options; question_answered clears pendingQuestion and resolves the row (both single and batch payloads); phase transitions for interrupted/resumed/session_idle/session_error/session_stopped.
   - View model: with a fake action source — answer single via option and via text; batch positional mapping (mixed option/text); failed_precondition on answer sets actionError without crashing and doesn't corrupt state; interrupt/resume/stop invoke the source; send(text:) passes through.
6. plans/ios-client-smoke.md: extend the existing runbook (it exists from task 0180) with the phase-1-complete steps: open a live session, watch streaming, answer an ask_user (option + free text + a batch), kill the network mid-stream (simulator: toggle Mac Wi‑Fi or stop/start daemon) and verify replay-from-seq reconnect with no dup/gap, interrupt → steer via input bar → resume, stop with confirmation. Update the intro paragraph that says "phase-1 step 1 cut".
7. Verify: `cd clients/ios/YccKit && swift test`; `cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build`. Live-daemon smoke is manual per the runbook; note in work log.

### Starting points
- clients/ios/YccKit/Sources/YccKit/YccClient.swift — existing wrapper pattern (listProjects etc.); add the 6 unary RPCs here
- clients/ios/YccKit/Sources/YccKit/SessionProjection.swift:239 applyQuestionAsked / firstQuestion — extend to full batch; add phase folding
- clients/ios/YccKit/Sources/YccKit/SessionViewModel.swift + SessionTranscriptSource.swift — injectable source pattern used for testability
- clients/ios/App/SessionView.swift — read-only view to extend (toolbar, safeAreaInset input bar, sheet)
- proto/ycc/v1/ycc.proto:51-105 — SendInput/AnswerQuestion(optionIndex)/AnswerQuestions(positional QuestionAnswer)/Interrupt/Resume/StopSession
- internal/session/interaction.go askData/askManyData — question_asked payload shapes: {question,options} vs {questions:[{question,options}]}
- docs/remote-api.md §AnswerQuestion/AnswerQuestions + error table (failed_precondition = no pending question)
- clients/ios/YccKit/Tests/YccKitTests/SessionViewModelTests.swift — existing fake-source test style
- build/test: cd clients/ios/YccKit && swift test; cd clients/ios && xcodegen generate && xcodebuild -project Ycc.xcodeproj -scheme Ycc -destination 'generic/platform=iOS Simulator' build (generated .xcodeproj + App/Info.plist are git-ignored; keep ad-hoc code signing)

## Work log
- 2026-07-08 plan: Add interactive controls to the iOS session view (docs/design/ios-client.md §6 phase 1 step 4): input bar, question sheets, interrupt/resume/stop, state banners, plus runbook extension. Keep all logi
…[truncated]
- 2026-07-08 context hints: 9 recorded with plan
- 2026-07-08 context hints: clients/ios/YccKit/Sources/YccKit/YccClient.swift — existing wrapper pattern (listProjects etc.); add the 6 unary RPCs here; clients/ios/YccKit/Sources/YccKit/SessionProjection.swift:239 applyQuesti
…[truncated]
- 2026-07-08 implementer report: Implemented iOS session interactions (task 0183): input bar, answer sheets, interrupt/resume/stop, phase-driven chrome, and the smoke runbook. All logic is headless-testable in YccKit; App/ is a thin 
…[truncated]
- 2026-07-08 review tier: single-opus — reviewers: claude
- 2026-07-08 review (claude): accept — The change fully implements task 0183: input bar (SendInput), single/batch answer sheets (AnswerQuestion/AnswerQuestions positional), interrupt/resume/stop with confirmation, phase-driven banners, not
…[truncated]
- 2026-07-08 revision: Addressed the reviewer defect: SessionProjection now reads the `session_error` message under the production `"msg"` key first, falling back to `"error"` then `"text"`, in both places.  ## Changes (Ycc
…[truncated]
- 2026-07-08 review (claude): accept — The revision resolves the only prior finding: session_error's message is now read from the daemon's actual "msg" field (with "error"/"text" fallbacks) in both foldPhase and systemSummary, so the error
…[truncated]
- 2026-07-08 decision: accept — commit: iOS: session interactions — input bar, answer sheets, interrupt/resume/stop, phase banners + smoke runbook (task 0183)
