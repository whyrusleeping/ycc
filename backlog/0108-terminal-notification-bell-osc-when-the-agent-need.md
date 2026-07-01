---
id: "0108"
title: Terminal notification (bell/OSC) when the agent needs the user or finishes
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18.2 Settings overlay
    - 11. Interaction levels
---

## Description
## Description
When the agent asks a question, pauses, or goes idle while the user is in another tmux window/desktop, nothing signals it — the "walk away and come back" workflow (the whole point of a daemon-first harness) depends on polling the screen. Emit a terminal bell (BEL) and/or OSC 9 / OSC 777 desktop notification on `question_asked`, `interrupted`, `session_idle`, and `session_error`, gated behind a UI preference (default: bell on, desktop notification opt-in). Client-only — no daemon change.

## Acceptance criteria
- [ ] Bell emitted on question_asked / session_idle / session_error / interrupted when enabled
- [ ] Preference in the settings overlay UI-prefs section, persisted via clientconfig
- [ ] No bell for events replayed on reopen/transcript load (only genuinely new events)
- [ ] Optional: OSC 9 notification with the question text when supported

## Acceptance criteria

## Work log
