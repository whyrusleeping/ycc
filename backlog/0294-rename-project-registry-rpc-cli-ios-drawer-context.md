---
id: "0294"
title: 'Rename project: registry+RPC+CLI+iOS drawer context menu'
status: in_review
priority: 3
created: "2026-08-08"
updated: "2026-08-08"
depends_on: []
spec_refs:
    - 3.1 Daemon lifecycle & projects
---

## Description
Add project rename across the stack:

- `project.Registry.Rename(old, new)` (persisted, collision = error)
- `workstream.Registry.RenameProject(old, new)` so existing workstreams follow the new name (live sessions & work loops are keyed by workspace path and need no change)
- `Manager.RenameProject`, `RenameProject` RPC (proto + server handler), `ycc project rename` CLI
- iOS: `YccClient.renameProject`, `SessionListModel.renameProject`, "Rename project…" item in the drawer's project-row context menu with a TextField alert
- spec §3.1 / remote-api docs updated; Go + Swift protos regenerated

Acceptance: rename persists across daemon restart; existing workstreams still list under the new name; iOS drawer context menu offers Rename next to Remove; unit tests for both registries and the server handler.

## Acceptance criteria

## Work log
