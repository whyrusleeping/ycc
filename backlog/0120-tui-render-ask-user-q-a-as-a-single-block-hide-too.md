---
id: "0120"
title: 'TUI: render ask_user Q&A as a single block; hide tool plumbing rows'
status: done
priority: 2
created: "2026-07-02"
updated: "2026-07-02"
depends_on: []
spec_refs: []
---

## Description
The ask_user flow renders one exchange up to four times in the session transcript: the `tool_call ask_user` row (raw JSON args, stuck on the in-flight ○ glyph because question events break tool_call/tool_result adjacency so the fold never happens), the auto-expanded `question_asked` row, the `question_answered` row, and the unmerged `tool_result` row (which for batch asks dumps the whole Q/A transcript again). While pending, the footer picker shows the question yet again.

Fix (TUI-only; no event schema/engine changes):

## Acceptance criteria
- [x] `ask_user` tool_call and its tool_result rows are hidden in the transcript when a matching `question_asked` event follows; an errored ask_user call with no question_asked (e.g. validation error) still renders.
- [x] `question_answered` folds into its preceding `question_asked` row as one block (question + `→ answer`); batch asks interleave Q/A pairs in one block instead of separate question/answer rows.
- [x] While the single-question picker is active in the footer, the question row's body collapses to a pointer (like the wizard already does) instead of duplicating the full question.
- [x] Autonomous auto-answered questions render compactly (one dim line noting auto-answer) instead of the full canned autonomousAutoAnswer paragraph.
- [x] question_asked navigation (N/P jump), selection/click mapping, and follow-mode line accounting stay correct.
- [x] Tests cover the hidden plumbing rows, the fold, the errored-call fallback, and the compact auto-answer rendering.

## Acceptance criteria

## Work log
- Implemented in internal/tui/tui.go: new same-actor, non-adjacent pairing helpers
  (askQuestionIdx, resultCallIdx, answerIdxFor, questionIdxForAnswer, answerEventFor)
  plus hiddenRow predicates isAskUserPlumbing / isFoldedAnswer. question_asked now
  renders the whole exchange via questionBody/batchQuestionBody (answer folded as
  "→ …", options dropped once answered, compact dim line for autonomous auto-answers,
  "answer below ↓" pointer while the footer picker/wizard owns the prompt — tracked by
  the new model.pendingSeq field). Errored ask_user results stay visible. The stateless
  transcript renderer (renderTranscript) resets picker/wizard state so replayed
  transcripts always render questions in full. Selection/search/jump all flow through
  hiddenRow, so navigation needed no changes. Spec §18.3 documents the rendering. Six
  new tests in tui_test.go; full suite green.
