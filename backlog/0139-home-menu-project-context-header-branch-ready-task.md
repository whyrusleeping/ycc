---
id: "0139"
title: 'Home menu: project context header (branch, ready tasks, today''s spend) + continue-last-session'
status: todo
priority: 3
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 18. Client UI (TUI)
---

## Description
The home menu shows modes + blocked/waiting warnings, but no orientation: *which* project am I in, on what branch, is the tree dirty, how much work is ready, what have I spent today? Users juggling multiple projects (persistent daemon) or returning after hours away need this at a glance. All the data already exists (workspace path, git helpers, ListBacklog readiness, usage aggregator).

## Acceptance criteria
- [ ] A one/two-line context header on the home menu: project name/workspace path · git branch (+ dirty marker) · N ready / M blocked tasks · today's spend (when priced usage exists).
- [ ] Degrades gracefully: non-git workspace, no backlog, no usage → segments drop out (reuse the status bar's priority-fit approach).
- [ ] Nice-to-have: when a recent resumable session exists, a "c continue last session" affordance on the menu (one keypress instead of ctrl+r → pick → o).

## Work log
