---
id: "0210"
title: Split the giant TUI monolith into cohesive components
status: todo
priority: 3
created: "2026-07-15"
updated: "2026-07-15"
depends_on: []
spec_refs:
    - Client UI (TUI)
    - Package layout
---

## Description
`internal/tui/tui.go` is roughly 10.9k lines and `tui_test.go` roughly 6.7k lines. Backend setup, settings, history, workstreams, backlog, cost, diff rendering, session input, and home-menu behavior share one large implementation/test file, increasing merge conflicts and making hidden state coupling difficult to reason about.

Refactor incrementally into cohesive files and, where boundaries are stable, subpackages/components. Preserve behavior and Bubble Tea message flow; this is structural work, not a visual redesign. Good first seams include backend/setup forms, settings, browser/history, backlog, workstreams, commit diff, selection/mouse, and session rendering.

## Acceptance criteria
- [ ] `tui.go` is reduced to the core model/state machine and top-level dispatch rather than containing every screen implementation.
- [ ] Major modal/screen domains live in clearly named files or internal subpackages with narrow interfaces and ownership of their state/messages/rendering.
- [ ] `tui_test.go` is split into corresponding focused test files with shared helpers isolated cleanly.
- [ ] Package dependency direction avoids cycles and does not expose broad mutable model internals merely to move code.
- [ ] Existing keyboard, mouse, render-cache, session-stream, settings, backlog, history, workstream, and E2E behavior remains unchanged.
- [ ] Warm/cold transcript render benchmarks do not materially regress.
- [ ] `go test ./...`, `go test -race ./internal/tui/...`, and the E2E suite pass.
- [ ] A short package/file map is added to developer documentation so future features land in the appropriate component.

## Work log
