---
id: "0533"
title: 'Web + iOS: show the last automatic backlog groom and a "groom now" action'
status: done
priority: 3
created: "2026-11-10"
updated: "2026-11-10"
depends_on:
    - "0531"
spec_refs:
    - spec.md#6.2 Backlog
---

## Description

## Description
## Why
Task 0531 added daemon-scheduled backlog grooming (spec §6.2): `Manager.BacklogGroomInfo` reports whether it is enabled, any running groom, and the latest run (`.ycc/backlog-groom.json`: session, outcome, changed count, batch ids, clipped final report). 0531's criteria also ask that web and iOS show the last groom result and offer "groom now", and allow splitting that UI out. It was split because another session had uncommitted changes in `proto/ycc/v1/ycc.proto`, `internal/server/server.go` and the web bundle at the time, and the commit tool commits whole files.

## Behaviour
- Expose backlog-groom status over RPC, next to the memory-groom status that `GetMemory`-style responses already carry (`MemoryGroomRun`). Regenerate Go, web and Swift protos.
- Web backlog page and iOS backlog view: show the last run (when, outcome, how many tasks it reviewed, the report, a link to its session) and a running indicator.
- "Groom now" starts the `backlog-groom` preset by name, like the memory page's groom action.
- Point users at the standing "Backlog groom: decisions needed" proposed task when one exists.

## Acceptance criteria
- An RPC returns enabled, running session and last run for a project. Server test covers it.
- Web and iOS show the last run and offer "groom now". Light/dark screenshots, web bundle rebuilt, iOS built and tested on the Mac.

## Acceptance criteria

- An RPC returns enabled, running session and last run for a project. Server test covers it.
- Web and iOS show the last run and offer "groom now". Light/dark screenshots, web bundle rebuilt, iOS built and tested on the Mac.

## Outcome

GetBacklogGroom RPC reports auto-groom enabled, the live automatic groom, the last run (changed/batch/deferred/report) and the proposed "Backlog groom: decisions needed" task; TestGetBacklogGroom. Web backlog page GroomBanner (status line, session link, report toggle, decisions link, Groom now/Watch) with unit tests, light/dark shots and a Groom now click verified in the sidebar-v2 harness; bundle rebuilt. iOS backlog strip + detail sheet (polls while a groom runs), BacklogGroomSummary tests; swift test 594 pass, xcodebuild build/test pass on the Mac; iOS UI not viewed in a simulator (no AXe).

Commit: Backlog groom status: GetBacklogGroom RPC (enabled, running groom, last run, decisions task) with web backlog banner and iOS backlog strip/sheet offering Groom now (0533)
