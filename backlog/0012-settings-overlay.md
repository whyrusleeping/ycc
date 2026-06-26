---
id: "0012"
title: Settings overlay (esc) with mid-session interaction level + per-role model config
status: done
priority: 2
created: "2026-06-26"
updated: "2026-06-26"
depends_on:
    - "0006"
spec_refs:
    - Client UI (TUI)
    - Interaction levels
    - RPC protocol
    - Backends & model registry
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
- 2026-06-26 plan: Implement a modal settings overlay (Esc) in the TUI plus the backing RPCs and session-state plumbing for mid-session interaction-level and per-role model configuration.  1. Proto (proto/*.proto): add 
…[truncated]
- 2026-06-26 review (claude): accept — The change fully and correctly satisfies the task. All three new RPCs (ListModels, SetInteractionLevel, SetRoleConfig) are defined in proto, generated, and implemented server-side. Esc now opens a mod
…[truncated]
- 2026-06-26 revision: Addressed the two minor review nits (no other changes):  1. **Cleaned up the garbled `toggleReviewer` doc comment** (internal/tui/tui.go) so it accurately describes the multi-select toggle behavior: e
…[truncated]
- 2026-06-26 review (claude): accept — The revision addresses my prior findings. The garbled toggleReviewer doc comment is now clear and accurately describes the rotating multi-select behavior; a new mutex-guarded Session.Level() accessor 
…[truncated]
- 2026-06-26 plan: Task 0012 was already implemented and twice reviewed with "accept" verdicts (settings overlay + ListModels/SetInteractionLevel/SetRoleConfig RPCs + per-role model config + client UI prefs), but it rem
…[truncated]
- 2026-06-26 implementer report: Task 0012 (Settings overlay with mid-session interaction level + per-role model config) was already implemented and committed as cdfbc2a. I verified the full implementation is present and coherent:  *
…[truncated]
- 2026-06-26 review (claude): accept — The change fully satisfies Task 0012. Commit cdfbc2a adds the three new RPCs (ListModels, SetInteractionLevel, SetRoleConfig) in the proto, generated connect/pb code, and server handlers. Esc now open
…[truncated]
- 2026-06-26 decision: accept — commit 6ddf2cf: docs(0012): record resume verification of settings overlay task  Implementation (settings overlay, ListModels/SetInteractionLevel/SetRoleConfig RPCs, per-role model config, client UI prefs) was previo
…[truncated]
