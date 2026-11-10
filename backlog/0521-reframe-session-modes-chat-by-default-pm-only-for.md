---
id: "0521"
title: 'Reframe session modes: chat by default, PM only for project onboarding, maintenance presets off the new-session screen (spec + daemon + web)'
status: done
priority: 2
created: "2026-11-10"
updated: "2026-11-10"
depends_on: []
spec_refs:
    - spec.md#9. Modes
    - docs/design/web-client.md
---

## Description

**Evidence (2026-11-11 usage review):** since session origin was recorded, the owner has started 152 chat sessions, 18 work sessions and 0 PM sessions. The only PM sessions are memory-groom runs, mostly scheduled. Every web new-session submit in analytics is chat. Presets were clicked 4 times, and 3 of those ended in `replace_prompt.cancel`. PM-shaped requests ("do a pass over the spec and backlog and think about the product…") go to chat, because chat already has the backlog tools, memory, ask_user and spawn_agent. PM is a strict subset of chat, minus code edits and subagents, so it never wins for interactive work.

**Owner direction:** chat is the interactive default. PM stays user-facing for one job: onboarding a new user or project, where it learns the project and builds out the backlog interactively with the user. Modes should differ by who drives the workflow (you, the work pipeline, or the scheduler), not by which tools were removed.

**Scope**
- Spec §9: chat is the default interactive mode. Work is started from a task (picking a task implies work), or by the loop. PM is user-facing only as project onboarding; internally it still runs memory-groom, spec-doctor and other scheduled or maintenance runs. Record the rationale briefly.
- Daemon: reframe the `pm` mode title and description around onboarding/intake (`Modes()` in internal/orchestrator/modes.go). Expose enough metadata in ListModes (e.g. a per-preset placement or `user_facing` flag) that clients don't hardcode preset names. memory-groom is already started from the Memory page by name; keep that working.
- Web new session (clients/web/src/features/newSession): project + prompt, starting chat. No mode picker or preset grid in the main flow; an "advanced" disclosure may keep explicit mode choice. Starting from a task (backlog/task pages) still opens work for that task. Offer "Onboard this project" only when the project needs onboarding (`needsOnboarding`; `suggestedPresets` already gates this), plus the existing Get started checklist (0505).
- Relocate spec-doctor to a contextual action, e.g. on the project page or docs/files view ("Check docs against code"). It should not be a new-session suggestion. Memory-groom stays on the Memory page and in auto-groom.
- An explicit re-onboard / "plan the backlog with PM" entry for already-onboarded projects, so the PM onboarding flow is reachable after first run (project or backlog page).

**Acceptance**
- The spec describes the reframed modes. ListModes metadata lets clients place presets without name lists.
- Web new session defaults to chat and shows no mode/preset grid in the default flow. Onboard appears only for projects that need it. Spec-doctor and re-onboarding are reachable from their contextual places. Task-started sessions still use work.
- Existing deep links and drafts that carry a mode or preset still start correctly.
- Web checks via the sidebar-v2 or onboarding harness: light/dark screenshots of new session (onboarded vs. un-onboarded project) and the relocated actions. Bundle rebuilt. Go and web tests green.
- The TUI is being retired (0481); no TUI work required.

## Outcome

ListModes adds default_mode (chat) and Preset.placement (onboarding/project/backlog/memory); pm retitled Project onboarding, spec-doctor "Check docs against code". Web new session starts chat with mode cards behind a collapsed Advanced disclosure; only onboarding is suggested, for projects needing it; kept drafts keep mode/preset. Project page: Onboard/Re-onboard + Check docs against code; backlog ⋯ menu: Groom the backlog + Re-onboard; GetStarted uses placement; Memory and task starts unchanged. Spec §9, remote-api, web-client docs updated; Go/web/Swift regen (YccKit builds), bundle rebuilt; go test ./... and 438 web tests green; light/dark shots in .ycc/tmp/w0521.

Commit: Modes reframed: new sessions start chat, pm is project onboarding, presets placed by ListModes metadata (default_mode, Preset.placement) at their contextual pages (0521)
