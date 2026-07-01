---
id: "0109"
title: 'Guard ctrl+c: don''t instantly kill a running session on a one-shot daemon'
status: todo
priority: 3
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 3.1 Daemon lifecycle & projects
    - 18.2 Settings overlay
---

## Description
## Description
`ctrl+c` returns `tea.Quit` from every state. With the default one-shot in-process daemon, quitting tears down the daemon and any in-flight agent work — possibly mid-commit or mid-write. One accidental keypress ends everything with no confirmation, while the settings overlay deliberately made *leaving a session* an intentional act.

Add a lightweight guard when (a) the daemon is one-shot AND (b) a session is running/paused/pending-question: first ctrl+c shows "agent running — ctrl+c again (within ~2s) to quit"; second one quits. Attached to a persistent daemon (work survives), quit stays immediate.

## Acceptance criteria
- [ ] Double-press (or equivalent) required to quit while a one-shot session is running
- [ ] Immediate quit preserved when idle, on the home menu with no live session, or on a persistent daemon
- [ ] The guard message is visible where the user is looking (status/footer)
- [ ] Overlay "quit" row uses the same guard

## Acceptance criteria

## Work log
