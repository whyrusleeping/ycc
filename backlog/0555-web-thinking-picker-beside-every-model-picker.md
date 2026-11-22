---
id: "0555"
title: 'Web: thinking picker beside every model picker'
status: done
priority: 2
created: "2026-11-15"
updated: "2026-11-22"
depends_on:
    - "0554"
spec_refs: []
---

## Description

Status: todo and ready. Nothing is implemented yet: `SessionSettings.tsx` still has the separate "Reasoning scope" picker, and no web source reads `thinking_levels`. Its dependency 0554 (daemon: per-model thinking levels and session-only thinking overrides) is done in `60846e4`, and the iOS sibling 0556 is done. This task delivers the web-client part of the 2026-11-15 user request (session s_4b1af51cc639dc47, event 2; scope answers at event 64): every model picker gets a thinking picker beside it. It is separate from 0554 because it is a separate client deliverable with its own verification.

## Acceptance criteria

- [ ] Each surface above shows a thinking select next to its model select, offering only that model's levels.
- [ ] A new session or workstream started with a level runs at it, and the saved default is unchanged. A session-panel change affects only that session.
- [ ] `npm run typecheck` and the vitest suite pass (node v24 via nvm). Playwright screenshots of new session, the session panel and the watch editor have been checked.

## Outcome

Each web model picker now has a reasoning select limited to ModelInfo.thinking_levels, and a model change moves a picked level with the daemon's clamp rule. Covered: new session and workstream (coordinatorThinking), the session panel (a select per role replaces the scope picker; session-only), Settings roles, review-tier slots, and the watch editor (thinking). The model editor edits the thinking_levels override. Reasoning is held while a role's model choice is unapplied or an assignment is saving. Typecheck and vitest pass (480 tests). Playwright light/dark checks against a scratch daemon confirmed the offered levels and clamping, that a session started at low ran at low with the default unchanged, and that panel changes stay session-only. web-client.md updated and dist rebuilt.

Commit: Web: thinking picker beside every model picker, limited to the model's levels (0555)
