---
id: "0620"
title: Weekly agent-activity heatmap on the web Usage page
status: done
priority: 3
created: "2026-11-26"
updated: "2026-11-26"
depends_on: []
spec_refs:
    - spec.md#20.3 Aggregation
    - spec.md#20.5 Surfaces
---

## Description

The user wants to see when agent work happens across the week.

### Acceptance criteria
- The web Usage page shows a 7-day × 24-hour heatmap in the browser's local time, for the page's scope (project or all projects), date range and task filter.
- The metric can be switched between tokens (default), model turns, and busy time.
- Busy time is per-session wall time during which a model turn or tool call was running (excluding time blocked on `ask_user`). Concurrent agents in one session count once; concurrent sessions add up.
- The daemon serves hourly buckets recomputed from the session event logs through the existing cached usage scan, honouring fork markers like the usage breakdown does.

## Outcome

New GetUsageHours RPC. The usage reducer keeps per-session hourly tokens, turns and 10-second busy-slot bitmasks inside the existing cached scan. It honours fork markers and skips ask_user time and synthetic preload turns. The web Usage page sums the hours into a local-time weekday × hour heatmap with Tokens/Turns/Busy time toggles. Turn totals matched an independent count of the log. A cold scan of 521 MB of logs went from about 0.3 s to 0.6 s. Known approximations: in half-hour time zones each UTC hour is filed under the local hour it starts in, and an interval's busy time goes to the task in focus when it finishes. Verified with go test, web vitest, web-build, Playwright screenshots against a scratch daemon, and a YccKit build on the Mac; a sol review found the synthetic-turn defect, which is now fixed.

Commit: usage: weekly agent-activity heatmap (GetUsageHours) on the web Usage page (0620)
