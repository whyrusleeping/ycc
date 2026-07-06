---
id: "0137"
title: 'Spend guard: budget caps for sessions and the work loop'
status: todo
priority: 2
created: "2026-07-06"
updated: "2026-07-06"
depends_on: []
spec_refs:
    - 20. Token usage & cost accounting
    - 11. Interaction levels
---

## Description
Usage/cost tracking is rich (per-turn usage events, live Σ tokens/$ in the status bar, `ycc cost`), but nothing acts on it. The scariest UX moment in an agentic harness is walking away from `work (loop)` in autonomous mode with no ceiling on spend. A budget guard converts the existing telemetry into trust: users will run longer unattended loops when they know the harness stops at a line they drew.

## Acceptance criteria
- [ ] Optional config (e.g. `[budget]` in ycc.toml): per-session cost/token cap and a per-loop-run cap; absent = unlimited (current behaviour).
- [ ] Status bar shows a visually distinct warning when a session passes ~80% of its budget.
- [ ] On breach in an attended session: a Confirm gate ("budget reached — continue?") like `switch_to_work`'s gate; declining stops gracefully.
- [ ] On breach in autonomous / loop sessions: graceful halt at the next safe checkpoint (current task finishes or is marked blocked), recorded in the loop digest and the event log — never a silent overrun, never a mid-write kill.
- [ ] Models with no pricing configured count tokens against a token cap only (no invented dollars, matching §20.4's degrade-gracefully rule).

## Work log
