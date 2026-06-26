---
id: "0006"
title: Home menu, spec/backlog/feature/bug modes, TUI (M4)
status: todo
priority: 4
created: 2026-06-25
updated: 2026-06-25
depends_on: ["0005"]
spec_refs: ["Modes", "System architecture"]
---

## Description
Round out the product surface: the home menu (ListModes + StartSession), the remaining
coordinator modes (spec authoring, backlog building, feature/bug intake that proposes a
plan and updates spec+backlog then optionally flows into work), and a Bubble Tea TUI as
the primary local client.

## Acceptance criteria
- [ ] ListModes + home menu in the TUI
- [ ] spec mode (section-wise update_spec) and backlog mode (create/update tasks)
- [ ] feature/bug mode: understand → propose_plan → on accept update spec+backlog
- [ ] mode_changed transition from feature/bug into work within one session
- [ ] TUI renders event stream, subagent drill-down, ask_user prompts

## Work log
