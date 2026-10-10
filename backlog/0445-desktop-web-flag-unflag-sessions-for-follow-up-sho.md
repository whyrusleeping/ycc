---
id: "0445"
title: 'Desktop web: flag/unflag sessions for follow-up, show marker and Follow-up filter'
status: todo
priority: 3
created: "2026-10-10"
updated: "2026-10-10"
depends_on:
    - "0441"
spec_refs: []
---

## Description
Split from 0441. 0441 adds the daemon's follow-up flag: `SetSessionFollowUp` RPC, `SessionSummary.follow_up` / `follow_up_at`, and TUI support. This task covers the desktop web client (clients/web).

## Acceptance criteria
- The user can flag and unflag a session from the web session list. If the session view has a natural action spot, it can be done there too.
- Flagged sessions show a clear marker. The list can be filtered to only flagged sessions, or shows a "Follow up" section at the top.
- The flag follows 0441's documented clearing behavior: manual clear only.
- Web tests cover the toggle and the filter. Regenerate the web bundle in internal/web/dist (scripts/web-build.sh) and keep the Go freshness test passing.

## Work log
