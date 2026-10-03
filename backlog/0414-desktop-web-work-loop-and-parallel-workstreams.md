---
id: "0414"
title: 'Desktop web: work loop and parallel workstreams'
status: todo
priority: 3
created: "2026-10-02"
updated: "2026-10-02"
depends_on:
    - "0410"
spec_refs:
    - Unattended work loop
    - Parallel workstreams
    - docs/design/web-client.md#Desktop-first layout
---

## Description
Fifth phase of docs/design/web-client.md. Reference iOS WorkLoopView/WorkLoopModel and WorkstreamsView/WorkstreamsModel.

- Work loop panel: GetWorkLoop status (current task, progress, history/digest), StartWorkLoop with options and StopWorkLoop. Link to the sessions the loop spawned. SetWorkImplementation where exposed.
- Workstreams: ListWorkstreams with status, SpawnWorkstream, PreviewMerge (diff in the inspector), MergeWorkstream, DiscardWorkstream (confirmed), RetryIntegration. Integration-queue state and errors are visible.
- The sidebar shows work-loop and workstream activity indicators.

## Acceptance criteria
- [ ] Start and stop the work loop and follow its sessions from the browser.
- [ ] Spawn, preview, merge, and discard a workstream from the browser, and surface a failed integration with a retry.

## Work log
