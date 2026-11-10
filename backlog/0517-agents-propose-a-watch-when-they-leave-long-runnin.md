---
id: "0517"
title: Agents propose a watch when they leave long-running external work, instead of waiting for "how's it going?"
status: todo
priority: 2
created: "2026-11-09"
updated: "2026-11-09"
depends_on: []
spec_refs:
    - spec.md#9.2 Watches
    - docs/design/watches.md
---

## Description
**Evidence (2026-11-09 usage analysis):** 24 owner messages are pure check-ins ("how's it going?", "check in", "where are we at on the backfill?"). 18 of them came after the agent had ended its turn with a statement while external work kept running (soaks, replays, backfills in vals and wheeler). The owner has to remember to poke the session. Watches (0498, spec §9.2) now provide daemon-owned recurring checks with notifications. 0499/0500 add the client panels.

**Change:**
- Chat/work prompts: when a turn ends while external long-running work it started is still going (beyond a tracked Bash job, which already wakes the session), the agent calls schedule_watch. The watch gets a brief, a cadence suited to the work, stop/expiry conditions, and what counts as wrong. The user confirms the definition as usual. The final report says what is still running and which watch covers it.
- Don't create a watch for work that finishes within the turn or is already tracked as a job.

**Acceptance:**
- Prompt and spec text are updated.
- A scripted-model or behavioural eval of a turn that leaves a remote soak running produces a schedule_watch proposal with expiry, and a turn whose work completes produces none.
- Watch runs stay out of recent sessions per 0499/0500.

Narrower than the 0391 design scope (bounded run records, leases), which stays proposed.

## Acceptance criteria

## Work log
