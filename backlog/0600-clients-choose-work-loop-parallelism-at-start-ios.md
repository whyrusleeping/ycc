---
id: "0600"
title: 'Clients: choose work-loop parallelism at Start; iOS parity for loop slots, manual landings and created tasks'
status: done
priority: 3
created: "2026-11-24"
updated: "2026-11-24"
depends_on: []
spec_refs:
    - spec.md#9.1
---

## Description
Accepted by the user 2026-11-24 in chat.

The parallel work loop shipped in 0596 (atomic claims, per-session created tasks), 0597 (sessions waiting on their own shell jobs), 0598 (N parallel slots) and 0599 (exclusive leases). The daemon accepts `StartWorkLoopRequest.parallel` (0 = the project's `[work_loop] parallel`, default 1; at most 16; values above 1 are experimental). No client sends it: web `WorkLoopPage.tsx` calls `startWorkLoop({ project })`, iOS `YccClient.startWorkLoop(project:)` sets only the project, and the CLI has no flag. The only way to run N slots today is editing ycc.toml by hand.

iOS shows the "2/3 working, 1 waiting" slots line and counts manual landings in the summary line. It does not show what web shows:
- the "Live sessions" list of `WorkLoopInfo.slots` (session link, claimed task, working/waiting state);
- the "Landed manually" list of `manual_landings` (sha, subject);
- per-session `WorkLoopSession.created_tasks` in the "Sessions run" rows.

## Acceptance criteria
- Web and iOS Start flows let the user pick a slot count (default: the project's configured value; range 1..16, with values above 1 marked experimental) and send it as `parallel`. Leaving the default unchanged sends 0, so the project's config applies.
- iOS WorkLoopView lists live slots (each session opens its transcript, each task its detail), manual landings, and each run session's created tasks, matching the web sections.
- Web `npm run typecheck` and vitest pass. iOS builds, YccKit `swift test` passes, and the new iOS sections are screenshot-checked on the Mac simulator against a daemon running a loop with parallel > 1 (per the standing iOS verification preference).

## Outcome

Web and iOS Start flows pick 1..16 loop sessions, defaulting to the project's `[work_loop] parallel`, which the new `GetWorkLoopPreflightResponse.configured_parallel` reports. Values above 1 are marked experimental, and an unchanged default sends 0. The iOS Backlog banner's Start dialog names the count and links to the picker. iOS WorkLoopView now lists Live sessions (session opens its transcript, task its detail), Landed manually (opens the commit diff) and per-session Created task chips. Spec §9.1 landed separately (spec.md also held another session's uncommitted work). Verified: Go session/server/web tests on HEAD plus the task files, web typecheck and 490 vitest tests with a rebuilt bundle, an iOS build and 687 YccKit tests on the Mac, and simulator and Playwright checks against a scratch daemon running loops with parallel 2 and 3.

Commit: Clients pick work-loop parallelism at Start; iOS loop slots, manual landings and created tasks (0600)
