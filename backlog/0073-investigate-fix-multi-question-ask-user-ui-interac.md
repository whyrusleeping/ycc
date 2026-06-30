---
id: "0073"
title: Investigate/fix multi-question ask_user UI (interactive/judgement modes)
status: todo
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
