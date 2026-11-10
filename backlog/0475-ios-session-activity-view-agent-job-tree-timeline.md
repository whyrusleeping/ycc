---
id: "0475"
title: 'iOS: session Activity view (agent/job tree, timeline, drill-down)'
status: done
priority: 2
created: "2026-10-21"
updated: "2026-11-10"
depends_on:
    - "0473"
spec_refs: []
---

## Description
## Why
iOS should get an Activity view similar to the web client's, adapted for a phone.

## Acceptance criteria
- From a session, the user can open an Activity view. It shows a collapsible coordinator → subagent → job tree with live status, model, current tool, turns and tokens, plus a compact timeline.
- The view refreshes while the session is live.
- Tapping an agent shows its actor-filtered transcript. Tapping a shell job shows its output, following the tail while the job is live, or its final result.
- The app builds and its tests pass on the Mac mini.

## Work log
- 2026-11-10: Owner-requested simulator review with a scripted mock that spawns subagents and a background job. The tree, live refresh, agent detail with filtered transcript, and job output (running, finished, lost) all work. PASS. The tree's progress line said "1 turns"; fixed to "1 turn". Screenshots in .ycc/tmp/ios-review/C/shots/.
