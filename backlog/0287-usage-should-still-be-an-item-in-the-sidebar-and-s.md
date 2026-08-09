---
id: "0287"
title: Usage should still be an item in the sidebar and show overall usage when selected
status: in_review
priority: 3
created: "2026-08-07"
updated: "2026-08-08"
depends_on: []
spec_refs: []
---

## Description

## Acceptance criteria

## Plan

Context: the iOS drawer refactor deliberately removed project-scoped destinations (backlog/workstreams/usage) from the WorkspaceDrawer because they need a project. The user wants Usage back in the drawer ("sidebar"), showing OVERALL usage (across all projects) when selected there. Today `Manager.UsageReport("")` errors when >1 project is registered, so overall usage needs daemon support first.

1) Daemon (Go), internal/session/session.go `Manager.UsageReport`:
   - When `project == ""` and multiple projects are registered, aggregate across ALL registered projects: resolve each project's workspace, dedupe absolute paths, `usage.Scan` each, concatenate entries, and run one `usage.Aggregate` over the combined entries. Leave `res.Workspace` empty for the overall report (it is no longer a single path).
   - Keep the existing sole-project resolution and the "no projects registered" error.
   - Test: a Go test (internal/session or internal/server) with two registered temp projects each carrying usage logs; `UsageReport("", …)` returns merged rows/total; a named project still returns only its own.

2) CLI polish (cmd/ycc/main.go costCommand): update the `--project` flag help ("required when multiple exist" → omit for all projects) and make `renderCost`'s workspace header degrade gracefully (e.g. show "all projects" when Workspace is empty).

3) Spec: §20.5 "Project rollup" — one sentence noting that omitting the project yields the overall (all registered projects) rollup. Run `go run ./cmd/ycc spec-check` (or the repo's spec-check invocation) if it gates symbols.

4) iOS:
   - WorkspaceDrawer: add a "Usage" DrawerRow (systemImage "chart.bar") in the footer above Settings, calling `onOpen(.usage(project: ""))`; update the drawer's doc comment (usage is now valid unscoped = overall).
   - UsageView projectFilter: add an "All projects" picker option with tag "" so the user can widen/narrow scope; keep `showsProjectFilter` (>1 project).
   - UsageModel: drop the auto-select-sole-project fallback in `refresh()` (empty selection is now a first-class "overall" scope; with one project the data is identical and the filter is hidden anyway). Keep the toolbar (projectDestinations) Usage entry in LandingView as-is.
   - Update/add YccKit UsageModelTests for the empty-project (overall) behavior.

5) Verify: `go build ./... && go test ./internal/session ./internal/server ./cmd/...`; Swift cannot be built here (no toolchain) — task ends in_review per iOS convention (awaiting on-device use).

### Starting points
- internal/session/session.go: Manager.UsageReport (~line 2745), resolveProjectWorkspace (~1546)
- internal/server/server.go: GetUsage (~946)
- cmd/ycc/main.go: costCommand (~461), renderCost
- clients/ios/App/WorkspaceDrawer.swift: footer, DrawerRow
- clients/ios/App/UsageView.swift: projectFilter (~213), ensureLoaded
- clients/ios/YccKit/Sources/YccKit/UsageModel.swift: refresh() auto-select at ~line 165, showsProjectFilter
- clients/ios/YccKit/Tests/YccKitTests/UsageModelTests.swift
- spec.md §20.5 'Project rollup' (~line 2163)
- usage package: usage.Scan(absWS), usage.Aggregate(entries, reg, opts) — internal/usage

## Work log
- 2026-08-08 plan: Context: the iOS drawer refactor deliberately removed project-scoped destinations (backlog/workstreams/usage) from the WorkspaceDrawer because they need a project. The user wants Usage back in the dra
…[truncated]
- 2026-08-08 context hints: 9 recorded with plan
- 2026-08-08 context hints: internal/session/session.go: resolveProjectWorkspace (~1546) shows the current empty-project error path; internal/server/server.go GetUsage (~946) passes req.Msg.Project straight through — likely un
…[truncated]
- 2026-08-08 preload: 3 file(s), ~13 KiB seeded into implementer context
- 2026-08-08 implementer report: Implemented task 0287.  Changes: - Extended `Manager.UsageReport("")` to scan and aggregate all registered project workspaces when multiple projects exist, deduplicating absolute workspace paths and l
…[truncated]
- 2026-08-08 review tier: single-opus — reviewers: sol
- 2026-08-08 review (sol): accept — Task 0287 is implemented correctly. `Manager.UsageReport` now aggregates a single report over all distinct registered workspace paths when the project is omitted and multiple projects exist, while pre
…[truncated]
- 2026-08-08 usage: 1,525,569 tok (in 780,364, out 19,957, cache_r 1,559,668, cache_w 32,041) · cost n/a (unpriced)
  implementer: 1,097,786 tok (in 497,619, out 7,015, cache_r 593,152, cache_w 0) · cost n/a (unpriced)
  reviewer:sol: 417,258 tok (in 282,721, out 2,441, cache_r 132,096, cache_w 0) · cost n/a (unpriced)
  coordinator: 10,525 tok (in 24, out 10,501, cache_r 834,420, cache_w 32,041) · cost n/a (unpriced)
