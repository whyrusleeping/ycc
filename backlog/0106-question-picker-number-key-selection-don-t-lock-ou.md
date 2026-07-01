---
id: "0106"
title: 'Question picker: number-key selection + don''t lock out scrolling/browsers'
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.3 Structured interactive questions (Claude-Code style)
---

## Description
## Description
While an ask_user options picker is active (`m.picking`), every key except ↑/↓/enter/ctrl+c is swallowed. The user cannot scroll the transcript to re-read the question's context, cannot open the backlog browser (often exactly what's needed to answer "which task next?"), and cannot press `1`–`9` to pick — though spec §18.3 promises "arrow-key/number navigable".

## Acceptance criteria
- [ ] Digits 1–9 select the corresponding option directly (single-question picker and wizard steps)
- [ ] pgup/pgdn and mouse wheel scroll the transcript while a picker is active
- [ ] ctrl+b (backlog) and ctrl+n (capture) work while a question is pending; the picker re-renders on return
- [ ] Same affordances in the multi-question wizard
- [ ] Footer hint documents number selection

## Acceptance criteria

## Work log
