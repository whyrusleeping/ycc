---
id: "0039"
title: TUI cost view — browse usage/cost breakdown via GetUsage (shared browser modal)
status: todo
priority: 3
created: "2026-06-27"
updated: "2026-06-27"
depends_on:
    - "0029"
    - "0035"
spec_refs:
    - Token usage & cost accounting
    - Shared modal browser surface
    - Modes (the home menu)
---

## Description
## Description
Surface the cross-session usage/cost breakdown (spec §20.5, §18.6) in the TUI. Today the
`GetUsage` RPC (task 0029) is only consumed by the `ycc cost` CLI; the TUI has no cost
view. Per spec the cost view is a **modal that shares the generic list+detail "browser"
surface** with the backlog browser (§18.5) and the session history browser (§18.6),
opened over the home menu or a session and dismissed with Esc, routed from a small
"browsers" menu.

## Context
- RPC already exists: `GetUsage(GetUsageRequest{project, group_by, since, until})` →
  `GetUsageResponse{rows, total, workspace}` (internal/server, proto/ycc/v1).
- CLI reference rendering lives in `cmd/ycc/main.go` (`runCost`) — mirror its
  table/columns and priced/unpriced/partial ("—" / `$x.xxxx` / `*`) treatment.
- TUI lives in internal/tui/tui.go; the backlog browser (ctrl+b) is the closest existing
  pattern. Task 0035 introduces the shared list+detail modal — prefer building on that
  component so navigation isn't re-implemented. (Could be done earlier atop the existing
  backlog-browser pattern if prioritized before 0035, then folded into the shared
  component.)

## Acceptance criteria
- [ ] A cost view reachable from the TUI (e.g. a keybinding and/or the "browsers" menu)
      that calls `GetUsage` and renders the breakdown (default group-by task) with token
      counts and dollar cost, unpriced rows shown as tokens-only.
- [ ] Supports at least switching the group-by dimension (task/model/session/day); a
      project/date-range control is optional but note it if deferred.
- [ ] Opens as a modal over the home menu or a session and dismisses with Esc, consistent
      with the backlog browser; reuses the shared list+detail modal (0035) where available.
- [ ] Respects the selected project (multi-project daemon) like the backlog browser.
- [ ] Test coverage consistent with existing TUI tests (model update/rendering).

## Acceptance criteria

## Work log
