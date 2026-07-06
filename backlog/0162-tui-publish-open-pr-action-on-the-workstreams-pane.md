---
id: "0162"
title: 'TUI: publish (open PR) action on the Workstreams panel'
status: proposed
priority: 5
created: "2026-07-06"
updated: "2026-07-06"
depends_on:
    - "0157"
spec_refs:
    - docs/design/forge-integration.md#6. Flow 2 — workstream → PR (publish)
---

## Description
From docs/design/forge-integration.md §6/§11 (design spike 0146).

TUI surface for publish: a "Publish (open PR)" action beside merge/discard on the Workstreams panel, showing the PR-body preview + accept gate (mirroring the merge preview flow) and the resulting PR URL on success. Render the `workstream_published` event in the session view like the other workstream_* lifecycle events.

## Acceptance criteria
- [ ] Workstreams panel offers publish with preview/accept; success shows the PR URL.
- [ ] `workstream_published` renders in the session stream.

## Work log
