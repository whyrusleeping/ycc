---
id: "0474"
title: 'Web: session Activity panel (agent/job tree, timeline, drill-down)'
status: done
priority: 2
created: "2026-10-21"
updated: "2026-10-21"
depends_on:
    - "0473"
spec_refs: []
---

## Description
## Why
Users want to see a session's agents, subagents and jobs graphically, and to drill into each one.

## Acceptance criteria
- A session can open an Activity panel. It shows the coordinator → subagent → background-job tree with live status, model, current tool, turns and tokens.
- The panel also has a swimlane timeline with one lane per actor, drawing rounds and jobs as bars over time. It refreshes while the session is live.
- Selecting an agent node shows that agent's transcript, filtered to its actor.
- Selecting a shell job shows its output. Live output follows the tail; when output isn't retained, the final result is shown instead.
- The bundle in internal/web/dist is rebuilt.

## Work log
