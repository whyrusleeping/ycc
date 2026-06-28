---
id: "0047"
title: Support multiple questions with per-question answer sets in ask-user tool
status: todo
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
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
