---
id: "0555"
title: 'Web: thinking picker beside every model picker'
status: todo
priority: 2
created: "2026-11-15"
updated: "2026-11-15"
depends_on:
    - "0554"
spec_refs: []
---

## Description
Status: todo. Blocked by 0554 (daemon: per-model thinking levels and session-only thinking overrides). This task delivers the web-client part of the 2026-11-15 user request: every model picker gets a thinking picker beside it. It is separate from 0554 because it is a separate client deliverable with its own verification.

## Work
- Shared web control: a thinking select limited to the selected model's `ModelInfo.thinking_levels`. When the model changes, it moves to the nearest supported level. An empty value means "model default" wherever the RPC allows it.
- New session (`features/newSession`): coordinator thinking next to the Model select, sent through StartSession and SpawnWorkstream. Stored in the draft like `model`.
- Session settings (`features/session/SessionSettings.tsx`): replace the separate "Reasoning scope" picker with a reasoning select next to each role model. The copy says the change applies to this session only.
- Settings → Who does what and the review tier slots: limit the existing reasoning selects to the model's supported levels.
- Watches editor: thinking next to Model.
- Model editor: edit the `thinking_levels` override.
- Update docs/design/web-client.md.

## Acceptance criteria
- [ ] Each surface above shows a thinking select next to its model select, offering only that model's levels.
- [ ] A new session or workstream started with a level runs at it, and the saved default is unchanged. A session-panel change affects only that session.
- [ ] `npm run typecheck` and the vitest suite pass (node v24 via nvm). Playwright screenshots of new session, the session panel and the watch editor have been checked.

## Work log
