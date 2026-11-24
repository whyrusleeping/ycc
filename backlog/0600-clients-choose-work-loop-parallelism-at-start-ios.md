---
id: "0600"
title: 'Clients: choose work-loop parallelism at Start; iOS parity for loop slots, manual landings and created tasks'
status: todo
priority: 3
created: "2026-11-24"
updated: "2026-11-24"
depends_on: []
spec_refs:
    - spec.md#9.1
---

## Description
Status: todo; accepted by the user 2026-11-24 in chat. Nothing blocks it.

The parallel work loop shipped in 0596 (atomic claims, per-session created tasks), 0597 (sessions waiting on their own shell jobs), 0598 (N parallel slots) and 0599 (exclusive leases). The daemon accepts `StartWorkLoopRequest.parallel` (0 = the project's `[work_loop] parallel`, default 1; at most 16; values above 1 are experimental). No client sends it: web `WorkLoopPage.tsx` calls `startWorkLoop({ project })`, iOS `YccClient.startWorkLoop(project:)` sets only the project, and the CLI has no flag. The only way to run N slots today is editing ycc.toml by hand.

iOS shows the "2/3 working, 1 waiting" slots line and counts manual landings in the summary line. It does not show what web shows:
- the "Live sessions" list of `WorkLoopInfo.slots` (session link, claimed task, working/waiting state);
- the "Landed manually" list of `manual_landings` (sha, subject);
- per-session `WorkLoopSession.created_tasks` in the "Sessions run" rows.

## Acceptance criteria
- Web and iOS Start flows let the user pick a slot count (default: the project's configured value; range 1..16, with values above 1 marked experimental) and send it as `parallel`. Leaving the default unchanged sends 0, so the project's config applies.
- iOS WorkLoopView lists live slots (each session opens its transcript, each task its detail), manual landings, and each run session's created tasks, matching the web sections.
- Web `npm run typecheck` and vitest pass. iOS builds, YccKit `swift test` passes, and the new iOS sections are screenshot-checked on the Mac simulator against a daemon running a loop with parallel > 1 (per the standing iOS verification preference).

## Work log
