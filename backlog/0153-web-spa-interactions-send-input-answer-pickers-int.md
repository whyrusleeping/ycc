---
id: "0153"
title: 'Web SPA interactions: send input, answer pickers, interrupt/stop'
status: done
priority: 4
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0152"
spec_refs:
    - docs/design/web-client.md#7. Phone-form-factor layout
---

## Description
Third slice of docs/design/web-client.md (§7 chrome, §9 item 3): make the web client interactive.

- Sticky bottom input bar → `SendInput` (prod running/idle sessions).
- `question_asked` raises a bottom-sheet answer picker: suggested options as buttons + free-text field; wired to `AnswerQuestion` (single; optionIndex >= 0 selects, -1 sends free text) and `AnswerQuestions` (batch, positional answers[i]); `question_answered` dismisses the sheet.
- Overflow menu (⋯) with `Interrupt` / `Resume` / `StopSession`.
- Auto-follow scroll pinned to newest while at bottom; scrolling up stops auto-follow and shows a "jump to latest" pill; new events must not yank the user's scroll position.
- Surface RPC errors (e.g. failed_precondition "no pending question") as non-fatal toasts.

## Acceptance criteria
- [ ] From a phone browser: send input to a live session, answer a single and a batched ask_user gate (option and free-text), interrupt/resume, and stop a session — all reflected in the event feed.
- [ ] Answer sheet dismisses on question_answered (including when answered from another client).
- [ ] Scroll behavior: auto-follow at bottom, pill when scrolled up, no scroll yank on new events.

## Plan

Extend the existing vanilla SPA (internal/web/dist/) with the interactive chrome from docs/design/web-client.md §7 / §9 item 3. No new RPCs, no framework, no build step; keep the pure-logic/DOM split and ES2019-only code (Node v12 CI) established in task 0152.

**1. Pure logic first (testable under Node)**
- Extend the feed state (makeFeed/feedIngest) with pending-question tracking: a durable `question_asked` sets `feed.pending = {questions:[{prompt,options[]}], batch:bool, auto:bool, seq}` (normalize single `{question,options}` vs batch `{questions:[...]}` payloads exactly as internal/session/interaction.go askData/askManyData emit them); a durable `question_answered` (or `session_idle`/`session_error`/`session_stopped`) clears it. feedIngest's returned action gains enough info for the renderer to sync the answer sheet after each event (e.g. `pendingChanged` flag or the renderer just reads feed.pending after each ingest). Duplicates/replays must not re-raise a dismissed sheet (seq rules already handle this).
- Add a pure answer-request builder: `buildAnswerBody(pending, answers)` → `{sessionId?, optionIndex, text}` for single (optionIndex >= 0 selects; -1 + text for free text) and `{answers:[{optionIndex,text},...]}` positional for batch, matching docs/remote-api.md AnswerQuestion/AnswerQuestions.
- Export the new helpers via the existing module.exports block; add Node tests in internal/web/app_test.js: pending set/clear across ask→answer, batch normalization, replay does not re-raise, terminal events clear pending, answer-body shapes (single option, single free-text, batch mixed).

**2. Sticky bottom input bar → SendInput**
- In the session view for LIVE sessions only: a sticky bottom bar (textarea/input + send button, thumb-reachable). Send → `rpc("SendInput", {sessionId, text})`; clear field on success; on error show a toast (non-fatal). Disable while in flight. Hidden for persisted (non-live) sessions.

**3. Answer bottom sheet → AnswerQuestion / AnswerQuestions**
- When feed.pending is set (live session), raise a bottom sheet over the input bar: per question, its prompt + option buttons + a free-text field. Single question: tapping an option sends AnswerQuestion immediately with that optionIndex; submitting free text sends optionIndex:-1 + text. Batch: each question collects a selection (option tap or free text), plus one "Send answers" button issuing AnswerQuestions with positional answers[i].
- While sending, disable the sheet's controls; do NOT locally dismiss on RPC success — the durable `question_answered` event is authoritative and dismisses the sheet (this also covers answers from another client, per acceptance criteria). On RPC error (e.g. failed_precondition "no pending question"), re-enable and toast the message.
- Sheet must not yank scroll; if the user is scrolled up it appears without scrolling the feed. The existing static questionNode row in the feed stays as the durable record.
- Avoid a flash during replay/catch-up: sync the sheet to feed.pending via a short debounce (e.g. rAF or ~50ms timer) rather than per-frame DOM churn.

**4. Overflow menu (⋯) → Interrupt / Resume / StopSession**
- A ⋯ button in the session topbar (live sessions) opening a small menu: Interrupt, Resume, Stop. Stop asks a one-tap inline confirm (hard-terminate, hard to reverse). Each action is a unary rpc(); success → toast (brief) or rely on the resulting feed events; error → toast. Close menu on tap-outside/escape.

**5. Auto-follow scroll + "jump to latest" pill**
- Replace the implicit per-append nearBottom check with explicit follow state: a scroll listener on the feed sets `following = nearBottom()`. Appends/tail updates scroll only when following. When !following and new content arrives, show a floating "↓ jump to latest" pill; tapping it scrolls to bottom, resumes following, hides the pill. Scrolling back to the bottom manually also re-enables following and hides the pill.

**6. Toasts**
- A minimal toast container (bottom, above input bar): `toast(msg)` shows a few seconds, auto-dismisses, textContent only. Used for all non-fatal RPC errors (Connect error `message` field when parseable).

**7. CSS** — bottom bar, bottom sheet, overflow menu, pill, toasts in app.css; phone-first, dark/light per existing scheme; comfortable tap targets.

**8. Verification**
- `go build ./... && go test ./...` (web_test.go runs the Node tests; keep ES2019 — no `?.`/`??` anywhere in app.js/app_test.js).
- `node internal/web/app_test.js` passes locally.
- Extend plans/web-client-smoke.md with the interaction steps (send input, answer single/batch via option and free text, interrupt/resume, stop, scroll pill), since full phone verification needs a real browser.

### Starting points
- internal/web/dist/app.js — existing SPA; pure helpers (makeFeed/feedIngest, module.exports) at top, DOM app below; renderSession/handleEvent/appendRow/nearBottom are the integration points
- internal/web/dist/app.css, internal/web/dist/index.html
- internal/web/app_test.js — Node test file to extend; run via internal/web/web_test.go (skips without node); Node v12 → ES2019 only
- docs/remote-api.md §SendInput/§AnswerQuestion/§Interrupt/§StopSession — exact request JSON shapes; errors surface as {code,message}
- internal/session/interaction.go askData/askManyData — question_asked dataJson: single {question,options?,auto?} vs batch {questions:[{question,options?}],auto?}
- docs/design/web-client.md §7 — chrome spec (input bar, bottom sheet, overflow, pill)
- plans/web-client-smoke.md — smoke runbook to extend

## Work log
- 2026-07-06 plan: Extend the existing vanilla SPA (internal/web/dist/) with the interactive chrome from docs/design/web-client.md §7 / §9 item 3. No new RPCs, no framework, no build step; keep the pure-logic/DOM spli
…[truncated]
- 2026-07-06 context hints: 7 recorded with plan
- 2026-07-06 context hints: internal/web/dist/app.js — existing SPA; pure helpers (makeFeed/feedIngest, module.exports) at top, DOM app below; renderSession/handleEvent/appendRow/nearBottom/questionNode are the integration poi
…[truncated]
- 2026-07-06 implementer report: Implemented the third slice of the web client (docs/design/web-client.md §7/§9): interactive chrome on the existing dependency-free vanilla SPA. No new RPCs, no framework, no build step; kept the pu
…[truncated]
- 2026-07-06 review tier: single-opus — reviewers: Claude
- 2026-07-06 review (Claude): accept — The change delivers the third slice of the web client cleanly. It extends the pure feed state with pending ask_user tracking (pendingFromAsk normalizes both single {question,options?,auto?} and batch 
…[truncated]
- 2026-07-06 decision: accept — commit: web: interactive chrome — send input, answer sheet, session controls, follow scroll (task 0153)
- 2026-07-06 usage: 45,019 tok (in 144, out 44,875, cache_r 2,973,040, cache_w 212,778) · cost n/a (unpriced)
  implementer: 29,665 tok (in 62, out 29,603, cache_r 1,804,307, cache_w 78,067) · cost n/a (unpriced)
  coordinator: 8,633 tok (in 32, out 8,601, cache_r 629,774, cache_w 101,404) · cost n/a (unpriced)
  reviewer:Claude: 6,721 tok (in 50, out 6,671, cache_r 538,959, cache_w 33,307) · cost n/a (unpriced)
