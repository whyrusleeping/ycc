---
id: "0119"
title: Search + jump navigation in the histModal transcript (session browser over a live session)
status: todo
priority: 5
created: "2026-07-02"
updated: "2026-07-02"
depends_on:
    - "0116"
spec_refs:
    - 18.6 Session history browser & reopen
    - 18. Client UI (TUI)
---

## Description
## Description
Task 0116 added `/` incremental search (n/N, esc) and jump-to-event keys ({}()<>[]) to the live session view and the stateHistory transcript drill-in — the two surfaces sharing the m.evs/m.vp event pipeline. The session browser opened as a modal OVER a live session (ctrl+r from within a session, task 0112) renders transcripts into a separate plain-string viewport (histModalVP via renderTranscriptContent) and was explicitly out of scope, so its transcripts have no search or jump navigation.

Extend the same navigation to the histModal transcript. Since it doesn't use the event pipeline, this likely means either (a) line-based search over the rendered string content (scroll-to-line instead of selection), or (b) refactoring the modal to reuse the shared searchable-text helpers against msg.events kept alongside the viewport.

## Acceptance criteria
- [ ] `/` search with n/N next/prev and esc-clear works inside a histModal transcript
- [ ] Jump keys for question/review/commit/error events work (or a documented, deliberate subset if line-based search is chosen)
- [ ] The live session behind the modal is never disturbed (no m.evs/m.vp mutation)
- [ ] help.go "session browser" section updated per the maintenance contract

## Acceptance criteria

## Work log
