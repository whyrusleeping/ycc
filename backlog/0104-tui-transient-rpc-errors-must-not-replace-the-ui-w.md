---
id: "0104"
title: 'TUI: transient RPC errors must not replace the UI with a fatal error screen'
status: todo
priority: 2
created: "2026-07-01"
updated: "2026-07-01"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
## Description
Nearly every TUI command (`sendInput`, `interrupt`, `resume`, `answerQuestion`, `setLevel`, `setThinking`, `setRoleConfig`, fetches…) returns `errMsg{err}` on failure, and `render()` short-circuits to a full-screen `error: … (ctrl+c to quit)`. `m.err` is never cleared, so a single transient RPC hiccup permanently nukes a live session view whose event stream is fine.

Reserve the fatal screen for genuinely unrecoverable startup failures (can't reach the daemon). Everything else should surface as an inline, dismissible status-bar/toast error while the session view keeps rendering.

## Acceptance criteria
- [ ] A failed `SendInput`/`Interrupt`/`Answer*`/settings RPC shows an inline error (status bar or toast) and the session view stays usable
- [ ] Inline errors clear on the next successful action/event (or a key/timeout)
- [ ] Fatal screen retained only for unrecoverable cases; if kept, it offers a retry/back affordance rather than only ctrl+c
- [ ] Tests cover: transient send failure → view still renders events and accepts input

## Acceptance criteria

## Work log
