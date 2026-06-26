# Backlog

> Generated index. Canonical task data lives in `backlog/<id>-<slug>.md`.

| id | title | status | pri | depends on |
|----|-------|--------|-----|------------|
| [0001](backlog/0001-gollama-unified-turn.md) | Add unified Turn dispatch to gollama | done | 1 | — |
| [0002](backlog/0002-agent-loop.md) | Core agent loop with worker tools (M0 spike) | done | 1 | 0001 |
| [0003](backlog/0003-daemon-event-log.md) | Daemon, event log, and first client (M1) | done | 2 | 0002 |
| [0004](backlog/0004-work-mode-happy-path.md) | work mode happy path (M2) | done | 2 | 0003 |
| [0005](backlog/0005-multimodel-review-revise-levels.md) | Multi-model review, revise loop, interaction levels (M3) | done | 3 | 0004 |
| [0006](backlog/0006-home-menu-modes-tui.md) | Home menu, spec/backlog/feature/bug modes, TUI (M4) | done | 4 | 0005 |
| [0007](backlog/0007-remote-sync.md) | Remote session sync + phone-facing surface (M5) | todo | 5 | 0006 |
| [0008](backlog/0008-reviewer-sandboxing.md) | Sandbox reviewer bash to prevent workspace mutation | todo | 6 | 0005 |
| [0009](backlog/0009-session-lifecycle-interrupt.md) | Session lifecycle — Interrupt RPC and stop/GC | todo | 3 | 0003 |
| [0010](backlog/0010-context-window-management.md) | Context-window management for long sessions | todo | 3 | 0002 |
| [0011](backlog/0011-multiline-input.md) | Multiline session input (textarea) | todo | 3 | 0006 |
| [0012](backlog/0012-settings-overlay.md) | Settings overlay (esc) with mid-session interaction level + per-role model config | done | 2 | 0006 |
| [0013](backlog/0013-structured-questions.md) | Structured interactive ask_user questions (option pickers) | done | 2 | 0006 |
| [0014](backlog/0014-daemon-lifecycle-oneshot.md) | Daemon lifecycle — one-shot in-process default, opt-in persistence | done | 2 | 0003 |
| [0015](backlog/0015-multi-project-registry.md) | Multi-project daemon — project registry, RPCs, and TUI picker | todo | 2 | 0014, 0006 |
| [0016](backlog/0016-quick-add-backlog-mid-session.md) | Quick-add backlog items mid-session (TUI capture overlay) | todo | 3 | 0006 |
| [0017](backlog/0017-tool-call-syntax-highlighting.md) | Smarter language inference for tool-call syntax highlighting | todo | 3 | 0006 |
| [0018](backlog/0018-implementer-turn-limit.md) | Remove / raise / make-configurable the implementer turn limit | done | 2 | 0002 |
| [0020](backlog/0020-save-rerun-plans.md) | Persist and re-run coordinator / testing plans (plan library) | todo | 3 | 0004 |
| [0021](backlog/0021-collapse-modes-into-pm.md) | Collapse spec/backlog/feature/bug into a single `pm` (project manager) mode | in_progress | 2 | 0006 |
