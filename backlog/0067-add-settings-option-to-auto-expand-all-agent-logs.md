---
id: "0067"
title: Add settings option to auto-expand all agent logs by default
status: done
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
- 2026-06-29 plan: Add an "auto-expand agent logs" setting to the TUI.  1. internal/clientconfig/clientconfig.go: add `AutoExpandLogs bool` field (json:"autoExpandLogs") to Prefs. Zero value (false) preserves current be
…[truncated]
- 2026-06-29 implementer report: Added an "auto-expand agent logs" setting to the TUI.  Changes: - internal/clientconfig/clientconfig.go: added `AutoExpandLogs bool` field (json:"autoExpandLogs") to Prefs. Default stays false (curren
…[truncated]
- 2026-06-29 revision: Re-applied the auto-expand feature on the clean v1 (github.com/charmbracelet/*) baseline. No dependency/import/module changes; go.mod, go.sum, internal/setup/*, internal/tui/theme.go, theme_test.go al
…[truncated]
- 2026-06-29 review tier: single-opus — reviewers: Claude
- 2026-06-29 review (Claude): accept — The change correctly adds an "auto-expand agent logs" setting. A new `AutoExpandLogs` field is added to `Prefs` with a JSON tag (default false preserves prior behavior), persisted via clientconfig.Sav
…[truncated]
- 2026-06-29 decision: accept — commit: Add settings toggle to auto-expand agent logs by default (0067)  Add a client pref AutoExpandLogs (persisted via clientconfig) and a settings overlay row to toggle it. A new eventExpanded helper makes
…[truncated]
