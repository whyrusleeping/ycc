---
id: "0012"
title: Settings overlay (esc) with mid-session interaction level + per-role model config
status: todo
priority: 2
created: 2026-06-26
updated: 2026-06-26
depends_on: ["0006"]
spec_refs: ["Client UI (TUI)", "Interaction levels", "RPC protocol", "Backends & model registry"]
---

## Description
A modal settings overlay opened with Esc (video-game style). Esc no longer jumps
straight to the home menu; "Back to home menu" becomes an explicit overlay choice. The
overlay exposes: interaction level (changeable mid-session), per-role model
configuration (coordinator/implementer single-pick, reviewers multi-select), UI prefs
(theme, follow/auto-scroll), and Quit. The headline is per-role model selection — pick
which configured model drives each role for the session. See spec §18.2.

## Acceptance criteria
- [ ] Esc opens a modal overlay over menu and session states; Esc closes it
- [ ] leaving a session is an explicit "Back to home menu" menu item, not bare Esc
- [ ] new RPCs: `ListModels`, `SetInteractionLevel(session, level)`,
      `SetRoleConfig(session, coordinator, implementer, reviewers[])`
- [ ] interaction level change takes effect at the next gate and emits a log event
- [ ] role config change rebuilds the relevant gollama clients; next coordinator turn
      / next spawned subagent uses the new assignment
- [ ] reviewers role supports multi-select; pickers populated from `ListModels`
- [ ] UI prefs (theme, follow toggle) are client-only and persist in local client config

## Work log
