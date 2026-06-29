---
id: "0047"
title: Support multiple questions with per-question answer sets in ask-user tool
status: in_progress
priority: 3
created: "2026-06-27"
updated: "2026-06-29"
depends_on: []
spec_refs: []
---

## Description
## Summary
The `ask user` tool currently lets the model pose a single question (with one answer/choice set). Extend it so the model can specify **multiple questions in one call**, each with its own set of answer options.

## Motivation
When the model needs several pieces of input from the user, it must currently make multiple sequential ask-user calls. Allowing a batch of questions — each with its own answer set — lets the model gather all needed clarifications in a single round-trip and presents a cleaner UX.

## Scope
- Update the ask-user tool input schema to accept a list of questions, where each question carries its own prompt text and its own set of answer choices (and any existing per-question options such as free-text/allow-custom).
- Render all questions in the user-facing surface (TUI / phone-facing), collecting an answer for each.
- Return the answers back to the model keyed/ordered so it can map each answer to its question.
- Preserve backward compatibility (or cleanly migrate) the single-question form.

## Acceptance Criteria
- [ ] The ask-user tool schema accepts multiple questions, each with an independent answer/choice set.
- [ ] The user is presented with all questions and can answer each independently before submitting.
- [ ] The tool result conveys every answer mapped unambiguously to its corresponding question.
- [ ] Single-question usage continues to work (no regression).
- [ ] Validation handles edge cases (empty question list, a question with no choices, etc.).

## Acceptance criteria

## Work log
- 2026-06-29 plan: Extend ask_user to support a batch of questions, each with its own option set, end-to-end while preserving the single-question form.  1. Proto (proto/ycc/v1/ycc.proto): add `QuestionAnswer {string tex
…[truncated]
- 2026-06-29 implementer report: Implemented task 0047: ask_user now supports multiple questions, each with its own answer set, end-to-end, while preserving the single-question form.  Changes: - **proto/ycc/v1/ycc.proto**: added `Que
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): revise — The multi-question ask_user feature is implemented end-to-end (proto, orchestrator routing via AskMany, session batch gating, AnswerQuestions RPC, regenerated protobuf/connect, TUI wizard) with good t
…[truncated]
- 2026-06-29 revision: Addressed the reviewer's mixed-batch focus defect:  1. **internal/tui/tui.go `loadWizQuestion()`**: changed it to return a `tea.Cmd`. In the free-text branch (current wizard question has no options) i
…[truncated]
- 2026-06-29 review (Claude): accept — The revision resolves the previously flagged major TUI focus bug: loadWizQuestion now focuses the textarea for free-text wizard questions and returns the blink cmd, which recordWizAnswer propagates th
…[truncated]
