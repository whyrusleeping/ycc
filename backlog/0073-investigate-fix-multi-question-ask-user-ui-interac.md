---
id: "0073"
title: Investigate/fix multi-question ask_user UI (interactive/judgement modes)
status: done
priority: 3
created: "2026-06-30"
updated: "2026-06-30"
depends_on: []
spec_refs: []
---

## Description
## Description
A user running a work session reported the multi-question `ask_user` UI looked broken:
- all questions appeared "at once" (likely the raw `question_asked` log line listing every
  question, shown above/around the one-at-a-time wizard prompt — confusing);
- the per-question options were "not shown at the bottom"; and
- it was unclear how to pick an answer.

The TUI already has a questionnaire wizard (`startWizard`/`loadWizQuestion`/`wizardView`,
`internal/tui/tui.go`) that walks questions one at a time with a picker or free-text input,
so the parsing/state machine looks correct. The complaint is most likely a *presentation*
problem: the event-log rendering of the `question_asked` event dumps all the questions inline,
competing with the wizard, and/or the picker/footer affordance isn't obvious.

NOTE: this was originally hit in **work (loop)** mode, which now runs **autonomous** (it no
longer surfaces questions at all — see the loop-autonomy change), so this only affects
explicit interactive/judgement sessions now.

## Acceptance criteria
- [ ] Reproduce a multi-question `ask_user` in an interactive/judgement session and capture
      what the user actually sees (wizard vs. inline log line).
- [ ] Ensure the active question and its options are clearly the focal point; the raw
      `question_asked` log dump should not compete with / obscure the wizard.
- [ ] Per-question options are clearly visible with an obvious way to choose (picker arrows +
      enter) and to switch to free text.
- [ ] Footer/help text makes the interaction obvious.
- [ ] Tests cover the corrected rendering; `go build ./...` and `go test ./internal/tui/...` pass.

## Acceptance criteria

## Work log
- 2026-06-30 plan: Fix the multi-question ask_user presentation in internal/tui/tui.go so the one-at-a-time wizard is the clear focal point and the inline log dump no longer competes with it.  1. Stop the inline log dum
…[truncated]
- 2026-06-30 implementer report: Fixed the multi-question ask_user TUI presentation in internal/tui/tui.go so the one-at-a-time wizard is the clear focal point.  Changes: 1. Added `wizSeq int64` field to the model struct (seq of the 
…[truncated]
- 2026-06-30 review tier: single-opus — reviewers: Claude
- 2026-06-30 review (Claude): accept — The change correctly fixes the multi-question ask_user presentation: while the wizard is active, the inline question_asked log body is condensed to a single summary pointing at the wizard (tracked via
…[truncated]
- 2026-06-30 decision: accept — commit: tui: make multi-question ask_user wizard the focal point  Condense the inline question_asked log dump while the questionnaire wizard is collecting answers (tracked via new wizSeq), so it no longer dup
…[truncated]
- 2026-06-30 usage: 19,465 tok (in 96, out 19,369, cache_r 745,296, cache_w 60,275) · cost n/a (unpriced)
