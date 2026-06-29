---
id: "0067"
title: Add settings option to auto-expand all agent logs by default
status: todo
priority: 3
created: "2026-06-29"
updated: "2026-06-29"
depends_on: []
spec_refs:
    - Client UI (TUI)
---

## Description
## Description

The session event stream renders agent log events in a collapsed state by default (with `▸`/`▼` affordances to expand individual rows). Users who want to read full detail must expand each event manually.

Add a settings menu option that, when enabled, automatically expands all agent log events (tool calls, results, thinking, subagent work, etc.) so the full transcript is visible without per-row expansion.

### Scope
- Add a toggle in the settings menu (e.g. "Auto-expand agent logs").
- When enabled, new and existing events render expanded by default in the event stream.
- Persist the setting so the choice survives across sessions.
- Manual per-row collapse/expand should still work on top of the default.

## Acceptance criteria
- [ ] Settings menu exposes a toggle to auto-expand all agent logs.
- [ ] When enabled, agent log events render expanded by default; when disabled, behavior is unchanged (collapsed by default).
- [ ] Setting persists across sessions.
- [ ] Manual expand/collapse of individual rows continues to work; click/keyboard line mapping stays intact.
- [ ] TUI unit tests cover the auto-expand behavior.

## Acceptance criteria

## Work log
